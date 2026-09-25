package main

import (
	"fmt"
	"net/rpc"
	"os"
	"slices"
	"sync"
	"time"

	lab "distvis/sdk/rpc"
)

const (
	quorumTimeout = 5 * time.Second // give up on a quorum instead of waiting for the 10 s RPC timeout
	settleWindow  = 6 * time.Second // after a Get returns, keep listening this long to judge staleness
)

// Version orders writes: a Lamport counter, ties broken by the coordinator's index.
type Version struct {
	C int `json:"c"`
	N int `json:"n"`
}

func (a Version) Less(b Version) bool { return a.C < b.C || (a.C == b.C && a.N < b.N) }
func (a Version) String() string      { return fmt.Sprintf("%d.%d", a.C, a.N) }

type Item struct {
	Value string  `json:"value"`
	Ver   Version `json:"ver"`
}

type StoreArgs struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	C     int    `json:"c"`
	N     int    `json:"n"`
}
type StoreReply struct {
	OK bool `json:"ok"`
}
type FetchArgs struct {
	Key string `json:"key"`
}
type FetchReply struct {
	Found bool   `json:"found"`
	Value string `json:"value"`
	C     int    `json:"c"`
	N     int    `json:"n"`
}
type ConfigArgs struct {
	W          int  `json:"w"`
	R          int  `json:"r"`
	ReadRepair bool `json:"readRepair"`
}
type PutArgs struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type PutReply struct {
	Version string   `json:"version"`
	Acks    []string `json:"acks"`
	Ms      int64    `json:"ms"`
}
type GetArgs struct {
	Key string `json:"key"`
}
type GetReply struct {
	Found   bool     `json:"found"`
	Value   string   `json:"value"`
	Version string   `json:"version"`
	From    []string `json:"from"`
	Ms      int64    `json:"ms"`
}
type Empty struct{}

type LastGet struct {
	GetReply
	Key    string `json:"key"`
	Stale  bool   `json:"stale"`  // a replica outside the read quorum had a newer version
	Latest string `json:"latest"` // highest version seen from all replies within the settle window
}
type LastPut struct {
	PutReply
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Quorum struct {
	mu      sync.Mutex
	node    *lab.Runtime
	index   int
	peers   map[string]*rpc.Client
	store   map[string]Item
	counter int
	cfg     ConfigArgs
	lastPut *LastPut
	lastGet *LastGet
}
type Application struct{ q *Quorum }

func newQuorum(node *lab.Runtime) (*Quorum, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	n := len(node.Nodes)
	return &Quorum{node: node, index: slices.Index(node.Nodes, node.ID) + 1, peers: peers,
		store: map[string]Item{}, cfg: ConfigArgs{W: n/2 + 1, R: n/2 + 1}}, nil
}

func (q *Quorum) report() error {
	n := len(q.node.Nodes)
	store := map[string]map[string]string{}
	for k, it := range q.store {
		store[k] = map[string]string{"value": it.Value, "version": it.Ver.String()}
	}
	state := map[string]any{
		"role": "replica", "n": n, "w": q.cfg.W, "r": q.cfg.R, "overlap": q.cfg.W+q.cfg.R > n,
		"readRepair": q.cfg.ReadRepair, "counter": q.counter, "store": store,
	}
	if q.lastPut != nil {
		state["lastPut"] = q.lastPut
	}
	if q.lastGet != nil {
		state["lastGet"] = q.lastGet
	}
	return q.node.Report(state)
}

func (q *Quorum) logReport() {
	if err := q.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// apply keeps the newer of the stored and incoming versions (last writer wins by version).
func (q *Quorum) apply(key string, it Item) {
	q.counter = max(q.counter, it.Ver.C)
	if cur, ok := q.store[key]; !ok || cur.Ver.Less(it.Ver) {
		q.store[key] = it
	}
}

func (q *Quorum) Store(args StoreArgs, reply *StoreReply) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.apply(args.Key, Item{args.Value, Version{args.C, args.N}})
	reply.OK = true
	return q.report()
}

func (q *Quorum) Fetch(args FetchArgs, reply *FetchReply) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	it, ok := q.store[args.Key]
	*reply = FetchReply{Found: ok, Value: it.Value, C: it.Ver.C, N: it.Ver.N}
	return nil
}

func (q *Quorum) Config(args ConfigArgs, reply *Empty) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cfg = args
	return q.report()
}

type answer struct {
	node  string
	reply FetchReply
	err   error
}

// broadcast sends one RPC per peer; replies (or errors) arrive on the returned channel.
// Stragglers keep running in the background, bounded by the 10 s RPC timeout.
func (q *Quorum) broadcast(method string, args any) chan answer {
	ch := make(chan answer, len(q.peers)+1)
	for peer, client := range q.peers {
		go func() {
			var r FetchReply
			var reply any = &r
			switch method {
			case "Q.Store":
				reply = &StoreReply{}
			case "Q.Config":
				reply = &Empty{}
			}
			err := client.Call(method, args, reply)
			ch <- answer{peer, r, err}
		}()
	}
	return ch
}

func (a *Application) SetQuorum(args ConfigArgs, reply *Empty) error {
	q := a.q
	n := len(q.node.Nodes)
	if args.W < 1 || args.W > n || args.R < 1 || args.R > n {
		return fmt.Errorf("need 1 <= w, r <= %d", n)
	}
	if err := q.Config(args, reply); err != nil {
		return err
	}
	q.broadcast("Q.Config", args)
	return nil
}

func (a *Application) Put(args PutArgs, reply *PutReply) error {
	q := a.q
	start := time.Now()
	q.mu.Lock()
	q.counter++
	ver := Version{q.counter, q.index}
	q.apply(args.Key, Item{args.Value, ver})
	w, n := q.cfg.W, len(q.node.Nodes)
	q.logReport()
	q.mu.Unlock()

	ch := q.broadcast("Q.Store", StoreArgs{args.Key, args.Value, ver.C, ver.N})
	acks, failed := []string{q.node.ID}, 0
	deadline := time.After(quorumTimeout)
wait:
	for len(acks) < w && failed <= n-w {
		select {
		case a := <-ch:
			if a.err != nil {
				failed++
			} else {
				acks = append(acks, a.node)
			}
		case <-deadline:
			break wait
		}
	}
	if len(acks) < w {
		return fmt.Errorf("only %d/%d acks %v; the value stays on those replicas", len(acks), w, acks)
	}
	*reply = PutReply{Version: ver.String(), Acks: acks, Ms: time.Since(start).Milliseconds()}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.lastPut = &LastPut{PutReply: *reply, Key: args.Key, Value: args.Value}
	return q.report()
}

func (a *Application) Get(args GetArgs, reply *GetReply) error {
	q := a.q
	start := time.Now()
	q.mu.Lock()
	r, n, repair := q.cfg.R, len(q.node.Nodes), q.cfg.ReadRepair
	q.mu.Unlock()
	local := answer{node: q.node.ID}
	q.Fetch(FetchArgs(args), &local.reply)

	ch := q.broadcast("Q.Fetch", FetchArgs(args))
	got, failed := []answer{local}, 0
	deadline := time.After(quorumTimeout)
wait:
	for len(got) < r && failed <= n-r {
		select {
		case a := <-ch:
			if a.err != nil {
				failed++
			} else {
				got = append(got, a)
			}
		case <-deadline:
			break wait
		}
	}
	if len(got) < r {
		return fmt.Errorf("only %d/%d replies", len(got), r)
	}
	best := newest(got)
	*reply = GetReply{Found: best.reply.Found, Value: best.reply.Value, Ms: time.Since(start).Milliseconds()}
	if best.reply.Found {
		reply.Version = Version{best.reply.C, best.reply.N}.String()
	}
	for _, a := range got {
		reply.From = append(reply.From, a.node)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if best.reply.Found {
		q.counter = max(q.counter, best.reply.C)
	}
	last := &LastGet{GetReply: *reply, Key: args.Key, Latest: reply.Version}
	q.lastGet = last
	go q.settle(last, got, ch, n-len(got)-failed, repair)
	return q.report()
}

// settle waits for the replies outside the read quorum: they show whether the returned value
// was stale, and read repair pushes the newest version to every replica that answered older.
func (q *Quorum) settle(last *LastGet, all []answer, ch chan answer, pending int, repair bool) {
	timeout := time.After(settleWindow)
collect:
	for ; pending > 0; pending-- {
		select {
		case a := <-ch:
			if a.err == nil {
				all = append(all, a)
			}
		case <-timeout:
			break collect
		}
	}
	best := newest(all)
	q.mu.Lock()
	defer q.mu.Unlock()
	if best.reply.Found {
		v := Version{best.reply.C, best.reply.N}
		q.counter = max(q.counter, v.C)
		last.Latest = v.String()
		last.Stale = last.Version != last.Latest
		if repair {
			for _, a := range all {
				if !a.reply.Found || (Version{a.reply.C, a.reply.N}).Less(v) {
					args := StoreArgs{last.Key, best.reply.Value, v.C, v.N}
					if a.node == q.node.ID {
						q.apply(args.Key, Item{args.Value, v})
					} else {
						go q.peers[a.node].Call("Q.Store", args, &StoreReply{})
					}
				}
			}
		}
	}
	if q.lastGet == last {
		q.logReport()
	}
}

func newest(as []answer) answer {
	best := as[0]
	for _, a := range as[1:] {
		if a.reply.Found && (!best.reply.Found || (Version{best.reply.C, best.reply.N}).Less(Version{a.reply.C, a.reply.N})) {
			best = a
		}
	}
	return best
}
