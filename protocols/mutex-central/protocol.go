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

type RequestArgs struct {
	From string `json:"from"`
}
type RequestReply struct {
	Queued int `json:"queued"` // position in the coordinator's queue; 0 = granted right away
}
type GrantReply struct {
	OK bool `json:"ok"` // false: the client no longer wants the lock
}
type ReleaseArgs struct {
	From string `json:"from"`
}
type Empty struct{}

type AcquireArgs struct {
	HoldMs int `json:"holdMs"` // time spent inside the critical section; 0 = 2000
}
type AcquireReply struct {
	Coordinator string `json:"coordinator"`
}
type AutoArgs struct {
	Enabled    bool `json:"enabled"`
	IntervalMs int  `json:"intervalMs"` // mean pause between requests; 0 = 3000
	HoldMs     int  `json:"holdMs"`
}
type PersistArgs struct {
	Enabled bool `json:"enabled"`
}

// saved is the coordinator's state on disk (only written while persist is on).
type saved struct {
	Persist bool
	Holder  string
	Queue   []string
}

type Central struct {
	mu    sync.Mutex
	node  *lab.Runtime
	peers map[string]*rpc.Client
	coord string
	sent  int

	// coordinator (node-1)
	holder  string
	queue   []string
	granted []string
	persist bool
	boot    string

	// client
	role      string // released / wanted / critical
	seq       int
	hold      time.Duration
	since     time.Time
	entries   int
	lastWait  int64
	releasing bool
	auto      AutoArgs
}

type Coord struct{ c *Central }
type Client struct{ c *Central }
type Application struct{ c *Central }

func newCentral(node *lab.Runtime) (*Central, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	c := &Central{node: node, peers: peers, coord: node.Nodes[0], role: "released", boot: "fresh"}
	if c.isCoord() {
		c.role = "leader"
		var s saved
		if err := sdk.Load(&s); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if s.Persist {
			c.persist, c.holder, c.queue, c.boot = true, s.Holder, s.Queue, "restored"
		}
	}
	return c, nil
}

func (c *Central) isCoord() bool { return c.node.ID == c.coord }

func (c *Central) report() error {
	state := map[string]any{"role": c.role, "messagesSent": c.sent}
	if c.isCoord() {
		state["holder"] = c.holder
		state["queue"] = append([]string{}, c.queue...)
		state["grantOrder"] = append([]string{}, c.granted...)
		state["persist"] = c.persist
		state["bootState"] = c.boot
	} else {
		state["coordinator"] = c.coord
		state["entries"] = c.entries
		state["lastWaitMs"] = c.lastWait
		state["auto"] = c.auto.Enabled
	}
	return c.node.Report(state)
}

func (c *Central) logReport() {
	if err := c.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (c *Central) save() error {
	if !c.persist {
		return nil
	}
	return sdk.Save(saved{true, c.holder, c.queue})
}

func (c *Central) call(peer, method string, args, reply any) error {
	err := c.peers[peer].Call(method, args, reply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s -> %s: %v\n", method, peer, err)
	}
	return err
}

// ---- coordinator ----

// Request queues the client; a repeated request (a client retrying after a timeout) is not queued twice.
func (s *Coord) Request(args RequestArgs, reply *RequestReply) error {
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isCoord() {
		return errors.New("不是协调者")
	}
	switch {
	case c.holder == args.From:
	case slices.Contains(c.queue, args.From):
		reply.Queued = slices.Index(c.queue, args.From) + 1
	case c.holder == "":
		c.grant(args.From)
	default:
		c.queue = append(c.queue, args.From)
		reply.Queued = len(c.queue)
	}
	if err := c.save(); err != nil {
		return err
	}
	return c.report()
}

func (s *Coord) Release(args ReleaseArgs, reply *Empty) error {
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isCoord() {
		return errors.New("不是协调者")
	}
	if c.holder == args.From {
		c.next()
	}
	if err := c.save(); err != nil {
		return err
	}
	return c.report()
}

func (c *Central) next() {
	c.holder = ""
	if len(c.queue) > 0 {
		j := c.queue[0]
		c.queue = c.queue[1:]
		c.grant(j)
	}
}

func (c *Central) grant(to string) {
	c.holder = to
	c.granted = append(c.granted, to)
	c.sent++
	go c.sendGrant(to)
}

// sendGrant retries until the holder answers; a client that no longer wants the lock refuses it.
func (c *Central) sendGrant(to string) {
	for {
		var reply GrantReply
		err := c.call(to, "Client.Grant", Empty{}, &reply)
		c.mu.Lock()
		if c.holder != to {
			c.mu.Unlock()
			return
		}
		if err == nil {
			if !reply.OK {
				c.next()
				if err := c.save(); err != nil {
					fmt.Fprintln(os.Stderr, err)
				}
				c.logReport()
			}
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		time.Sleep(time.Second)
	}
}

// ---- client ----

func (s *Client) Grant(args Empty, reply *GrantReply) error {
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.role {
	case "wanted":
		c.role, c.entries = "critical", c.entries+1
		c.lastWait = time.Since(c.since).Milliseconds()
		seq := c.seq
		time.AfterFunc(c.hold, func() { c.leave(seq) })
		reply.OK = true
	case "critical":
		reply.OK = true // duplicate grant from a coordinator that restored its state
	}
	return c.report()
}

func (c *Central) acquire(hold int) error {
	if c.isCoord() {
		return errors.New("node-1 是协调者，请在其他节点申请")
	}
	if c.role != "released" {
		return errors.New("已有未完成的请求：" + c.role)
	}
	if c.releasing {
		return errors.New("上一次 Release 还没有送达协调者")
	}
	if hold <= 0 {
		hold = 2000
	}
	c.seq++
	c.role, c.since, c.hold = "wanted", time.Now(), time.Duration(hold)*time.Millisecond
	c.sent++
	go c.request(c.seq)
	return c.report()
}

// request is retried while the coordinator is unreachable; the coordinator ignores duplicates.
func (c *Central) request(seq int) {
	for {
		var reply RequestReply
		if c.call(c.coord, "Coord.Request", RequestArgs{c.node.ID}, &reply) == nil {
			return
		}
		time.Sleep(time.Second)
		c.mu.Lock()
		stale := c.seq != seq || c.role != "wanted"
		c.mu.Unlock()
		if stale {
			return
		}
	}
}

func (c *Central) leave(seq int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.role != "critical" || c.seq != seq {
		return
	}
	c.role, c.releasing = "released", true
	c.sent++
	go func() {
		for c.call(c.coord, "Coord.Release", ReleaseArgs{c.node.ID}, &Empty{}) != nil {
			time.Sleep(time.Second)
		}
		c.mu.Lock()
		c.releasing = false
		c.mu.Unlock()
	}()
	c.logReport()
}

// ---- application ----

func (a *Application) Acquire(args AcquireArgs, reply *AcquireReply) error {
	a.c.mu.Lock()
	defer a.c.mu.Unlock()
	reply.Coordinator = a.c.coord
	return a.c.acquire(args.HoldMs)
}

func (a *Application) Auto(args AutoArgs, reply *Empty) error {
	a.c.mu.Lock()
	defer a.c.mu.Unlock()
	if a.c.isCoord() {
		return errors.New("node-1 是协调者，不申请锁")
	}
	a.c.auto = args
	return a.c.report()
}

// Persist makes the coordinator write holder and queue to disk on every change.
func (a *Application) Persist(args PersistArgs, reply *Empty) error {
	c := a.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isCoord() {
		return errors.New("只有协调者 node-1 有需要持久化的状态")
	}
	c.persist = args.Enabled
	if !args.Enabled {
		if err := sdk.Save(saved{}); err != nil {
			return err
		}
	} else if err := c.save(); err != nil {
		return err
	}
	return c.report()
}

// run re-sends a restored grant and drives the optional load generator.
func (c *Central) run(ctx context.Context) error {
	c.mu.Lock()
	if c.isCoord() && c.holder != "" {
		c.sent++
		go c.sendGrant(c.holder)
	}
	c.mu.Unlock()
	for {
		c.mu.Lock()
		auto := c.auto
		c.mu.Unlock()
		wait := max(auto.IntervalMs, 0)
		if wait == 0 {
			wait = 3000
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(wait/2+rand.Intn(wait)) * time.Millisecond):
		}
		c.mu.Lock()
		if c.auto.Enabled && c.role == "released" && !c.releasing {
			if err := c.acquire(c.auto.HoldMs); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
		c.mu.Unlock()
	}
}
