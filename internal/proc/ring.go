package proc

import (
	"bytes"
	"sync"
)

// Ring keeps the last max lines written to it and fans new lines out to subscribers.
type Ring struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial []byte
	subs    map[chan string]struct{}
}

func NewRing(max int) *Ring { return &Ring{max: max, subs: map[chan string]struct{}{}} }

func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		line := string(r.partial[:i])
		r.partial = r.partial[i+1:]
		r.lines = append(r.lines, line)
		if len(r.lines) > r.max {
			r.lines = r.lines[len(r.lines)-r.max:]
		}
		for ch := range r.subs {
			select {
			case ch <- line:
			default: // slow subscriber: drop rather than block the process
			}
		}
	}
	return len(p), nil
}

func (r *Ring) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := max(0, len(r.lines)-n)
	return append([]string(nil), r.lines[start:]...)
}

func (r *Ring) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 256)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, ok := r.subs[ch]; ok {
			delete(r.subs, ch)
			close(ch)
		}
	}
}

// CloseSubs ends every subscription, e.g. when the box's log is closed.
func (r *Ring) CloseSubs() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch := range r.subs {
		delete(r.subs, ch)
		close(ch)
	}
}
