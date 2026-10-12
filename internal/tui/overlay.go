package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// overlay is a modal that swallows every key until it closes itself.
type overlay interface {
	handle(m *Model, k tea.KeyMsg) tea.Cmd
	view(m *Model) string
}

// toggle is one optional boolean on a dialog, flipped by a single key.
type toggle struct {
	key, label string
	on         bool
}

func (t *toggle) line() string {
	box := "[ ]"
	if t.on {
		box = "[x]"
	}
	return stMuted.Render(t.key+" ") + box + " " + t.label
}

type confirm struct {
	title, body string
	toggle      *toggle
	yesOnly     bool // enter does not confirm (destructive second asks)
	yes         func(m *Model, on bool) tea.Cmd
}

func (c *confirm) handle(m *Model, k tea.KeyMsg) tea.Cmd {
	s := k.String()
	if s == "y" || (s == "enter" && !c.yesOnly) {
		m.overlay = nil
		return c.yes(m, c.toggle != nil && c.toggle.on)
	}
	switch s {
	case "n", "esc":
		m.overlay = nil
	default:
		if c.toggle != nil && k.String() == c.toggle.key {
			c.toggle.on = !c.toggle.on
		}
	}
	return nil
}

func (c *confirm) view(m *Model) string {
	lines := []string{}
	if c.toggle != nil {
		lines = append(lines, c.toggle.line())
	}
	if c.yesOnly {
		lines = append(lines, stMuted.Render("y yes · n/esc no"))
	} else {
		lines = append(lines, stMuted.Render("y/enter yes · n/esc no"))
	}
	return dialog(m.width, c.title, c.body, lines...)
}

type prompt struct {
	title  string
	input  textinput.Model
	toggle *toggle
	submit func(m *Model, value string, on bool) tea.Cmd
}

func newPrompt(title, placeholder string, t *toggle, noBlink bool, submit func(m *Model, value string, on bool) tea.Cmd) *prompt {
	in := textinput.New()
	in.Placeholder = placeholder
	in.CharLimit = 200
	in.Width = 40
	if noBlink {
		in.Cursor.SetMode(cursor.CursorStatic) // Focus() and typing then return no blink commands
	}
	in.Focus()
	return &prompt{title: title, input: in, toggle: t, submit: submit}
}

func (p *prompt) handle(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		m.overlay = nil
		return p.submit(m, strings.TrimSpace(p.input.Value()), p.toggle != nil && p.toggle.on)
	case "esc":
		m.overlay = nil
		return nil
	}
	if p.toggle != nil && k.String() == p.toggle.key {
		p.toggle.on = !p.toggle.on
		return nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	return cmd
}

func (p *prompt) view(m *Model) string {
	lines := []string{p.input.View()}
	if p.toggle != nil {
		lines = append(lines, p.toggle.line())
	}
	lines = append(lines, stMuted.Render("enter ok · esc cancel"))
	return dialog(m.width, p.title, "", lines...)
}

// dialog renders a bordered box; width is the terminal width.
func dialog(width int, title, body string, lines ...string) string {
	w := min(width-4, 72)
	parts := []string{stHeader.Render(title)}
	if body != "" {
		parts = append(parts, lipgloss.NewStyle().Width(w-2).Render(body))
	}
	parts = append(parts, lines...)
	return stDialog.Width(w).Render(strings.Join(parts, "\n"))
}
