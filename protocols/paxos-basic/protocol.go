package main

import (
	"encoding/json"
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

const (
	phaseTimeout = 6 * time.Second
	maxAttempts  = 40
)

// Ballot is a proposal number: rounds are compared first, ties broken by node index,
// so it prints like the slides' "round.node" (P 1.1, P 1.4).
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

type PrepareArgs struct {
	N Ballot `json:"n"`
}
type PrepareReply struct {
	OK        bool    `json:"ok"`
	Higher    *Ballot `json:"higher,omitempty"` // on reject: the number this acceptor already promised
	AcceptedN *Ballot `json:"acceptedN,omitempty"`
	AcceptedV string  `json:"acceptedV,omitempty"`
}
type AcceptArgs struct {
	N Ballot `json:"n"`
	V string `json:"v"`
}
type AcceptReply struct {
	OK     bool    `json:"ok"`
	Higher *Ballot `json:"higher,omitempty"`
}
type DecideArgs struct {
	V string `json:"v"`
}
type Empty struct{}

type ProposeArgs struct {
	Value   string `json:"value"`   // empty = 紫荆/澜园/桃李 by node index
	PauseMs int    `json:"pauseMs"` // wait between phase 1 and phase 2
	Retry   bool   `json:"retry"`   // after a rejection, retry with a higher number
}
type ProposeReply struct {
	N string `json:"n"`
}
type ToggleArgs struct {
	Enabled bool `json:"enabled"`
}

// durable is what an acceptor must not forget across a reboot.
type durable struct {
	Promised  Ballot
	AcceptedN Ballot
	AcceptedV string
	MaxRound  int
	Chosen    string
	Amnesia   bool
}

type Proposal struct {
	N          string `json:"n"`
	Value      string `json:"value"`
	Phase      string `json:"phase"` // prepare / paused / accept / done
	Promises   string `json:"promises"`
	Accepts    string `json:"accepts,omitempty"`
	Result     string `json:"result,omitempty"` // chosen / rejected / timeout
	RejectedBy string `json:"rejectedBy,omitempty"`
	Attempt    int    `json:"attempt"`
}

type Paxos struct {
	mu        sync.Mutex
	node      *lab.Runtime
	index     int
	peers     map[string]*rpc.Client
	st        durable
	conflict  string
	backoff   bool
	recovered bool
	proposing bool
	last      *Proposal
}
type Application struct{ p *Paxos }

func newPaxos(node *lab.Runtime) (*Paxos, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	p := &Paxos{node: node, index: slices.Index(node.Nodes, node.ID) + 1, peers: peers, backoff: true}
	err = sdk.Load(&p.st)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	p.recovered = err == nil
	return p, nil
}

func (p *Paxos) majority() int { return len(p.node.Nodes)/2 + 1 }

// persist must run before an acceptor replies. With amnesia on, only the flag itself is
// kept, so a rebooted acceptor comes back with no promise and no vote.
func (p *Paxos) persist() error {
	if p.st.Amnesia {
		return sdk.Save(durable{Amnesia: true})
	}
	return sdk.Save(p.st)
}

func (p *Paxos) report() error {
	role := "acceptor"
	if p.proposing {
		role = "proposer"
	}
	state := map[string]any{
		"role": role, "promised": p.st.Promised.String(), "accepted": "", "maxRound": p.st.MaxRound,
		"amnesia": p.st.Amnesia, "backoff": p.backoff, "recovered": p.recovered,
	}
	if !p.st.AcceptedN.IsZero() {
		state["accepted"] = p.st.AcceptedN.String() + " " + p.st.AcceptedV
	}
	if p.st.Chosen != "" {
		state["chosen"] = p.st.Chosen
	}
	if p.conflict != "" {
		state["conflictingDecide"] = p.conflict
	}
	if p.last != nil {
		state["lastProposal"] = *p.last
	}
	return p.node.Report(state)
}

func (p *Paxos) logReport() {
	if err := p.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (p *Paxos) see(round int) { p.st.MaxRound = max(p.st.MaxRound, round) }

func (p *Paxos) prepare(n Ballot) (PrepareReply, error) {
	p.see(n.Round)
	if !p.st.Promised.Less(n) {
		h := p.st.Promised
		return PrepareReply{Higher: &h}, nil
	}
	p.st.Promised = n
	r := PrepareReply{OK: true}
	if !p.st.AcceptedN.IsZero() {
		a := p.st.AcceptedN
		r.AcceptedN, r.AcceptedV = &a, p.st.AcceptedV
	}
	return r, p.persist()
}

func (p *Paxos) accept(n Ballot, v string) (AcceptReply, error) {
	p.see(n.Round)
	if n.Less(p.st.Promised) {
		h := p.st.Promised
		return AcceptReply{Higher: &h}, nil
	}
	p.st.Promised, p.st.AcceptedN, p.st.AcceptedV = n, n, v
	return AcceptReply{OK: true}, p.persist()
}

func (p *Paxos) learn(v string) error {
	if p.st.Chosen != "" && p.st.Chosen != v {
		p.conflict = v
		return nil
	}
	p.st.Chosen = v
	return p.persist()
}

func (p *Paxos) Prepare(args PrepareArgs, reply *PrepareReply) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, err := p.prepare(args.N)
	if err != nil {
		return err
	}
	*reply = r
	return p.report()
}

func (p *Paxos) Accept(args AcceptArgs, reply *AcceptReply) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, err := p.accept(args.N, args.V)
	if err != nil {
		return err
	}
	*reply = r
	return p.report()
}

func (p *Paxos) Decide(args DecideArgs, reply *Empty) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.learn(args.V); err != nil {
		return err
	}
	return p.report()
}

// broadcast sends one request to every peer and feeds each successful reply to handle
// until handle returns true (outcome known), all peers answered, or the phase times out.
// Unreachable acceptors are simply never counted.
func (p *Paxos) broadcast(method string, args any, newReply func() any, handle func(any) bool) bool {
	done := make(chan *rpc.Call, len(p.peers))
	for _, c := range p.peers {
		c.Go(method, args, newReply(), done)
	}
	timeout := time.After(phaseTimeout)
	for range len(p.peers) {
		select {
		case call := <-done:
			if call.Error == nil && handle(call.Reply) {
				return true
			}
		case <-timeout:
			return false
		}
	}
	return false
}

// attempt runs one proposal number through both phases and returns the proposal's result.
func (p *Paxos) attempt(own string, pause time.Duration, attempt int) string {
	p.mu.Lock()
	n := Ballot{p.st.MaxRound + 1, p.index}
	prop := &Proposal{N: n.String(), Value: own, Phase: "prepare", Attempt: attempt}
	p.last = prop
	total := len(p.node.Nodes)
	promises, rejected := 0, false
	var best Ballot
	value := own
	onPromise := func(r PrepareReply) bool {
		if !r.OK {
			rejected = true
			p.see(r.Higher.Round)
			prop.RejectedBy = r.Higher.String()
			return true
		}
		promises++
		if r.AcceptedN != nil && best.Less(*r.AcceptedN) {
			best, value = *r.AcceptedN, r.AcceptedV
		}
		prop.Promises = fmt.Sprintf("%d/%d", promises, total)
		return promises >= p.majority()
	}
	self, err := p.prepare(n)
	decided := err == nil && onPromise(self)
	p.logReport()
	p.mu.Unlock()
	if err != nil {
		return p.finish(prop, "error: "+err.Error())
	}
	if !decided {
		decided = p.broadcast("Paxos.Prepare", PrepareArgs{n}, func() any { return new(PrepareReply) }, func(r any) bool {
			p.mu.Lock()
			defer p.mu.Unlock()
			d := onPromise(*r.(*PrepareReply))
			p.logReport()
			return d
		})
	}
	if rejected {
		return p.finish(prop, "rejected")
	}
	if !decided {
		return p.finish(prop, "timeout")
	}

	if pause > 0 {
		p.mu.Lock()
		prop.Phase, prop.Value = "paused", value
		p.logReport()
		p.mu.Unlock()
		time.Sleep(pause)
	}

	p.mu.Lock()
	prop.Phase, prop.Value = "accept", value
	accepts := 0
	onAccept := func(r AcceptReply) bool {
		if !r.OK {
			rejected = true
			p.see(r.Higher.Round)
			prop.RejectedBy = r.Higher.String()
			return true
		}
		accepts++
		prop.Accepts = fmt.Sprintf("%d/%d", accepts, total)
		return accepts >= p.majority()
	}
	selfA, err := p.accept(n, value)
	decided = err == nil && onAccept(selfA)
	p.logReport()
	p.mu.Unlock()
	if err != nil {
		return p.finish(prop, "error: "+err.Error())
	}
	if !decided {
		decided = p.broadcast("Paxos.Accept", AcceptArgs{n, value}, func() any { return new(AcceptReply) }, func(r any) bool {
			p.mu.Lock()
			defer p.mu.Unlock()
			d := onAccept(*r.(*AcceptReply))
			p.logReport()
			return d
		})
	}
	if rejected {
		return p.finish(prop, "rejected")
	}
	if !decided {
		return p.finish(prop, "timeout")
	}
	p.mu.Lock()
	if err := p.learn(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	p.mu.Unlock()
	for _, c := range p.peers {
		c.Go("Paxos.Decide", DecideArgs{value}, &Empty{}, make(chan *rpc.Call, 1))
	}
	return p.finish(prop, "chosen")
}

func (p *Paxos) finish(prop *Proposal, result string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	prop.Phase, prop.Result = "done", result
	p.logReport()
	return result
}

func (p *Paxos) run(value string, pause time.Duration, retry bool) {
	for attempt := 1; ; attempt++ {
		result := p.attempt(value, pause, attempt)
		p.mu.Lock()
		stop := result == "chosen" || !retry || p.st.Chosen != "" || attempt >= maxAttempts
		wait := 0
		if p.backoff {
			wait = 200 + rand.Intn(800*min(attempt, 4))
		}
		if stop {
			p.proposing = false
			p.logReport()
		}
		p.mu.Unlock()
		if stop {
			return
		}
		time.Sleep(time.Duration(wait) * time.Millisecond)
	}
}

func (a *Application) Propose(args ProposeArgs, reply *ProposeReply) error {
	p := a.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proposing {
		return errors.New("本节点已有进行中的提案")
	}
	value := args.Value
	if value == "" {
		value = []string{"紫荆", "澜园", "桃李"}[(p.index-1)%3]
	}
	p.proposing = true
	reply.N = Ballot{p.st.MaxRound + 1, p.index}.String()
	go p.run(value, time.Duration(max(args.PauseMs, 0))*time.Millisecond, args.Retry)
	return p.report()
}

// Amnesia turns off persistence of promises and votes (the slides' "one machine reboots").
func (a *Application) Amnesia(args ToggleArgs, reply *Empty) error {
	p := a.p
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.Amnesia = args.Enabled
	if err := p.persist(); err != nil {
		return err
	}
	return p.report()
}

// Backoff toggles the randomized delay before a retry; without it dueling proposers can livelock.
func (a *Application) Backoff(args ToggleArgs, reply *Empty) error {
	p := a.p
	p.mu.Lock()
	defer p.mu.Unlock()
	p.backoff = args.Enabled
	return p.report()
}
