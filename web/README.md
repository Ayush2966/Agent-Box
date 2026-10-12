# agentbox dashboard

React + TypeScript + Vite. Built into the `agentbox` binary with `go:embed`.

    npm ci
    npm run check   # tsc + vitest
    npm run build   # writes ../internal/ui/dist (then `go build ./cmd/agentbox` embeds it)
    npm run dev     # http://127.0.0.1:5173/ui/ with /v1 proxied to a running daemon on :7777

Sign in during development: run `agentbox ui --no-open`, open the printed link once (it sets the
session cookie on 127.0.0.1, which the Vite dev server shares), then open the dev URL.
The dev proxy rewrites `Origin` to the daemon's own origin because the daemon only accepts writes from its own origin.
