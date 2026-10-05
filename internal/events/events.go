// Package events is the daemon's in-process pub/sub used for live UI updates.
package events

import "sync"

const (
	BoxCreated = "box.created"
	BoxState   = "box.state"
	BoxStep    = "box.step"
	BoxRemoved = "box.removed"
)

type Event struct {
	Type string `json:"type"`
	Box  string `json:"box"`
	Data any    `json:"data,omitempty"`
}

type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

// Publish delivers e to every subscriber that has room; it never blocks.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe returns a channel of future events and an idempotent cancel func.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
}
