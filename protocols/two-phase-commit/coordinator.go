package main

import (
	"context"
	"errors"
	"fmt"
	"net/rpc"
	"os"
	"slices"
	"sync"
	"time"

	"distvis/sdk"
	lab "distvis/sdk/rpc"
)

const (
	voteTimeout   = 3 * time.Second // a participant that has not voted by then counts as NO
	notifyTimeout = 2 * time.Second
)

// CTxn is the coordinator's log record for one transaction.
type CTxn struct {
	ID     string            `json:"id"`
	From   string            `json:"from"`
	To     string            `json:"to"`
	Amount int               `json:"amount"`
	State  string            `json:"state"` // preparing | committed | aborted
	Votes  map[string]string `json:"votes"`
	Acked  map[string]bool   `json:"acked"` // participants that confirmed a commit
}

func (t *CTxn) participants() []string { return []string{t.From, t.To} }

// coordLog is the coordinator's stable storage. The decision is saved before anyone is told.
type coordLog struct {
	Next int     `json:"next"`
	Txns []*CTxn `json:"txns"`
}

type Coordinator struct {
	mu    sync.Mutex
	node  *lab.Runtime
	peers map[string]*rpc.Client
	log   coordLog
	phase string // what the current transaction is doing, for the headline
}

type TransferArgs struct {
	From                  string `json:"from"`
	To                    string `json:"to"`
	Amount                int    `json:"amount"`
	PauseBeforeDecisionMs int    `json:"pauseBeforeDecisionMs"` // window to crash the coordinator after the votes
	PauseAfterDecisionMs  int    `json:"pauseAfterDecisionMs"`  // window to crash it after the commit point
}
type TransferReply struct {
	Txn string `json:"txn"`
}
type CoordinatorApp struct{ c *Coordinator }

func newCoordinator(node *lab.Runtime) (*Coordinator, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	c := &Coordinator{node: node, peers: peers}
	if err := sdk.Load(&c.log); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return c, nil
}

func (c *Coordinator) find(id string) *CTxn {
	for _, t := range c.log.Txns {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (c *Coordinator) report() error {
	txns := map[string]string{}
	for _, t := range c.log.Txns {
		txns[t.ID] = t.State
	}
	state := map[string]any{"role": "coordinator", "txns": txns}
	if c.phase != "" {
		state["phase"] = c.phase
	}
	if n := len(c.log.Txns); n > 0 {
		state["lastTxn"] = c.log.Txns[n-1]
	}
	return c.node.Report(state)
}

func (c *Coordinator) logReport() {
	if err := c.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (c *Coordinator) save() {
	if err := sdk.Save(c.log); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
	}
}

func (a *CoordinatorApp) Transfer(args TransferArgs, reply *TransferReply) error {
	c := a.c
	accounts := c.node.Nodes[1:]
	if !slices.Contains(accounts, args.From) || !slices.Contains(accounts, args.To) || args.From == args.To {
		return fmt.Errorf("from/to must be two different participants of %v", accounts)
	}
	if args.Amount <= 0 {
		return errors.New("amount must be positive")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log.Next++
	t := &CTxn{ID: fmt.Sprintf("T%d", c.log.Next), From: args.From, To: args.To, Amount: args.Amount,
		State: "preparing", Votes: map[string]string{}, Acked: map[string]bool{}}
	c.log.Txns = append(c.log.Txns, t)
	c.save()
	reply.Txn = t.ID
	go c.execute(t, args)
	return c.report()
}

// execute runs both phases of one transaction.
func (c *Coordinator) execute(t *CTxn, args TransferArgs) {
	type vote struct {
		peer string
		v    string
	}
	votes := make(chan vote, 2)
	for _, p := range t.participants() {
		delta := t.Amount
		if p == t.From {
			delta = -t.Amount
		}
		go func() {
			var r PrepareReply
			if err := call(c.peers[p], "RM.Prepare", PrepareArgs{Txn: t.ID, Delta: delta}, &r, voteTimeout); err != nil {
				votes <- vote{p, "timeout"}
			} else {
				votes <- vote{p, r.Vote}
			}
		}()
	}
	c.setPhase(t.ID + " phase 1: collecting votes")
	commit := true
	for range t.participants() {
		v := <-votes
		c.mu.Lock()
		t.Votes[v.peer] = v.v
		c.logReport()
		c.mu.Unlock()
		commit = commit && v.v == "yes"
	}
	if args.PauseBeforeDecisionMs > 0 {
		c.setPhase(t.ID + " votes in, decision pending")
		time.Sleep(time.Duration(args.PauseBeforeDecisionMs) * time.Millisecond)
	}
	c.mu.Lock()
	t.State = "aborted"
	if commit {
		t.State = "committed"
	}
	c.save() // commit point
	c.mu.Unlock()
	if args.PauseAfterDecisionMs > 0 {
		c.setPhase(t.ID + " " + t.State + ", not yet notified")
		time.Sleep(time.Duration(args.PauseAfterDecisionMs) * time.Millisecond)
	}
	c.setPhase(t.ID + " phase 2: notifying")
	c.notify(t)
	c.setPhase("")
}

func (c *Coordinator) setPhase(phase string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.phase = phase
	c.logReport()
}

// notify delivers the decision. A commit is retried until every participant confirms;
// an abort is sent once (presumed abort: a participant that misses it will ask and hear "aborted").
func (c *Coordinator) notify(t *CTxn) {
	var wg sync.WaitGroup
	for _, p := range t.participants() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				c.mu.Lock()
				state, done := t.State, t.Acked[p]
				c.mu.Unlock()
				if done {
					return
				}
				method := "RM.Commit"
				if state == "aborted" {
					method = "RM.Abort"
				}
				err := call(c.peers[p], method, TxnArgs{t.ID}, &Empty{}, notifyTimeout)
				if state == "aborted" {
					return
				}
				if err == nil {
					c.mu.Lock()
					t.Acked[p] = true
					c.save()
					c.logReport()
					c.mu.Unlock()
					return
				}
				time.Sleep(time.Second)
			}
		}()
	}
	wg.Wait()
}

// Status answers a prepared participant that has not heard the outcome.
func (c *Coordinator) Status(args TxnArgs, reply *StatusReply) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.find(args.Txn)
	if t == nil {
		reply.State = "aborted" // presumed abort: no record, no commit
		return nil
	}
	reply.State = t.State
	if t.State == "preparing" {
		reply.State = "pending"
	}
	return nil
}

// recover runs after every (re)start: undecided transactions are aborted, decided commits are re-sent.
func (c *Coordinator) recover(ctx context.Context) error {
	c.mu.Lock()
	var redo []*CTxn
	for _, t := range c.log.Txns {
		if t.State == "preparing" {
			t.State = "aborted"
			redo = append(redo, t)
		} else if t.State == "committed" && len(t.Acked) < 2 {
			redo = append(redo, t)
		}
	}
	if len(redo) > 0 {
		c.save()
		c.phase = fmt.Sprintf("recovery: %d txn(s)", len(redo))
		c.logReport()
	}
	c.mu.Unlock()
	if len(redo) > 0 {
		go func() {
			for _, t := range redo {
				c.notify(t)
			}
			c.setPhase("")
		}()
	}
	return nil
}
