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

	"distvis/sdk"
	lab "distvis/sdk/rpc"
)

// A vote request that is not answered within voteTimeout counts as DENY (crashed voter).
const voteTimeout = 2 * time.Second

// Try identifies one attempt of one requester, so a Release that arrives late can never
// free a vote that the same requester got in a later attempt.
type VoteArgs struct {
	From string `json:"from"`
	Try  int    `json:"try"`
}
type VoteReply struct {
	Grant bool `json:"grant"` // false = DENY: this node's vote is held by someone else
}
type Empty struct{}

type AcquireArgs struct {
	HoldMs int `json:"holdMs"` // time spent inside the critical section; 0 = 2000
}
type AcquireReply struct {
	Needed int `json:"needed"`
}
type AutoArgs struct {
	Enabled    bool `json:"enabled"`
	IntervalMs int  `json:"intervalMs"` // mean pause between requests; 0 = 3000
	HoldMs     int  `json:"holdMs"`
}
type PersistArgs struct {
	Enabled bool `json:"enabled"`
}

// disk is what survives a crash. Incarnation is always kept (it only counts reboots);
// the vote itself is restored only when persist was on.
type disk struct {
	Incarnation int
	Persist     bool
	VotedFor    string
	VotedTry    int
}

type Majority struct {
	mu    sync.Mutex
	node  *lab.Runtime
	peers map[string]*rpc.Client
	need  int
	disk  disk

	role     string // released / wanted / critical
	phase    string // voting / backoff while wanted
	try      int
	attempts int
	grants   map[string]bool
	answered int
	retries  int
	hold     time.Duration
	since    time.Time
	entries  int
	lastWait int64
	sent     int
	auto     AutoArgs
}

type Voter struct{ m *Majority }
type Application struct{ m *Majority }

func newMajority(node *lab.Runtime) (*Majority, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	m := &Majority{node: node, peers: peers, need: len(node.Nodes)/2 + 1, role: "released", grants: map[string]bool{}}
	if err := sdk.Load(&m.disk); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if !m.disk.Persist {
		m.disk.VotedFor, m.disk.VotedTry = "", 0
	}
	m.disk.Incarnation++
	return m, m.save()
}

func (m *Majority) save() error { return sdk.Save(m.disk) }

func (m *Majority) report() error {
	state := map[string]any{
		"role": m.role, "votedFor": m.disk.VotedFor, "needed": m.need,
		"entries": m.entries, "retries": m.retries, "lastWaitMs": m.lastWait, "messagesSent": m.sent,
		"persist": m.disk.Persist, "incarnation": m.disk.Incarnation, "auto": m.auto.Enabled,
	}
	if m.role != "released" {
		votes := []string{}
		for peer := range m.grants {
			votes = append(votes, peer)
		}
		slices.Sort(votes)
		state["votes"] = votes
		state["attempts"] = m.attempts
	}
	if m.role == "wanted" {
		state["phase"] = m.phase
	}
	return m.node.Report(state)
}

func (m *Majority) logReport() {
	if err := m.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// ---- voter: every node is a coordinator with one vote ----

func (v *Voter) Request(args VoteArgs, reply *VoteReply) error {
	m := v.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disk.VotedFor == "" || m.disk.VotedFor == args.From {
		m.disk.VotedFor, m.disk.VotedTry = args.From, args.Try
		reply.Grant = true
		if err := m.save(); err != nil {
			return err
		}
	}
	return m.report()
}

func (v *Voter) Release(args VoteArgs, reply *Empty) error {
	m := v.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disk.VotedFor == args.From && m.disk.VotedTry == args.Try {
		m.disk.VotedFor, m.disk.VotedTry = "", 0
		if err := m.save(); err != nil {
			return err
		}
	}
	return m.report()
}

// ---- requester ----

func (m *Majority) acquire(hold int) error {
	if m.role != "released" {
		return errors.New("已有未完成的请求：" + m.role)
	}
	if hold <= 0 {
		hold = 2000
	}
	m.role, m.attempts = "wanted", 0
	m.hold, m.since = time.Duration(hold)*time.Millisecond, time.Now()
	m.attempt()
	return m.report()
}

// attempt asks every node (its own vote locally, the others by RPC) for its vote.
func (m *Majority) attempt() {
	m.try++
	m.attempts++
	m.phase, m.grants, m.answered = "voting", map[string]bool{}, 0
	me := m.node.ID
	if m.disk.VotedFor == "" {
		m.disk.VotedFor, m.disk.VotedTry = me, m.try
		m.grants[me] = true
		if err := m.save(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	for peer := range m.peers {
		m.sent++
		go m.ask(peer, m.try)
	}
	m.check()
}

func (m *Majority) ask(peer string, try int) {
	var reply VoteReply
	call := m.peers[peer].Go("Vote.Request", VoteArgs{m.node.ID, try}, &reply, make(chan *rpc.Call, 1))
	select {
	case <-call.Done:
		m.answer(peer, try, call.Error == nil && reply.Grant)
	case <-time.After(voteTimeout):
		m.answer(peer, try, false)
		// The request may still be granted later: give such a late vote straight back.
		<-call.Done
		if call.Error == nil && reply.Grant {
			m.mu.Lock()
			m.giveBack(peer, try)
			m.mu.Unlock()
		}
	}
}

func (m *Majority) answer(peer string, try int, grant bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case try == m.try && m.role == "wanted" && m.phase == "voting":
		m.answered++
		if grant {
			m.grants[peer] = true
		}
		m.check()
		m.logReport()
	case try == m.try && m.role == "critical" && grant:
		m.grants[peer] = true // arrived after we already had a majority; returned on exit
		m.logReport()
	case grant:
		m.giveBack(peer, try)
	}
}

func (m *Majority) check() {
	if len(m.grants) >= m.need {
		m.role, m.entries = "critical", m.entries+1
		m.lastWait = time.Since(m.since).Milliseconds()
		try := m.try
		time.AfterFunc(m.hold, func() { m.leave(try) })
		return
	}
	if m.answered < len(m.peers) {
		return
	}
	// Not enough votes: return the ones we got and retry after a random backoff.
	m.retries++
	m.returnVotes()
	m.phase = "backoff"
	try := m.try
	time.AfterFunc(time.Duration(400+rand.Intn(1600))*time.Millisecond, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.try == try && m.role == "wanted" {
			m.attempt()
			m.logReport()
		}
	})
}

func (m *Majority) returnVotes() {
	for peer := range m.grants {
		m.giveBack(peer, m.try)
	}
	m.grants = map[string]bool{}
}

func (m *Majority) giveBack(peer string, try int) {
	if peer == m.node.ID {
		if m.disk.VotedFor == peer && m.disk.VotedTry == try {
			m.disk.VotedFor, m.disk.VotedTry = "", 0
			if err := m.save(); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
		return
	}
	m.sent++
	go func() {
		if err := m.peers[peer].Call("Vote.Release", VoteArgs{m.node.ID, try}, &Empty{}); err != nil {
			fmt.Fprintf(os.Stderr, "Vote.Release -> %s: %v\n", peer, err)
		}
	}()
}

func (m *Majority) leave(try int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.role != "critical" || m.try != try {
		return
	}
	m.returnVotes()
	m.role = "released"
	m.logReport()
}

// ---- application ----

func (a *Application) Acquire(args AcquireArgs, reply *AcquireReply) error {
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	reply.Needed = a.m.need
	return a.m.acquire(args.HoldMs)
}

func (a *Application) Auto(args AutoArgs, reply *Empty) error {
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	a.m.auto = args
	return a.m.report()
}

// Persist makes this node's vote survive a crash (written with sdk.Save on every change).
func (a *Application) Persist(args PersistArgs, reply *Empty) error {
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	a.m.disk.Persist = args.Enabled
	if err := a.m.save(); err != nil {
		return err
	}
	return a.m.report()
}

// run drives the optional load generator.
func (m *Majority) run(ctx context.Context) error {
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
			if err := m.acquire(m.auto.HoldMs); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
		m.mu.Unlock()
	}
}
