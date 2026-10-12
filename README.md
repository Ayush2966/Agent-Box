# agentbox

**Isolated dev environments for parallel AI coding agents.**

Running three agents on one repo? They fight over files, ports and your database. `agentbox new a` gives each agent its own
git worktree, its own ports, its own database copy, its own `.env`, and its own running dev server, in seconds.

```
$ agentbox new a && agentbox new b && agentbox new c
$ agentbox ls
NAME  STATE    PORTS           DB                AHEAD  DIRTY
a     running  APP_PORT=20000  myapp_agentbox_a  0      0
b     running  APP_PORT=20001  myapp_agentbox_b  2      1
c     running  APP_PORT=20002  myapp_agentbox_c  0      3
```

## Install

    go install github.com/agentbox-dev/agentbox/cmd/agentbox@latest
    # or: brew install agentbox-dev/tap/agentbox

`go install` builds without the dashboard (everything else works; `agentbox ui` says so). Release
binaries include it. From a checkout: `make build` (needs Node 20+).

macOS and Linux. Needs `git` ≥ 2.38, plus `pg_dump`/`pg_restore` if you use Postgres.

## Quick start

    cd your-repo
    agentbox init          # writes a starter agentbox.yaml. Review it.
    agentbox new feature-x # prints the box dir: point your agent there
    agentbox ui            # web dashboard; the box also answers at http://feature-x.localhost:7777/
    agentbox logs feature-x -f
    agentbox merge feature-x
    agentbox rm feature-x

## Configuration

`agentbox.yaml` at the repo root (read from your main checkout, so uncommitted edits apply):

```yaml
version: 1
setup: npm install            # runs once per new box
dev: npm run dev              # supervised; restarted with backoff if it crashes
ports: [APP_PORT, API_PORT]   # each gets a free port per box
proxy: APP_PORT               # which port http://<box>.localhost:7777 forwards to (default: the first)
db:
  driver: postgres            # postgres | sqlite
  url_env: DATABASE_URL
  source: postgres://localhost:5432/myapp
env:
  template: .env.example      # copied and rewritten into the box's .env
  set: { NODE_ENV: development }
copy: [.env.local]            # untracked files to copy from your main checkout
```

Your app must read its ports from these env vars. Boxes can't share a hard-coded port.

## Commands

| Command | |
|---|---|
| `init` | draft `agentbox.yaml` from the repo |
| `new <name> [--from REF] [--no-dev]` | create a box |
| `ls [--all] [--json]` | list boxes |
| `logs <name> [-f]` | setup + dev-server output |
| `start` / `stop <name>` | control the dev server |
| `exec <name> -- <cmd>` | run a command in the box with its env |
| `merge <name> [--squash]` | merge into the main checkout's current branch (never forces) |
| `rm <name> [--force] [--keep-branch]` | remove; asks before discarding work |
| `db refresh` | re-snapshot the source database |
| `doctor [--fix]` | find and safely repair drift |
| `ui [--no-open]` | open the web dashboard (signed in) |

## Dashboard and box URLs

`agentbox ui` opens `http://127.0.0.1:7777/ui/`, signed in via a one-time link, and shows every repo
and box live: state, logs, diff, doctor, and the same create/start/stop/merge/remove actions as the CLI.

Every box with ports is also served at **`http://<box>.localhost:7777/`** (or
`http://<box>.<repo-slug>.localhost:7777/` when two repos share a box name). The daemon forwards to the
port named by `proxy:` in `agentbox.yaml` (default: the first of `ports`), WebSockets included. A box that
is stopped or still being created gets an explanatory page instead of a connection error.

Chrome, Edge and Firefox resolve `*.localhost` on their own. Safari and `curl` on macOS use the system
resolver, which does not: use `curl -H 'Host: a.localhost' http://127.0.0.1:7777/`, or add
`127.0.0.1 a.localhost` to `/etc/hosts`. Your app should use relative URLs (or honour `X-Forwarded-Host`)
rather than redirecting to `http://localhost:3000`.

`api_addr` in `~/.agentbox/config.yaml` must be a loopback address (`127.0.0.1:7777`, `localhost:7777`, `[::1]:7777`); the proxy and dashboard have no remote access, and a wildcard or LAN address is rejected at startup.

## How it works

A small daemon (started automatically) owns every box and serves a local API on a Unix socket and `127.0.0.1:7777` (token-protected).
Boxes live in `~/.agentbox/boxes/`, outside your repo, so your file watchers never see them. Postgres boxes are cloned with
`CREATE DATABASE … TEMPLATE` from a `pg_dump` snapshot, so cloning is instant and your main database can stay in use.

## Roadmap

Terminal UI → Docker Compose & MySQL drivers → desktop app.
