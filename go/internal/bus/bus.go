// Package bus is the high-traffic in-process message bus (ADR 0035).
// Agent-to-agent streaming uses goroutines and bounded channels; producers
// block when a subscriber's channel is full (backpressure), never grow memory.
package bus

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned when Publish is called after Close.
var ErrClosed = errors.New("bus: closed")

// Message is one agent-to-agent (or hub) payload.
type Message struct {
	From    string
	To      string // empty = broadcast to all subscribers
	Payload []byte
}

// Bus fans messages out to subscribers over bounded channels.
type Bus struct {
	mu   sync.RWMutex
	subs map[string]*subscription
	cap  int
	done chan struct{}
}

type subscription struct {
	id   string
	ch   chan Message
	once sync.Once
}

// New creates a bus. subscriberCap is the bounded buffer per subscriber (>=1).
func New(subscriberCap int) *Bus {
	if subscriberCap < 1 {
		subscriberCap = 1
	}
	return &Bus{
		subs: make(map[string]*subscription),
		cap:  subscriberCap,
		done: make(chan struct{}),
	}
}

// Subscribe registers a subscriber. The returned channel is bounded; receives
// must drain it. Unsubscribe (or Close) releases it. Context cancel does not
// auto-unsubscribe — call Unsubscribe so the goroutine count drops.
func (b *Bus) Subscribe(id string) <-chan Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.done:
		ch := make(chan Message)
		close(ch)
		return ch
	default:
	}
	if old, ok := b.subs[id]; ok {
		old.once.Do(func() { close(old.ch) })
		delete(b.subs, id)
	}
	sub := &subscription{id: id, ch: make(chan Message, b.cap)}
	b.subs[id] = sub
	return sub.ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *Bus) Unsubscribe(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.subs[id]; ok {
		sub.once.Do(func() { close(sub.ch) })
		delete(b.subs, id)
	}
}

// Publish sends msg with backpressure: for each matching subscriber it blocks
// until the message is accepted or ctx is cancelled / the bus is closed.
func (b *Bus) Publish(ctx context.Context, msg Message) error {
	b.mu.RLock()
	select {
	case <-b.done:
		b.mu.RUnlock()
		return ErrClosed
	default:
	}
	targets := make([]*subscription, 0, len(b.subs))
	for _, sub := range b.subs {
		if msg.To == "" || msg.To == sub.id {
			targets = append(targets, sub)
		}
	}
	b.mu.RUnlock()

	for _, sub := range targets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.done:
			return ErrClosed
		case sub.ch <- msg:
		}
	}
	return nil
}

// SubscriberCount is the number of active subscriptions.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Close shuts down the bus and closes all subscriber channels.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.done:
		return
	default:
		close(b.done)
	}
	for id, sub := range b.subs {
		sub.once.Do(func() { close(sub.ch) })
		delete(b.subs, id)
	}
}
