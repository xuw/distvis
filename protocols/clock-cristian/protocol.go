package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/rpc"
	"os"
	"slices"
	"sync"
	"time"

	lab "distvis/sdk/rpc"
)

const serverID = "node-1"

type Empty struct{}
type TimeReply struct {
	T string `json:"t"` // server clock when it handled the request
}

type SyncArgs struct {
	MinMs int `json:"minMs"` // estimated minimum one-way delay, used only for the accuracy bound
}
type LastSync struct {
	ServerTime    string `json:"serverTime"`
	RTTMs         int64  `json:"rttMs"`
	ErrorBeforeMs int64  `json:"errorBeforeMs"`
	AdjustMs      int64  `json:"adjustMs"`
	ErrorAfterMs  int64  `json:"errorAfterMs"`
	BoundMs       int64  `json:"boundMs"` // accuracy RTT/2 - min
	WithinBound   bool   `json:"withinBound"`
}
type AutoArgs struct {
	PeriodMs int `json:"periodMs"` // 0 = off
	MinMs    int `json:"minMs"`
}
type DelayArgs struct {
	DelayMs int `json:"delayMs"` // server queueing/processing time before it reads its clock
}
type ClockArgs struct {
	OffsetMs      float64 `json:"offsetMs"`
	DriftMsPerSec float64 `json:"driftMsPerSec"`
}

type Cristian struct {
	mu       sync.Mutex
	node     *lab.Runtime
	clock    *Clock
	server   *rpc.Client
	delayMs  int
	served   int
	syncs    int
	last     *LastSync
	periodMs int
	autoGen  int
	peak     int64 // worst |error| since periodic sync started (-1: not tracking)
}
type Application struct{ c *Cristian }

func newCristian(node *lab.Runtime) (*Cristian, error) {
	c := &Cristian{node: node, clock: loadClock(slices.Index(node.Nodes, node.ID) + 1), peak: -1}
	if node.ID != serverID {
		client, err := node.RPC(serverID)
		if err != nil {
			return nil, err
		}
		c.server = client
	}
	return c, nil
}

func (c *Cristian) report() error {
	state := c.clock.fields(map[string]any{})
	if c.server == nil {
		state["role"] = "server"
		state["served"] = c.served
		state["delayMs"] = c.delayMs
		return c.node.Report(state)
	}
	state["role"] = "client"
	state["syncs"] = c.syncs
	state["autoPeriodMs"] = c.periodMs
	if c.last != nil {
		state["lastSync"] = c.last
	}
	if c.peak >= 0 {
		state["autoPeakMs"] = c.peak
	}
	return c.node.Report(state)
}

func (c *Cristian) logReport() {
	if err := c.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// Now is the time server: it reads its clock after the (optional) queueing delay.
func (c *Cristian) Now(args Empty, reply *TimeReply) error {
	c.mu.Lock()
	delay := c.delayMs
	c.mu.Unlock()
	time.Sleep(time.Duration(delay) * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	reply.T = stamp(c.clock.Now())
	c.served++
	return c.report()
}

// sync is one exchange: RTT is measured with the client's own clock, then the clock is set to t + RTT/2.
func (c *Cristian) sync(minMs int, auto bool) (*LastSync, error) {
	if c.server == nil {
		return nil, errors.New("node-1 是时间服务器，请在其他节点上同步")
	}
	c.mu.Lock()
	sent := c.clock.Now()
	c.mu.Unlock()
	var reply TimeReply
	if err := callTimeout(c.server, "Time.Now", Empty{}, &reply, 8*time.Second); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	recv := c.clock.Now()
	t, err := parseStamp(reply.T)
	if err != nil {
		return nil, err
	}
	rtt := float64(recv.Sub(sent)) / float64(time.Millisecond)
	adjust := float64(diffMs(t, msOfDay(recv))) + rtt/2
	before := c.clock.ErrorMs()
	c.clock.Step(adjust)
	after := c.clock.ErrorMs()
	bound := int64(math.Round(rtt/2)) - int64(minMs)
	c.last = &LastSync{
		ServerTime: reply.T, RTTMs: int64(math.Round(rtt)), ErrorBeforeMs: before, AdjustMs: int64(math.Round(adjust)),
		ErrorAfterMs: after, BoundMs: bound, WithinBound: abs(after) <= bound+2, // +2: millisecond rounding
	}
	c.syncs++
	if auto {
		if c.peak >= 0 {
			c.peak = max(c.peak, abs(before))
		} else {
			c.peak = 0
		}
	}
	return c.last, c.report()
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func (c *Cristian) autoLoop(gen, period, minMs int) {
	for {
		c.mu.Lock()
		stop := c.autoGen != gen
		c.mu.Unlock()
		if stop {
			return
		}
		if _, err := c.sync(minMs, true); err != nil {
			fmt.Fprintln(os.Stderr, "auto sync:", err)
		}
		time.Sleep(time.Duration(period) * time.Millisecond)
	}
}

// run reports once a second, since the simulated clock changes by itself.
func (c *Cristian) run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.mu.Lock()
			if c.peak >= 0 {
				c.peak = max(c.peak, abs(c.clock.ErrorMs()))
			}
			c.logReport()
			c.mu.Unlock()
		}
	}
}

func (a *Application) Sync(args SyncArgs, reply *LastSync) error {
	last, err := a.c.sync(args.MinMs, false)
	if last != nil {
		*reply = *last
	}
	return err
}

func (a *Application) AutoSync(args AutoArgs, reply *Empty) error {
	c := a.c
	if c.server == nil {
		return errors.New("node-1 是时间服务器")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.autoGen++
	c.periodMs, c.peak = max(args.PeriodMs, 0), -1
	if c.periodMs > 0 {
		go c.autoLoop(c.autoGen, max(c.periodMs, 200), args.MinMs)
	}
	return c.report()
}

func (a *Application) SetDelay(args DelayArgs, reply *Empty) error {
	a.c.mu.Lock()
	defer a.c.mu.Unlock()
	a.c.delayMs = min(max(args.DelayMs, 0), 5000)
	return a.c.report()
}

func (a *Application) SetClock(args ClockArgs, reply *Empty) error {
	a.c.mu.Lock()
	defer a.c.mu.Unlock()
	a.c.clock.Set(args.OffsetMs, args.DriftMsPerSec)
	return a.c.report()
}
