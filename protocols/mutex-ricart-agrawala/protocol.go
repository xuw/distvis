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

// Stamp is a totally ordered Lamport timestamp: ties on L are broken by node index,
// so it prints like the slides' "L.id" notation.
type Stamp struct {
	L  int
	ID int
}

func (a Stamp) Less(b Stamp) bool { return a.L < b.L || (a.L == b.L && a.ID < b.ID) }
func (a Stamp) String() string    { return fmt.Sprintf("%d.%d", a.L, a.ID) }

type RequestArgs struct {
	L    int    `json:"L"`
	From string `json:"from"`
}
type RequestReply struct {
	OK bool `json:"ok"` // false: the receiver deferred its OK until it leaves the CS
	L  int  `json:"L"`
}
type OKArgs struct {
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

type Mutex struct {
	mu       sync.Mutex
	node     *lab.Runtime
	index    int
	peers    map[string]*rpc.Client
	clock    int
	role     string // released / wanted / critical
	request  Stamp
	oks      map[string]bool
	deferred []string
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
	return &Mutex{node: node, index: slices.Index(node.Nodes, node.ID) + 1, peers: peers, role: "released", oks: map[string]bool{}}, nil
}

func (m *Mutex) indexOf(id string) int { return slices.Index(m.node.Nodes, id) + 1 }

func (m *Mutex) report() error {
	state := map[string]any{
		"role": m.role, "clock": m.clock, "entries": m.entries, "messagesSent": m.sent,
		"deferred": append([]string{}, m.deferred...), "lastWaitMs": m.lastWait,
		"auto": m.auto.Enabled,
	}
	if m.role != "released" {
		oks := []string{}
		for peer := range m.oks {
			oks = append(oks, peer)
		}
		slices.Sort(oks)
		state["request"] = m.request.String()
		state["okFrom"] = oks
		state["okNeeded"] = len(m.peers)
	}
	return m.node.Report(state)
}

func (m *Mutex) logReport() {
	if err := m.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// Request handles another node's broadcast. The reply is the OK message unless this node
// is in the CS or wants it with an earlier timestamp; then the OK is sent later by leave().
func (m *Mutex) Request(args RequestArgs, reply *RequestReply) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clock = max(m.clock, args.L) + 1
	theirs := Stamp{args.L, m.indexOf(args.From)}
	if m.role == "critical" || (m.role == "wanted" && m.request.Less(theirs)) {
		m.deferred = append(m.deferred, args.From)
		reply.OK = false
	} else {
		reply.OK = true
	}
	reply.L = m.clock
	return m.report()
}

// OK is the deferred reply, sent when the other node leaves its critical section.
func (m *Mutex) OK(args OKArgs, reply *Empty) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clock = max(m.clock, args.L) + 1
	m.granted(args.From)
	return m.report()
}

func (m *Mutex) granted(peer string) {
	if m.role != "wanted" {
		return
	}
	m.oks[peer] = true
	if len(m.oks) < len(m.peers) {
		return
	}
	m.role, m.entries = "critical", m.entries+1
	m.lastWait = time.Since(m.since).Milliseconds()
	req := m.request
	time.AfterFunc(m.hold, func() { m.leave(req) })
}

func (m *Mutex) leave(req Stamp) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.role != "critical" || m.request != req {
		return
	}
	m.role = "released"
	m.clock++
	for _, peer := range m.deferred {
		m.sent++
		go m.call(peer, "RA.OK", OKArgs{m.clock, m.node.ID}, &Empty{})
	}
	m.deferred = nil
	m.logReport()
}

func (m *Mutex) acquire(hold int) (Stamp, error) {
	if m.role != "released" {
		return Stamp{}, errors.New("已有未完成的请求：" + m.role)
	}
	if hold <= 0 {
		hold = 2000
	}
	m.clock++
	m.role, m.request, m.oks = "wanted", Stamp{m.clock, m.index}, map[string]bool{}
	m.hold, m.since = time.Duration(hold)*time.Millisecond, time.Now()
	for peer := range m.peers {
		m.sent++
		go m.ask(peer, m.request)
	}
	if len(m.peers) == 0 {
		m.granted("")
	}
	return m.request, m.report()
}

// ask sends one request. A failed call (crashed node or blocked link) is retried with the
// same timestamp, so the requester stays blocked until the peer answers.
func (m *Mutex) ask(peer string, req Stamp) {
	for {
		var reply RequestReply
		err := m.call(peer, "RA.Request", RequestArgs{req.L, m.node.ID}, &reply)
		m.mu.Lock()
		if m.role != "wanted" || m.request != req {
			m.mu.Unlock()
			return
		}
		if err == nil {
			m.clock = max(m.clock, reply.L) + 1
			if reply.OK {
				m.granted(peer)
			}
			m.logReport()
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		time.Sleep(time.Second)
	}
}

func (m *Mutex) call(peer, method string, args, reply any) error {
	err := m.peers[peer].Call(method, args, reply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s -> %s: %v\n", method, peer, err)
	}
	return err
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

// run drives the optional load generator: each node requests the CS at random intervals.
func (m *Mutex) run(ctx context.Context) error {
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
