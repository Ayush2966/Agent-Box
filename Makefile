.PHONY: ui build test e2e

## ui: build the dashboard into internal/ui/dist (needs Node 20+)
ui:
	cd web && npm ci && npm run build

## build: build ./agentbox with the dashboard embedded
build: ui
	CGO_ENABLED=0 go build -ldflags "-s -w" -o agentbox ./cmd/agentbox

## test: vet + race tests (no Node needed)
test:
	go vet ./... && go test -race ./...

## e2e: real boxes on the example apps (needs node; build the UI first for the dashboard test)
e2e:
	go test -tags e2e ./e2e/...
