package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/errs"
)

type (
	reposMsg struct {
		repos []api.RepoView
		err   error
	}
	boxesMsg struct {
		boxes []api.BoxView
		err   error
	}
	boxMsg struct {
		id  string
		box api.BoxView
		err error
	}
	subscribedMsg struct{ ch <-chan client.Event }
	eventMsg      struct{ ev client.Event }
	eventsDownMsg struct{ err error } // subscribe failed or the stream closed
	reconnectMsg  struct{}
	doneMsg       struct {
		what string
		err  error
	} // an action finished; what is the success toast
	unmergedMsg struct {
		box     api.BoxView
		keep    bool
		message string
	}
	createdMsg struct {
		id  string
		err error
	}
	findingsMsg struct {
		repo     string
		findings []box.Finding
		err      error
	}
	diffMsg struct {
		id, text string
		err      error
	}
	logsOpenMsg struct {
		id   string
		seq  int
		ch   <-chan string
		stop func()
	}
	logLineMsg struct {
		id   string
		seq  int
		line string
	}
	logsDoneMsg struct {
		id  string
		seq int
		err error
	}
	toastGoneMsg struct{ seq int }
	toastMsg     struct {
		text string
		err  bool
	} // a toast raised from a value-receiver helper
	shellDoneMsg struct{ err error }
)

// tell is how value-receiver helpers raise a toast: the message reaches
// Update, which owns the model that must change.
func tell(isErr bool, text string) tea.Cmd {
	return func() tea.Msg { return toastMsg{text, isErr} }
}

func (m Model) loadAll() tea.Cmd { return tea.Batch(m.loadRepos(), m.loadBoxes()) }

func (m Model) loadRepos() tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg { rs, err := a.Repos(ctx); return reposMsg{rs, err} }
}

func (m Model) loadBoxes() tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg { bs, err := a.ListBoxes(ctx, ""); return boxesMsg{bs, err} }
}

func (m Model) fetchBox(id string) tea.Cmd {
	a, ctx := m.api, m.ctx
	repo, name := splitID(id)
	return func() tea.Msg { b, err := a.GetBox(ctx, repo, name); return boxMsg{id, b, err} }
}

func (m Model) subscribe() tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg {
		ch, err := a.Events(ctx)
		if err != nil {
			return eventsDownMsg{err}
		}
		return subscribedMsg{ch}
	}
}

// waitEvent delivers the next event, or eventsDownMsg when the stream closes.
func waitEvent(ch <-chan client.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return eventsDownMsg{}
		}
		return eventMsg{ev}
	}
}

func reconnectAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return reconnectMsg{} })
}

// action runs fn and reports its outcome as a toast.
func (m Model) action(what string, fn func(ctx context.Context) error) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg { return doneMsg{what, fn(ctx)} }
}

func (m Model) remove(b api.BoxView, force, keep bool) tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg {
		err := a.RemoveBox(ctx, b.Repo, b.Name, force, keep)
		if e := errs.From(err); e != nil && !force && e.Code == errs.UnmergedWork {
			return unmergedMsg{b, keep, e.Message}
		}
		return doneMsg{"removed " + b.Name, err}
	}
}

func (m Model) create(opts box.NewOptions) tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg { c, err := a.CreateBox(ctx, opts); return createdMsg{c.ID, err} }
}

func (m Model) runDoctor(repo string, fix bool) tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg { fs, err := a.Doctor(ctx, repo, fix); return findingsMsg{repo, fs, err} }
}

func (m Model) loadDiff(id, against string) tea.Cmd {
	a, ctx := m.api, m.ctx
	repo, name := splitID(id)
	return func() tea.Msg { d, err := a.Diff(ctx, repo, name, against); return diffMsg{id, d, err} }
}

func (m Model) openLogs(id string, seq int) tea.Cmd {
	a := m.api
	ctx, cancel := context.WithCancel(m.ctx)
	repo, name := splitID(id)
	return func() tea.Msg {
		ch, err := a.LogLines(ctx, repo, name, 500)
		if err != nil {
			cancel()
			return logsDoneMsg{id, seq, err}
		}
		return logsOpenMsg{id, seq, ch, cancel}
	}
}

func waitLine(id string, seq int, ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return logsDoneMsg{id, seq, nil}
		}
		return logLineMsg{id, seq, line}
	}
}

func toastTimer(seq int, ttl time.Duration) tea.Cmd {
	return tea.Tick(ttl, func(time.Time) tea.Msg { return toastGoneMsg{seq} })
}
