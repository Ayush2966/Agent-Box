package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/events"
)

func terminalState(s string) bool {
	return s == "ready" || s == "running" || s == "stopped" || s == "failed"
}

// applyEvent mirrors the web dashboard's reducer. It returns the follow-up
// command: a fetch of the box for created/state events, a toast when the box
// on screen was removed.
func (m *Model) applyEvent(ev client.Event) tea.Cmd {
	str := func(k string) (string, bool) {
		v, ok := ev.Data[k].(string)
		return v, ok
	}
	switch ev.Type {
	case events.BoxStep:
		if s, ok := ev.Data["step"]; ok {
			m.steps[ev.Box] = fmt.Sprint(s)
			m.refreshTable() // the STATE cell shows the step
		}
		return nil
	case events.BoxState:
		state, _ := str("state")
		if terminalState(state) {
			delete(m.steps, ev.Box)
		}
		if e, ok := str("error"); ok {
			m.failures[ev.Box] = e
		}
		if b, ok := m.boxes[ev.Box]; ok {
			b.State = state
			if le, ok := str("last_error"); ok {
				b.LastError = le
			}
			m.boxes[ev.Box] = b
			m.refreshTable()
		}
		m.refreshInfo()
		return m.fetchBox(ev.Box)
	case events.BoxCreated:
		return m.fetchBox(ev.Box)
	case events.BoxRemoved:
		delete(m.boxes, ev.Box)
		delete(m.steps, ev.Box)
		if e, ok := str("error"); ok {
			m.failures[ev.Box] = e
		}
		m.refreshTable()
		m.refreshInfo()
		if m.mode == modeBox && m.cur == ev.Box {
			_, name := splitID(ev.Box)
			m.leaveBox()
			return m.say(false, "box "+name+" was removed")
		}
		return nil
	}
	return nil
}
