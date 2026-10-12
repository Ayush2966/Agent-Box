.PHONY: ui build test e2e desktop

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

## desktop: build build/Agentbox.app (macOS only; needs Node 20+ and the Xcode Command Line Tools)
desktop: ui
	mkdir -p build
	CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -ldflags "-s -w -X github.com/Ayush2966/Agent-Box/internal/cli.Version=$$(git describe --tags --always 2>/dev/null || echo dev)" -o build/agentbox-desktop ./cmd/agentbox-desktop
	sh desktop/mkapp.sh build/agentbox-desktop build/Agentbox.app "$$(git describe --tags --always 2>/dev/null || echo dev)"
