# agentbox — Phase 2 Design: Web Dashboard + Reverse Proxy

**Date:** 2026-10-09
**Status:** Approved by ruling (see §0)
**Builds on:** `2026-09-25-agentbox-phase1-design.md` (phase 1 is built and committed on `main`)

## 0. How this spec was approved

The user asked for the roadmap to be finished autonomously ("continue and finish it yourself").
The normal brainstorming gates (design approval, spec review, plan review) were therefore
cleared by the executor. Every decision that would normally have been a question to the user is
recorded as a **Ruling** with its reasoning and its cost-if-wrong, so it can be reversed later.

## 1. Problem

Phase 1 gives each agent a box, but a developer running three agents has no single place to
*see* them: which is running, which crashed, what each one changed, what its logs say. Each box
also lives on an anonymous port (`20000`, `20001`, …) that nobody remembers and that changes
between boxes.

## 2. Goal

- `agentbox ui` opens a local web dashboard that shows every repo and box, streams their state,
  logs and diffs live, and can create, start, stop, merge and remove boxes.
- Every running box is reachable at a stable, memorable address: `http://<box>.localhost:7777/`.

**Success criteria**
- `agentbox new a && agentbox ui` shows box `a` running within a second of its state changing,
  with a clickable link to `http://a.localhost:7777/` that serves the box's dev server.
- Every CLI lifecycle operation can be done from the dashboard, with the same error messages
  (step, cause, hint) the CLI prints.
- The dashboard is embedded in the single `agentbox` binary; no separate process, no Node at
  runtime.
- A page on the open internet cannot drive the local API, even via DNS rebinding, and the API
  token is never placed in a URL.
- `go test ./...` stays green without Node installed. Node is needed only to *build* the UI.

## 3. Scope

**In phase 2:** reverse proxy on the existing TCP listener, browser session auth, the `ui`
package (embedded SPA + login), the React dashboard, `agentbox ui`, the `proxy:` config key,
CI/release changes to build the UI.

**Non-goals**
- HTTPS, remote access, multi-user. The listener stays on `127.0.0.1`.
- Editing `agentbox.yaml` from the dashboard.
- A terminal in the browser (`exec` stays CLI-only).
- Stable hostnames for anything but the box's dev server (one port per box is proxied).
- Making `*.localhost` resolve for tools that don't do it themselves (see §5.5).

## 4. Architecture

```
                    127.0.0.1:7777 (one TCP listener)
                             │
                   dispatch on Host header
                 ┌───────────┴───────────┐
       Host ends in ".localhost"      Host is 127.0.0.1 / localhost / ::1
                 │                               │              (anything else → 421)
          internal/proxy                   api mux (+ auth)
      <box>.localhost → box port          /v1/*   JSON API (Bearer OR session cookie)
      <box>.<repo>.localhost              /ui/*   embedded dashboard + login
                                          /       → 302 /ui/

     unix socket (CLI): api mux, no auth, same routes (incl. POST /v1/ui/login-codes)
```

New packages: `internal/proxy` (HTTP only), `internal/ui` (embedded SPA, login, cookie).
Touched: `config` (`proxy:` key), `box` (`ProxyTarget`), `api` (auth, `url` field, login codes,
health), `daemon` (dispatch, effective address file), `cli` (`ui` command, `new` prints URL),
`web/` (new, the React app), CI and GoReleaser.

**Ruling R1 — one listener, dispatched on Host, instead of a second port for the proxy.**
One number to remember (`7777`) and one firewall prompt on macOS. The API's Host allowlist and
the proxy's `.localhost` suffix are disjoint, so neither can be reached through the other.
Cost if wrong: a config knob `proxy_addr` can split them later without changing URLs' shape.

## 5. Reverse proxy

### 5.1 Hostnames

| Host | Routes to |
|---|---|
| `<name>.localhost` | the box called `<name>`, if exactly one repo has one |
| `<name>.<repo-slug>.localhost` | that repo's box; always unambiguous |

`repo-slug` is phase 1's `<basename>-<6 hex>` and is a valid hostname label. Matching is
case-insensitive on the host (browsers lower-case it). The port in the Host header is ignored.

### 5.2 Target port

`agentbox.yaml` gains an optional key:

```yaml
proxy: APP_PORT   # which of `ports` the proxy forwards to; default: the first entry of `ports`
```

Validation: `proxy` must name an entry of `ports`. The port number is the box's allocated value
for that name. The config is read at request time from the main checkout (like every other
config read), so adding `proxy:` later applies to existing boxes. If the config no longer parses
or the named port is missing from the box, the proxy falls back to the box's lowest allocated
port, which is the first one allocated.

### 5.3 Behaviour by box state

| Situation | Response |
|---|---|
| `running` | reverse-proxied to `127.0.0.1:<port>`; WebSocket upgrades pass through; `X-Forwarded-Host` and `X-Forwarded-Proto` set |
| `ready`, `stopped`, `failed` | `503` HTML page: box name, state, `last_error`, a link to the dashboard's box page, and the `agentbox start <name>` command |
| `creating` | `503` HTML page saying the box is still being created; `Refresh: 2` header |
| box has no `ports` | `404` HTML page explaining the box exposes no port |
| no such box | `404` HTML page listing the boxes that do exist, as links |
| `<name>` exists in several repos | `300` HTML page listing each `<name>.<slug>.localhost` link |
| dev server refuses the connection (crashed between restarts) | `502` HTML page with the box's last 20 log lines |

The HTML pages are plain templates in `internal/proxy`, no assets, and link to the dashboard
using the dashboard base URL the daemon passes in.

### 5.4 API exposure

`BoxView` gains `url` (`http://<name>.localhost:<port>/`, empty when the box has no ports).
`agentbox new` prints it as `url:`; `agentbox ls --json` carries it. The table output of `ls`
is unchanged (it is already wide).

### 5.5 Resolution of `*.localhost`

Chrome, Edge and Firefox resolve any `*.localhost` name to loopback themselves. Safari and
`curl` on macOS use the system resolver, which does not. The README documents the fallback:
`curl -H 'Host: a.localhost' http://127.0.0.1:7777/` or an `/etc/hosts` line. Nothing in
agentbox edits system files.

**Ruling R2 — the dashboard itself is served on `127.0.0.1`, not `agentbox.localhost`.**
It must open in every browser, including ones that cannot resolve `*.localhost`. Consequence:
the session cookie is host-only for `127.0.0.1` and browsers do not scope cookies by port, so
it is also sent to *every* server the user opens by that IP — a box's dev server
(`http://127.0.0.1:20000`), but equally any other local service (a notebook, Storybook, a
Docker-published port). Phase 1 already states agentbox is not a jail: anything running locally
runs as the same user and can read `~/.agentbox/token` anyway, so this is a tidiness concern,
not a boundary. Links from the dashboard always go through `<name>.localhost`, which never
receives the cookie, and `/ui/` pages refuse to be framed (`X-Frame-Options: DENY`,
`frame-ancestors 'none'`) so a same-site page cannot clickjack a signed-in dashboard.
`api_addr` is validated to be a loopback address, so the proxy can never be reached from the LAN.
Cost if wrong: add `agentbox.localhost` as a second dashboard origin later; nothing else moves.

## 6. Browser session auth

The API's TCP listener keeps `Authorization: Bearer <token>`. For the browser, which cannot
set headers on `EventSource`, a session cookie is added.

### 6.1 Login codes

- `POST /v1/ui/login-codes` → `{"code": "<64 hex>", "url": "http://127.0.0.1:7777/ui/login?code=…", "expires_at": …}`.
  Reachable on the unix socket (no auth; it is how `agentbox ui` works) and on TCP with Bearer
  auth. Codes are 32 random bytes, single use, expire after 2 minutes, kept in memory only.
- `GET /ui/login?code=<code>` redeems the code: sets the cookie and answers `303 /ui/`.
  An unknown, used or expired code answers `403` with a page saying to run `agentbox ui`.
- `POST /ui/login` with form field `token` sets the cookie when the token matches (for people
  who copy `~/.agentbox/token` by hand, e.g. over SSH port-forwarding). Wrong token → `403`.
- `POST /ui/logout` clears the cookie and answers `303 /ui/`.

**Ruling R3 — the API token is never put in a URL.** A URL ends up in shell history, browser
history and proxy logs; a one-shot code that dies in two minutes does not matter there.

### 6.2 The cookie

`agentbox_session=<token>; Path=/; HttpOnly; SameSite=Lax; Max-Age=31536000`. No `Secure`
(plain HTTP on loopback), no `Domain` (host-only). Its value is the API token itself. The
one-year `Max-Age` keeps the user signed in across browser restarts; the token is already on
the same user's disk in `~/.agentbox/token`, so the cookie jar adds no new exposure.

**Ruling R4 — cookie value = API token, no session store.** One secret, nothing new to persist
or expire, and a daemon restart does not log the user out. A separate session id would have the
same power as the token, so it would add state without adding a boundary.

**Ruling R5 — `SameSite=Lax`, not `Strict`.** `Strict` drops the cookie on the top-level
navigation from a `<name>.localhost` page (the proxy's "box is stopped" page links back to the
dashboard), which would show a logged-out dashboard. `Lax` sends it on top-level GETs only, and
every state-changing endpoint is `POST`/`DELETE`, so `Lax` gives the same CSRF protection here.

### 6.3 Request rules on the TCP listener (in this order)

1. **Host allowlist.** The Host's hostname must be `127.0.0.1`, `localhost`, `::1`/`[::1]`
   or end in `.localhost`. Anything else → `421 Misdirected Request`. This is what defeats DNS
   rebinding: a page at `evil.example` whose name now resolves to `127.0.0.1` still sends
   `Host: evil.example:7777`.
2. **Proxy split.** Hosts ending in `.localhost` go to the proxy and stop here. No API, no cookie.
3. **Origin allowlist** (unchanged from phase 1): a non-empty `Origin` must be one of
   `http://127.0.0.1:<port>`, `http://localhost:<port>`, `http://[::1]:<port>`. Otherwise `403`.
4. **Authentication:** `Authorization: Bearer <token>` **or** a valid session cookie. For cookie
   auth on a non-`GET`/`HEAD` request, the request must additionally carry an allowed `Origin`
   (browsers always send one on cross-origin and on same-origin `fetch` POSTs) and must not carry
   `Sec-Fetch-Site: cross-site`. Fails → `401` for a missing/invalid credential, `403` for a
   cookie request that fails the extra checks.
5. Paths under `/ui/` are public except as noted in §6.1 (the SPA shell must load before it can
   discover it is logged out; it then shows the sign-in screen).

## 7. The `ui` package (Go)

```
internal/ui/
  ui.go        Handler(Options) http.Handler; Options{Token, Codes *LoginCodes, Built fs.FS}
  codes.go     LoginCodes: Mint() (code string, expiresAt), Redeem(code) bool; mutex + TTL sweep
  session.go   SessionCookie = "agentbox_session"; ValidSession(r, token) bool; setCookie/clearCookie
  pages.go     the 403 "run agentbox ui" page and the 503 "dashboard not built" page (html/template)
  dist/        Vite output; `all:dist` is embedded; only `.gitkeep` is committed
```

- `GET /ui/` and any `/ui/<path>` that is not a file in `dist` serve `index.html` (SPA fallback)
  with `Cache-Control: no-cache`. Files under `/ui/assets/` are content-hashed by Vite and get
  `Cache-Control: public, max-age=31536000, immutable`.
- If `dist/index.html` is absent from the embedded FS, every `/ui/*` page answers `503` with a
  page explaining that this binary was built without the dashboard and how to get one
  (`make ui` with Node ≥ 20, or a release binary). `agentbox ui` detects this through
  `GET /v1/health` (`"ui": false`) and prints the same explanation instead of opening a browser.
- `Handler` takes the FS as an option so tests inject a fake build and never depend on Node.

**Ruling R6 — the built dashboard is not committed.** Committing a hashed bundle on every UI
change makes diffs unreadable. Consequence: `go install …@latest` yields a UI-less binary
(everything else works, and the binary says so). Release binaries and `make build` include it.
Cost if wrong: commit `dist/` later; the embed directive already covers it.

## 8. The dashboard (`web/`)

### 8.1 Stack

React 18, TypeScript, Vite 5, no component library, no state library. Vitest for pure logic.
Versions are pinned in `package.json`. `base: '/ui/'`; `build.outDir: '../internal/ui/dist'`,
`emptyOutDir: true`. Dev server proxies `/v1` and `/ui/login`, `/ui/logout` to
`http://127.0.0.1:7777` so the session cookie (host-only on `127.0.0.1`) works during
development.

**Ruling R7 — no React Query / Zustand / router libraries.** The whole state is "the list of
boxes, kept fresh by one SSE stream"; a reducer and `useSyncExternalStore` cover it in under a
hundred lines, and the embedded bundle stays small. Hash routing (`#/r/<slug>`,
`#/r/<slug>/b/<name>`) is twenty lines and survives a refresh. Phase 5 (Wails) reuses the same
app with a different API base, which none of these libraries would make easier.

### 8.2 Modules

```
web/src/
  api/client.ts     fetch wrapper: base URL, JSON, ErrorBody → ApiError{code, message, step, hint}; 401 → signed-out
  api/types.ts      BoxView, RepoView, Finding, Event (mirrors internal/api/types.go)
  api/sse.ts        EventSource helper with reconnect (1s→16s backoff) and a connection-state signal
  store/boxes.ts    reducer: load(list) | event(box.*) | remove ; selectors by repo; useBoxes()
  store/route.ts    hash router: parse/format, useRoute()
  ui/App.tsx        shell: TopBar, repo nav, main area, Toasts, SignIn gate
  ui/TopBar.tsx     daemon status pill (health + SSE state), repo <select>, New box, Doctor, DB refresh
  ui/BoxTable.tsx   rows: name (link to box page), state pill, URL link, ports, ahead/behind/dirty, actions
  ui/NewBoxDialog.tsx  name, from, no-dev; on submit POST then show box.step stream until ready/running/failed
  ui/BoxPage.tsx    header + tabs: Overview (info, env), Logs, Diff, Danger (merge, remove)
  ui/LogView.tsx    SSE lines, 5 000-line ring, auto-scroll that pauses when the user scrolls up, Clear
  ui/DiffView.tsx   `against` input (default: box base), fetch, +/- colouring, "no changes" state
  ui/DoctorPanel.tsx findings list with kind pills; Fix button (POST) re-renders with `fixed`
  ui/Toasts.tsx     errors as "step failed: message" + hint; auto-dismiss 8 s; success toasts 3 s
  ui/confirm.tsx    small confirm dialog used by remove/merge/db-refresh
  styles.css        custom properties; light/dark via prefers-color-scheme; monospace for names/ports/logs
```

### 8.3 Behaviour

- On load: `GET /v1/health` (401 → SignIn screen: "run `agentbox ui`", plus a token form that
  POSTs to `/ui/login`). Then `GET /v1/repos`, `GET /v1/boxes`, open `/v1/events`.
- Events: `box.created` and `box.state` → `GET` that box and upsert; `box.step` → progress
  text on the box row and in the New-box dialog; `box.removed` → delete. If the stream drops,
  the status pill turns amber, the client reconnects with backoff and reloads the list on
  reconnect (events may have been missed).
- Actions call the phase 1 endpoints exactly as the CLI does. `rm` asks for confirmation; on
  `409 unmerged_work` it shows the message and offers "Remove anyway" (`force=true`) and
  "Keep branch". `merge` asks for `into` (optional; left empty it means the API default, the main
  checkout's current branch) and `squash`; `412` shows the precondition text. `db refresh` offers `terminate`.
- Logs: `GET …/logs?follow=true&tail=500` via `EventSource`; closed when leaving the tab.
- Diff: `GET …/diff?against=`; a 1 MB cap client-side with a "diff truncated" notice.
- Keyboard: `Esc` closes dialogs; `n` opens New box when no input is focused.
- Accessibility: real buttons and labels, visible focus, state conveyed by text as well as colour.

### 8.4 Visual direction

Dense, calm, terminal-adjacent: a neutral surface, one accent for actions, state pills in
muted colours (running = green, stopped = grey, failed = red, creating = blue, removing = amber),
system UI font for prose and a monospace stack for identifiers. No gradients, no cards-in-cards.
Works down to 900 px wide (the table collapses secondary columns).

## 9. CLI

- `agentbox ui [--no-open]`: ensure the daemon; `POST /v1/ui/login-codes` over the
  socket; print `dashboard: <url>`; open it with `open` (macOS) / `xdg-open` (Linux) unless
  `--no-open`. If the daemon reports no TCP listener (health `api_addr` empty), exit 1 with the
  reason from the daemon log (port in use) and the `config.yaml` key to change.
  If health reports `"ui": false`, print the "built without the dashboard" explanation, exit 1.
- `agentbox new` prints `url: http://<name>.localhost:<port>/` after the ports line when the
  box has ports.
- Exit codes unchanged.

## 10. Daemon

- After binding TCP, the effective address is written to `<home>/api_addr` (0600) and reported
  by `GET /v1/health` as `api_addr`. Tests bind `127.0.0.1:0`, so this is how clients (and
  `agentbox ui`) learn the real port. `/v1/health` becomes
  `{"ok": true, "pid": N, "version": "…", "api_addr": "127.0.0.1:7777", "ui": true}`.
- The TCP handler is `dispatch(hosts, proxy, apiAndUI)` as in §6.3. The unix-socket handler is
  the API mux alone.
- `box.Manager.ProxyTarget(name, repoSlug string) (ProxyTarget, error)` resolves §5.1–5.3:
  `ProxyTarget{Repo, Name, State, Port int, LastError, DashboardPath, Tail func(n int) []string}`
  (`Tail` reads the box's log ring for the 502 page, only when needed); errors are
  `ErrNoSuchBox{Candidates []string}`, `ErrAmbiguous{Hosts []string}`, `ErrNoPorts`.
  The proxy package depends only on a `Lookup` function with that signature, not on `box`.

## 11. Config

`proxy: <PORT_NAME>` as in §5.2. The unknown-key suggestion list learns the new key.
`agentbox init` adds a commented `# proxy: APP_PORT` line when it detects more than one port.

## 12. API (v1 additions; nothing existing changes shape)

```
GET    /v1/health                         + pid, version, api_addr, ui
POST   /v1/ui/login-codes                 → 201 {code, url, expires_at}
GET    /ui/login?code=                    → 303 /ui/ + Set-Cookie   (403 page on failure)
POST   /ui/login  (form: token)           → 303 /ui/ + Set-Cookie   (403 on mismatch)
POST   /ui/logout                         → 303 /ui/ + cleared cookie
GET    /ui/, /ui/*                        → dashboard (SPA fallback), 503 page if not built
GET    /                                  → 302 /ui/
BoxView.url                               "http://<name>.localhost:<port>/" or ""
```

## 13. Security summary

- Loopback only. Host allowlist (421) defeats DNS rebinding for both the API and the dashboard.
- Token never in a URL (login codes). Cookie is HttpOnly, host-only, `Lax`.
- Cookie-authenticated writes require an allowed `Origin` and no `Sec-Fetch-Site: cross-site`.
- The proxy forwards to `127.0.0.1:<port>` only, never to a configurable host.
- The proxy's error pages escape every box name and log line (`html/template`).
- The proxy strips hop-by-hop headers (stdlib) and does not forward the session cookie, because
  `*.localhost` hosts never receive it in the first place.

## 14. Testing

**Go (no Node needed):**
- `config`: `proxy` accepted, rejected when not in `ports`, suggestion on typo.
- `ui`: SPA fallback and asset caching from an injected `fstest.MapFS`; 503 page when
  `index.html` is missing; mint → redeem sets the cookie and redirects; second redeem fails;
  expired code fails; token form; logout clears.
- `api`: Bearer still works; cookie works for GET and for POST with Origin; POST with cookie but
  no Origin → 403; `Sec-Fetch-Site: cross-site` → 403; `Host: evil.example` → 421 before auth;
  `/v1/health` fields; `/v1/ui/login-codes` 201 on the socket handler and 401 without Bearer on TCP.
- `proxy`: an `httptest` backend; `a.localhost` routes and sets `X-Forwarded-Host`; a WebSocket
  upgrade is relayed (raw HTTP/1.1 handshake against a hijacking backend); `503` page for a
  stopped box names the box; `404` lists candidates; `300` lists qualified hosts; `502` carries
  log lines; `AGENTBOX.LOCALHOST` matches case-insensitively.
- `box`: `ProxyTarget` picks `proxy:` key, falls back to the first port, falls back to the lowest
  port when the config is gone, disambiguates by slug, reports ambiguity.
- `daemon`: `api_addr` file written with the real port; dispatch sends `.localhost` hosts to the
  proxy and everything else to the API.
- `cli`: `ui --no-open` against an in-process daemon prints a login URL on the real port, and
  redeeming it (no-redirect client) answers 303 with the cookie; `new` prints `url:`.
- `e2e` (tag `e2e`, needs Node): create `a` on the sqlite example; `GET /` with
  `Host: a.localhost` returns the box's own `AGENTBOX_NAME`; stop → 503 page; login-code round
  trip → cookie → `GET /v1/boxes` 200 and `POST …/start` 204 with Origin.

**Web (`web/`):** `tsc --noEmit`; Vitest for the boxes reducer (events in any order converge),
the hash router (parse/format round trip) and `ApiError` parsing; `vite build` succeeds.

**CI:** ubuntu + macos: `actions/setup-node@v4` (Node 20), `npm ci && npm run check && npm run build`
in `web/`, then the Go steps as today (the embed then contains a real build). A second Go job
without Node proves the Go suite does not need it. **Release:** the same build step runs before
GoReleaser (`before.hooks`), so every binary ships the dashboard.

## 15. Risks

- **`*.localhost` in Safari/curl** (§5.5): documented, not solvable without touching the system.
- **Apps that redirect to an absolute `http://localhost:3000`** escape the proxy. The README
  tells users to make their app use relative URLs or the forwarded host.
- **Vite HMR** through the proxy: the HMR client connects to `ws://<box>.localhost:7777` and the
  upgrade is relayed; apps that pin `server.hmr.port` will need to use the forwarded port.
- **Port `7777` taken**: already handled in phase 1 (API disabled with a warning). `agentbox ui`
  now explains it instead of failing with a connection error.
- **Node at build time** is a new toolchain dependency for contributors; `go test ./...` and
  `go build` keep working without it, by design (§7).
