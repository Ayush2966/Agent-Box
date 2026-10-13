.PHONY: ui build test e2e desktop icon

# Release tags are vX.Y.Z; the bundle wants the bare X.Y.Z, and an untagged
# build gets 0.0.0 (CFBundleVersion must be numeric). `agentbox version` keeps
# the full git description either way.
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null | sed 's/^v//')
ifeq ($(VERSION),)
VERSION := 0.0.0
endif
GIT_VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
# Architectures in the desktop binary; the release builds both and lipo joins them.
DESKTOP_ARCHS ?= $(shell uname -m | sed -e 's/x86_64/amd64/')
DESKTOP_LDFLAGS := -s -w -X github.com/Ayush2966/Agent-Box/internal/cli.Version=$(GIT_VERSION)

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

## desktop: build build/Agentbox.app (macOS only; needs Node 20+ and the Xcode Command Line Tools).
## DESKTOP_ARCHS="arm64 amd64" builds a universal binary (what the release ships).
desktop: ui
	mkdir -p build
	for a in $(DESKTOP_ARCHS); do \
	  CGO_ENABLED=1 GOARCH=$$a CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -ldflags "$(DESKTOP_LDFLAGS)" -o build/agentbox-desktop-$$a ./cmd/agentbox-desktop || exit 1; \
	done
	lipo -create -output build/agentbox-desktop $(foreach a,$(DESKTOP_ARCHS),build/agentbox-desktop-$(a))
	sh desktop/mkapp.sh build/agentbox-desktop build/Agentbox.app "$(VERSION)"

## icon: re-render desktop/agentbox.icns from desktop/icon/main.go (macOS only)
icon:
	go run desktop/icon/main.go -o desktop/icon/agentbox-1024.png
	sh desktop/mkicns.sh desktop/icon/agentbox-1024.png desktop/agentbox.icns
