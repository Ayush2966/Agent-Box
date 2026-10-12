package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/agentbox-dev/agentbox/internal/api"
	"github.com/agentbox-dev/agentbox/internal/box"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

type newForm struct {
	repo      api.RepoView
	name      textinput.Model
	from      textinput.Model
	noDev     bool
	focus     int    // 0 name, 1 from, 2 no-dev
	submitted string // the box id once CreateBox accepted it
	err       string
}

func validateName(s string) string {
	switch {
	case s == "":
		return "a name is required"
	case !nameRe.MatchString(s):
		return "names are lowercase letters, digits and dashes (up to 31, starting with a letter or digit)"
	}
	return ""
}

// newRepo picks the repo a new box goes into: the filter, the selected
// box's repo, or the only repo there is.
func (m Model) newRepo() (api.RepoView, bool) {
	b, ok := m.selected()
	id := m.repoFor(b, ok)
	for _, r := range m.repos {
		if r.ID == id {
			return r, true
		}
	}
	return api.RepoView{}, false
}

func (m Model) startNew() (tea.Model, tea.Cmd) {
	repo, ok := m.newRepo()
	if !ok {
		cmd := m.say(true, "pick a repo first: press r to filter by repo, or select a box in it")
		return m, cmd
	}
	f := newForm{repo: repo, name: textinput.New(), from: textinput.New()}
	f.name.Placeholder, f.name.CharLimit, f.name.Width = "feature-x", 31, 32
	f.from.Placeholder, f.from.CharLimit, f.from.Width = "HEAD (a branch, tag or commit)", 100, 40
	if m.opts.NoBlink {
		f.name.Cursor.SetMode(cursor.CursorStatic)
		f.from.Cursor.SetMode(cursor.CursorStatic)
	}
	cmd := f.name.Focus()
	m.form, m.mode = f, modeNew
	return m, cmd
}

func (m *Model) focusField() tea.Cmd {
	f := &m.form
	f.name.Blur()
	f.from.Blur()
	switch f.focus {
	case 0:
		return f.name.Focus()
	case 1:
		return f.from.Focus()
	}
	return nil
}

func (m Model) updateNew(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := &m.form
	if f.submitted != "" { // progress view
		switch k.String() {
		case "esc", "enter":
			m.mode = modeBoxes
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeBoxes
		return m, nil
	case "tab", "down":
		f.focus = (f.focus + 1) % 3
		cmd := m.focusField()
		return m, cmd
	case "shift+tab", "up":
		f.focus = (f.focus + 2) % 3
		cmd := m.focusField()
		return m, cmd
	case "enter":
		return m.submitNew()
	case " ":
		if f.focus == 2 {
			f.noDev = !f.noDev
			return m, nil
		}
	}
	var cmd tea.Cmd
	switch f.focus {
	case 0:
		f.name, cmd = f.name.Update(k)
		f.err = validateName(f.name.Value())
	case 1:
		f.from, cmd = f.from.Update(k)
	}
	return m, cmd
}

func (m Model) submitNew() (tea.Model, tea.Cmd) {
	f := &m.form
	name := strings.TrimSpace(f.name.Value())
	if msg := validateName(name); msg != "" {
		f.err = msg
		return m, nil
	}
	f.err = ""
	return m, m.create(box.NewOptions{RepoPath: f.repo.Path, Name: name, From: strings.TrimSpace(f.from.Value()), NoDev: f.noDev})
}

func (m Model) onCreated(msg createdMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeNew {
		return m, nil
	}
	if msg.err != nil {
		m.form.err = describe(msg.err)
		return m, nil
	}
	m.form.submitted = msg.id
	return m, nil
}

// afterBoxUpdate opens the box the new-box screen is waiting on once it is up.
func (m *Model) afterBoxUpdate(id string) tea.Cmd {
	if m.mode != modeNew || m.form.submitted != id {
		return nil
	}
	if b := m.boxes[id]; b.State == "running" || b.State == "ready" {
		return m.openBox(id)
	}
	return nil
}

// forwardToInputs routes non-key messages (cursor blinks, pastes) to the live text input.
func (m Model) forwardToInputs(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if p, ok := m.overlay.(*prompt); ok {
		p.input, cmd = p.input.Update(msg)
		return m, cmd
	}
	if m.mode == modeNew && m.form.submitted == "" {
		switch m.form.focus {
		case 0:
			m.form.name, cmd = m.form.name.Update(msg)
		case 1:
			m.form.from, cmd = m.form.from.Update(msg)
		}
	}
	return m, cmd
}

func (m Model) viewNew(h int) string {
	f := m.form
	title := stHeader.Render("New box") + stMuted.Render(" in "+repoBase(f.repo.Path))
	if f.submitted != "" {
		_, name := splitID(f.submitted)
		lines := []string{title, "", "Creating " + stHeader.Render(name) + "…"}
		if step, ok := m.steps[f.submitted]; ok && step != "" {
			lines = append(lines, stMuted.Render("  "+step+"…"))
		}
		if fail, ok := m.failures[f.submitted]; ok {
			lines = append(lines, "", stErr.Render(fail))
		}
		if b, ok := m.boxes[f.submitted]; ok && b.State == "stopped" && b.LastError != "" {
			lines = append(lines, "", stErr.Render("the dev server keeps crashing: "+b.LastError))
		}
		if b, ok := m.boxes[f.submitted]; ok && b.State == "failed" {
			lines = append(lines, "", stErr.Render("creation failed: "+b.LastError))
		}
		lines = append(lines, "", stMuted.Render("esc  back to the list"))
		return strings.Join(lines, "\n")
	}
	mark := func(i int) string {
		if f.focus == i {
			return stAccent.Render("› ")
		}
		return "  "
	}
	check := "[ ]"
	if f.noDev {
		check = "[x]"
	}
	lines := []string{title, "",
		mark(0) + stMuted.Render("name        ") + f.name.View(),
		mark(1) + stMuted.Render("start from  ") + f.from.View(),
		mark(2) + check + " don't start the dev server" + stMuted.Render("   (space toggles)"),
	}
	if f.err != "" {
		lines = append(lines, "", stErr.Render(f.err))
	}
	return strings.Join(lines, "\n")
}
