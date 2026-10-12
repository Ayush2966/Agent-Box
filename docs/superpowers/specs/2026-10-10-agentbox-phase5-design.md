# agentbox — Phase 5 Design: Desktop app (Wails)

**Date:** 2026-10-10
**Status:** Approved by ruling (see §0)
**Builds on:** phases 1–4 (phase 1 committed; 2–4 built, uncommitted)

## 0. How this spec was approved

As before: the user asked for the roadmap to be finished autonomously; the executor cleared the
design gates and recorded every decision as a **Ruling**. This phase was preceded by a spike
(§1.1) whose findings fix several choices.

## 1. Problem

The dashboard lives in a browser tab and the TUI in a terminal. Some developers want the
dashboard as an app: a Dock icon, its own window, no tab to lose, no sign-in link to paste.

### 1.1 Spike findings (2026-10-10, on this macOS 15 / go1.24.2 machine)

- Wails v2.13–v2.16 require Go ≥ 1.25. **v2.12.0** declares `go 1.22` and keeps the module's
  directive at `1.24.2`; its deps add nothing above that.
- A minimal Wails app builds **without the `wails` CLI**: `go build -tags desktop,production`,
  with `CGO_LDFLAGS="-framework UniformTypeIdentifiers"` (v2.12 omits that framework in its cgo
  directives on recent SDKs). Output: an 11.6 MB arm64 binary. CGO and the Command Line Tools
  are required; the main `agentbox` binary stays CGO-free.
- The Wails asset server (`assetserver.Options{Assets, Handler}`) forwards every request that is
  not a static asset to a Go `http.Handler`; with `Assets: nil` *everything* goes to the Handler.
  Its macOS implementation delivers a response only when the handler returns, so **Server-Sent
  Events cannot stream through it** — live updates and logs need a different channel.

## 2. Goal

`Agentbox.app` opens the phase 2 dashboard in a native window, already signed in, with live
updates, and starts the daemon if needed. No second copy of the dashboard code; no changes to the
daemon's API.

**Success criteria**
- Double-clicking `Agentbox.app` (or running `agentbox-desktop`) shows the box list within two
  seconds, with no sign-in screen; a box started from the CLI appears `running` in the window
  without a reload; the Logs tab streams.
- `make desktop` produces `Agentbox.app` on macOS from a clean checkout plus Node and the
  Command Line Tools; `go test ./...` and `go build ./cmd/agentbox` are unaffected (no CGO).
- Box URL links open in the user's browser, not inside the app window.

## 3. Scope

**In:** `internal/desktop` (pure Go: bootstrap, authenticating proxy, event/log bridge),
`cmd/agentbox-desktop` (the Wails wiring, build-tagged), a Wails branch in the React app's
streaming helper and external-link handling, `make desktop` + `.app` packaging, a macOS CI job
that builds it, README. **Non-goals:** Windows/Linux builds (Wails supports them; untested
here, build tag keeps the door open); app signing/notarisation; auto-update; a tray icon;
Wails v3.

## 4. Rulings

- **R1 — Wails v2.12.0, pinned; no `wails` CLI.** The newest Wails needs Go 1.25, which this repo
  does not use; v2.12 builds with plain `go build`. The CLI only orchestrates frontend build +
  packaging, which the Makefile does. Cost if wrong: bump when the repo moves to Go 1.25.
- **R2 — one Go module, build tag `desktop` on the Wails-touching package.** A nested module would
  need `replace` directives and could not embed the dashboard; a build tag keeps `go build ./...`,
  `go vet ./...`, goreleaser (CGO-free) and the `go-without-ui` CI job untouched. Pure logic lives
  untagged in `internal/desktop` so it is tested by `go test ./...`. Cost if wrong: move files.
- **R3 — the app serves the dashboard by proxying the daemon, not by embedding a second copy.**
  `Assets: nil`; the Handler forwards `/` → `/ui/` and everything else to `http://<api_addr>`
  with `Authorization: Bearer <token>` injected (read from `<home>/token`) and the `Origin`
  header dropped. The React bundle keeps using relative `fetch('/v1/…')`, so it is unchanged, and
  the daemon's auth rules are the same ones phase 2 reviewed. Cost if wrong: embed `web/dist` in
  the app and point only `/v1` at the daemon.
- **R4 — streams go over Wails events, not SSE.** Because of §1.1: the Go side subscribes to
  `/v1/events` and emits `agentbox:event`; logs are started per box through a bound method and
  emitted as `agentbox:log:<id>`. The React `connectSSE` helper gains a Wails branch keyed on
  `window.runtime`/`window.go`, so components do not change. Cost if wrong: none for the web.
- **R5 — the desktop binary is also the CLI.** `main` runs `cli.Execute()` when any argument is
  present; otherwise it opens the window. `daemon.EnsureRunning` spawns `os.Executable() daemon run`,
  so the app can start the daemon with no separate `agentbox` binary installed. Cost if wrong:
  look up `agentbox` on PATH instead.
- **R6 — macOS is the only tested target; the `.app` is zipped by a `macos-latest` CI job as an
  artifact, not released by GoReleaser.** GoReleaser builds CGO-free from Linux; a native app
  needs the macOS toolchain. Cost if wrong: add a GoReleaser `builds` entry later.
- **R7 — smoke test by launching the built app for a few seconds.** The build proves linking;
  only a launch proves the webview, the proxy and the bridge. The executor launches the app
  with a timeout against a throwaway `AGENTBOX_HOME`, checks the daemon came up and the proxy
  answered `/v1/health` through the app's own HTTP port, then kills it. If macOS blocks the
  window (headless session), that is recorded. Cost if wrong: none.

## 5. Architecture

```
cmd/agentbox-desktop/main.go        //go:build desktop — wails.Run with the asset Handler, bindings, OnStartup
internal/desktop/bootstrap.go       Bootstrap(home) → (apiAddr, token, error): EnsureRunning, read api_addr + token (retry 3 s)
internal/desktop/proxy.go           NewProxy(apiAddr, token) http.Handler: "/"→"/ui/", inject Bearer, strip Origin, ReverseProxy to 127.0.0.1
internal/desktop/bridge.go          Bridge{API, Emit func(name string, data any)}: Run(ctx) pumps /v1/events → "agentbox:event";
                                    StartLogs(repo, name) (id, error) → "agentbox:log:<id>" lines; StopLogs(id)
web/src/api/sse.ts                  connectSSE: Wails branch (runtime.EventsOn / go.main.App.StartLogs)
web/src/ui/bits.tsx                 openExternal(url): BrowserOpenURL in Wails, window.open otherwise; Sign-out hidden in Wails
desktop/Info.plist.tmpl, desktop/mkapp.sh   .app bundle assembly (used by `make desktop`)
```

Startup: `main` → `Bootstrap(home)` (shows an error dialog and exits 1 if the daemon cannot start)
→ `wails.Run(App{Title: "agentbox", Width: 1200, Height: 800, MinWidth: 900, MinHeight: 600,
AssetServer: {Handler: NewProxy(...)}, Bind: [app], OnStartup: start the bridge})`. The window
loads `/` → proxied `/ui/` → the dashboard, whose first `GET /v1/health` carries the injected
Bearer → signed in.

## 6. Behaviour

- **Proxy:** `GET /` and `/index.html` → `/ui/`; every path forwarded verbatim otherwise; request
  headers forwarded minus `Origin` (and `Cookie`, which never exists here) plus `Authorization`;
  responses forwarded verbatim (the daemon's `no-store`/framing headers included); the proxy
  target is always `127.0.0.1:<port>` from `api_addr`.
- **Bridge:** on startup subscribe to events; on stream end, reconnect with the same 1s→16s
  backoff as the clients and emit `agentbox:conn` with `live|reconnecting`. `StartLogs` returns an
  id and streams `{line}` events; a second `StartLogs` for the same box returns a new id and the
  old stream keeps running until `StopLogs` (the frontend stops the old one).
- **Frontend (Wails branch):** `connectSSE({url, events, onEvent, onState, onReconnect})` maps
  `url === api.eventsURL` to `EventsOn("agentbox:event")` (+ `agentbox:conn` for `onState`), and a
  logs URL to `StartLogs(repo, name)` + `EventsOn("agentbox:log:<id>")`; the returned closer calls
  `EventsOff` and `StopLogs`. `openExternal` replaces `target="_blank"` anchors' default action.
- **Errors:** if `Bootstrap` fails (daemon did not start, token unreadable) the app shows a native
  message dialog with the error and the log path, then exits 1. Proxy failures (daemon died)
  render as the dashboard's `reconnecting` pill via the bridge; the next request errors toast.

## 7. Build and packaging

- `make desktop`: `cd web && npm ci && npm run build` (the daemon serves it, so the dashboard
  build must be in the `agentbox` binary the app spawns — i.e. the desktop binary itself, R5),
  then `CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production
  -ldflags "-s -w" -o build/agentbox-desktop ./cmd/agentbox-desktop`, then `desktop/mkapp.sh`
  assembles `build/Agentbox.app` (Info.plist with `CFBundleIdentifier dev.agentbox.desktop`,
  `CFBundleExecutable agentbox-desktop`, `NSHighResolutionCapable`, minimum macOS 12).
- CI job `desktop` on `macos-latest`: Node 22 + Go 1.24, `make desktop`, `go test ./internal/desktop/`,
  upload `build/Agentbox.app` as an artifact.
- `go.mod` gains `github.com/wailsapp/wails/v2 v2.12.0` and its deps; the go directive stays `1.24.2`
  (verified in the spike).

## 8. Testing

- `internal/desktop` (no CGO): `TestProxyInjectsTokenAndRewritesRoot` (httptest backend asserts
  `Authorization`, no `Origin`, `/`→`/ui/`, other paths verbatim, response headers pass through);
  `TestProxyRefusesToTargetNonLoopback`; `TestBootstrapReadsAddrAndToken` (fake home with the
  two files; retry until they appear); `TestBridgePumpsEventsAndLogs` (fake API + recording emitter:
  events emitted, reconnect emits `agentbox:conn`, StartLogs/StopLogs lifecycle, stale stream
  ignored after Stop).
- `web/`: Vitest `sse.test.ts` with a fake `window.runtime` + `window.go`: the Wails branch
  subscribes/unsubscribes, maps URLs, and the browser branch still uses `EventSource`.
- `cmd/agentbox-desktop`: compile check in the macOS CI job (`go vet -tags desktop ./cmd/agentbox-desktop`).
- Smoke (R7): launch, poll the daemon's `api_addr`, GET the app's `/v1/health` through the proxy
  (the proxy is a plain `http.Handler`, so the smoke test can also serve it on a local port with
  `httptest`), kill.

## 9. Risks

- Wails v2.12 on macOS 15 prints a deprecation warning (`setShowsBaselineSeparator`) — harmless.
- Unsigned app: Gatekeeper warns on first launch; documented (right-click → Open).
- The `desktop` build tag means a typo in the tagged package is only caught by the macOS CI job.
