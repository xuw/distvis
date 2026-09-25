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

// No UTC authority in Berkeley: the master's own clock is wrong too.
func init() { defaultOffsets[0], defaultDrifts[0] = 400, 8 }

type Empty struct{}
type PollReply struct {
	T string `json:"t"`
}
type AdjustArgs struct {
	AdjMs int64 `json:"adjMs"` // relative adjustment: average - this node's difference
	Step  bool  `json:"step"`
}

type Row struct {
	DiffMs int64 `json:"diffMs"` // clock value relative to the master's clock
	RTTMs  int64 `json:"rttMs"`
	Used   bool  `json:"used"` // false: outlier, excluded from the average
	AdjMs  int64 `json:"adjMs"`
}
type RoundReply struct {
	AvgMs    int64 `json:"avgMs"`
	SpreadMs int64 `json:"spreadMs"`
	Outliers int   `json:"outliers"`
}
type PeriodArgs struct {
	PeriodMs int `json:"periodMs"` // 0 = only manual rounds
}
type ThresholdArgs struct {
	ThresholdMs int `json:"thresholdMs"`
}
type ModeArgs struct {
	Step bool `json:"step"` // true: jump; false: slew gradually
}
type ClockArgs struct {
	OffsetMs      float64 `json:"offsetMs"`
	DriftMsPerSec float64 `json:"driftMsPerSec"`
}

type Berkeley struct {
	mu      sync.Mutex
	roundMu sync.Mutex
	node    *lab.Runtime
	master  bool
	peers   map[string]*rpc.Client
	clock   *Clock
	// every node
	adjustments int
	lastAdj     int64
	adjAt       time.Time
	target      int64 // expected error once the last adjustment is applied, if the clock did not drift
	// master only
	periodMs    int
	thresholdMs int
	step        bool
	rounds      int
	lastRoundAt time.Time
	lastRound   map[string]Row
	noReply     []string
	avg, median int64
	spread      []int64
}
type Application struct{ b *Berkeley }

func newBerkeley(node *lab.Runtime) (*Berkeley, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	index := slices.Index(node.Nodes, node.ID) + 1
	return &Berkeley{
		node: node, master: index == 1, peers: peers, clock: loadClock(index),
		periodMs: 5000, thresholdMs: 3000, lastRoundAt: time.Now(),
	}, nil
}

func (b *Berkeley) report() error {
	state := b.clock.fields(map[string]any{"role": "client", "adjustments": b.adjustments})
	if b.adjustments > 0 {
		state["lastAdjMs"] = b.lastAdj
		state["sinceAdjustS"] = int(time.Since(b.adjAt).Seconds())
		state["driftSinceAdjMs"] = state["errorMs"].(int64) - b.target
	}
	if b.master {
		state["role"] = "master"
		state["rounds"] = b.rounds
		state["periodMs"] = b.periodMs
		state["thresholdMs"] = b.thresholdMs
		state["mode"] = map[bool]string{false: "slew", true: "step"}[b.step]
		if b.rounds > 0 {
			state["lastRound"] = b.lastRound
			state["avgMs"], state["medianMs"] = b.avg, b.median
			state["spreadMs"] = b.spread[len(b.spread)-1]
			state["spreadHistory"] = b.spread
			if len(b.noReply) > 0 {
				state["noReply"] = b.noReply
			}
		}
	}
	return b.node.Report(state)
}

func (b *Berkeley) logReport() {
	if err := b.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// Poll returns this node's clock; the master compensates with RTT/2 like Cristian.
func (b *Berkeley) Poll(args Empty, reply *PollReply) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	reply.T = stamp(b.clock.Now())
	return nil
}

func (b *Berkeley) Adjust(args AdjustArgs, reply *Empty) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.apply(args.AdjMs, args.Step)
	return b.report()
}

func (b *Berkeley) apply(adj int64, step bool) {
	b.target = b.clock.ErrorMs() + adj
	if step {
		b.clock.Step(float64(adj))
	} else {
		b.clock.Slew(float64(adj))
	}
	b.adjustments++
	b.lastAdj, b.adjAt = adj, time.Now()
}

type measured struct {
	peer      string
	diff, rtt float64
	err       error
}

func (b *Berkeley) poll(peer string) measured {
	b.mu.Lock()
	sent := b.clock.Now()
	b.mu.Unlock()
	var reply PollReply
	if err := callTimeout(b.peers[peer], "Berkeley.Poll", Empty{}, &reply, 2*time.Second); err != nil {
		return measured{peer: peer, err: err}
	}
	b.mu.Lock()
	recv := b.clock.Now()
	b.mu.Unlock()
	t, err := parseStamp(reply.T)
	rtt := float64(recv.Sub(sent)) / float64(time.Millisecond)
	return measured{peer: peer, diff: float64(diffMs(t, msOfDay(recv))) + rtt/2, rtt: rtt, err: err}
}

// round: poll everyone, discard outliers far from the median, average the rest (master included),
// and send each node its relative adjustment.
func (b *Berkeley) round() (RoundReply, error) {
	b.roundMu.Lock()
	defer b.roundMu.Unlock()
	results := make(chan measured, len(b.peers))
	for peer := range b.peers {
		go func() { results <- b.poll(peer) }()
	}
	diffs := map[string]float64{b.node.ID: 0}
	rtts := map[string]float64{}
	var noReply []string
	for range b.peers {
		m := <-results
		if m.err != nil {
			fmt.Fprintln(os.Stderr, m.peer, m.err)
			noReply = append(noReply, m.peer)
			continue
		}
		diffs[m.peer], rtts[m.peer] = m.diff, m.rtt
	}
	slices.Sort(noReply)

	b.mu.Lock()
	values := make([]float64, 0, len(diffs))
	for _, d := range diffs {
		values = append(values, d)
	}
	slices.Sort(values)
	median := values[len(values)/2]
	if len(values)%2 == 0 {
		median = (values[len(values)/2-1] + values[len(values)/2]) / 2
	}
	sum, used := 0.0, 0
	for _, d := range diffs {
		if math.Abs(d-median) <= float64(b.thresholdMs) {
			sum += d
			used++
		}
	}
	avg := sum / float64(used)
	table := map[string]Row{}
	for peer, d := range diffs {
		table[peer] = Row{
			DiffMs: int64(math.Round(d)), RTTMs: int64(math.Round(rtts[peer])),
			Used: math.Abs(d-median) <= float64(b.thresholdMs), AdjMs: int64(math.Round(avg - d)),
		}
	}
	step := b.step
	b.apply(table[b.node.ID].AdjMs, step)
	b.rounds++
	b.lastRound, b.noReply, b.lastRoundAt = table, noReply, time.Now()
	b.avg, b.median = int64(math.Round(avg)), int64(math.Round(median))
	spread := int64(math.Round(values[len(values)-1] - values[0]))
	b.spread = append(b.spread, spread)
	if len(b.spread) > 10 {
		b.spread = b.spread[1:]
	}
	b.logReport()
	b.mu.Unlock()

	var wg sync.WaitGroup
	for peer, row := range table {
		if peer == b.node.ID {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := callTimeout(b.peers[peer], "Berkeley.Adjust", AdjustArgs{row.AdjMs, step}, &Empty{}, 2*time.Second); err != nil {
				fmt.Fprintln(os.Stderr, peer, err)
			}
		}()
	}
	wg.Wait()
	return RoundReply{b.avg, spread, len(values) - used}, nil
}

// run reports every second and, on the master, starts periodic rounds.
func (b *Berkeley) run(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		b.mu.Lock()
		due := b.master && b.periodMs > 0 && time.Since(b.lastRoundAt) >= time.Duration(b.periodMs)*time.Millisecond
		if i%4 == 0 {
			b.logReport()
		}
		b.mu.Unlock()
		if due {
			if _, err := b.round(); err != nil {
				fmt.Fprintln(os.Stderr, "round:", err)
			}
		}
	}
}

var errNotMaster = errors.New("只有 master（node-1）发起同步轮次")

func (a *Application) SyncRound(args Empty, reply *RoundReply) error {
	if !a.b.master {
		return errNotMaster
	}
	r, err := a.b.round()
	*reply = r
	return err
}

func (a *Application) SetPeriod(args PeriodArgs, reply *Empty) error {
	if !a.b.master {
		return errNotMaster
	}
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	a.b.periodMs = max(args.PeriodMs, 0)
	return a.b.report()
}

func (a *Application) SetThreshold(args ThresholdArgs, reply *Empty) error {
	if !a.b.master {
		return errNotMaster
	}
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	a.b.thresholdMs = max(args.ThresholdMs, 1)
	return a.b.report()
}

func (a *Application) SetMode(args ModeArgs, reply *Empty) error {
	if !a.b.master {
		return errNotMaster
	}
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	a.b.step = args.Step
	return a.b.report()
}

func (a *Application) SetClock(args ClockArgs, reply *Empty) error {
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	a.b.clock.Set(args.OffsetMs, args.DriftMsPerSec)
	a.b.target = a.b.clock.ErrorMs()
	return a.b.report()
}
