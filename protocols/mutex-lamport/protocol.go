package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/rpc"
	"os"
	"slices"
	"sync"
	"time"

	lab "distvis/sdk/rpc"
)

// Stamp is a totally ordered Lamport timestamp printed as "L.id".
type Stamp struct {
	L  int
	ID int
}

func (a Stamp) Less(b Stamp) bool { return a.L < b.L || (a.L == b.L && a.ID < b.ID) }
func (a Stamp) String() string    { return fmt.Sprintf("%d.%d", a.L, a.ID) }

// Msg is the body of Request, Reply and Release: the sender's Lamport time.
type Msg struct {
	L    int    `json:"L"`
	From string `json:"from"`
}
type Empty struct{}

type AcquireArgs struct {
	HoldMs int `json:"holdMs"` // time spent inside the critical section; 0 = 2000
}
type AcquireReply struct {
	Request string `json:"request"`
}
type AutoArgs struct {
	Enabled    bool `json:"enabled"`
	IntervalMs int  `json:"intervalMs"` // mean pause between requests; 0 = 3000
	HoldMs     int  `json:"holdMs"`
}
type TickArgs struct {
	Events int `json:"events"` // local events that advance the Lamport clock
}
type TickReply struct {
	Clock int `json:"clock"`
}

type outgoing struct {
	method string
	msg    Msg
}

type Mutex struct {
	mu       sync.Mutex
	node     *lab.Runtime
	index    int
	peers    map[string]*rpc.Client
	outbox   map[string]chan outgoing
	pending  int
	clock    int
	queue    []Stamp
	lastRecv map[string]int // Lamport time of the latest message from each peer
	role     string         // released / wanted / critical
	request  Stamp
	hold     time.Duration
	since    time.Time
	entries  int
	lastWait int64
	sent     int
	auto     AutoArgs
}
type Application struct{ m *Mutex }

func newMutex(node *lab.Runtime) (*Mutex, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	m := &Mutex{node: node, index: slices.Index(node.Nodes, node.ID) + 1, peers: peers,
		outbox: map[string]chan outgoing{}, lastRecv: map[string]int{}, role: "released"}
	for peer := range peers {
		m.outbox[peer] = make(chan outgoing, 4096)
	}
	return m, nil
}

func (m *Mutex) indexOf(id string) int { return slices.Index(m.node.Nodes, id) + 1 }

func (m *Mutex) report() error {
	queue := []string{}
	for _, s := range m.queue {
		queue = append(queue, s.String())
	}
	state := map[string]any{
		"role": m.role, "clock": m.clock, "queue": queue, "entries": m.entries,
		"lastWaitMs": m.lastWait, "messagesSent": m.sent, "unsent": m.pending, "auto": m.auto.Enabled,
	}
	if m.role != "released" {
		state["request"] = m.request.String()
		state["laterFrom"] = m.laterFrom()
		state["laterNeeded"] = len(m.peers)
	}
	return m.node.Report(state)
}

func (m *Mutex) logReport() {
	if err := m.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// laterFrom lists the peers from which a message stamped after our request has arrived.
// With FIFO channels, such a peer can no longer send a request that precedes ours.
func (m *Mutex) laterFrom() []string {
	out := []string{}
	for peer := range m.peers {
		if m.request.Less(Stamp{m.lastRecv[peer], m.indexOf(peer)}) {
			out = append(out, peer)
		}
	}
	slices.Sort(out)
	return out
}

func (m *Mutex) enqueue(s Stamp) {
	m.queue = slices.DeleteFunc(m.queue, func(x Stamp) bool { return x.ID == s.ID })
	i, _ := slices.BinarySearchFunc(m.queue, s, func(a, b Stamp) int {
		if a.Less(b) {
			return -1
		}
		if b.Less(a) {
			return 1
		}
		return 0
	})
	m.queue = slices.Insert(m.queue, i, s)
}

func (m *Mutex) receive(msg Msg) {
	m.clock = max(m.clock, msg.L) + 1
	m.lastRecv[msg.From] = max(m.lastRecv[msg.From], msg.L)
}

// send hands a message to the peer's single sender goroutine, which keeps the per-link FIFO order.
func (m *Mutex) send(peer, method string) {
	m.sent++
	m.pending++
	m.outbox[peer] <- outgoing{method, Msg{m.clock, m.node.ID}}
}

func (m *Mutex) Request(args Msg, reply *Empty) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.receive(args)
	m.enqueue(Stamp{args.L, m.indexOf(args.From)})
	m.clock++
	m.send(args.From, "Lamport.Reply")
	return m.report()
}

func (m *Mutex) Reply(args Msg, reply *Empty) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.receive(args)
	m.check()
	return m.report()
}

func (m *Mutex) Release(args Msg, reply *Empty) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.receive(args)
	id := m.indexOf(args.From)
	m.queue = slices.DeleteFunc(m.queue, func(x Stamp) bool { return x.ID == id })
	m.check()
	return m.report()
}

func (m *Mutex) check() {
	if m.role != "wanted" || len(m.queue) == 0 || m.queue[0] != m.request || len(m.laterFrom()) < len(m.peers) {
		return
	}
	m.role, m.entries = "critical", m.entries+1
	m.lastWait = time.Since(m.since).Milliseconds()
	req := m.request
	time.AfterFunc(m.hold, func() { m.leave(req) })
}

func (m *Mutex) acquire(hold int) (Stamp, error) {
	if m.role != "released" {
		return Stamp{}, errors.New("已有未完成的请求：" + m.role)
	}
	if hold <= 0 {
		hold = 2000
	}
	m.clock++
	m.role, m.request = "wanted", Stamp{m.clock, m.index}
	m.hold, m.since = time.Duration(hold)*time.Millisecond, time.Now()
	m.enqueue(m.request)
	for peer := range m.peers {
		m.send(peer, "Lamport.Request")
	}
	m.check()
	return m.request, m.report()
}

func (m *Mutex) leave(req Stamp) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.role != "critical" || m.request != req {
		return
	}
	m.role = "released"
	m.queue = slices.DeleteFunc(m.queue, func(x Stamp) bool { return x == req })
	m.clock++
	for peer := range m.peers {
		m.send(peer, "Lamport.Release")
	}
	m.logReport()
}

// deliver sends one peer's messages strictly one after another. A failed call (crashed
// node or blocked link) is retried, so everything behind it waits: that is the FIFO channel.
func (m *Mutex) deliver(ctx context.Context, peer string) {
	for {
		var out outgoing
		select {
		case <-ctx.Done():
			return
		case out = <-m.outbox[peer]:
		}
		for {
			err := m.peers[peer].Call(out.method, out.msg, &Empty{})
			if err == nil {
				break
			}
			fmt.Fprintf(os.Stderr, "%s -> %s: %v\n", out.method, peer, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		m.mu.Lock()
		m.pending--
		m.logReport()
		m.mu.Unlock()
	}
}

func (a *Application) Acquire(args AcquireArgs, reply *AcquireReply) error {
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	req, err := a.m.acquire(args.HoldMs)
	reply.Request = req.String()
	return err
}

func (a *Application) Auto(args AutoArgs, reply *Empty) error {
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	a.m.auto = args
	return a.m.report()
}

func (a *Application) Tick(args TickArgs, reply *TickReply) error {
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	a.m.clock += max(args.Events, 1)
	reply.Clock = a.m.clock
	return a.m.report()
}

// run starts one sender per peer and drives the optional load generator.
func (m *Mutex) run(ctx context.Context) error {
	for peer := range m.peers {
		go m.deliver(ctx, peer)
	}
	for {
		m.mu.Lock()
		auto := m.auto
		m.mu.Unlock()
		wait := max(auto.IntervalMs, 0)
		if wait == 0 {
			wait = 3000
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(wait/2+rand.Intn(wait)) * time.Millisecond):
		}
		m.mu.Lock()
		if m.auto.Enabled && m.role == "released" {
			if _, err := m.acquire(m.auto.HoldMs); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
		m.mu.Unlock()
	}
}
