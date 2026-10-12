# agentbox — Phase 4 Design: Docker Compose services and the MySQL driver

**Date:** 2026-10-10
**Status:** Approved by ruling (see §0)
**Builds on:** phases 1–3 (phase 1 committed; phases 2–3 built, uncommitted)

## 0. How this spec was approved

As for phases 2 and 3: the user asked for the roadmap to be finished autonomously; the design
gates were cleared by the executor and every decision is a **Ruling** with its reasoning and
cost-if-wrong.

## 1. Problem

Two kinds of projects cannot use agentbox yet:
- Projects whose dev environment is `docker compose up` (a database, a cache, a queue). Three
  boxes would all try to bind the same host ports and share the same containers.
- Projects on MySQL/MariaDB. Phase 1 ships only Postgres and SQLite drivers.

## 2. Goal

- `compose:` in `agentbox.yaml` gives every box its **own Compose project** (own containers,
  own volumes, own host ports), brought up with the box and torn down with it.
- `db.driver: mysql` clones the source database per box exactly like the Postgres driver does.

**Success criteria**
- On the new `examples/compose-redis` app, `agentbox new a && agentbox new b` yields two Redis
  containers in two Compose projects on two host ports, each box's app talking to its own; `rm`
  removes containers and volumes; `doctor` finds and (with `--fix`) removes an orphaned project.
- On the new `examples/node-mysql` app, three boxes get `myapp_agentbox_{a,b,c}` databases with
  the source's rows; `rm` drops them; the Postgres test suite's expectations hold for MySQL.
- Nothing in the Go unit suite needs Docker or a MySQL server; both are exercised by gated
  integration tests (`AGENTBOX_TEST_DOCKER=1`, `AGENTBOX_TEST_MYSQL_URL`) and by CI.

## 3. Scope

**In:** `compose:` config, `internal/compose` (driver over the `docker compose` CLI), the
`compose` lifecycle step in `box.Manager` (new/start/stop/rm/doctor), `internal/db/mysql.go`,
`mysql://` detection in `init`, two examples, CI jobs, README.
**Non-goals:** Podman/nerdctl (anything that answers to `docker compose` works, nothing else is
tested); Kubernetes; managing Compose for the *main* checkout; MariaDB-specific features
(MariaDB is used through the MySQL protocol and tools); Windows.

## 4. Rulings

- **R1 — Compose is a lifecycle step, not a `db` driver.** A Compose stack is "processes that
  belong to the box" (like the dev server), not "a copy of data" (like a DB clone). It gets its
  own config key and its own step; `db:` stays independent, so a project can run Redis in
  Compose and clone a host Postgres. Cost if wrong: none; the two never touch.
- **R2 — Per-box host ports come from `ports:`, through the box env file.** Compose
  interpolates `${VAR}` from the project's `.env`; the box's env file *is* that `.env` (or is
  passed with `--env-file` when `env.file` is custom). A compose file that writes
  `ports: ["${DB_PORT}:5432"]` and lists `DB_PORT` under `ports:` gets a free host port per box
  with no new mechanism. Hard-coded host ports collide, exactly as for the dev server, and
  `agentbox init` warns when it sees one. Cost if wrong: a later phase could rewrite ports.
- **R3 — The driver shells out to `docker compose`; no Docker SDK dependency.** The CLI is what
  users already have and debug with; the SDK would add megabytes and a moving API. Every
  invocation is logged to the box log. Cost if wrong: swap the executor behind the same interface.
- **R4 — Project name `agentbox-<repo-slug>-<box>`, run from the box directory.** Deterministic,
  so `doctor` can list orphans with `docker compose ls` and nothing new is stored in state.
  Running in the box dir makes relative volumes (`./data`) land inside the box. Cost if wrong:
  derive differently in one function.
- **R5 — `stop` stops the stack too; `start` brings it back; `rm` runs `down -v`.** Spec §7 of
  phase 1 says start/stop "control only the dev server"; the stack is in the same category
  (processes), so it follows the dev server. Volumes survive `stop` and die with `rm`, like the
  DB clone. Cost if wrong: a `compose.keep_running: true` knob.
- **R6 — MySQL clones with `mysqldump | mysql`, no template database.** Postgres has a template
  only because `CREATE DATABASE … TEMPLATE` is instant; MySQL has no such thing, and a dump is
  needed per clone anyway, so a template would only add a second copy to keep fresh.
  `db refresh` is therefore a no-op for MySQL (as for SQLite). Cost if wrong: add a template
  later behind the same `Driver` interface.
- **R7 — Credentials never go on a MySQL command line.** `mysql`/`mysqldump` get a temporary
  `--defaults-extra-file` (0600, deleted after) carrying host/port/user/password; the Go side
  uses `go-sql-driver/mysql v1.9.3` (pinned; `go` directive stays 1.24.2). Cost if wrong: none.
- **R8 — Testing without Docker or MySQL on the dev machine.** The compose driver takes an
  executor (`func(ctx, args, dir, env, out) error` plus a JSON lister); unit tests use a fake
  that records argv and scripts outputs. The MySQL driver's pure logic (DSN conversion, names,
  argument building) is unit-tested; its live paths are gated on `AGENTBOX_TEST_MYSQL_URL`
  (CI runs them against a `mysql:8` service). Docker-backed tests are gated on
  `AGENTBOX_TEST_DOCKER=1` (CI's ubuntu runner). Locally the executor may start Docker Desktop
  to run them once (`open -a Docker`), and quits it afterwards; that is a reversible, logged
  action. Cost if wrong: none for users.

## 5. Configuration

```yaml
compose:
  file: docker-compose.yml     # optional; default: compose.yaml, compose.yml, docker-compose.yml, docker-compose.yaml (first that exists)
  services: [db, redis]        # optional; default: every service in the file
  wait: 60s                    # optional; `up --wait` timeout; default 60s
db:
  driver: mysql                # new value next to postgres | sqlite
  url_env: DATABASE_URL
  source: mysql://app:secret@127.0.0.1:3306/myapp
```

Validation: `compose.file` must be a local path inside the repo; `services` entries match
`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`; `wait` is a Go duration ≥ 1s. `db.source` for `mysql` must be a
`mysql://` URL with a database name; its host must not be empty.

## 6. Compose lifecycle

Step order in `new` becomes: worktree → ports → db → env → **compose** → setup → dev. The
compose step runs after env (it needs the env file) and before setup (setup may migrate the
stack's database). Each step registers its undo; compose's undo is `down -v --remove-orphans`.

| operation | command (always `docker compose -p <project> -f <box>/<file> [--env-file <box>/<env.file>]`, cwd = box dir, output → box log) |
|---|---|
| new / start | `up -d --wait --wait-timeout <s> [services]` (`--wait` fails if a healthcheck fails) |
| stop | `stop` |
| rm, undo, doctor --fix | `down -v --remove-orphans` |
| doctor | `docker compose ls --all --format json` → projects named `agentbox-<slug>-*` with no box |

`start` on a box with `compose:` brings the stack up before the dev server; if the stack fails
to come up, the dev server is not started and the error names the step `compose`.
`stop` stops the dev server first, then the stack. `Recover` (daemon restart) does nothing to
stacks: containers keep running under Docker, which is right — the box was `running`.

The `docker` binary is looked up on PATH at use; a missing binary is an `errs.Precondition`
with the hint "install Docker Desktop or the docker CLI with the compose plugin". `doctor`
reports a missing `docker` only when some repo configures `compose:`.

Box env gains `COMPOSE_PROJECT_NAME=<project>` (so `docker compose` run by hand inside the box
dir hits the box's stack) — appended after `AGENTBOX_ROOT`.

## 7. MySQL driver

- `Check`: `mysql` and `mysqldump` on PATH; a connection to the source. (The first draft also
  parsed `SHOW GRANTS` for a CREATE privilege; dropped — grants are hard to parse reliably and
  `Clone`'s `CREATE DATABASE` reports the real error with a hint.)
- `Prepare`: `Check`. `Refresh`: no-op.
- `Clone`: `CREATE DATABASE <src>_agentbox_<box>` (`DROP` first if it exists — a leftover from
  a crash), then `mysqldump --defaults-extra-file=… --single-transaction --routines --triggers
  --events --no-tablespaces <src> | mysql --defaults-extra-file=… <clone>`. Name length ≤ 64.
  The box URL is the source URL with the database path replaced.
- `Drop`: `DROP DATABASE IF EXISTS <name>`, refusing names outside the `<src>_agentbox_` prefix
  (same guard as Postgres).
- `List`: `SHOW DATABASES`, filtered in Go by the `<src>_agentbox_` prefix (a `LIKE ?`
  placeholder cannot be server-prepared there).
- Clone names map the box name's `-` to `_` (as Postgres does); identifiers are quoted with
  backticks and validated against `^[A-Za-z0-9_]+$` first.
- The temporary option file double-quotes every value (`\` and `"` escaped, control characters
  refused) and sets `protocol=tcp`, so `localhost` never falls back to the Unix socket.
- Errors: a connection error says "connect to mysql://user@host:port/db: …"; a dump/restore
  failure names the tool that failed and keeps the other's stderr as context (the Postgres
  `dumpRestoreError` is reused).

## 8. `init`

- `mysql://` / `mariadb://` in `.env.example` → `db.driver: mysql`.
- A compose file at the repo root → `compose:` block with `file:`; a warning if the file has a
  literal host port (`"3306:3306"`) telling the user to use `${VAR}` and list it under `ports:`.

## 9. Examples

- `examples/compose-redis`: `compose.yaml` with a `redis` service on `${REDIS_PORT}:6379`, a
  Node server that increments a counter in Redis and reports `{box, hits}`; `agentbox.yaml`
  with `ports: [APP_PORT, REDIS_PORT]`, `compose: {services: [redis]}`.
- `examples/node-mysql`: the Postgres example's twin using `mysql2`.

## 10. UI surfaces

No dashboard/TUI change is required. The box overview already shows every managed env var,
which now includes `COMPOSE_PROJECT_NAME`. (A later polish could list containers.)

## 11. Testing

- **config:** `compose` keys parse, defaults resolve, bad service names / `wait` / escapes rejected;
  `db.driver: mysql` accepted with a valid URL, rejected without a database or with another scheme.
- **compose (fake executor):** argv for up/stop/down/ls exactly as §6; `--env-file` only when
  `env.file != ".env"`; project naming; default file resolution order; `ls` JSON parsing and
  orphan detection; a failing `up` surfaces the executor's output.
- **box:** the compose step runs after env and before setup; its undo runs `down -v` on a later
  failure (`TestNewRollsBackEachStep` gains `StepCompose`); `stop` runs `stop`, `start` runs
  `up`; `rm` runs `down -v`; `doctor` lists an orphan and `--fix` downs it — all with the fake
  executor injected through `Deps`.
- **db/mysql (pure):** DSN conversion (`mysql://u:p@h:3306/db?x=1` ↔ Go DSN), name/length
  validation, defaults-file contents and mode, `List` pattern escaping.
- **db/mysql (live, gated):** clone has the source's rows, source stays untouched, drop, list,
  refusal to drop a foreign database, crash leftover is replaced.
- **e2e (gated):** `TestThreeBoxesMySQL` (mirrors Postgres), `TestComposeRedis` (two boxes, two
  projects, two ports, `rm` leaves no containers: `docker compose ls` shows none).
- **CI:** a `mysql` job (service `mysql:8`, `mysql-client` apt package, `AGENTBOX_TEST_MYSQL_URL`)
  and a `docker` job (ubuntu has Docker; `AGENTBOX_TEST_DOCKER=1`).

## 12. Risks

- Compose files that hard-code host ports (R2) — documented and warned about by `init`.
- `docker compose ls` output format drift — parsed from `--format json`, tested against a
  recorded sample; a parse failure degrades `doctor` to "could not list compose projects".
- Slow `up --wait` on first pull — the default 60 s timeout is configurable.
- `mysqldump` needing `PROCESS` privilege for `--no-tablespaces`-less dumps on MySQL 8 — hence
  `--no-tablespaces` by default.
