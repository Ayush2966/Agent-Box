package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/client"
)

type mode int

const (
	modeBoxes mode = iota
	modeBox
	modeNew
	modeDoctor
)

type connState int

const (
	connConnecting connState = iota
	connLive
	connReconnecting
)

// Options tune the program; zero values are the defaults.
type Options struct {
	Repo          string                 // repo ID preselected as the filter; "" = all repos
	OpenBrowser   func(url string) error // nil = the platform opener
	Shell         string                 // "" = $SHELL, then sh
	ToastTTL      time.Duration          // 0 = 5s; < 0 = toasts never expire (tests)
	ReconnectBase time.Duration          // 0 = 1s; doubles up to 16×
	NoBlink       bool                   // static cursors in text inputs (tests; dumb terminals)
}

type toast struct {
	text string
	err  bool
}

type Model struct {
	api  API
	ctx  context.Context
	opts Options
	keys keyMap
	help help.Model

	width, height int
	mode          mode
	overlay       overlay
	showHelp      bool

	repos    []api.RepoView
	filter   string // repo ID, "" = all
	query    string
	querying bool
	boxes    map[string]api.BoxView
	steps    map[string]string
	failures map[string]string
	table    table.Model
	rowIDs   []string

	conn    connState
	backoff time.Duration
	events  <-chan client.Event

	// box screen
	cur         string
	tab         boxTab
	logs        []string
	logFollow   bool
	logID       string
	logSeq      int // identifies the current log stream
	logCh       <-chan string
	logStop     func()
	logView     viewport.Model
	infoView    viewport.Model
	diff        string
	diffLoaded  bool
	diffLoading bool
	diffAgainst string
	diffView    viewport.Model

	form   newForm
	doctor doctorState

	toast    *toast
	toastSeq int
}

func New(ctx context.Context, a API, opts Options) Model {
	if opts.ToastTTL == 0 {
		opts.ToastTTL = 5 * time.Second
	}
	if opts.ReconnectBase == 0 {
		opts.ReconnectBase = time.Second
	}
	if opts.OpenBrowser == nil {
		opts.OpenBrowser = openBrowser
	}
	if opts.Shell == "" {
		opts.Shell = os.Getenv("SHELL")
		if opts.Shell == "" {
			opts.Shell = "sh"
		}
	}
	st := table.DefaultStyles()
	st.Header = st.Header.Bold(true).Foreground(lipgloss.Color("244")).BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).BorderForeground(lipgloss.Color("240"))
	st.Selected = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("238"))
	t := table.New(table.WithFocused(true), table.WithHeight(10), table.WithStyles(st))
	return Model{
		api: a, ctx: ctx, opts: opts, keys: newKeyMap(), help: help.New(),
		filter: opts.Repo,
		boxes:  map[string]api.BoxView{}, steps: map[string]string{}, failures: map[string]string{},
		table: t, logFollow: true,
		logView: viewport.New(80, 10), infoView: viewport.New(80, 10), diffView: viewport.New(80, 10),
	}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.loadAll(), m.subscribe()) }

// bodyHeight is the room between the header and footer lines.
func (m Model) bodyHeight() int { return max(m.height-2, 1) }

func (m *Model) layout() {
	h := m.bodyHeight()
	m.table.SetWidth(m.width)
	m.table.SetHeight(max(h-1, 1))
	// head + tabs + status line sit above the viewports
	m.logView.Width, m.logView.Height = m.width, max(h-3, 1)
	m.diffView.Width, m.diffView.Height = m.width, max(h-3, 1)
	m.infoView.Width, m.infoView.Height = m.width, max(h-3, 1)
	m.help.Width = m.width
	m.help.ShortSeparator = "  " // the default " • " pushes the boxes help past 80 columns
	m.refreshTable()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)

	case reposMsg:
		if msg.err != nil {
			cmd := m.say(true, describe(msg.err))
			return m, cmd
		}
		m.repos = msg.repos
		if m.filter != "" && !m.hasRepo(m.filter) {
			m.filter = ""
		}
		m.refreshTable()
		return m, nil
	case boxesMsg:
		if msg.err != nil {
			cmd := m.say(true, describe(msg.err))
			return m, cmd
		}
		m.boxes = map[string]api.BoxView{}
		for _, b := range msg.boxes {
			m.boxes[b.ID] = b
		}
		for id := range m.failures {
			if _, ok := m.boxes[id]; ok {
				delete(m.failures, id)
			}
		}
		for id := range m.steps {
			if b, ok := m.boxes[id]; !ok || terminalState(b.State) {
				delete(m.steps, id)
			}
		}
		m.refreshTable()
		if m.mode == modeBox {
			if _, ok := m.boxes[m.cur]; !ok {
				m.leaveBox()
			}
		}
		m.refreshInfo()
		var cmd tea.Cmd
		if m.mode == modeNew && m.form.submitted != "" {
			cmd = m.afterBoxUpdate(m.form.submitted)
		}
		return m, cmd
	case boxMsg:
		if msg.err != nil {
			return m, nil // removed meanwhile
		}
		m.boxes[msg.id] = msg.box
		m.refreshTable()
		if msg.id == m.cur {
			m.refreshInfo()
		}
		cmd := m.afterBoxUpdate(msg.id)
		return m, cmd

	case subscribedMsg:
		m.conn, m.backoff, m.events = connLive, 0, msg.ch
		var logsCmd tea.Cmd
		if m.mode == modeBox && m.tab == tabLogs && m.logID == "" {
			logsCmd = m.setTab(tabLogs)
		}
		return m, tea.Batch(m.loadAll(), waitEvent(msg.ch), logsCmd)
	case eventMsg:
		cmd := m.applyEvent(msg.ev)
		return m, tea.Batch(cmd, waitEvent(m.events))
	case eventsDownMsg:
		m.conn = connReconnecting
		switch {
		case m.backoff == 0:
			m.backoff = m.opts.ReconnectBase
		case m.backoff < 16*m.opts.ReconnectBase:
			m.backoff *= 2
		}
		return m, reconnectAfter(m.backoff)
	case reconnectMsg:
		return m, m.subscribe()

	case doneMsg:
		if msg.err != nil {
			cmd := m.say(true, describe(msg.err))
			return m, tea.Batch(cmd, m.loadBoxes())
		}
		cmd := m.say(false, msg.what)
		return m, tea.Batch(cmd, m.loadBoxes())
	case unmergedMsg:
		b := msg.box
		m.showHelp = false
		m.overlay = &confirm{title: "Discard unmerged work?", body: msg.message,
			toggle:  &toggle{key: "K", label: "keep the " + b.Branch + " branch", on: msg.keep},
			yesOnly: true,
			yes:     func(m *Model, keep bool) tea.Cmd { return m.remove(b, true, keep) }}
		return m, nil
	case toastGoneMsg:
		if msg.seq == m.toastSeq {
			m.toast = nil
		}
		return m, nil
	case toastMsg:
		cmd := m.say(msg.err, msg.text)
		return m, cmd
	case shellDoneMsg:
		var ee *exec.ExitError
		if msg.err != nil && !errors.As(msg.err, &ee) { // a non-zero exit is the user's shell, not a failure
			cmd := m.say(true, "shell: "+msg.err.Error())
			return m, cmd
		}
		return m, m.loadBoxes()

	case createdMsg:
		return m.onCreated(msg)
	case findingsMsg:
		return m.onFindings(msg)
	case diffMsg:
		return m.onDiff(msg)
	case logsOpenMsg:
		return m.onLogsOpen(msg)
	case logLineMsg:
		return m.onLogLine(msg)
	case logsDoneMsg:
		return m.onLogsDone(msg)
	}
	// Anything else (cursor blinks, …) goes to whichever text input is live.
	return m.forwardToInputs(msg)
}

func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlC {
		m.stopLogs()
		return m, tea.Quit
	}
	if m.overlay != nil {
		cmd := m.overlay.handle(&m, k)
		return m, cmd
	}
	if m.querying {
		return m.handleQueryKey(k)
	}
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}
	if m.mode == modeNew { // text fields own every key, including q
		return m.updateNew(k)
	}
	if key.Matches(k, m.keys.Quit) {
		m.stopLogs()
		return m, tea.Quit
	}
	if key.Matches(k, m.keys.Help) {
		m.showHelp = true
		return m, nil
	}
	switch m.mode {
	case modeBox:
		return m.updateBox(k)
	case modeDoctor:
		return m.updateDoctor(k)
	}
	return m.updateBoxes(k)
}

func (m Model) handleQueryKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEscape:
		m.querying, m.query = false, ""
	case tea.KeyEnter:
		m.querying = false
	case tea.KeyBackspace:
		if m.query != "" {
			m.query = m.query[:len(m.query)-1]
		}
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyRunes:
		m.query += string(k.Runes)
	}
	m.refreshTable()
	return m, nil
}

// say shows a toast for ToastTTL. Each toast has a sequence number, so an
// older timer can't clear a newer toast.
func (m *Model) say(isErr bool, text string) tea.Cmd {
	m.toastSeq++
	m.toast = &toast{text: text, err: isErr}
	if m.opts.ToastTTL < 0 { // tests: never expire on their own
		return nil
	}
	return toastTimer(m.toastSeq, m.opts.ToastTTL)
}

func (m Model) hasRepo(id string) bool {
	for _, r := range m.repos {
		if r.ID == id {
			return true
		}
	}
	return false
}

func (m Model) repoPath(id string) string {
	for _, r := range m.repos {
		if r.ID == id {
			return r.Path
		}
	}
	return id
}

// --- view ---

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if m.width < 60 || m.height < 10 {
		return "terminal too small: agentbox tui needs at least 60×10"
	}
	h := m.bodyHeight()
	var body string
	switch {
	case m.overlay != nil:
		body = lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, m.overlay.view(&m))
	case m.showHelp:
		body = m.help.FullHelpView(m.fullHelp())
	default:
		switch m.mode {
		case modeBox:
			body = m.viewBox(h)
		case modeNew:
			body = m.viewNew(h)
		case modeDoctor:
			body = m.viewDoctor(h)
		default:
			body = m.viewBoxes(h)
		}
	}
	body = lipgloss.NewStyle().Width(m.width).Height(h).MaxHeight(h).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), body, m.footer())
}

func (m Model) header() string {
	repo := "all repos"
	if m.filter != "" {
		repo = repoBase(m.repoPath(m.filter))
	}
	parts := []string{stBrand.Render("agentbox"), connPill(m.conn), stMuted.Render("repo: ") + repo,
		stMuted.Render(fmt.Sprintf("%d boxes", len(m.visibleBoxes())))}
	if m.querying || m.query != "" {
		q := "/" + m.query
		if m.querying {
			q += "▏"
		}
		parts = append(parts, stAccent.Render(q))
	}
	return lipgloss.NewStyle().Width(m.width).MaxHeight(1).Render(strings.Join(parts, "  "))
}

func (m Model) footer() string {
	var s string
	if m.toast != nil {
		if m.toast.err {
			s = stErr.Render(m.toast.text)
		} else {
			s = stOK.Render(m.toast.text)
		}
	} else {
		s = m.help.ShortHelpView(m.shortHelp())
	}
	return lipgloss.NewStyle().Width(m.width).MaxHeight(1).Render(s)
}

// --- shared actions (boxes and box screens) ---

func (m Model) toggleRun(b api.BoxView) tea.Cmd {
	a := m.api
	switch b.State {
	case "running":
		return m.action("stopped "+b.Name, func(ctx context.Context) error { return a.StopBox(ctx, b.Repo, b.Name) })
	case "ready", "stopped":
		return m.action("started "+b.Name, func(ctx context.Context) error { return a.StartBox(ctx, b.Repo, b.Name) })
	}
	return tell(true, fmt.Sprintf("%s is %s; nothing to start or stop", b.Name, b.State))
}

func (m *Model) askRemove(b api.BoxView) {
	m.overlay = &confirm{title: "Remove " + b.Name + "?",
		body:   "Stops the dev server, drops the database copy, frees the ports and deletes the worktree at " + b.Dir + ".",
		toggle: &toggle{key: "K", label: "keep the " + b.Branch + " branch"},
		yes:    func(m *Model, keep bool) tea.Cmd { return m.remove(b, false, keep) }}
}

func (m *Model) askMerge(b api.BoxView) {
	a := m.api
	m.overlay = newPrompt("Merge "+b.Branch+" into…", "the branch checked out in the main checkout",
		&toggle{key: "ctrl+s", label: "squash into one commit"}, m.opts.NoBlink,
		func(m *Model, into string, squash bool) tea.Cmd {
			ctx := m.ctx
			return func() tea.Msg {
				res, err := a.Merge(ctx, b.Repo, b.Name, box.MergeOptions{Into: into, Squash: squash})
				if err != nil {
					return doneMsg{"", err}
				}
				return doneMsg{"merged " + b.Branch + " into " + res.Into + " — remove the box when you are done", nil}
			}
		})
}

func (m *Model) askRefresh(repo string) {
	a := m.api
	m.overlay = &confirm{title: "Refresh the database template of " + repoBase(m.repoPath(repo)) + "?",
		body:   "Re-snapshots the source database so that new boxes start from its current contents. Existing boxes keep their copies.",
		toggle: &toggle{key: "T", label: "terminate stray connections to the template (Postgres)"},
		yes: func(m *Model, terminate bool) tea.Cmd {
			return m.action("database template refreshed", func(ctx context.Context) error { return a.RefreshDB(ctx, repo, terminate) })
		}}
}

func (m Model) openURL(b api.BoxView) tea.Cmd {
	if b.URL == "" {
		return tell(true, b.Name+" has no ports, so no URL")
	}
	open := m.opts.OpenBrowser
	return m.action("opened "+b.URL, func(context.Context) error { return open(b.URL) })
}

func (m Model) shell(b api.BoxView) tea.Cmd {
	c := exec.Command(m.opts.Shell)
	c.Dir = b.Dir
	c.Env = append(os.Environ(), envList(b.Env)...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return shellDoneMsg{err} })
}

// openEditor asks the daemon to open the box in the configured editor; it
// opens in its own window, so the TUI keeps running.
func (m Model) openEditor(b api.BoxView) tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg {
		what, err := a.OpenBox(ctx, b.Repo, b.Name, "editor")
		if err != nil {
			return doneMsg{"", err}
		}
		return doneMsg{"opened " + b.Name + " in " + what, nil}
	}
}

func openBrowser(u string) error {
	var c *exec.Cmd
	if runtime.GOOS == "darwin" {
		c = exec.Command("open", u)
	} else {
		c = exec.Command("xdg-open", u)
	}
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait() // reap the opener
	return nil
}
