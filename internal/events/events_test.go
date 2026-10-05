package events

import (
	"testing"
	"time"
)

func TestPublishSubscribe(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe()
	b.Publish(Event{Type: BoxState, Box: "r/a"})
	select {
	case ev := <-ch:
		if ev.Box != "r/a" || ev.Type != BoxState {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	cancel()
	cancel() // idempotent
	b.Publish(Event{Type: BoxState})
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewBus()
	_, cancel := b.Subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		for range 10_000 {
			b.Publish(Event{Type: BoxStep})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that never reads")
	}
}
