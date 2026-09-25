package main

import (
	"context"
	"fmt"
	"net/rpc"
	"os"
	"sync"
	"time"
)

type outgoing struct {
	method string
	args   any
}

// link is the FIFO channel to one peer: a single goroutine sends queued messages in order
// and retries the head until the peer answers, so no message is reordered or lost.
type link struct {
	peer   string
	client *rpc.Client
	mu     sync.Mutex
	ready  chan struct{}
	queue  []outgoing
}

func newLink(peer string, client *rpc.Client) *link {
	return &link{peer: peer, client: client, ready: make(chan struct{}, 1)}
}

func (l *link) push(method string, args any) {
	l.mu.Lock()
	l.queue = append(l.queue, outgoing{method, args})
	l.mu.Unlock()
	select {
	case l.ready <- struct{}{}:
	default:
	}
}

func (l *link) run(ctx context.Context) {
	for {
		l.mu.Lock()
		empty := len(l.queue) == 0
		var next outgoing
		if !empty {
			next = l.queue[0]
		}
		l.mu.Unlock()
		if empty {
			select {
			case <-ctx.Done():
				return
			case <-l.ready:
			}
			continue
		}
		if err := l.call(next); err != nil {
			fmt.Fprintf(os.Stderr, "%s -> %s: %v (retrying)\n", next.method, l.peer, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		l.mu.Lock()
		l.queue = l.queue[1:]
		l.mu.Unlock()
	}
}

func (l *link) call(m outgoing) error {
	call := l.client.Go(m.method, m.args, &Empty{}, nil)
	select {
	case <-call.Done:
		return call.Error
	case <-time.After(8 * time.Second):
		return fmt.Errorf("timeout")
	}
}
