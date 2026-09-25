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
	initialBalance = 100
	askAfter       = 3 * time.Second // a prepared participant starts asking the coordinator after this
	askTimeout     = 1500 * time.Millisecond
)

type PTxn struct {
	Delta int    `json:"delta"`
	State string `json:"state"` // prepared | committed | aborted
}

// partLog is the participant's stable storage: the "prepared" record must survive a crash,
// because after voting YES the participant may no longer decide on its own.
type partLog struct {
	Balance int              `json:"balance"`
	Locked  string           `json:"locked"`
	Txns    map[string]*PTxn `json:"txns"`
}

type Participant struct {
	mu        sync.Mutex
	node      *lab.Runtime
	tm        *rpc.Client
	log       partLog
	policy    string    // auto | no
	uncertain bool      // the last outcome query to the coordinator failed
	since     time.Time // when the lock was taken; backdated at startup so a restarted participant asks at once
}

type VoteArgs struct {
	Policy string `json:"policy"` // auto | no
}
type ParticipantApp struct{ p *Participant }

func newParticipant(node *lab.Runtime) (*Participant, error) {
	tm, err := node.RPC(node.Nodes[0])
	if err != nil {
		return nil, err
	}
	p := &Participant{node: node, tm: tm, policy: "auto", since: time.Now().Add(-askAfter), log: partLog{Balance: initialBalance, Txns: map[string]*PTxn{}}}
	if err := sdk.Load(&p.log); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return p, nil
}

func (p *Participant) report() error {
	role := "idle"
	if p.log.Locked != "" {
		role = "prepared"
		if p.uncertain {
			role = "uncertain"
		}
	}
	txns := map[string]string{}
	for id, t := range p.log.Txns {
		txns[id] = t.State
	}
	return p.node.Report(map[string]any{
		"role": role, "balance": p.log.Balance, "locked": p.log.Locked, "vote": p.policy, "txns": txns,
	})
}

func (p *Participant) logReport() {
	if err := p.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (p *Participant) save() {
	if err := sdk.Save(p.log); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
	}
}

// Prepare validates the update, logs "prepared" and locks the account before voting YES.
func (p *Participant) Prepare(args PrepareArgs, reply *PrepareReply) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.log.Txns[args.Txn]; t != nil { // duplicate or late Prepare
		reply.Vote = "no"
		if t.State != "aborted" {
			reply.Vote = "yes"
		}
		return nil
	}
	switch {
	case p.policy == "no":
		reply.Vote, reply.Why = "no", "forced veto"
	case p.log.Locked != "":
		reply.Vote, reply.Why = "no", "locked by "+p.log.Locked
	case p.log.Balance+args.Delta < 0:
		reply.Vote, reply.Why = "no", fmt.Sprintf("balance %d", p.log.Balance)
	default:
		reply.Vote = "yes"
	}
	state := "aborted" // a NO vote lets the participant abort unilaterally
	if reply.Vote == "yes" {
		state = "prepared"
		p.log.Locked = args.Txn
		p.since = time.Now()
		p.uncertain = false
	}
	p.log.Txns[args.Txn] = &PTxn{Delta: args.Delta, State: state}
	p.save()
	return p.report()
}

func (p *Participant) Commit(args TxnArgs, reply *Empty) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finish(args.Txn, "committed")
}

func (p *Participant) Abort(args TxnArgs, reply *Empty) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finish(args.Txn, "aborted")
}

func (p *Participant) finish(id, outcome string) error {
	t := p.log.Txns[id]
	if t == nil {
		if outcome == "committed" {
			return errors.New("commit for unknown txn " + id)
		}
		p.log.Txns[id] = &PTxn{State: "aborted"}
	} else if t.State == "prepared" {
		t.State = outcome
		if outcome == "committed" {
			p.log.Balance += t.Delta
		}
		if p.log.Locked == id {
			p.log.Locked, p.uncertain = "", false
		}
	}
	p.save()
	return p.report()
}

// run is the termination protocol: while prepared, periodically ask the coordinator for the
// outcome. A prepared participant can neither commit nor abort by itself, so it keeps the lock.
func (p *Participant) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		p.mu.Lock()
		id := p.log.Locked
		due := id != "" && time.Since(p.since) >= askAfter
		p.mu.Unlock()
		if !due {
			continue
		}
		var r StatusReply
		err := call(p.tm, "TM.Status", TxnArgs{id}, &r, askTimeout)
		p.mu.Lock()
		if p.log.Locked == id {
			p.uncertain = err != nil
			if err == nil && (r.State == "committed" || r.State == "aborted") {
				p.finish(id, r.State)
			} else {
				p.logReport()
			}
		}
		p.mu.Unlock()
	}
}

func (a *ParticipantApp) SetVote(args VoteArgs, reply *Empty) error {
	if args.Policy != "auto" && args.Policy != "no" {
		return errors.New("policy must be auto or no")
	}
	a.p.mu.Lock()
	defer a.p.mu.Unlock()
	a.p.policy = args.Policy
	return a.p.report()
}
