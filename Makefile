GO ?= go
PREFIX ?= $(HOME)/.local
VERSION ?= 0.1.0-dev

.PHONY: build test check bench generate install uninstall dist
build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags "-s -w -X github.com/Kayyo321/agent-arena/internal/arena.Version=$(VERSION)" -o bin/arena ./cmd/arena

generate:
	$(GO) generate ./internal/contract

test:
	$(GO) test ./...

check:
	$(GO) vet ./...
	$(GO) test -race ./...

# Deterministic conflict and rollback benchmark; see docs/BENCHMARKS.md.
bench:
	$(GO) test ./internal/arena -run '^TestBench' -count=1 -v

install: build
	ARENA_PREFIX="$(PREFIX)" sh scripts/install.sh --from bin/arena

uninstall:
	"$(PREFIX)/bin/arena" uninstall --yes

dist:
	$(GO) run ./cmd/release -version "$(VERSION)"
