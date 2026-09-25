package main

import (
	"fmt"
	"math"
	"net/rpc"
	"os"
	"time"

	"distvis/sdk"
)

// Clock simulates a node's hardware clock. All containers share the host clock, so each node
// derives its own: local(now) = anchorLocal + (now-anchorReal)*rate. True time (UTC) is time.Now().
// The same file is copied into every clock-* protocol.
type Clock struct {
	AnchorReal    time.Time `json:"anchorReal"`
	AnchorLocal   time.Time `json:"anchorLocal"`
	Drift         float64   `json:"drift"`   // ms per second; base rate = 1 + drift/1000
	Rate          float64   `json:"rate"`    // current rate, differs from the base rate while slewing
	SlewEnd       time.Time `json:"slewEnd"` // zero when not slewing
	BackwardJumps int       `json:"backwardJumps"`
}

var zone = time.FixedZone("CST", 8*3600)

// Deterministic defaults for node i (1-based): offset in ms and drift in ms/s, exaggerated so
// that drift is visible within a one-minute run.
var (
	defaultOffsets = []float64{0, 1800, -1200, 2600, -700, 900, -2300, 1500, -500, 2000, -1600, 600}
	defaultDrifts  = []float64{0, 20, -15, 10, -25, 12, -6, 30, -10, 5, -20, 15}
)

func defaultClock(index int) *Clock {
	i := (index - 1) % len(defaultOffsets)
	return newClock(defaultOffsets[i], defaultDrifts[i])
}

// loadClock restores the clock after a crash: the hardware clock kept running while the node was down.
func loadClock(index int) *Clock {
	var c Clock
	if err := sdk.Load(&c); err == nil && c.Rate > 0 {
		return &c
	}
	return defaultClock(index)
}

func newClock(offsetMs, drift float64) *Clock {
	now := time.Now()
	return &Clock{AnchorReal: now, AnchorLocal: now.Add(msDur(offsetMs)), Drift: drift, Rate: 1 + drift/1000}
}

func msDur(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }

func (c *Clock) base() float64 { return 1 + c.Drift/1000 }

func (c *Clock) at(real time.Time) time.Time {
	return c.AnchorLocal.Add(time.Duration(float64(real.Sub(c.AnchorReal)) * c.Rate))
}

// settle ends a finished slew by re-anchoring at its end point.
func (c *Clock) settle(now time.Time) {
	if !c.SlewEnd.IsZero() && !now.Before(c.SlewEnd) {
		c.AnchorLocal, c.AnchorReal = c.at(c.SlewEnd), c.SlewEnd
		c.Rate, c.SlewEnd = c.base(), time.Time{}
	}
}

func (c *Clock) reanchor(now time.Time) {
	c.settle(now)
	c.AnchorLocal, c.AnchorReal = c.at(now), now
}

// Now returns the local clock reading.
func (c *Clock) Now() time.Time {
	now := time.Now()
	c.settle(now)
	return c.at(now)
}

// ErrorMs is local minus true time.
func (c *Clock) ErrorMs() int64 {
	now := time.Now()
	c.settle(now)
	return c.at(now).Sub(now).Milliseconds()
}

// Step jumps the clock by deltaMs; a negative step makes local time run backwards.
func (c *Clock) Step(deltaMs float64) {
	c.reanchor(time.Now())
	c.AnchorLocal = c.AnchorLocal.Add(msDur(deltaMs))
	c.Rate, c.SlewEnd = c.base(), time.Time{}
	if deltaMs < 0 {
		c.BackwardJumps++
	}
	c.save()
}

// Slew applies deltaMs gradually by changing the rate by at most 0.8 (so the clock always moves
// forward), for at least 3 s. A new slew replaces the unfinished part of an earlier one.
func (c *Clock) Slew(deltaMs float64) time.Duration {
	now := time.Now()
	c.reanchor(now)
	d := math.Max(3000, math.Abs(deltaMs)/0.8)
	c.Rate = c.base() + deltaMs/d
	c.SlewEnd = now.Add(msDur(d))
	c.save()
	return msDur(d)
}

// Set replaces offset and drift (used by the SetClock input); it is not counted as a jump.
func (c *Clock) Set(offsetMs, drift float64) {
	jumps := c.BackwardJumps
	*c = *newClock(offsetMs, drift)
	c.BackwardJumps = jumps
	c.save()
}

func (c *Clock) SlewRemainingMs() int64 {
	now := time.Now()
	c.settle(now)
	if c.SlewEnd.IsZero() {
		return 0
	}
	return c.SlewEnd.Sub(now).Milliseconds()
}

func (c *Clock) save() {
	if err := sdk.Save(c); err != nil {
		fmt.Fprintln(os.Stderr, "save clock:", err)
	}
}

// fields adds the common clock fields to a state report.
func (c *Clock) fields(state map[string]any) map[string]any {
	now := time.Now()
	c.settle(now)
	local := c.at(now)
	state["localTime"] = stamp(local)
	state["errorMs"] = local.Sub(now).Milliseconds()
	state["driftMsPerSec"] = c.Drift
	state["backwardJumps"] = c.BackwardJumps
	if !c.SlewEnd.IsZero() {
		state["slewMsLeft"] = c.SlewEnd.Sub(now).Milliseconds()
		state["rate"] = math.Round(c.Rate*1000) / 1000
	}
	return state
}

// Timestamps travel as "HH:MM:SS.mmm" so message labels stay readable.
func stamp(t time.Time) string { return t.In(zone).Format("15:04:05.000") }

func msOfDay(t time.Time) int64 {
	t = t.In(zone)
	return int64(t.Hour())*3600000 + int64(t.Minute())*60000 + int64(t.Second())*1000 + int64(t.Nanosecond()/1e6)
}

func parseStamp(s string) (int64, error) {
	var h, m, sec, ms int64
	if _, err := fmt.Sscanf(s, "%d:%d:%d.%d", &h, &m, &sec, &ms); err != nil {
		return 0, fmt.Errorf("bad timestamp %q", s)
	}
	return h*3600000 + m*60000 + sec*1000 + ms, nil
}

// diffMs returns a-b for times of day, wrapped into (-12h, 12h] so midnight does not matter.
func diffMs(a, b int64) int64 {
	d := (a - b) % 86400000
	if d > 43200000 {
		d -= 86400000
	} else if d <= -43200000 {
		d += 86400000
	}
	return d
}

// callTimeout gives up after d; a late reply is ignored.
func callTimeout(c *rpc.Client, method string, args, reply any, d time.Duration) error {
	call := c.Go(method, args, reply, make(chan *rpc.Call, 1))
	select {
	case <-call.Done:
		return call.Error
	case <-time.After(d):
		return fmt.Errorf("%s: no reply within %v", method, d)
	}
}
