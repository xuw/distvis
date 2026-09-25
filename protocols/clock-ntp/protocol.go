package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/rpc"
	"os"
	"slices"
	"sync"
	"time"

	lab "distvis/sdk/rpc"
)

const burstSize = 8

type QueryArgs struct {
	T0 string `json:"t0"` // client transmit time
}
type QueryReply struct {
	T1 string `json:"t1"` // server receive time
	T2 string `json:"t2"` // server transmit time
}
type Empty struct{}

type SyncReply struct {
	Upstream     string `json:"upstream"`
	DelayMs      int64  `json:"delayMs"`
	OffsetMs     int64  `json:"offsetMs"`
	ErrorAfterMs int64  `json:"errorAfterMs"`
}
type AutoArgs struct {
	PeriodMs int `json:"periodMs"` // pause between bursts; 0 = off
}
type ServerArgs struct {
	DelayMs  int `json:"delayMs"`  // processing time between t1 and t2
	JitterMs int `json:"jitterMs"` // random extra delay (0..jitterMs) before t1 and after t2
}
type ClockArgs struct {
	OffsetMs      float64 `json:"offsetMs"`
	DriftMsPerSec float64 `json:"driftMsPerSec"`
}

type sample struct {
	from          string
	offset, delay float64
	proc          int64
}

type LastSync struct {
	Upstream       string   `json:"upstream"`
	DelayMs        int64    `json:"delayMs"`
	OffsetMs       int64    `json:"offsetMs"`
	ServerProcMs   int64    `json:"serverProcMs"`
	BoundMs        int64    `json:"boundMs"`
	ErrorBeforeMs  int64    `json:"errorBeforeMs"`
	ErrorAfterMs   int64    `json:"errorAfterMs"`
	WithinBound    bool     `json:"withinBound"`
	SampleSpreadMs int64    `json:"sampleSpreadMs"`
	Samples        []string `json:"samples"`
}

type NTP struct {
	mu        sync.Mutex
	node      *lab.Runtime
	clock     *Clock
	stratum   int
	upstreams []string
	peers     map[string]*rpc.Client
	delayMs   int
	jitterMs  int
	served    int
	last      *LastSync
	bursts    int
	sumErr    float64
	sumSample float64
	samples   int
	periodMs  int
	autoGen   int
	syncing   bool
}
type Application struct{ n *NTP }

// Strata: node-1 has UTC (stratum 1); node-2/3 sync from node-1; node-4+ sync from node-2 and node-3.
func newNTP(node *lab.Runtime) (*NTP, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	index := slices.Index(node.Nodes, node.ID) + 1
	n := &NTP{node: node, clock: loadClock(index), peers: peers}
	switch {
	case index == 1:
		n.stratum = 1
	case index <= 3:
		n.stratum, n.upstreams = 2, []string{node.Nodes[0]}
	default:
		n.stratum, n.upstreams = 3, []string{node.Nodes[1], node.Nodes[2]}
	}
	return n, nil
}

func (n *NTP) report() error {
	state := n.clock.fields(map[string]any{
		"role": fmt.Sprintf("stratum-%d", n.stratum), "stratum": n.stratum, "served": n.served,
	})
	if n.delayMs > 0 || n.jitterMs > 0 {
		state["serverDelayMs"], state["serverJitterMs"] = n.delayMs, n.jitterMs
	}
	if n.stratum > 1 {
		state["upstreams"] = n.upstreams
		state["autoPeriodMs"] = n.periodMs
		state["bursts"] = n.bursts
	}
	if n.last != nil {
		state["lastSync"] = n.last
	}
	if n.bursts > 0 {
		state["avgErrMs"] = int64(math.Round(n.sumErr / float64(n.bursts)))
		state["avgSampleErrMs"] = int64(math.Round(n.sumSample / float64(n.samples)))
	}
	return n.node.Report(state)
}

func (n *NTP) logReport() {
	if err := n.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// jitter simulates variable network delay: a sleep before t1 or after t2 is indistinguishable
// from a slower packet. The cubed uniform is mostly small with occasional long queues.
func jitter(maxMs int) {
	if maxMs > 0 {
		u := rand.Float64()
		time.Sleep(msDur(float64(maxMs) * u * u * u))
	}
}

// Query is the server side of one NTP exchange; the processing time t2-t1 is measured by the client.
func (n *NTP) Query(args QueryArgs, reply *QueryReply) error {
	n.mu.Lock()
	delay, jit := n.delayMs, n.jitterMs
	n.mu.Unlock()
	jitter(jit)
	n.mu.Lock()
	reply.T1 = stamp(n.clock.Now())
	n.served++
	n.mu.Unlock()
	time.Sleep(time.Duration(delay) * time.Millisecond)
	n.mu.Lock()
	reply.T2 = stamp(n.clock.Now())
	n.mu.Unlock()
	jitter(jit)
	return nil
}

func (n *NTP) exchange(peer string) (sample, error) {
	n.mu.Lock()
	t0 := n.clock.Now()
	n.mu.Unlock()
	var reply QueryReply
	if err := callTimeout(n.peers[peer], "NTP.Query", QueryArgs{stamp(t0)}, &reply, 5*time.Second); err != nil {
		return sample{}, err
	}
	n.mu.Lock()
	t3 := n.clock.Now()
	n.mu.Unlock()
	t1, err1 := parseStamp(reply.T1)
	t2, err2 := parseStamp(reply.T2)
	if err := errors.Join(err1, err2); err != nil {
		return sample{}, err
	}
	T0, T3 := msOfDay(t0), msOfDay(t3)
	return sample{
		from:   peer,
		offset: float64(diffMs(t1, T0)+diffMs(t2, T3)) / 2,
		delay:  float64(diffMs(T3, T0) - diffMs(t2, t1)),
		proc:   diffMs(t2, t1),
	}, nil
}

// sync runs one burst: 8 exchanges with every upstream in parallel, then applies the sample with
// the minimum round-trip delay.
func (n *NTP) sync() (*LastSync, error) {
	n.mu.Lock()
	if n.stratum == 1 {
		n.mu.Unlock()
		return nil, errors.New("stratum 1 直接接收 UTC，不需要同步")
	}
	if n.syncing {
		n.mu.Unlock()
		return nil, errors.New("上一轮同步尚未结束")
	}
	n.syncing = true
	upstreams := n.upstreams
	n.mu.Unlock()
	defer func() { n.mu.Lock(); n.syncing = false; n.mu.Unlock() }()

	var wg sync.WaitGroup
	results := make([]sample, len(upstreams)*burstSize)
	ok := make([]bool, len(results))
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := n.exchange(upstreams[i/burstSize])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return
			}
			results[i], ok[i] = s, true
		}()
	}
	wg.Wait()
	var got []sample
	for i, s := range results {
		if ok[i] {
			got = append(got, s)
		}
	}
	if len(got) == 0 {
		return nil, errors.New("所有上游都没有回复")
	}
	best := slices.MinFunc(got, func(a, b sample) int { return int(a.delay - b.delay) })

	n.mu.Lock()
	defer n.mu.Unlock()
	before := n.clock.ErrorMs()
	n.clock.Step(best.offset)
	after := n.clock.ErrorMs()
	lo, hi := math.Inf(1), math.Inf(-1)
	lines := make([]string, 0, len(got))
	for _, s := range got {
		lo, hi = math.Min(lo, s.offset), math.Max(hi, s.offset)
		// Error this sample alone would have left: applying it instead of best shifts the clock by the difference.
		n.sumSample += math.Abs(float64(after) + s.offset - best.offset)
		mark := ""
		if s == best {
			mark = " ←"
		}
		lines = append(lines, fmt.Sprintf("%s d=%.0f o=%+.0f%s", s.from, s.delay, s.offset, mark))
	}
	n.samples += len(got)
	n.bursts++
	n.sumErr += math.Abs(float64(after))
	bound := int64(math.Round(best.delay / 2))
	n.last = &LastSync{
		Upstream: best.from, DelayMs: int64(math.Round(best.delay)), OffsetMs: int64(math.Round(best.offset)),
		ServerProcMs: best.proc, BoundMs: bound, ErrorBeforeMs: before, ErrorAfterMs: after,
		WithinBound: abs(after) <= bound+2, SampleSpreadMs: int64(math.Round(hi - lo)), Samples: lines,
	}
	return n.last, n.report()
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func (n *NTP) autoLoop(gen, period int) {
	for {
		n.mu.Lock()
		stop := n.autoGen != gen
		n.mu.Unlock()
		if stop {
			return
		}
		if _, err := n.sync(); err != nil {
			fmt.Fprintln(os.Stderr, "auto sync:", err)
		}
		time.Sleep(time.Duration(period) * time.Millisecond)
	}
}

func (n *NTP) run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			n.mu.Lock()
			n.logReport()
			n.mu.Unlock()
		}
	}
}

func (a *Application) Sync(args Empty, reply *SyncReply) error {
	last, err := a.n.sync()
	if last != nil {
		*reply = SyncReply{last.Upstream, last.DelayMs, last.OffsetMs, last.ErrorAfterMs}
	}
	return err
}

func (a *Application) AutoSync(args AutoArgs, reply *Empty) error {
	n := a.n
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stratum == 1 {
		return errors.New("stratum 1 不需要同步")
	}
	n.autoGen++
	n.periodMs = max(args.PeriodMs, 0)
	if n.periodMs > 0 {
		go n.autoLoop(n.autoGen, max(n.periodMs, 200))
	}
	return n.report()
}

func (a *Application) SetServer(args ServerArgs, reply *Empty) error {
	a.n.mu.Lock()
	defer a.n.mu.Unlock()
	a.n.delayMs = min(max(args.DelayMs, 0), 5000)
	a.n.jitterMs = min(max(args.JitterMs, 0), 2000)
	return a.n.report()
}

func (a *Application) SetClock(args ClockArgs, reply *Empty) error {
	a.n.mu.Lock()
	defer a.n.mu.Unlock()
	a.n.clock.Set(args.OffsetMs, args.DriftMsPerSec)
	return a.n.report()
}
