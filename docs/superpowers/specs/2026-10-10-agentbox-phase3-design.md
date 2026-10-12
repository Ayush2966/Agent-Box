# agentbox — Phase 3 Design: Terminal UI

**Date:** 2026-10-10
**Status:** Approved by ruling (see §0)
**Builds on:** phase 1 (committed) and phase 2 (`2026-10-09-agentbox-phase2-design.md`, built, uncommitted)

## 0. How this spec was approved

As for phase 2: the user asked for the roadmap to be finished autonomously, so the brainstorming
gates were cleared by the executor. Every decision that would normally have been a question is a
**Ruling** with its reasoning and cost-if-wrong.

## 1. Problem

Developers who run agents from a terminal multiplexer do not want to leave it to look at a browser
tab. `agentbox ls` is a snapshot; `agentbox logs -f` is one box at a time. There is no live,
keyboard-driven view of every box.

## 2. Goal

`agentbox tui` opens a full-screen terminal dashboard over the phase 1 API: every repo and box,
live state, logs and diff per box, and the same actions as the CLI and the web dashboard
(new, start, stop, merge, remove, doctor, db refresh), plus two things only a terminal can do
well: open a shell inside a box, and open the box's URL in the browser.

**Success criteria**
- `agentbox tui` shows a box's state change within a second of `agentbox start` running in
  another terminal, with no key pressed.
- Every lifecycle action is reachable in at most two keystrokes from the box list, and every
  destructive one asks first.
- The whole model is unit-testable without a terminal: `Update` is driven with messages and
  `View` is asserted as text, against a fake API.
- Works in 80×24; degrades by hiding columns, never by crashing.

## 3. Scope

**In:** the `internal/tui` package (bubbletea), `agentbox tui`, two client additions
(`Repos`, `LogLines`), tests, README. **Non-goals:** mouse support beyond what bubbletea gives
for free; editing `agentbox.yaml`; themes/config; a TUI for `init`; Windows.

## 4. Rulings

- **R1 — command is `agentbox tui`; bare `agentbox` keeps printing help.** Lazygit-style
  "bare command opens the UI" surprises scripts and `--help` readers; a subcommand is the cobra
  convention this CLI already follows. Cost if wrong: alias later.
- **R2 — transport is the unix socket through `internal/client`, no token.** The TUI runs as the
  same user on the same machine; the socket is already the CLI's channel. It auto-starts the
  daemon like every other command. Cost if wrong: none.
- **R3 — scope is all repos, with a repo filter that defaults to the current repo when the TUI
  is started inside one.** The web dashboard shows a repo at a time; a terminal user who runs
  three agents on two repos wants one screen. `r` cycles the filter. Cost if wrong: a flag.
- **R4 — one root model with a `mode` enum, not nested bubbletea sub-models per screen.** The
  screens share almost all state (boxes, selection, events, toasts); nesting would mean
  forwarding every message three levels down. Components from `bubbles` (table, viewport,
  textinput, spinner, help, key) are used for the leaves. Cost if wrong: refactor into
  sub-models when a fourth screen appears.
- **R5 — the API is consumed through a small interface the model owns (`tui.API`), satisfied by
  `*client.Client`.** It is what makes the model testable with a fake and keeps the TUI off the
  HTTP details. Cost if wrong: none; the interface mirrors the client.
- **R6 — dependencies:** `github.com/charmbracelet/bubbletea v1.3.10`, `bubbles v1.0.0`,
  `lipgloss v1.1.0`, all pinned with `go get <mod>@<ver>`. Probed in a scratch module: their
  transitive deps raise the `go` directive from `1.24.0` to **`1.24.2`**, which the local
  toolchain (go1.24.2) and CI (`go-version: '1.24'`) satisfy. The directive bump is accepted and
  recorded; no `toolchain` line is added. Cost if wrong: pin older charm versions.
- **R7 — "shell in box" (`e`) and "open URL" (`o`) are in scope.** They are cheap with
  `tea.ExecProcess` and the phase 2 `openBrowser`, and they are the two things a terminal user
  does most. `exec` stays CLI-only as a command; the TUI's shell is `$SHELL` (fallback `sh`) in
  the box dir with the box env. Cost if wrong: two keys to remove.
- **R8 — no `teatest`/golden tests.** They add a dependency and bind to exact rendering; the
  text-contains assertions on `View()` plus message-driven `Update` tests cover behaviour.
  Cost if wrong: add golden tests later.

## 5. Architecture

```
internal/tui/
  tui.go        Run(ctx, api API, opts Options) error — builds the model, runs tea.NewProgram (alt screen, ctx)
  api.go        type API interface { Repos, ListBoxes, GetBox, CreateBox, StartBox, StopBox, RemoveBox,
                  Merge, RefreshDB, Doctor, Diff, Events, LogLines }  (signatures = internal/client)
  model.go      Model struct, New(api, opts), Init, Update (dispatch by overlay → mode), View (header/body/footer)
  msgs.go       message types + the tea.Cmds that call the API (loadAll, fetchBox, waitEvent, streamLogs…)
  reduce.go     applyEvent(model, client.Event) — the same reducer rules as the web app (steps, failures, removed)
  keys.go       keyMap (key.Binding per action), ShortHelp/FullHelp per mode
  styles.go     lipgloss styles: header, pills per state, muted, error, selected row
  view_boxes.go the table screen: columns, row building, column hiding by width
  view_box.go   the box screen: facts + tabs (overview | logs | diff) with viewports
  view_new.go   the new-box form (name, from, no-dev) + progress
  view_doctor.go the doctor screen
  overlay.go    confirm {title, body, yes → cmd}, prompt {title, textinput, submit(string) → cmd}, toast
internal/client/client.go   + Repos(ctx) ([]api.RepoView, error); + LogLines(ctx, repo, name, tail) (<-chan string, error)
internal/cli/tui_cmd.go     `agentbox tui [--repo PATH] [--all]`
```

Message flow: `Init` → `loadAll` (repos + boxes) and `subscribe` (events). Events arrive one per
`waitEvent` cmd (the standard channel-pump pattern) and go through `applyEvent`; `box.created`
and `box.state` also trigger `fetchBox`. If the event channel closes, the model shows
`reconnecting`, waits 1 s → 2 s → … → 16 s, resubscribes and reloads everything on success.

## 6. Screens and keys

**Header** (one line): `agentbox` · connection pill (`live` / `reconnecting` / `connecting`) ·
`repo: <basename>` or `repo: all repos` · `N boxes`. **Footer**: short help for the current mode, or
the toast (error = red with the hint, ok = green) for 5 s.

### 6.1 Boxes (default)

Columns, in order: NAME · STATE (state text, plus the step while creating or `failed (<step>)`
when failed — plain text, since the table component truncates with a non-ANSI-aware width;
coloured pills appear in the header and on the box screen) · AHEAD · BEHIND (hidden < 90 cols) ·
DIRTY · URL (`a.localhost:7777`, hidden < 100) · REPO (only when filter = all, hidden < 120) ·
PORTS (hidden < 145). The thresholds are what the fixed column widths plus padding fit; the
spec's first draft listed narrower ones that did not. Sorted by repo then name. Empty state tells the user to run
`agentbox new <name>` in a repo.

| key | action |
|---|---|
| `↑/k` `↓/j` `g/G` | move |
| `enter` / `l` | open box screen |
| `n` | new box (needs a selected repo: the filter, or the selected row's repo) |
| `s` | start if ready/stopped, stop if running |
| `x` | remove (confirm; on `unmerged_work` a second confirm "Remove anyway?" with `K` to keep branch) |
| `m` | merge (prompt for `into`, empty = default; `ctrl+s` in the prompt toggles squash — a plain `S` would type into the field) |
| `d` | doctor screen for the row's repo |
| `R` | refresh DB template for the row's repo (confirm) |
| `r` | cycle repo filter: all → repo1 → repo2 → all |
| `o` | open the box URL in the browser |
| `e` | shell in the box (`$SHELL` in the box dir with the box env; returns to the TUI on exit) |
| `/` | filter rows by name substring (`esc` clears) |
| `g` (with no row) / `ctrl+r` / `F5` | reload |
| `?` | full help |
| `q` / `ctrl+c` | quit |

### 6.2 Box

Header line: `← boxes   <name>  [state]  <step…>`. Tabs: `1` overview · `2` logs · `3` diff
(`tab` cycles). Overview: the same facts as the web page (dir, branch, base, ahead/behind, dirty,
ports, url, db, pid, created, last error) and the managed env. Logs: streamed via `LogLines`
(tail 500, 5 000-line ring) into a viewport that follows the tail unless scrolled up (`G` to
re-follow, `c` to clear). Diff: against the box base, in a viewport; `a` prompts for another ref.
Keys: `s` `x` `m` `o` `e` as in the list; `esc`/`h` back. Entering the logs tab starts the
stream; leaving the box screen stops it.

### 6.3 New box

Fields: name (validated against `^[a-z0-9][a-z0-9-]{0,30}$` live), start from (ref), no dev
(`space` toggles). `enter` submits → `CreateBox`; the screen then shows the step stream for that
box id and the failure text if creation fails or rolls back; when the box reaches `ready`/`running`
it opens the box screen. `esc` cancels (before submit) or returns to the list (after).

### 6.4 Doctor

List of findings with kind pills; `f` fixes what is safe (`POST`), `r` re-checks, `esc` back.
"✓ no problems found" when empty.

### 6.5 Overlays

Confirm: `y`/`enter` yes, `n`/`esc` no; the second, destructive confirm ("Discard unmerged
work?") takes `y` only, because it appears asynchronously and an `enter` meant for the list must
not force-remove; a confirm may carry one toggle (`K` keep branch, `T` terminate connections). Prompt: a text input with `enter`/`esc`. Overlays swallow every other key.

## 7. Errors and edge cases

- Every API error becomes a toast `step failed: message — hint`. `precondition`/`conflict`
  codes are shown as-is; the action is not retried.
- Terminal narrower than 60 cols or shorter than 10 rows shows a one-line "terminal too small".
- If stdout is not a terminal, `agentbox tui` exits 1 with "agentbox tui needs an interactive
  terminal (try `agentbox ls --json` for scripts)".
- A box that disappears while its screen is open returns to the list with a toast.
- The daemon going away: the events channel closes → `reconnecting`; `EnsureRunning` is the
  CLI's job before the program starts, the TUI only reconnects.

## 8. Testing

- **Unit (no terminal, fake API):** boxes table renders rows and hides columns by width; events
  update rows/steps/failures; key navigation and `enter` opens the box; `s` toggles via the fake
  and toasts; `x` → confirm → remove; `unmerged_work` → second confirm → force remove; `m` prompt →
  merge; new-box form validation and submit → steps → opens box; doctor fix; repo filter cycling;
  reconnect backoff schedule; too-small terminal; toast expiry.
- **Integration:** an in-process daemon (as in the CLI tests) with a real `client.Client`:
  `loadAll` returns the created box; `LogLines` streams the box's log; `Events` delivers a
  `box.state` after `StartBox`.
- **CLI:** `tui` refuses a non-terminal with exit 1 and the hint; `--help` lists it.
- `go vet`, `go test -race ./...`, and the existing e2e stay green.

## 9. Risks

- bubbletea's alt screen + `ExecProcess` for the shell: bubbletea restores the terminal around
  the child; tested manually on macOS Terminal and tmux.
- Rendering width of CJK/emoji in box names is impossible (names are `[a-z0-9-]`), so column
  math is safe.
- `go` directive moves to `1.24.2` (R6).
