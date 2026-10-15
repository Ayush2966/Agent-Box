package notify

import (
	"context"
	"testing"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/events"
)

func ev(typ, box string, data map[string]any) events.Event {
	return events.Event{Type: typ, Box: box, Data: data}
}

func TestWatcherPicksWhatMatters(t *testing.T) {
	w := NewWatcher()
	now := time.Unix(1000, 0)
	w.now = func() time.Time { return now }
	type step struct {
		e         events.Event
		wantTitle string
	}
	steps := []step{
		{ev("box.created", "r/a", nil), ""},
		{ev("box.step", "r/a", map[string]any{"step": "setup"}), ""},
		{ev("box.state", "r/a", map[string]any{"state": "ready"}), "Box a is ready"},
		{ev("box.state", "r/a", map[string]any{"state": "running"}), ""}, // ready → running: not a creation
		{ev("box.state", "r/a", map[string]any{"state": "stopped"}), ""}, // the user stopped it
		{ev("box.state", "r/a", map[string]any{"state": "running"}), ""}, // user start
		{ev("box.state", "r/a", map[string]any{"state": "stopped", "reason": "crashed", "last_error": "exit status 1"}), "Box a: dev server kept crashing"},
		{ev("box.created", "r/b", nil), ""},
		{ev("box.state", "r/b", map[string]any{"state": "failed", "error": "setup failed"}), "Box b failed"},
		{ev("box.removed", "r/b", map[string]any{"error": "setup failed"}), "Box b could not be created"},
		{ev("box.removed", "r/a", nil), ""}, // a normal rm
	}
	for i, s := range steps {
		title, body, ok := w.Message(s.e)
		if (s.wantTitle != "") != ok || title != s.wantTitle {
			t.Fatalf("step %d (%s %v): got %q/%q ok=%v, want title %q", i, s.e.Type, s.e.Data, title, body, ok, s.wantTitle)
		}
		if ok && body == "" {
			t.Fatalf("step %d: empty body", i)
		}
	}
}

func TestWatcherDedupesRepeats(t *testing.T) {
	w := NewWatcher()
	now := time.Unix(1000, 0)
	w.now = func() time.Time { return now }
	crash := ev("box.state", "r/a", map[string]any{"state": "stopped", "reason": "crashed", "last_error": "boom"})
	if _, _, ok := w.Message(crash); !ok {
		t.Fatal("first crash must notify")
	}
	if _, _, ok := w.Message(crash); ok {
		t.Fatal("same crash within 2s must not notify again")
	}
	now = now.Add(3 * time.Second)
	if _, _, ok := w.Message(crash); !ok {
		t.Fatal("after the window it notifies again")
	}
}

func TestRunForwardsBusEvents(t *testing.T) {
	bus := events.NewBus()
	got := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Run(ctx, bus, func(title, body string) error { got <- title + ": " + body; return nil })
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // let Run subscribe
	bus.Publish(ev("box.state", "r/x", map[string]any{"state": "failed", "error": "no node"}))
	select {
	case s := <-got:
		if s != "Box x failed: no node" {
			t.Fatalf("got %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notification")
	}
	cancel()
	<-done
}
