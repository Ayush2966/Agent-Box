// Package notify turns box events into desktop notifications, so a crashed
// dev server or a finished creation is seen without watching the dashboard.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/events"
)

// Sender shows one notification. It should return quickly.
type Sender func(title, body string) error

// System returns the platform's notifier, or nil when there is none:
// osascript on macOS, notify-send on Linux.
func System() Sender {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("osascript"); err != nil {
			return nil
		}
		return func(title, body string) error {
			// Title and body travel as arguments, never inside the script.
			return exec.Command("osascript",
				"-e", "on run argv",
				"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
				"-e", "end run", title, body).Run()
		}
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			return nil
		}
		return func(title, body string) error {
			return exec.Command("notify-send", "--app-name=agentbox", title, body).Run()
		}
	}
	return nil
}

// Watcher decides which events deserve a notification. It remembers each
// box's last state, so a box becoming ready is reported once, after creating.
type Watcher struct {
	mu   sync.Mutex
	last map[string]string // box id → last state seen
	seen map[string]time.Time
	now  func() time.Time
}

func NewWatcher() *Watcher {
	return &Watcher{last: map[string]string{}, seen: map[string]time.Time{}, now: time.Now}
}

const dedupe = 2 * time.Second

// Message returns the notification for e, if any.
func (w *Watcher) Message(e events.Event) (title, body string, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	data, _ := e.Data.(map[string]any)
	str := func(k string) string {
		v, _ := data[k].(string)
		return v
	}
	name := e.Box
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	prev := w.last[e.Box]
	switch e.Type {
	case events.BoxCreated, events.BoxStep:
		w.last[e.Box] = "creating"
		return "", "", false
	case events.BoxRemoved:
		delete(w.last, e.Box)
		if err := str("error"); err != "" {
			title, body = "Box "+name+" could not be created", err
		} else {
			return "", "", false
		}
	case events.BoxState:
		st := str("state")
		w.last[e.Box] = st
		switch {
		case st == "failed":
			title, body = "Box "+name+" failed", firstOf(str("error"), str("last_error"), "see agentbox logs "+name)
		case st == "stopped" && str("reason") == "crashed":
			title, body = "Box "+name+": dev server kept crashing", firstOf(str("last_error"), "see agentbox logs "+name)
		case (st == "running" || st == "ready") && prev == "creating":
			title, body = "Box "+name+" is "+st, "point your agent at it"
		default:
			return "", "", false
		}
	default:
		return "", "", false
	}
	key := e.Box + "|" + title + "|" + body
	if t, ok := w.seen[key]; ok && w.now().Sub(t) < dedupe {
		return "", "", false
	}
	w.seen[key] = w.now()
	return title, body, true
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Run forwards notifications for bus events until ctx ends.
func Run(ctx context.Context, bus *events.Bus, send Sender) {
	ch, cancel := bus.Subscribe()
	defer cancel()
	w := NewWatcher()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if title, body, ok := w.Message(e); ok {
				if err := send(title, body); err != nil {
					slog.Warn("notification", "title", title, "err", fmt.Sprint(err))
				}
			}
		}
	}
}
