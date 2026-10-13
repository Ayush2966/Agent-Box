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

    go install github.com/Ayush2966/Agent-Box/cmd/agentbox@latest
    # or: brew install Ayush2966/tap/agentbox

`go install` builds without the dashboard (everything else works; `agentbox ui` says so). Release
binaries include it. From a checkout: `make build` (needs Node 20+).

macOS and Linux. Needs git ≥ 2.38, plus pg_dump/pg_restore for Postgres, mysql/mysqldump for MySQL, and Docker with Compose v2.17+ (for `up --wait`) for compose.

## Quick start

    cd your-repo
    agentbox init          # writes a starter agentbox.yaml. Review it.
    agentbox new feature-x # prints the box dir: point your agent there
    agentbox ui            # web dashboard; the box also answers at http://feature-x.localhost:7777/
    agentbox tui           # the same dashboard in your terminal
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
  driver: postgres            # postgres | mysql | sqlite
  url_env: DATABASE_URL
  source: postgres://localhost:5432/myapp
compose:                      # optional: a Docker Compose project per box
  file: compose.yaml          # default: compose.yaml / docker-compose.yml
  services: [redis]           # default: all services
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
| `tui [--all]` | terminal dashboard: live boxes, logs, diff, actions, shell into a box |

## Dashboard and box URLs

`agentbox ui` opens `http://127.0.0.1:7777/ui/`, signed in via a one-time link, and shows every repo
and box live: state, logs, diff, doctor, and the same create/start/stop/merge/remove actions as the CLI.

`agentbox tui` is the terminal version: a live table of every box (`r` cycles repos, `/` filters),
`enter` for a box's overview, logs and diff, `s`/`x`/`m` to start-stop, remove and merge, `e` to open
a shell inside the box with its env, `o` to open its URL, `d` for doctor, `?` for every key.

Every box with ports is also served at **`http://<box>.localhost:7777/`** (or
`http://<box>.<repo-slug>.localhost:7777/` when two repos share a box name). The daemon forwards to the
port named by `proxy:` in `agentbox.yaml` (default: the first of `ports`), WebSockets included. A box that
is stopped or still being created gets an explanatory page instead of a connection error.

Chrome, Edge and Firefox resolve `*.localhost` on their own. Safari and `curl` on macOS use the system
resolver, which does not: use `curl -H 'Host: a.localhost' http://127.0.0.1:7777/`, or add
`127.0.0.1 a.localhost` to `/etc/hosts`. Your app should use relative URLs (or honour `X-Forwarded-Host`)
rather than redirecting to `http://localhost:3000`.

`api_addr` in `~/.agentbox/config.yaml` must be a loopback address (`127.0.0.1:7777`, `localhost:7777`, `[::1]:7777`); the proxy and dashboard have no remote access, and a wildcard or LAN address is rejected at startup.

## Where boxes live

Worktrees go under `~/.agentbox/boxes/<repo>-<hash>/<box>/`, never inside the repo. To keep them
somewhere visible, set `boxes_dir` in `~/.agentbox/config.yaml` (absolute, or `~/…`) and restart the
daemon (`agentbox daemon restart`):

```yaml
boxes_dir: ~/Desktop/agentbox-boxes
```

Only new boxes land there; existing ones stay where they were created (their paths are stored).
The daemon's own state, logs and token stay in `~/.agentbox`.

## Docker Compose services

If your dev environment is `docker compose up`, add `compose:` to `agentbox.yaml`. Every box gets its
own Compose project (`agentbox-<repo>-<box>`): own containers, own volumes, own host ports. Write host
ports as `"${REDIS_PORT}:6379"` in the compose file and list `REDIS_PORT` under `ports:` — agentbox
allocates a free port per box and the box's `.env` feeds it to Compose. The stack comes up with the
box (`up -d --wait`), stops and starts with it, and is removed with `rm` (`down -v`). `doctor` finds
projects without a box. A `container_name:` in the compose file prevents a second box (container names are host-unique), so
leave it out. See `examples/compose-redis`.

MySQL and MariaDB work like Postgres: `driver: mysql`, a `mysql://` source, and `mysql`/`mysqldump`
on PATH. Each box gets `<db>_agentbox_<box>`, a fresh dump of the source.
The `db.source` user needs `CREATE` and `DROP` globally plus read access to the source (`SELECT`,
`SHOW VIEW`, `TRIGGER`, `EVENT`; routines need `SHOW_ROUTINE` on MySQL 8.0.20 and later). When the server
has GTIDs on, the dump is taken with `--set-gtid-purged=OFF`, which needs MySQL's own `mysqldump` (MariaDB's
rejects the flag). See `examples/node-mysql`.

## Desktop app

    brew install --cask Ayush2966/tap/agentbox-desktop

or download `Agentbox-<version>-macos-universal.zip` (Apple silicon + Intel) from a release, unzip,
and drop `Agentbox.app` into Applications. It starts the daemon itself and opens signed in; box links
open in your browser. The app is a native window around the same dashboard, so there is no second UI
to maintain, and its binary is also the full `agentbox` CLI
(`Agentbox.app/Contents/MacOS/agentbox-desktop ls` works).

The app is signed ad hoc, not with a Developer ID, so macOS refuses the first launch of a downloaded
copy: allow it under System Settings → Privacy & Security → Open Anyway, or run
`xattr -dr com.apple.quarantine Agentbox.app`. The cask does that for you.
Launched from the Dock, the app widens `PATH` with your login shell's so the daemon finds `pg_dump`,
`mysqldump`, `docker` and `node`. If startup fails after the daemon was spawned, the daemon keeps
running; `agentbox daemon stop` stops it.

From a checkout, `make desktop` builds `build/Agentbox.app` for this Mac (needs Node 20+ and the Xcode
Command Line Tools; `DESKTOP_ARCHS="arm64 amd64"` for a universal build) and `make icon` re-renders the
icon from `desktop/icon/main.go`. With a Developer ID certificate,
`make desktop CODESIGN_IDENTITY="Developer ID Application: …"` signs with it (hardened runtime,
timestamp) as the starting point for notarization.

## How it works

A small daemon (started automatically) owns every box and serves a local API on a Unix socket and `127.0.0.1:7777` (token-protected).
Boxes live in `~/.agentbox/boxes/`, outside your repo, so your file watchers never see them. Postgres boxes are cloned with
`CREATE DATABASE … TEMPLATE` from a `pg_dump` snapshot, so cloning is instant and your main database can stay in use.

## Roadmap

All five phases are in: core + CLI, web dashboard + proxy, terminal UI, Compose + MySQL, desktop app.
