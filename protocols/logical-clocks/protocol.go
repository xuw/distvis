package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"net/rpc"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	lab "distvis/sdk/rpc"
)

type Event struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // local / send / recv
	Label string `json:"label,omitempty"`
	L     int    `json:"L"`
	V     string `json:"V"`
	Total string `json:"total"` // "L.id": ties broken by process index
	vec   []int
}

type MessageArgs struct {
	L     int    `json:"L"`
	V     []int  `json:"V"`
	From  string `json:"from"`
	Label string `json:"label,omitempty"` // label of the receive event
}
type MessageReply struct {
	ID string `json:"id"` // the receive event
}
type EventArgs struct {
	ID string `json:"id"`
}
type EventReply struct {
	L int   `json:"L"`
	V []int `json:"V"`
}
type Empty struct{}

type LocalArgs struct {
	Label string `json:"label"`
}
type SendArgs struct {
	To        string `json:"to"`
	Label     string `json:"label"`
	RecvLabel string `json:"recvLabel"` // label of the receive event at the target; optional
}
type EventID struct {
	ID string `json:"id"`
}
type CompareArgs struct {
	A string `json:"a"` // event id "node-1:3" = third event of node-1
	B string `json:"b"`
}
type AutoArgs struct {
	Enabled    bool `json:"enabled"`
	IntervalMs int  `json:"intervalMs"` // mean pause between random events; 0 = 1500
}

// Comparison is what Compare reports: Lamport order vs. causal order of two events.
type Comparison struct {
	A           string `json:"a"`
	B           string `json:"b"`
	La          int    `json:"La"`
	Lb          int    `json:"Lb"`
	Va          string `json:"Va"`
	Vb          string `json:"Vb"`
	Lamport     string `json:"lamport"` // "<", "=", ">"
	LamportLess bool   `json:"lamportLess"`
	Vector      string `json:"vector"` // "a→b", "b→a", "concurrent", "same"
	Total       string `json:"total"`  // order of the total "L.id" stamps
	Agrees      bool   `json:"agrees"` // Lamport order says exactly what causality says
	Note        string `json:"note"`
}

type Process struct {
	mu      sync.Mutex
	node    *lab.Runtime
	index   int
	peers   map[string]*rpc.Client
	lamport int
	vector  []int
	events  []Event
	last    *Comparison
	auto    AutoArgs
}
type Application struct{ p *Process }

func newProcess(node *lab.Runtime) (*Process, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	return &Process{node: node, index: slices.Index(node.Nodes, node.ID) + 1, peers: peers, vector: make([]int, len(node.Nodes))}, nil
}

func vecString(v []int) string {
	parts := make([]string, len(v))
	for i, c := range v {
		parts[i] = strconv.Itoa(c)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (p *Process) report() error {
	recent := p.events[max(0, len(p.events)-10):]
	state := map[string]any{
		"role": "process", "lamport": p.lamport, "vector": vecString(p.vector),
		"eventCount": len(p.events), "events": recent, "auto": p.auto.Enabled,
	}
	if p.last != nil {
		state["lastCompare"] = p.last
	}
	return p.node.Report(state)
}

func (p *Process) logReport() {
	if err := p.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// event records one event; the clocks have already been advanced by the caller.
func (p *Process) event(kind, label string) Event {
	e := Event{
		ID: fmt.Sprintf("%s:%d", p.node.ID, len(p.events)+1), Kind: kind, Label: label,
		L: p.lamport, V: vecString(p.vector), Total: fmt.Sprintf("%d.%d", p.lamport, p.index),
		vec: slices.Clone(p.vector),
	}
	p.events = append(p.events, e)
	return e
}

// tick is rule 1 for both clocks: increment before each event.
func (p *Process) tick() {
	p.lamport++
	p.vector[p.index-1]++
}

func (p *Process) local(label string) Event {
	p.tick()
	return p.event("local", label)
}

func (p *Process) send(to, label, recvLabel string) (Event, error) {
	client := p.peers[to]
	if client == nil {
		return Event{}, fmt.Errorf("未知目标节点 %q", to)
	}
	p.tick()
	e := p.event("send", label)
	args := MessageArgs{L: e.L, V: slices.Clone(e.vec), From: p.node.ID, Label: recvLabel}
	go func() {
		if err := client.Call("Clock.Receive", args, &MessageReply{}); err != nil {
			fmt.Fprintf(os.Stderr, "send to %s: %v\n", to, err)
		}
	}()
	return e, nil
}

// Receive is rule 2: L := max(L, t) then rule 1; V := elementwise max, then own entry +1.
func (p *Process) Receive(args MessageArgs, reply *MessageReply) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lamport = max(p.lamport, args.L)
	for i := range p.vector {
		if i < len(args.V) {
			p.vector[i] = max(p.vector[i], args.V[i])
		}
	}
	p.tick()
	label := args.Label
	if label == "" {
		label = "from " + args.From
	}
	reply.ID = p.event("recv", label).ID
	return p.report()
}

// Event returns the timestamps of one of this node's events, for Compare on another node.
func (p *Process) Event(args EventArgs, reply *EventReply) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, err := p.own(args.ID)
	if err != nil {
		return err
	}
	*reply = EventReply{L: e.L, V: slices.Clone(e.vec)}
	return nil
}

func (p *Process) own(id string) (Event, error) {
	node, seq, ok := parseID(id)
	if !ok || node != p.node.ID {
		return Event{}, fmt.Errorf("事件 %q 不属于 %s", id, p.node.ID)
	}
	if seq < 1 || seq > len(p.events) {
		return Event{}, fmt.Errorf("%s 还没有第 %d 个事件", node, seq)
	}
	return p.events[seq-1], nil
}

func parseID(id string) (string, int, bool) {
	node, n, ok := strings.Cut(strings.TrimSpace(id), ":")
	seq, err := strconv.Atoi(n)
	return node, seq, ok && err == nil
}

// lookup fetches an event's timestamps locally or from its owner. Called without the lock.
func (p *Process) lookup(id string) (EventReply, error) {
	node, _, ok := parseID(id)
	if !ok {
		return EventReply{}, fmt.Errorf("事件编号应形如 node-1:3，而不是 %q", id)
	}
	if node == p.node.ID {
		p.mu.Lock()
		defer p.mu.Unlock()
		e, err := p.own(id)
		return EventReply{L: e.L, V: slices.Clone(e.vec)}, err
	}
	client := p.peers[node]
	if client == nil {
		return EventReply{}, fmt.Errorf("未知节点 %q", node)
	}
	var reply EventReply
	call := client.Go("Clock.Event", EventArgs{id}, &reply, nil)
	select {
	case <-call.Done:
		return reply, call.Error
	case <-time.After(5 * time.Second):
		return reply, fmt.Errorf("%s 没有回应", node)
	}
}

// happensBefore reports V(a) < V(b): every entry <=, and the vectors differ.
func happensBefore(a, b []int) bool {
	for i := range a {
		if a[i] > b[i] {
			return false
		}
	}
	return !slices.Equal(a, b)
}

func compare(a, b string, ea, eb EventReply) Comparison {
	c := Comparison{A: a, B: b, La: ea.L, Lb: eb.L, Va: vecString(ea.V), Vb: vecString(eb.V), LamportLess: ea.L < eb.L}
	switch {
	case ea.L < eb.L:
		c.Lamport = "<"
	case ea.L > eb.L:
		c.Lamport = ">"
	default:
		c.Lamport = "="
	}
	switch {
	case slices.Equal(ea.V, eb.V):
		c.Vector = "same"
	case happensBefore(ea.V, eb.V):
		c.Vector = "a→b"
	case happensBefore(eb.V, ea.V):
		c.Vector = "b→a"
	default:
		c.Vector = "concurrent"
	}
	ia, ib := nodeIndex(a), nodeIndex(b)
	ta, tb := fmt.Sprintf("%d.%d", ea.L, ia), fmt.Sprintf("%d.%d", eb.L, ib)
	switch {
	case ea.L < eb.L || (ea.L == eb.L && ia < ib):
		c.Total = ta + " < " + tb
	case ta == tb:
		c.Total = ta + " = " + tb
	default:
		c.Total = ta + " > " + tb
	}
	switch c.Vector {
	case "a→b":
		c.Agrees = c.Lamport == "<"
		c.Note = "a→b，因此 L(a)<L(b)（时钟条件）"
	case "b→a":
		c.Agrees = c.Lamport == ">"
		c.Note = "b→a，因此 L(a)>L(b)（时钟条件）"
	case "same":
		c.Agrees = true
		c.Note = "同一个事件"
	default:
		c.Agrees = false
		if c.Lamport == "=" {
			c.Note = "并发：L 相等，只能用 L.id 任意定序"
		} else {
			c.Note = "并发：L 不同，但 L 的大小不代表先后；只有向量时钟能判断"
		}
	}
	return c
}

func nodeIndex(id string) int {
	node, _, _ := parseID(id)
	n, _ := strconv.Atoi(strings.TrimPrefix(node, "node-"))
	return n
}

func (a *Application) Local(args LocalArgs, reply *EventID) error {
	a.p.mu.Lock()
	defer a.p.mu.Unlock()
	reply.ID = a.p.local(args.Label).ID
	return a.p.report()
}

func (a *Application) Send(args SendArgs, reply *EventID) error {
	a.p.mu.Lock()
	defer a.p.mu.Unlock()
	e, err := a.p.send(args.To, args.Label, args.RecvLabel)
	if err != nil {
		return err
	}
	reply.ID = e.ID
	return a.p.report()
}

func (a *Application) Compare(args CompareArgs, reply *Comparison) error {
	if args.A == "" || args.B == "" {
		return errors.New("需要两个事件编号 a、b，例如 node-1:2")
	}
	ea, err := a.p.lookup(args.A)
	if err != nil {
		return err
	}
	eb, err := a.p.lookup(args.B)
	if err != nil {
		return err
	}
	c := compare(args.A, args.B, ea, eb)
	a.p.mu.Lock()
	defer a.p.mu.Unlock()
	a.p.last = &c
	*reply = c
	return a.p.report()
}

func (a *Application) Auto(args AutoArgs, reply *Empty) error {
	a.p.mu.Lock()
	defer a.p.mu.Unlock()
	a.p.auto = args
	return a.p.report()
}

// run drives the optional random workload: local events and sends to random peers.
func (p *Process) run(ctx context.Context) error {
	for {
		p.mu.Lock()
		wait := p.auto.IntervalMs
		p.mu.Unlock()
		if wait <= 0 {
			wait = 1500
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(wait/2+rand.Intn(wait)) * time.Millisecond):
		}
		p.mu.Lock()
		if p.auto.Enabled {
			if peers := slices.Sorted(maps.Keys(p.peers)); rand.Intn(2) == 0 && len(peers) > 0 {
				_, _ = p.send(peers[rand.Intn(len(peers))], "", "")
			} else {
				p.local("")
			}
			p.logReport()
		}
		p.mu.Unlock()
	}
}
