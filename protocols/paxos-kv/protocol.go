package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/rpc"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"distvis/sdk"
	lab "distvis/sdk/rpc"
)

const (
	phaseTimeout  = 1500 * time.Millisecond
	submitTimeout = 6 * time.Second
	fillDelay     = time.Second
)

// Ballot is a proposal number "round.node". Every log slot has its own, independent numbers.
type Ballot struct {
	Round int
	Node  int
}

func (b Ballot) Less(o Ballot) bool {
	return b.Round < o.Round || (b.Round == o.Round && b.Node < o.Node)
}
func (b Ballot) IsZero() bool { return b == Ballot{} }
func (b Ballot) String() string {
	if b.IsZero() {
		return ""
	}
	return fmt.Sprintf("%d.%d", b.Round, b.Node)
}
func (b Ballot) MarshalJSON() ([]byte, error) { return json.Marshal(b.String()) }
func (b *Ballot) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*b = Ballot{}
	if s == "" {
		return nil
	}
	_, err := fmt.Sscanf(s, "%d.%d", &b.Round, &b.Node)
	return err
}

// An operation travels as one string, e.g. "put a=1 @2.5": kind, argument and a unique id
// (node index . per-node sequence), so a server can tell whether its own op won a slot.
const noop = "noop"

func parseOp(op string) (kind, key, value string) {
	body := op
	if i := strings.LastIndex(op, " @"); i >= 0 {
		body = op[:i]
	}
	kind, arg, _ := strings.Cut(body, " ")
	key, value, _ = strings.Cut(arg, "=")
	return kind, key, value
}
func showOp(op string) string {
	if i := strings.LastIndex(op, " @"); i >= 0 {
		return op[:i]
	}
	return op
}

type PrepareArgs struct {
	Slot int    `json:"slot"`
	N    Ballot `json:"n"`
}
type PrepareReply struct {
	OK        bool    `json:"ok"`
	Higher    *Ballot `json:"higher,omitempty"`
	AcceptedN *Ballot `json:"acceptedN,omitempty"`
	AcceptedV string  `json:"acceptedV,omitempty"`
}
type AcceptArgs struct {
	Slot int    `json:"slot"`
	N    Ballot `json:"n"`
	Op   string `json:"op"`
}
type AcceptReply struct {
	OK     bool    `json:"ok"`
	Higher *Ballot `json:"higher,omitempty"`
}
type DecideArgs struct {
	Slot int    `json:"slot"`
	Op   string `json:"op"`
}
type Empty struct{}

type PutArgs struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type PutReply struct {
	Slot int `json:"slot"`
}
type GetArgs struct {
	Key string `json:"key"`
}
type GetReply struct {
	Slot  int    `json:"slot"`
	Value string `json:"value"`
}

// Instance is the acceptor state of one log slot.
type Instance struct {
	Promised  Ballot
	AcceptedN Ballot
	AcceptedV string
	MaxRound  int
}

// durable is saved before every acceptor reply: promises, votes and the decided log.
// The key-value store is not saved; it is rebuilt by replaying the log.
type durable struct {
	Slots map[int]*Instance
	Log   map[int]string
	Seq   int
}

type KV struct {
	mu         sync.Mutex
	client     sync.Mutex // one client operation at a time per server
	node       *lab.Runtime
	index      int
	peers      map[string]*rpc.Client
	st         durable
	applied    int
	store      map[string]string
	gets       map[string]string // get op → value it read
	lastResult string
	conflicts  int
	proposing  int
	filling    map[int]bool
	recovered  bool
}
type Application struct{ kv *KV }

func newKV(node *lab.Runtime) (*KV, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	kv := &KV{node: node, index: slices.Index(node.Nodes, node.ID) + 1, peers: peers,
		store: map[string]string{}, gets: map[string]string{}, filling: map[int]bool{}}
	err = sdk.Load(&kv.st)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	kv.recovered = err == nil
	if kv.st.Slots == nil {
		kv.st.Slots = map[int]*Instance{}
	}
	if kv.st.Log == nil {
		kv.st.Log = map[int]string{}
	}
	kv.apply()
	return kv, nil
}

func (kv *KV) majority() int  { return len(kv.node.Nodes)/2 + 1 }
func (kv *KV) persist() error { return sdk.Save(kv.st) }

func (kv *KV) inst(slot int) *Instance {
	i := kv.st.Slots[slot]
	if i == nil {
		i = &Instance{}
		kv.st.Slots[slot] = i
	}
	return i
}

func (kv *KV) report() error {
	last := 0
	for s := range kv.st.Log {
		last = max(last, s)
	}
	log := []string{}
	for s := 1; s <= last; s++ {
		op, ok := kv.st.Log[s]
		if !ok {
			op = "?"
		}
		log = append(log, fmt.Sprintf("%d:%s", s, showOp(op)))
	}
	state := map[string]any{
		"role": "replica", "log": log, "appliedIndex": kv.applied, "store": kv.store,
		"lastResult": kv.lastResult, "conflicts": kv.conflicts, "recovered": kv.recovered,
	}
	if kv.proposing > 0 {
		state["proposingSlot"] = kv.proposing
	}
	if len(kv.filling) > 0 {
		state["catchingUp"] = len(kv.filling)
	}
	return kv.node.Report(state)
}

func (kv *KV) logReport() {
	if err := kv.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// apply executes decided slots strictly in log order, stopping at the first gap.
func (kv *KV) apply() {
	for {
		op, ok := kv.st.Log[kv.applied+1]
		if !ok {
			return
		}
		kv.applied++
		switch kind, key, value := parseOp(op); kind {
		case "put":
			kv.store[key] = value
		case "get":
			kv.gets[op] = kv.store[key]
		}
	}
}

func (kv *KV) learn(slot int, op string) error {
	if old, ok := kv.st.Log[slot]; ok {
		if old != op {
			fmt.Fprintf(os.Stderr, "slot %d: already decided %q, got %q\n", slot, old, op)
		}
		return nil
	}
	kv.st.Log[slot] = op
	if err := kv.persist(); err != nil {
		return err
	}
	kv.apply()
	return nil
}

// notice is called for every message about a slot. Traffic for a later slot means this
// server has missed decisions (e.g. it was crashed); it fills each gap by running Paxos
// with a no-op there: Prepare reveals the value already chosen, which then wins again.
func (kv *KV) notice(slot int) {
	for s := kv.applied + 1; s < slot; s++ {
		if _, ok := kv.st.Log[s]; !ok && !kv.filling[s] && s != kv.proposing {
			kv.filling[s] = true
			go kv.fill(s)
		}
	}
}

func (kv *KV) fill(slot int) {
	time.Sleep(fillDelay) // the Decide may simply still be on its way
	if _, err := kv.runPaxos(slot, noop, time.Now().Add(submitTimeout)); err != nil {
		fmt.Fprintln(os.Stderr, "catch-up:", err)
	}
	kv.mu.Lock()
	delete(kv.filling, slot)
	kv.logReport()
	kv.mu.Unlock()
}

func (kv *KV) prepare(slot int, n Ballot) (PrepareReply, error) {
	i := kv.inst(slot)
	i.MaxRound = max(i.MaxRound, n.Round)
	if !i.Promised.Less(n) {
		h := i.Promised
		return PrepareReply{Higher: &h}, nil
	}
	i.Promised = n
	r := PrepareReply{OK: true}
	if !i.AcceptedN.IsZero() {
		a := i.AcceptedN
		r.AcceptedN, r.AcceptedV = &a, i.AcceptedV
	}
	return r, kv.persist()
}

func (kv *KV) accept(slot int, n Ballot, op string) (AcceptReply, error) {
	i := kv.inst(slot)
	i.MaxRound = max(i.MaxRound, n.Round)
	if n.Less(i.Promised) {
		h := i.Promised
		return AcceptReply{Higher: &h}, nil
	}
	i.Promised, i.AcceptedN, i.AcceptedV = n, n, op
	return AcceptReply{OK: true}, kv.persist()
}

func (kv *KV) Prepare(args PrepareArgs, reply *PrepareReply) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	r, err := kv.prepare(args.Slot, args.N)
	if err != nil {
		return err
	}
	*reply = r
	kv.notice(args.Slot)
	return kv.report()
}

func (kv *KV) Accept(args AcceptArgs, reply *AcceptReply) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	r, err := kv.accept(args.Slot, args.N, args.Op)
	if err != nil {
		return err
	}
	*reply = r
	kv.notice(args.Slot)
	return kv.report()
}

func (kv *KV) Decide(args DecideArgs, reply *Empty) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if err := kv.learn(args.Slot, args.Op); err != nil {
		return err
	}
	kv.notice(args.Slot)
	return kv.report()
}

type vote struct {
	ok        bool
	higher    *Ballot
	acceptedN *Ballot
	acceptedV string
}

// phase counts the local acceptor's vote, then asks every peer, until a majority said ok,
// someone rejected (returns the higher number), or the phase timed out (returns nil, nil).
func (kv *KV) phase(self vote, method string, args any, newReply func() any, conv func(any) vote, deadline time.Time) ([]vote, *Ballot) {
	if !self.ok {
		return nil, self.higher
	}
	oks := []vote{self}
	if len(oks) >= kv.majority() {
		return oks, nil
	}
	done := make(chan *rpc.Call, len(kv.peers))
	for _, c := range kv.peers {
		c.Go(method, args, newReply(), done)
	}
	timer := time.NewTimer(max(min(phaseTimeout, time.Until(deadline)), 0))
	defer timer.Stop()
	for range len(kv.peers) {
		select {
		case call := <-done:
			if call.Error != nil {
				continue
			}
			v := conv(call.Reply)
			if !v.ok {
				return nil, v.higher
			}
			if oks = append(oks, v); len(oks) >= kv.majority() {
				return oks, nil
			}
		case <-timer.C:
			return nil, nil
		}
	}
	return nil, nil
}

// runPaxos drives one slot's Paxos instance, proposing op, until some value is chosen for
// the slot (returned, possibly another server's op) or the deadline passes.
func (kv *KV) runPaxos(slot int, op string, deadline time.Time) (string, error) {
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		kv.mu.Lock()
		if v, ok := kv.st.Log[slot]; ok {
			kv.mu.Unlock()
			return v, nil
		}
		n := Ballot{kv.inst(slot).MaxRound + 1, kv.index}
		self, err := kv.prepare(slot, n)
		kv.mu.Unlock()
		if err != nil {
			return "", err
		}
		oks, higher := kv.phase(vote{self.OK, self.Higher, self.AcceptedN, self.AcceptedV},
			"Paxos.Prepare", PrepareArgs{slot, n}, func() any { return new(PrepareReply) },
			func(r any) vote { p := r.(*PrepareReply); return vote{p.OK, p.Higher, p.AcceptedN, p.AcceptedV} }, deadline)
		if oks != nil {
			value, best := op, Ballot{}
			for _, v := range oks {
				if v.acceptedN != nil && best.Less(*v.acceptedN) {
					best, value = *v.acceptedN, v.acceptedV
				}
			}
			kv.mu.Lock()
			selfA, err := kv.accept(slot, n, value)
			kv.mu.Unlock()
			if err != nil {
				return "", err
			}
			oks, higher = kv.phase(vote{ok: selfA.OK, higher: selfA.Higher},
				"Paxos.Accept", AcceptArgs{slot, n, value}, func() any { return new(AcceptReply) },
				func(r any) vote { a := r.(*AcceptReply); return vote{ok: a.OK, higher: a.Higher} }, deadline)
			if oks != nil {
				kv.mu.Lock()
				err := kv.learn(slot, value)
				kv.logReport()
				kv.mu.Unlock()
				for _, c := range kv.peers {
					c.Go("Paxos.Decide", DecideArgs{slot, value}, &Empty{}, make(chan *rpc.Call, 1))
				}
				return value, err
			}
		}
		if higher != nil {
			kv.mu.Lock()
			kv.inst(slot).MaxRound = max(kv.inst(slot).MaxRound, higher.Round)
			kv.mu.Unlock()
		}
		wait := time.Duration(50+rand.Intn(200*min(attempt, 5))) * time.Millisecond
		time.Sleep(max(min(wait, time.Until(deadline)), 0))
	}
	return "", fmt.Errorf("slot %d 在 %v 内没有得到多数派", slot, submitTimeout)
}

// submit puts op into the lowest slot not yet known as decided. If another op wins that
// slot, it has been applied, and the server tries again at the next slot.
func (kv *KV) submit(kind, key, value string) (int, string, error) {
	if key == "" || strings.ContainsAny(key, " =") || strings.Contains(value, " @") {
		return 0, "", errors.New("key 不能为空或包含空格、=")
	}
	kv.client.Lock()
	defer kv.client.Unlock()
	kv.mu.Lock()
	kv.st.Seq++
	op := fmt.Sprintf("%s %s @%d.%d", kind, key, kv.index, kv.st.Seq)
	if kind == "put" {
		op = fmt.Sprintf("%s %s=%s @%d.%d", kind, key, value, kv.index, kv.st.Seq)
	}
	err := kv.persist()
	kv.mu.Unlock()
	if err != nil {
		return 0, "", err
	}
	deadline := time.Now().Add(submitTimeout)
	for {
		kv.mu.Lock()
		slot := kv.applied + 1
		kv.proposing = slot
		kv.logReport()
		kv.mu.Unlock()
		chosen, err := kv.runPaxos(slot, op, deadline)
		kv.mu.Lock()
		if err != nil || chosen == op {
			kv.proposing = 0
			result := kv.gets[op]
			switch {
			case err != nil:
				kv.lastResult = fmt.Sprintf("%s 失败：%v", showOp(op), err)
			case kind == "get":
				kv.lastResult = fmt.Sprintf("get %s = %s (slot %d)", key, result, slot)
			default:
				kv.lastResult = fmt.Sprintf("%s (slot %d)", showOp(op), slot)
			}
			kv.logReport()
			kv.mu.Unlock()
			return slot, result, err
		}
		kv.conflicts++
		kv.mu.Unlock()
	}
}

func (a *Application) Put(args PutArgs, reply *PutReply) error {
	slot, _, err := a.kv.submit("put", args.Key, args.Value)
	reply.Slot = slot
	return err
}

func (a *Application) Get(args GetArgs, reply *GetReply) error {
	slot, value, err := a.kv.submit("get", args.Key, "")
	reply.Slot, reply.Value = slot, value
	return err
}
