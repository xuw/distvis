package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"distvis/sdk"
	lab "distvis/sdk/rpc"
)

// Stamp is the total order used by the queue: Lamport time, ties broken by sender index.
type Stamp struct {
	L  int
	ID int
}

func (a Stamp) Less(b Stamp) bool { return a.L < b.L || (a.L == b.L && a.ID < b.ID) }
func (a Stamp) String() string    { return fmt.Sprintf("%d.%d", a.L, a.ID) }

type UpdateArgs struct {
	L     int    `json:"L"`
	From  string `json:"from"`
	Op    string `json:"op"` // deposit / interest
	Value int    `json:"value"`
}
type AckArgs struct {
	L    int    `json:"L"`
	From string `json:"from"`
	Msg  string `json:"msg"` // acknowledged message, "L.id"
}
type Empty struct{}

type DepositArgs struct {
	Amount int `json:"amount"`
}
type InterestArgs struct {
	Percent int `json:"percent"`
}
type ModeArgs struct {
	Mode string `json:"mode"` // ordered / naive
}
type StampReply struct {
	Stamp string `json:"stamp"`
}

// Entry is a queued update. An ACK can overtake the update itself (it travels on another
// link), so an entry may exist with acks but without the operation yet (Known=false).
type Entry struct {
	Stamp Stamp
	Known bool
	Op    string
	Value int
	Acks  []string
}

func opString(op string, value int) string {
	if op == "interest" {
		return fmt.Sprintf("+%d%%", value)
	}
	return fmt.Sprintf("%+d", value)
}

// State is everything that sdk.Save persists, so a recovered replica keeps its balance and queue.
type State struct {
	Mode      string
	Clock     int
	Cents     int64
	Queue     []*Entry
	Delivered []string // "L.id op", in delivery order
	Seen      map[string]bool
	Updates   int
	Acks      int
}

type Replica struct {
	mu    sync.Mutex
	node  *lab.Runtime
	index int
	links map[string]*link
	s     State
}
type Application struct{ r *Replica }

func newReplica(node *lab.Runtime) (*Replica, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	r := &Replica{node: node, index: slices.Index(node.Nodes, node.ID) + 1, links: map[string]*link{}}
	for id, client := range peers {
		r.links[id] = newLink(id, client)
	}
	r.reset("ordered")
	if err := sdk.Load(&r.s); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return r, nil
}

func (r *Replica) reset(mode string) {
	r.s = State{Mode: mode, Clock: r.s.Clock, Cents: 100000, Seen: map[string]bool{}}
}

func (r *Replica) indexOf(id string) int { return slices.Index(r.node.Nodes, id) + 1 }

func (r *Replica) report() error {
	queue := []string{}
	for _, e := range r.s.Queue {
		op := "?"
		if e.Known {
			op = opString(e.Op, e.Value)
		}
		queue = append(queue, fmt.Sprintf("%s %s acks=%d/%d", e.Stamp, op, len(e.Acks), len(r.node.Nodes)))
	}
	role := "replica"
	if len(queue) > 0 {
		role = "waiting"
	}
	return r.node.Report(map[string]any{
		"role": role, "mode": r.s.Mode, "clock": r.s.Clock, "balance": float64(r.s.Cents) / 100,
		"queue": queue, "delivered": r.s.Delivered[max(0, len(r.s.Delivered)-10):], "deliveredCount": len(r.s.Delivered),
		"updatesSent": r.s.Updates, "acksSent": r.s.Acks, "messagesSent": r.s.Updates + r.s.Acks,
	})
}

// commit persists and reports; called with the lock held after every change.
func (r *Replica) commit() error {
	if err := sdk.Save(r.s); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
	}
	return r.report()
}

func (r *Replica) multicast(op string, value int) (Stamp, error) {
	r.s.Clock++
	stamp := Stamp{r.s.Clock, r.index}
	args := UpdateArgs{L: stamp.L, From: r.node.ID, Op: op, Value: value}
	for _, l := range r.links {
		r.s.Updates++
		l.push("TOM.Update", args)
	}
	r.receive(args)
	return stamp, r.commit()
}

// receive handles an update from any sender, including this node's own.
func (r *Replica) receive(args UpdateArgs) {
	stamp := Stamp{args.L, r.indexOf(args.From)}
	if r.s.Seen[stamp.String()] {
		return // a retried send that had already arrived
	}
	if r.s.Mode == "naive" {
		r.s.Seen[stamp.String()] = true
		r.apply(stamp, args.Op, args.Value)
		return
	}
	e := r.entry(stamp)
	if e.Known {
		return
	}
	e.Known, e.Op, e.Value = true, args.Op, args.Value
	r.ack(stamp)
	r.deliver()
}

func (r *Replica) entry(stamp Stamp) *Entry {
	for _, e := range r.s.Queue {
		if e.Stamp == stamp {
			return e
		}
	}
	e := &Entry{Stamp: stamp}
	i, _ := slices.BinarySearchFunc(r.s.Queue, e, func(a, b *Entry) int {
		if a.Stamp.Less(b.Stamp) {
			return -1
		}
		return 1
	})
	r.s.Queue = slices.Insert(r.s.Queue, i, e)
	return e
}

// ack multicasts the acknowledgement; its timestamp is larger than the update's.
func (r *Replica) ack(stamp Stamp) {
	r.s.Clock++
	args := AckArgs{L: r.s.Clock, From: r.node.ID, Msg: stamp.String()}
	for _, l := range r.links {
		r.s.Acks++
		l.push("TOM.Ack", args)
	}
	r.acked(stamp, r.node.ID)
}

func (r *Replica) acked(stamp Stamp, from string) {
	e := r.entry(stamp)
	if !slices.Contains(e.Acks, from) {
		e.Acks = append(e.Acks, from)
		slices.Sort(e.Acks)
	}
}

// deliver applies queue heads that every process has acknowledged.
func (r *Replica) deliver() {
	for len(r.s.Queue) > 0 {
		head := r.s.Queue[0]
		if !head.Known || len(head.Acks) < len(r.node.Nodes) {
			return
		}
		r.s.Queue = r.s.Queue[1:]
		r.s.Seen[head.Stamp.String()] = true
		r.apply(head.Stamp, head.Op, head.Value)
	}
}

func (r *Replica) apply(stamp Stamp, op string, value int) {
	if op == "interest" {
		r.s.Cents = (r.s.Cents*int64(100+value) + 50) / 100
	} else {
		r.s.Cents += int64(value) * 100
	}
	r.s.Delivered = append(r.s.Delivered, stamp.String()+" "+opString(op, value))
}

func (r *Replica) Update(args UpdateArgs, reply *Empty) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s.Clock = max(r.s.Clock, args.L) + 1
	r.receive(args)
	return r.commit()
}

func (r *Replica) Ack(args AckArgs, reply *Empty) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s.Clock = max(r.s.Clock, args.L) + 1
	stamp, err := parseStamp(args.Msg)
	if err != nil {
		return err
	}
	if r.s.Mode == "naive" || r.s.Seen[args.Msg] {
		return r.report()
	}
	r.acked(stamp, args.From)
	r.deliver()
	return r.commit()
}

func parseStamp(s string) (Stamp, error) {
	l, id, _ := strings.Cut(s, ".")
	L, err1 := strconv.Atoi(l)
	ID, err2 := strconv.Atoi(id)
	if err1 != nil || err2 != nil {
		return Stamp{}, fmt.Errorf("bad stamp %q", s)
	}
	return Stamp{L, ID}, nil
}

func (a *Application) Deposit(args DepositArgs, reply *StampReply) error {
	if args.Amount == 0 {
		return errors.New("amount 不能为 0")
	}
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	stamp, err := a.r.multicast("deposit", args.Amount)
	reply.Stamp = stamp.String()
	return err
}

func (a *Application) Interest(args InterestArgs, reply *StampReply) error {
	if args.Percent <= 0 {
		return errors.New("percent 应为正整数")
	}
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	stamp, err := a.r.multicast("interest", args.Percent)
	reply.Stamp = stamp.String()
	return err
}

// Mode switches this replica's delivery rule and resets its account to 1000.
func (a *Application) Mode(args ModeArgs, reply *Empty) error {
	if args.Mode != "ordered" && args.Mode != "naive" {
		return errors.New("mode 只能是 ordered 或 naive")
	}
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	a.r.reset(args.Mode)
	return a.r.commit()
}

// run starts the per-peer senders. After a crash the persisted queue survives but the
// outgoing messages did not, so the replica re-sends its own queued updates and its ACKs.
func (r *Replica) run(ctx context.Context) error {
	r.mu.Lock()
	for _, e := range r.s.Queue {
		if !e.Known {
			continue
		}
		for _, l := range r.links {
			if e.Stamp.ID == r.index {
				l.push("TOM.Update", UpdateArgs{L: e.Stamp.L, From: r.node.ID, Op: e.Op, Value: e.Value})
			}
			l.push("TOM.Ack", AckArgs{L: r.s.Clock, From: r.node.ID, Msg: e.Stamp.String()})
		}
	}
	r.mu.Unlock()
	for _, l := range r.links {
		go l.run(ctx)
	}
	<-ctx.Done()
	return ctx.Err()
}
