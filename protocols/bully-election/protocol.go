package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/rpc"
	"os"
	"slices"
	"sync"
	"time"

	lab "distvis/sdk/rpc"
)

const (
	heartbeatEvery = time.Second
	detectAfter    = 3 * time.Second // plus up to 1 s of jitter, so followers rarely notice at the same moment
	okTimeout      = 1200 * time.Millisecond
	coordTimeout   = 3 * time.Second
)

type Msg struct {
	From string `json:"from"`
}
type Empty struct{}

type Bully struct {
	mu        sync.Mutex
	node      *lab.Runtime
	index     int
	peers     map[string]*rpc.Client
	role      string // leader / follower / election
	leader    string
	epoch     int // bumped whenever an election ends or restarts; stale timers compare it
	okFrom    []string
	started   int
	last      string
	heard     time.Time
	detect    time.Duration
	beat      time.Time
	sent      map[string]int
	leaderFor int // times this node became coordinator
}
type Application struct{ b *Bully }

func newBully(node *lab.Runtime) (*Bully, error) {
	peers, err := node.RPCPeers()
	if err != nil {
		return nil, err
	}
	return &Bully{node: node, index: slices.Index(node.Nodes, node.ID) + 1, peers: peers,
		role: "follower", sent: map[string]int{}}, nil
}

func (b *Bully) indexOf(id string) int { return slices.Index(b.node.Nodes, id) + 1 }

func (b *Bully) report() error {
	return b.node.Report(map[string]any{
		"role": b.role, "leader": b.leader, "electionsStarted": b.started, "lastElection": b.last,
		"okFrom": append([]string{}, b.okFrom...), "timesLeader": b.leaderFor,
		"sent": map[string]int{"election": b.sent["Election"], "ok": b.sent["OK"], "coordinator": b.sent["Coordinator"]},
	})
}

func (b *Bully) logReport() {
	if err := b.report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// send is fire-and-forget: the protocol reacts to messages, not to RPC returns, and a call
// to a crashed node simply times out in the background.
func (b *Bully) send(peer, kind string) {
	if kind != "Heartbeat" {
		b.sent[kind]++
	}
	b.peers[peer].Go("Bully."+kind, Msg{b.node.ID}, &Empty{}, make(chan *rpc.Call, 1))
}

func (b *Bully) resetTimer() {
	b.heard = time.Now()
	b.detect = detectAfter + time.Duration(rand.Intn(1000))*time.Millisecond
}

func (b *Bully) startElection(reason string) {
	if b.role == "election" {
		return
	}
	b.epoch++
	b.role, b.leader, b.okFrom, b.started, b.last = "election", "", nil, b.started+1, reason
	higher := 0
	for _, id := range b.node.Nodes {
		if b.indexOf(id) > b.index {
			higher++
			b.send(id, "Election")
		}
	}
	if higher == 0 {
		b.win("编号最大，直接成为协调者")
		return
	}
	epoch := b.epoch
	time.AfterFunc(okTimeout, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.epoch == epoch && b.role == "election" && len(b.okFrom) == 0 {
			b.win("更高编号无人回 OK，成为协调者")
			b.logReport()
		}
	})
}

func (b *Bully) win(why string) {
	b.epoch++
	b.role, b.leader, b.last, b.leaderFor = "leader", b.node.ID, why, b.leaderFor+1
	b.broadcast("Coordinator")
	b.beat = time.Now()
}

func (b *Bully) broadcast(kind string) {
	for peer := range b.peers {
		b.send(peer, kind)
	}
}

func (b *Bully) follow(leader string) {
	if b.role != "follower" || b.leader != leader {
		b.epoch++
		b.role, b.leader = "follower", leader
	}
	b.resetTimer()
}

// Election always comes from a lower node: answer OK, then take over the election.
func (b *Bully) Election(args Msg, reply *Empty) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.send(args.From, "OK")
	if b.role == "leader" {
		b.send(args.From, "Coordinator")
	} else {
		b.startElection("收到 " + args.From + " 的 Election，接手选举")
	}
	return b.report()
}

func (b *Bully) OK(args Msg, reply *Empty) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.role != "election" {
		return nil
	}
	b.okFrom = append(b.okFrom, args.From)
	if len(b.okFrom) > 1 {
		return b.report()
	}
	b.last = "收到 " + args.From + " 的 OK，等待 Coordinator"
	epoch := b.epoch
	time.AfterFunc(coordTimeout, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.epoch == epoch && b.role == "election" {
			b.role = "follower"
			b.startElection("OK 之后没有等到 Coordinator，重新选举")
			b.logReport()
		}
	})
	return b.report()
}

// Coordinator and Heartbeat both announce a leader. One from a lower node is bullied.
func (b *Bully) Coordinator(args Msg, reply *Empty) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.announce(args.From)
	return b.report()
}

func (b *Bully) Heartbeat(args Msg, reply *Empty) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	role, leader := b.role, b.leader
	b.announce(args.From)
	if b.role != role || b.leader != leader {
		return b.report()
	}
	return nil
}

func (b *Bully) announce(from string) {
	if b.indexOf(from) > b.index {
		b.follow(from)
		return
	}
	if b.role == "leader" {
		b.send(from, "Coordinator")
	} else {
		b.startElection(from + " 自称协调者，但本节点编号更大")
	}
}

func (a *Application) StartElection(args Empty, reply *Empty) error {
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	if a.b.role == "election" {
		return fmt.Errorf("选举已在进行")
	}
	a.b.role = "follower"
	a.b.startElection("手动发起")
	return a.b.report()
}

// run: a (re)started node begins with an election; afterwards the leader sends heartbeats
// and followers watch for them.
func (b *Bully) run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
	}
	b.mu.Lock()
	if b.role == "follower" && b.leader == "" {
		b.startElection("节点启动")
		b.logReport()
	}
	b.mu.Unlock()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			b.mu.Lock()
			switch {
			case b.role == "leader" && now.Sub(b.beat) >= heartbeatEvery:
				b.beat = now
				b.broadcast("Heartbeat")
			case b.role == "follower" && now.Sub(b.heard) > b.detect:
				b.startElection("心跳超时：" + b.leader + " 失联")
				b.logReport()
			}
			b.mu.Unlock()
		}
	}
}
