# agentbox — Phase 1 Design: Core Engine + CLI

**Date:** 2026-09-25
**Status:** Draft, awaiting review

## 1. Problem

Developers increasingly run several AI coding agents (Claude Code, Cursor, Codex, Aider) against one repository at the same time. The agents collide: they edit the same working tree, fight over dev-server ports, share one database (one agent's migration breaks another's tests), and share `.env` files. Git worktrees solve only the file part. Nothing sets up the *whole* dev environment per agent.

## 2. Goal

`agentbox` gives each parallel agent an isolated, running dev environment ("box"): its own git worktree, its own ports, its own database copy, its own env file, and a supervised dev server, all created in seconds from a small per-repo config file.

**Success criteria for phase 1**
- `agentbox new a && agentbox new b && agentbox new c` on the example app gives 3 dev servers running side by side, each on its own ports with its own database, and no conflicts.
- `new` finishes in under about 10 s, excluding the project's own `setup` command (e.g. `npm install`).
- Onboarding a project takes one `agentbox.yaml` file, and `agentbox init` generates a working starter one.
- No failure path leaves behind orphaned worktrees, databases, ports or processes.
- `rm` never silently discards unmerged or uncommitted work.

## 3. Scope

**In phase 1:** core engine, CLI, background daemon, versioned local HTTP API, Postgres and SQLite drivers, macOS and Linux.

**Non-goals for phase 1:**
- Security sandboxing of untrusted code. agentbox prevents conflicts; it is not a jail.
- Windows.
- Remote or cloud boxes.
- Reverse proxy or stable hostnames (phase 2).
- Docker Compose and MySQL drivers (phase 4).
- Sharing or caching `node_modules` between boxes. Each box runs `setup` itself.

## 4. Roadmap (for context; each phase gets its own spec)

| Phase | Deliverable |
|---|---|
| 1 | Core + CLI + daemon + API (this spec) |
| 2 | Web dashboard (`agentbox ui`, React embedded via `go:embed`), reverse proxy `<box>.localhost` |
| 3 | TUI (bubbletea) on the same API |
| 4 | Docker Compose driver, MySQL driver |
| 5 | Desktop app (Wails) reusing phase-2 React code |

Phase 1 must ship the API those phases need (`/v1/events`, log streaming, diff) even though only the CLI uses it at first.

## 5. Configuration: `agentbox.yaml`

It lives at the repo root and is committed.

```yaml
version: 1
setup: npm install            # optional; runs once in each new box, in the box dir, with box env
dev: npm run dev              # optional; supervised long-running process
ports:                        # env var names; each gets a free port per box
  - APP_PORT
  - API_PORT
db:                           # optional
  driver: postgres            # postgres | sqlite
  url_env: DATABASE_URL       # env var carrying the connection string / file path
  source: postgres://postgres@localhost:5432/myapp   # postgres: source DB URL; sqlite: path relative to repo
env:
  template: .env.example      # optional; base env file copied into the box as .env
  file: .env                  # optional; output file name inside the box (default .env)
  set:                        # optional; extra static vars
    NODE_ENV: development
copy:                         # optional; untracked files copied from the main checkout (e.g. local secrets)
  - .env.local
```

**Validation rules**
- Only `version: 1` is accepted. Unknown keys are an error, with a "did you mean" suggestion.
- Port names must be valid env var names and unique.
- `db.url_env` must not also appear in `ports`.
- Paths must stay inside the repo (no `..` escapes).

**Env generation order** (later entries win): template file, then `env.set`, then allocated ports, then DB URL, then `AGENTBOX_NAME=<box>` and `AGENTBOX_ROOT=<box dir>`. Keys in the template that are also agentbox-managed are rewritten in place, preserving comments and ordering. Missing managed keys are appended.

## 6. Box model

- **Identity:** `(repo, name)`. The name matches `^[a-z0-9][a-z0-9-]{0,30}$` and is unique per repo. The repo is identified by the absolute path of its main worktree.
- **Location:** `~/.agentbox/boxes/<repo-slug>/<name>/`, where `repo-slug` is the repo's base name plus a short hash of its path. The location can be overridden with `AGENTBOX_HOME`. Boxes live **outside** the repo on purpose: nested worktrees would be picked up by file watchers, bundlers, linters and test runners in the main checkout.
- **Branch:** `agentbox/<name>`, created from `--from` (default: the current `HEAD` of the main checkout).
- **States:** `creating → ready → running ⇄ stopped → removing → (gone)`, plus `failed`, which records `{step, error}`.
  - `ready` means created, with no dev server (or `--no-dev`).
  - A dev server that crashes is restarted with backoff (1 s, 2 s, 4 s, 8 s, 16 s). After 5 consecutive crashes within 60 s the box goes to `stopped`, with `last_error` set.

## 7. Lifecycle operations

### `new`
Steps run in order, and each step registers an undo. On failure the undos run in reverse, and the box ends up removed (the default) or in `failed` if `--keep-failed` is passed (for debugging).

| # | Step | Undo |
|---|---|---|
| 1 | Validate config and name; take the per-repo lock | Release the lock |
| 2 | `git worktree add -b agentbox/<name> <dir> <from>` | `git worktree remove --force`, delete the branch |
| 3 | Allocate ports (in a state-DB transaction) | Free the ports |
| 4 | Clone the DB | Drop the DB |
| 5 | Copy `copy:` files, then write the env file | Nothing needed (the dir is removed in step 2's undo) |
| 6 | Run `setup` (with streaming output) | Nothing needed |
| 7 | Start `dev` (unless `--no-dev`) | Stop the process |

It prints the box dir, branch, ports, DB name, and a ready-to-paste `cd` line.

### `rm`
The dev server is stopped, then the DB dropped, ports freed, worktree removed and branch deleted (unless `--keep-branch`). If the branch has commits not in the target branch, or the worktree has uncommitted changes, it asks for confirmation (`--force` skips it). Each step runs even if an earlier one fails. Failures are reported together at the end, and anything left over is flagged for `doctor`.

### `merge`
It merges `agentbox/<name>` into `--into` (default: the main checkout's current branch). Preconditions: the box worktree has no uncommitted changes, and the main checkout is clean and currently on the target branch. If a precondition fails, it refuses with an explanation and never switches branches or stashes on the user's behalf. It uses `--squash` if requested, otherwise a normal merge. On conflict it runs `git merge --abort` and reports the conflicting files. After a successful merge the box is kept, and the output suggests `agentbox rm <name>`.

### `start` / `stop`
These control only the dev server. The worktree, DB and ports are kept.

### `exec <name> -- <cmd...>`
Runs a command in the box dir with the box env. It is executed by the CLI directly (not the daemon), inherits the terminal, and exits with the command's exit code.

### `doctor [--fix]`
Detects drift and reports it:
- A state row whose worktree dir is missing.
- An `agentbox/*` branch or worktree with no state row.
- A `*_agentbox_*` DB with no box.
- An allocated port held by a process that doesn't belong to its box.
- The daemon pid file pointing to a dead process.
- Missing tools (`git`, and `pg_dump`/`pg_restore` when a postgres driver is configured).

`--fix` repairs what it can safely repair and lists the rest.

## 8. Database drivers

```go
type Driver interface {
    Prepare(ctx, cfg) error                        // one-time per repo, e.g. build the template
    Clone(ctx, cfg, box string) (url string, err error)
    Drop(ctx, cfg, box string) error
    Refresh(ctx, cfg) error                        // re-snapshot the source into the template
    Check(ctx, cfg) error                          // for doctor
}
```

### Postgres
- `CREATE DATABASE x TEMPLATE y` requires that nobody is connected to `y`, and the user's main dev server usually holds connections to the source DB. So agentbox never templates from the source directly.
- **Template DB:** `<srcdb>_agentbox_tpl`. It is built or refreshed with `pg_dump -Fc source | pg_restore` into a freshly created tpl DB. This works while the source is in use. It happens on the first `new` for the repo (`Prepare`) and on `agentbox db refresh`.
- **Box DB:** `<srcdb>_agentbox_<box>`, created with `CREATE DATABASE … TEMPLATE <tpl>`, which is effectively instant.
- If the tpl DB has stray connections, agentbox reports the pids and refuses, unless `--terminate` is passed, in which case it calls `pg_terminate_backend` on the tpl only, never on the source.
- Role requirement: the source URL's role needs `CREATEDB`. This is checked in `Check`.
- The box URL is the source URL with the database name replaced.

### SQLite
- **Clone:** `VACUUM INTO '<box dir>/<relative path>'`, which gives a consistent copy even when the source is in WAL mode or in use.
- **Drop:** delete the file and its `-wal`/`-shm` siblings. This is normally redundant, since the worktree is removed anyway.
- **Refresh:** a no-op, since every clone is fresh.
- The box "URL" is the file path, relative or absolute to match the form used in the source config.

## 9. Ports

- They are allocated from the range `20000–29999`, configurable in `~/.agentbox/config.yaml`.
- Allocation happens inside a state-DB transaction: pick the lowest port not recorded, then verify it's actually free with `net.Listen` on `127.0.0.1` and `::1`. If it's in use, it is skipped and not recorded. The whole thing is serialized by the daemon, so concurrent `new` calls can't pick the same port.
- Ports are freed on `rm`. `doctor` finds leaked ones.

## 10. Process supervisor

- The dev command runs as `sh -c "<dev>"` in the box dir, with the box env, in its **own process group**. It is stopped by sending SIGTERM to the whole group, then SIGKILL after 10 s. This matters because `npm run dev` spawns child processes.
- stdout and stderr go to `~/.agentbox/logs/<repo-slug>/<box>.log`, rotated at 10 MB with 3 kept, and to an in-memory ring buffer of the last 2,000 lines for fast `logs` and SSE.
- Supervised processes are children of the daemon. If the daemon dies they die with it (by process-group kill on shutdown and a parent-death check on Linux). On daemon start, boxes recorded as `running` whose pid is gone are marked `stopped`. Nothing is started automatically.

## 11. Daemon

- It is a single binary: `agentbox daemon run` is the daemon, and all other subcommands are clients.
- **Auto-start:** each CLI call tries the socket, and if it's absent, spawns `agentbox daemon run` detached (setsid), then waits up to 3 s for the socket to appear.
- There is one daemon per user per machine, shared across repos, because ports are a machine-wide resource.
- **Files under `~/.agentbox/`:** `state.db`, `daemon.sock` (0600), `daemon.pid`, `token` (0600), `agentbox.log`, `logs/`, `boxes/`, `config.yaml`.
- **Concurrency:** one mutex per repo for lifecycle operations (`new`, `rm`, `merge`, `db refresh`). Reads are never blocked.
- `agentbox daemon status|stop|restart` are provided. `stop` stops all dev servers first and prints what it stopped.

## 12. Local API (v1)

- **Transports:** Unix socket (used by the CLI; no token needed, since file permissions protect it) and `127.0.0.1:7777` (for the phase 2 and 3 UIs).
- **HTTP auth:** `Authorization: Bearer <token>` is required, and the token is read from `~/.agentbox/token`. Requests with an `Origin` header not on the allowlist are rejected, as a defense against DNS rebinding and browser CSRF. The phase 2 UI will get the token injected when served by the daemon.
- **Format:** JSON. Errors use `{"error": {"code", "message", "step", "hint"}}`.

```
GET    /v1/health
GET    /v1/repos                          known repos
GET    /v1/boxes?repo=                    list
POST   /v1/boxes                          {repo, name, from, no_dev, keep_failed} → 202 + box; progress via events
GET    /v1/boxes/{id}                     details: state, ports, db, git (ahead/behind, dirty count)
POST   /v1/boxes/{id}/start
POST   /v1/boxes/{id}/stop
POST   /v1/boxes/{id}/merge               {into, squash}
DELETE /v1/boxes/{id}?force=&keep_branch=
GET    /v1/boxes/{id}/logs?tail=&follow=  text; SSE when follow=true
GET    /v1/boxes/{id}/diff?against=       unified diff
POST   /v1/repos/{id}/db/refresh
GET    /v1/doctor?repo=
GET    /v1/events                         SSE: box.created, box.state, box.step, box.removed
```

`id` = `<repo-slug>/<name>`, URL-encoded. The CLI renders `new` progress by consuming `box.step` events.

## 13. CLI

Built with cobra. Commands: `init`, `new`, `ls`, `logs`, `start`, `stop`, `exec`, `merge`, `rm`, `db refresh`, `doctor`, `daemon status|stop|restart`, `version`.

- The repo is resolved from the current directory, including from inside a box dir, which maps back to its repo. `--repo <path>` overrides it.
- `ls` prints a table by default, or `--json` for scripts and agents.
- `init` looks at the repo for `package.json` scripts (`dev`), `go.mod`, `.env.example`, `DATABASE_URL`-like keys, `PORT`-like keys and `*.sqlite`/`*.db` files, then writes a commented `agentbox.yaml`. It never overwrites an existing file.
- **Exit codes:** 0 ok, 1 error, 2 usage, 3 precondition refused (e.g. merge on a dirty tree).

## 14. Errors and observability

- Every error names the **step**, the **cause** and a **hint**, e.g. `db clone failed: role "app" lacks CREATEDB — hint: ALTER ROLE app CREATEDB;`.
- The daemon writes structured logs (`log/slog`, JSON) to `~/.agentbox/agentbox.log`.
- `--verbose` on any command prints the git and SQL commands being run.

## 15. Testing

- **Unit:** config parsing and validation, env rewrite (comments and order preserved), port allocator (including concurrent allocation), state machine transitions, undo-stack ordering.
- **Integration:** real git in temp dirs (worktree add/remove, merge, conflict abort), real SQLite, and Postgres through testcontainers-go (template build, clone, drop, stray-connection refusal). Rollback is tested by injecting a failure at every step of `new` and asserting nothing is left over.
- **Supervisor:** a fake dev command that spawns children, to assert the whole group gets killed; crash-loop backoff reaching `stopped`.
- **E2E:** `examples/node-postgres` and `examples/sqlite`. The script creates 3 boxes, curls each box's port, checks each one's response reports its own DB name, then removes all 3 and asserts a clean `doctor`.
- **CI:** GitHub Actions on ubuntu-latest and macos-latest. Postgres tests run on Linux only (they need Docker).
- **Release:** GoReleaser producing darwin/linux × amd64/arm64 builds and a Homebrew tap.

## 16. Tech choices

- Go 1.24. `cobra` for the CLI. `modernc.org/sqlite` for the state DB and the SQLite driver (pure Go, no cgo, so cross-compiling stays easy). `pgx` for Postgres admin SQL. `pg_dump`/`pg_restore` as external tools. `net/http` with the standard mux (Go 1.22+ patterns). `log/slog` for logging.
- Git is driven through the `git` CLI, not a Go git library, for full worktree support and exactly the user's own git behavior.

## 17. Repo layout

```
agentbox/
  cmd/agentbox/main.go
  internal/
    config/     agentbox.yaml parse + validate
    worktree/   git worktree/branch/merge/diff wrapper
    ports/      allocator
    db/         Driver interface + postgres/ + sqlite/
    envfile/    template rewrite
    proc/       supervisor, process groups, log ring
    state/      state.db schema + queries
    box/        lifecycle orchestration + undo stack
    daemon/     startup, locks, auto-start client
    api/        HTTP handlers, SSE, auth
    cli/        cobra commands (API clients)
  examples/node-postgres/  examples/sqlite/
  docs/
```

## 18. Risks

- **Heavy `setup`:** `npm install` in every box is slow and uses disk. We accept this in v1. A later phase may add a shared-cache option.
- **Apps that ignore env ports** (hard-coded 3000): documented as a requirement. `init` warns when it finds hard-coded ports in dev scripts.
- **Large Postgres sources:** `pg_dump` for the template can be slow. It only runs on the first `new` and on explicit `db refresh`, never per box.
