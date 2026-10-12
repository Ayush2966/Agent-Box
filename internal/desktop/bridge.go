package desktop

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/agentbox-dev/agentbox/internal/client"
)

// API is the slice of the daemon client the bridge streams from.
type API interface {
	Events(ctx context.Context) (<-chan client.Event, error)
	LogLines(ctx context.Context, repoID, name string, tail int) (<-chan string, error)
}

// Emitter delivers an event to the window (runtime.EventsEmit in production).
type Emitter func(name string, data any)

// Bridge turns the daemon's SSE streams into Wails events, because the Wails
// asset server cannot stream a response (spec §1.1).
type Bridge struct {
	api  API
	emit Emitter
	base time.Duration

	mu    sync.Mutex
	state string // connecting, live or reconnecting
	logs  map[string]context.CancelFunc
}

func NewBridge(api API, emit Emitter) *Bridge {
	return &Bridge{api: api, emit: emit, base: time.Second, state: "connecting", logs: map[string]context.CancelFunc{}}
}

// setState records the connection state, then announces it.
// It reports whether the state changed; an unchanged state is not re-announced.
func (b *Bridge) setState(s string) bool {
	b.mu.Lock()
	changed := b.state != s
	b.state = s
	b.mu.Unlock()
	if changed {
		b.emit("agentbox:conn", s)
	}
	return changed
}

// State is the current connection state: connecting, live or reconnecting.
func (b *Bridge) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Run pumps daemon events to "agentbox:event" until ctx is done, reconnecting
// with 1s→16s backoff and reporting the connection as "agentbox:conn".
func (b *Bridge) Run(ctx context.Context) {
	backoff := b.base
	for {
		ch, err := b.api.Events(ctx)
		if err == nil {
			b.setState("live")
			backoff = b.base
			for ev := range ch {
				b.emit("agentbox:event", ev)
			}
		}
		if ctx.Err() != nil {
			return
		}
		b.setState("reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 16*b.base {
			backoff *= 2
		}
	}
}

// StartLogs follows a box's log (tail 500) as "agentbox:log:<id>" events, one
// line each, then "agentbox:log:<id>:end". The window picks the id and
// subscribes to it BEFORE calling: Wails delivers this call's reply and the
// stream's events as separate, unordered main-thread calls, so a bridge-chosen
// id would let the first tail lines (or a quick :end) arrive before the window
// could listen for them.
func (b *Bridge) StartLogs(id, repo, name string) error {
	if id == "" {
		return fmt.Errorf("log stream id must not be empty")
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	if _, dup := b.logs[id]; dup {
		b.mu.Unlock()
		cancel()
		return fmt.Errorf("log stream %s is already open", id)
	}
	b.logs[id] = cancel
	b.mu.Unlock()
	ch, err := b.api.LogLines(ctx, repo, name, 500)
	if err != nil {
		b.StopLogs(id)
		return err
	}
	go func() {
		for line := range ch {
			b.emit("agentbox:log:"+id, line)
		}
		b.emit("agentbox:log:"+id+":end", nil)
		b.StopLogs(id)
	}()
	return nil
}

func (b *Bridge) StopLogs(id string) {
	b.mu.Lock()
	cancel, ok := b.logs[id]
	delete(b.logs, id)
	b.mu.Unlock()
	if ok {
		cancel()
	}
}

// Close stops every log stream.
func (b *Bridge) Close() {
	b.mu.Lock()
	ids := make([]string, 0, len(b.logs))
	for id := range b.logs {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.StopLogs(id)
	}
}
