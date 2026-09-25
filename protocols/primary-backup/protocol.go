package main

import (
	"context"
	"errors"
	"fmt"
	"net/rpc"
	"os"
	"sync"
	"time"

	"distvis/sdk"
	lab "distvis/sdk/rpc"
)

const (
	syncAckTimeout = 4 * time.Second // sync Put gives up waiting for the backups' acks
	sessionTimeout = 5 * time.Second // session read gives up waiting for the local log to catch up
	forwardTimeout = 7 * time.Second // backup -> primary forwarding (must cover a sync Put)
	replicateRPC   = 2 * time.Second
)

// Entry is one log record; Index plays the role of the zxid on the slides.
type Entry struct {
	Index int    `json:"i"`
	Key   string `json:"k"`
	Value string `json:"v"`
}

type PutArgs struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type PutReply struct {
	Index int `json:"index"`
}
type ReplicateReply struct {
	Applied int `json:"applied"`
}
type ReadArgs struct {
	Key string `json:"key"`
}
type ReadReply struct {
	Value string `json:"value"`
	Found bool   `json:"found"`
	Index int    `json:"index"`
}
type ModeArgs struct {
	Mode               string `json:"mode"` // sync | async
	ReplicationDelayMs int    `json:"replicationDelayMs"`
}
type GetArgs struct {
	Key      string `json:"key"`
	Mode     string `json:"mode"`     // local | primary | session
	MinIndex int    `json:"minIndex"` // session: the index returned by the client's last Put
}
type GetReply struct {
	Value string `json:"value"`
	Found bool   `json:"found"`
	Index int    `json:"index"`
	Stale bool   `json:"stale"`
}
type Empty struct{}

type Read struct {
	Key      string `json:"key"`
	Mode     string `json:"mode"`
	Value    string `json:"value"`
	Found    bool   `json:"found"`
	Index    int    `json:"index"`
	Stale    bool   `json:"stale"`
	WaitedMs int64  `json:"waitedMs"`
}

// durable is what survives a crash (sdk.Save): the log and the replication mode.
type durable struct {
	Log     []Entry `json:"log"`
	Mode    string  `json:"mode"`
	DelayMs int     `json:"delayMs"`
}

type Replica struct {
	mu      sync.Mutex
	cond    *sync.Cond
	node    *lab.Runtime
	peers   map[string]*rpc.Client
	primary string
	d       durable
	store   map[string]string
	born    map[int]time.Time // when the primary appended each entry (for the async delay)
	next    map[string]int    // primary: next index to send to each backup
	acked   map[string]int    // primary: highest index each backup confirmed
	known   int               // highest index this node has heard of
	last    *Read
}
type Application struct{ p *Replica }

func newReplica(node *lab.Runtime) (*Replica, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	p := &Replica{node: node, peers: peers, primary: node.Nodes[0], d: durable{Mode: "sync"},
		store: map[string]string{}, born: map[int]time.Time{}, next: map[string]int{}, acked: map[string]int{}}
	p.cond = sync.NewCond(&p.mu)
	if err := sdk.Load(&p.d); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range p.d.Log {
		p.store[e.Key] = e.Value
	}
	p.known = len(p.d.Log)
	for peer := range peers {
		p.next[peer] = len(p.d.Log) + 1
	}
	return p, nil
}

func (p *Replica) isPrimary() bool { return p.node.ID == p.primary }
func (p *Replica) applied() int    { return len(p.d.Log) }

func (p *Replica) report() error {
	state := map[string]any{
		"role": "backup", "mode": p.d.Mode, "appliedIndex": p.applied(), "store": p.store,
	}
	if p.d.Mode == "async" {
		state["replicationDelayMs"] = p.d.DelayMs
	}
	if p.isPrimary() {
		state["role"] = "primary"
		state["acked"] = p.acked
	}
	if p.last != nil {
		state["lastRead"] = p.last
	}
	return p.node.Report(state)
}

func (p *Replica) logReport() {
	if err := p.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (p *Replica) save() {
	if err := sdk.Save(p.d); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
	}
}

func (p *Replica) append(e Entry) {
	p.d.Log = append(p.d.Log, e)
	p.store[e.Key] = e.Value
	p.known = max(p.known, e.Index)
	p.save()
	p.cond.Broadcast()
}

// Put runs on the primary: assign the next index, apply, then let the per-backup senders ship it.
func (p *Replica) Put(args PutArgs, reply *PutReply) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.isPrimary() {
		return errors.New("not the primary")
	}
	e := Entry{Index: p.applied() + 1, Key: args.Key, Value: args.Value}
	p.born[e.Index] = time.Now()
	p.append(e)
	reply.Index = e.Index
	p.logReport()
	if p.d.Mode != "sync" {
		return nil
	}
	deadline := time.Now().Add(syncAckTimeout)
	timer := time.AfterFunc(syncAckTimeout, func() {
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	})
	defer timer.Stop()
	for {
		missing := []string{}
		for _, peer := range p.node.Nodes {
			if peer != p.node.ID && p.acked[peer] < e.Index {
				missing = append(missing, peer)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("sync: no ack from %v for index %d (applied on primary, reply withheld)", missing, e.Index)
		}
		p.cond.Wait()
	}
}

// Replicate runs on a backup. Entries are applied strictly in index order; the reply tells
// the primary how far this backup is, so a gap (e.g. after a partition) is resent from there.
func (p *Replica) Replicate(e Entry, reply *ReplicateReply) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.Index == p.applied()+1 {
		p.append(e)
		p.logReport()
	}
	p.known = max(p.known, e.Index)
	reply.Applied = p.applied()
	return nil
}

func (p *Replica) Read(args ReadArgs, reply *ReadReply) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.isPrimary() {
		return errors.New("not the primary")
	}
	reply.Value, reply.Found = p.store[args.Key]
	reply.Index = p.applied()
	return nil
}

func (p *Replica) SetMode(args ModeArgs, reply *Empty) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.d.Mode, p.d.DelayMs = args.Mode, max(args.ReplicationDelayMs, 0)
	p.save()
	p.cond.Broadcast()
	return p.report()
}

// sender ships the log to one backup in order, one entry per RPC, retrying forever.
func (p *Replica) sender(ctx context.Context, peer string) {
	for ctx.Err() == nil {
		p.mu.Lock()
		for p.next[peer] > p.applied() {
			p.cond.Wait()
		}
		e := p.d.Log[p.next[peer]-1]
		wait := time.Duration(0)
		if p.d.Mode == "async" {
			wait = time.Until(p.born[e.Index].Add(time.Duration(p.d.DelayMs) * time.Millisecond))
		}
		p.mu.Unlock()
		if wait > 0 {
			time.Sleep(wait)
		}
		var reply ReplicateReply
		err := call(p.peers[peer], "PB.Replicate", e, &reply, replicateRPC)
		p.mu.Lock()
		if err == nil {
			p.acked[peer] = reply.Applied
			p.next[peer] = min(reply.Applied+1, p.applied()+1)
			p.cond.Broadcast()
			p.logReport()
		}
		p.mu.Unlock()
		if err != nil {
			time.Sleep(time.Second)
		}
	}
}

func (p *Replica) run(ctx context.Context) error {
	if !p.isPrimary() {
		return nil
	}
	for peer := range p.peers {
		go p.sender(ctx, peer)
	}
	return nil
}

// call is client.Go with a deadline shorter than the 10 s RPC default; a late reply is ignored.
func call(client *rpc.Client, method string, args, reply any, timeout time.Duration) error {
	c := client.Go(method, args, reply, make(chan *rpc.Call, 1))
	select {
	case <-c.Done:
		return c.Error
	case <-time.After(timeout):
		return fmt.Errorf("%s: no reply within %v", method, timeout)
	}
}

func (a *Application) Put(args PutArgs, reply *PutReply) error {
	p := a.p
	if p.isPrimary() {
		return p.Put(args, reply)
	}
	if err := call(p.peers[p.primary], "PB.Put", args, reply, forwardTimeout); err != nil {
		return fmt.Errorf("forward to primary: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.known = max(p.known, reply.Index)
	return nil
}

func (a *Application) Mode(args ModeArgs, reply *Empty) error {
	if args.Mode != "sync" && args.Mode != "async" {
		return errors.New("mode must be sync or async")
	}
	p := a.p
	if err := p.SetMode(args, reply); err != nil {
		return err
	}
	for _, client := range p.peers {
		go call(client, "PB.SetMode", args, &Empty{}, replicateRPC)
	}
	return nil
}

func (a *Application) Get(args GetArgs, reply *GetReply) error {
	p := a.p
	start := time.Now()
	mode := args.Mode
	if mode == "" {
		mode = "local"
	}
	switch mode {
	case "primary":
		var r ReadReply
		if p.isPrimary() {
			if err := p.Read(ReadArgs{args.Key}, &r); err != nil {
				return err
			}
		} else if err := call(p.peers[p.primary], "PB.Read", ReadArgs{args.Key}, &r, forwardTimeout); err != nil {
			return fmt.Errorf("primary unreachable: %w", err)
		}
		*reply = GetReply{Value: r.Value, Found: r.Found, Index: r.Index}
		p.mu.Lock()
		p.known = max(p.known, r.Index)
	case "local", "session":
		p.mu.Lock()
		if mode == "session" {
			p.known = max(p.known, args.MinIndex)
			deadline := start.Add(sessionTimeout)
			timer := time.AfterFunc(sessionTimeout, func() {
				p.mu.Lock()
				p.cond.Broadcast()
				p.mu.Unlock()
			})
			for p.applied() < args.MinIndex && time.Now().Before(deadline) {
				p.cond.Wait()
			}
			timer.Stop()
			if p.applied() < args.MinIndex {
				defer p.mu.Unlock()
				return fmt.Errorf("session: local log at %d, need %d", p.applied(), args.MinIndex)
			}
		}
		reply.Value, reply.Found = p.store[args.Key]
		reply.Index = p.applied()
		// A replica cannot know every write it missed; "stale" means an index it has
		// heard of (from a Put it forwarded or a replication message) is not applied yet.
		reply.Stale = reply.Index < p.known
	default:
		return errors.New("mode must be local, primary or session")
	}
	defer p.mu.Unlock()
	p.last = &Read{Key: args.Key, Mode: mode, Value: reply.Value, Found: reply.Found, Index: reply.Index,
		Stale: reply.Stale, WaitedMs: time.Since(start).Milliseconds()}
	return p.report()
}
