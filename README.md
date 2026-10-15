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

    brew tap Ayush2966/tap
    brew install agentbox                  # the CLI
    brew install --cask agentbox-desktop   # the macOS app (optional)

or `go install github.com/Ayush2966/Agent-Box/cmd/agentbox@latest`.

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
    agentbox pr 123            # review a pull request: box pr-123 from its head, at http://pr-123.localhost:7777/

## Step by step

1. **Install** the CLI (above) and, on macOS, optionally the app: `brew install --cask Ayush2966/tap/agentbox-desktop`.
   The daemon starts itself on the first command; nothing to run by hand.

2. **Describe the repo once.** In the repo root, `agentbox init` drafts `agentbox.yaml`. It recognises Vite,
   Next.js, webpack, Create React App, Angular, Nuxt, NestJS, plain Node, Go (`cmd/<name>`), Rails and Django,
   and writes a `dev` command that already passes the box's port (`--port "$APP_PORT"`, `-p`, or `PORT` in the
   env) plus a reproducible `setup` (`npm ci` when there is a lockfile). Check three things before the first box:
   - `setup`: the command that makes a fresh checkout runnable. Use what works in *this* repo: `npm ci`,
     `npm ci --legacy-peer-deps` when `npm install` fails on peer ranges, `go mod download`, `pip install -e .`.
   - `dev`: the command that starts the dev server **on the port agentbox hands out** (next point).
   - `ports`: the env var names your app reads its ports from, e.g. `[APP_PORT]`.

3. **Make the app take its port from the env.** Every box gets different ports; a hard-coded one means the
   second box fails. Both `setup` and `dev` run through `sh -c` with the box's env, so the command line can
   pass it on without touching the code:
   - webpack: `dev: npm start -- --port "$APP_PORT"` (the flag overrides `devServer.port`)
   - Vite: `dev: npm run dev -- --port "$APP_PORT" --strictPort` (Vite on recent Node binds only `[::1]` for
     "localhost"; the proxy tries `127.0.0.1` and then `[::1]`, so no `--host` is needed)
   - Express/Node: `app.listen(process.env.APP_PORT || 3000)`
   - Go: `flag` or `os.Getenv("APP_PORT")`
   - Docker Compose: `"${REDIS_PORT}:6379"` in the compose file and `REDIS_PORT` under `ports:`

4. **Keep secrets and local overrides working.** The box's env file (`env.file`, default `.env`) is written
   from `env.template` plus agentbox's own vars (`APP_PORT`, `DATABASE_URL`, `AGENTBOX_NAME`, `AGENTBOX_ROOT`).
   If your app loads a git-ignored `.env.local`, point both at it so the tracked `.env` stays untouched in the box:

   ```yaml
   env: { template: .env.local, file: .env.local }
   ```

   Other untracked files a box needs (`.npmrc` with a token, certificates) go under `copy:`.

5. **Create a box and hand it to the agent.** `agentbox new feature-x` creates the worktree (branch
   `agentbox/feature-x`, cut from your current HEAD, so commit what the box must see), allocates ports, copies the
   database, runs `setup`, starts `dev`, and prints the directory. Start your agent there:
   `cd ~/.agentbox/boxes/<repo>/feature-x` (or wherever `boxes_dir` points). The app answers at
   `http://feature-x.localhost:7777/` in Chrome, Firefox and Edge.

6. **Watch, intervene, finish.** `agentbox ui` or `agentbox tui` show every box live: state, logs, diff,
   ahead/dirty counts. `agentbox exec feature-x -- npm test` runs in the box's env; `agentbox logs feature-x -f`
   tails setup and dev output. When the work is good, `agentbox merge feature-x` merges the branch into your main
   checkout's current branch (never force); `agentbox rm feature-x` stops everything, drops the database copy,
   frees the ports and deletes the worktree, asking first if uncommitted work would be lost.

If `agentbox new` fails in `setup`, the box is rolled back and the last lines of output are printed; fix the
`setup` command and run `new` again (`--keep-failed` keeps the half-built box for a look).

**Keeping the config honest.** `agentbox check` validates `agentbox.yaml` and the files it names and warns about
the classic mistakes (a dev command that never gets the port, a tracked env file). `agentbox check --setup --dev 60s`
goes further: a temporary worktree of HEAD gets free ports like a box, `setup` runs, `dev` starts and must answer
on its port, then everything is removed. It needs no daemon, so commit `agentbox.yaml` and add that line to CI.

## Configuration

`agentbox.yaml` at the repo root (read from your main checkout, so uncommitted edits apply):

```yaml
version: 1
setup: npm install            # runs once per new box
dev: npm run dev              # supervised; restarted with backoff if it crashes
ports: [APP_PORT, API_PORT]   # each gets a free port per box
proxy: APP_PORT               # which port http://<box>.localhost:7777 forwards to (default: the first)
db:
  driver: postgres            # postgres | mysql | mongo | sqlite
  url_env: DATABASE_URL
  source: postgres://localhost:5432/myapp
compose:                      # optional: a Docker Compose project per box
  file: compose.yaml          # default: compose.yaml / docker-compose.yml
  services: [redis]           # default: all services
env:
  template: .env.example      # copied and rewritten into the box's .env
  files: [config/local.env]   # extra env files layered in (later wins; set and ports win over them)
  set: { NODE_ENV: development }
copy: [.env.local]            # untracked files to copy from your main checkout
links:                        # optional: the same-named box in another repo (see below)
  API_URL: api-admin          #   http://<box>.<repo-id>.localhost:7777/  (for the browser)
  API_ADDR: api-admin:addr    #   127.0.0.1:<its port>                     (for server-to-server)
```

Your app must read its ports from these env vars. Boxes can't share a hard-coded port.

## Commands

| Command | |
|---|---|
| `init` | draft `agentbox.yaml` from the repo |
| `check [--setup] [--dev 60s]` | validate `agentbox.yaml`; with flags, rehearse setup and the dev server in a throwaway worktree (no daemon; for CI) |
| `new <name> [--from REF] [--no-dev]` | create a box |
| `pr <number> [--name N] [--no-dev]` | create a box from a pull request on origin (`pr-<number>`; GitHub, GitLab, Gitea). The request's code runs on your machine like any checkout; for an untrusted contributor use `--no-dev` and read the diff first |
| `ls [--all] [--json]` | list boxes |
| `logs <name> [-f]` | setup + dev-server output |
| `start` / `stop <name>` | control the dev server |
| `exec <name> -- <cmd>` | run a command in the box with its env |
| `open <name> [--terminal\|--files]` | open the box in your editor (or a terminal / the file manager) |
| `merge <name> [--squash]` | merge into the main checkout's current branch (never forces) |
| `rm <name> [--force] [--keep-branch]` | remove; asks before discarding work |
| `prune [--all] [--force]` | remove the stopped and failed boxes (this repo, or every repo) |
| `db refresh` | re-snapshot the source database |
| `doctor [--fix]` | find and safely repair drift |
| `ui [--no-open]` | open the web dashboard (signed in) |
| `tui [--all]` | terminal dashboard: live boxes, logs, diff, actions, shell into a box |

## Dashboard and box URLs

`agentbox ui` opens `http://127.0.0.1:7777/ui/`, signed in via a one-time link. Repos sit in the left
sidebar, grouped by their parent folder, with a dot for running or failed boxes; pick one to see its boxes
live (state, URL, git ahead/behind/dirty), copy a box's path for your agent, and use the same
create/start/stop/merge/remove actions as the CLI. **All boxes** at the top of the sidebar lists every box
across repos in one table, with a filter. **New box** (`n`) can tick other repos to create the same-named
box in each of them at once.

The Logs tab streams live and has a filter box and an "errors only" toggle.

`agentbox tui` is the terminal version: a live table of every box (`r` cycles repos, `/` filters),
`enter` for a box's overview, logs and diff, `s`/`x`/`m` to start-stop, remove and merge, `e` to open
a shell inside the box with its env, `E` to open it in your editor, `o` to open its URL, `d` for doctor,
`?` for every key.

Every box with ports is also served at **`http://<box>.localhost:7777/`** (or
`http://<box>.<repo-slug>.localhost:7777/` when two repos share a box name). The daemon forwards to the
port named by `proxy:` in `agentbox.yaml` (default: the first of `ports`), WebSockets included. A box that
is stopped or still being created gets an explanatory page instead of a connection error.

Chrome, Edge and Firefox resolve `*.localhost` on their own. Safari and `curl` on macOS use the system
resolver, which does not: use `curl -H 'Host: a.localhost' http://127.0.0.1:7777/`, or add
`127.0.0.1 a.localhost` to `/etc/hosts`. Your app should use relative URLs (or honour `X-Forwarded-Host`)
rather than redirecting to `http://localhost:3000`.

`editor:` in `~/.agentbox/config.yaml` names the editor `agentbox open` and the dashboard's **Open in editor**
use: a command on PATH (`code`, `cursor`, `zed`, …) or on macOS an app name (`"Visual Studio Code"`); without
it, the first of code, cursor, windsurf, zed, subl and idea found on PATH. `terminal:` likewise picks the
terminal app (macOS default: Terminal).

The daemon sends a desktop notification when a box's dev server keeps crashing, when a creation fails or
is rolled back, and when a box you created is ready (macOS via `osascript`, Linux via `notify-send`), so
an agent's box can be left alone. `notifications: false` in `~/.agentbox/config.yaml` turns them off.

`api_addr` in `~/.agentbox/config.yaml` must be a loopback address (`127.0.0.1:7777`, `localhost:7777`, `[::1]:7777`); the proxy and dashboard have no remote access, and a wildcard or LAN address is rejected at startup.

## One feature across several repos

A box is one repo's worktree. For a feature that spans repos, create a box with the same name in each repo:

    for r in api-admin api-unified web-panel; do agentbox --repo ~/code/$r new nvo-9743; done

Each repo needs its own `agentbox.yaml`. In the dashboard, **New box** has a checklist of the other repos:
tick them and the same box is created in each; **All boxes** then shows them side by side. `tui` cycles
repos with `r`. Finish the same way: `merge` and `rm` in each repo.

Wiring the boxes to each other is `links:` in `agentbox.yaml`. Each entry names an env var and another
repo (by folder name, or by id such as `api-admin-1a2b3c` when two clones share a name); the box gets
the address of the **same-named box** in that repo:

```yaml
# web-panel/agentbox.yaml
dev: npm start -- --port "$APP_PORT"
ports: [APP_PORT]
links:
  REACT_APP_API_URL: api-admin        # http://nvo-9743.api-admin-1a2b3c.localhost:7777/
  UNIFIED_ADDR: api-unified:addr      # 127.0.0.1:20007
  UNIFIED_PORT: api-unified:port      # 20007
```

- **`url`** (the default) is the proxy URL, for the browser: Chrome, Firefox and Edge resolve `*.localhost`, and
  each box gets its own origin, so sessions do not bleed between boxes. It is known before the other box
  exists, so the order of creation does not matter.
- **`addr` / `port`** are the other box's own port, for server-to-server calls (Go, Node and curl do not
  resolve `*.localhost` on macOS). They resolve when the other box exists: links are filled in when a box
  is created and refreshed every time its dev server starts, in the env file (managed keys only; your own
  lines stay) and in the process env.

The linked repo must be one agentbox knows (run any agentbox command in it once). Links show up in the
box's **Managed env** on the dashboard.

- **Shared backends.** If the frontends are what the agents change, leave the backends on a shared dev
  environment and create boxes only in the frontend repos.

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

MongoDB works the same way: `driver: mongo`, a `mongodb://` or `mongodb+srv://` source with the database
in the path, and `mongodump`/`mongorestore` on PATH. Each box gets `<db>_agentbox_<box>`, dumped from the
source and renamed on restore (`--nsFrom`/`--nsTo`); `db.url_env` receives a URL with the clone's name.
The `db.source` user needs to read the source database and create and drop the box databases. Works with a
local server or an Atlas cluster (its Mongo is reachable from a laptop).

## Desktop app

    brew tap Ayush2966/tap && brew install --cask agentbox-desktop

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
