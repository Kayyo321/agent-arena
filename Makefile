GO ?= go
PREFIX ?= $(HOME)/.local
VERSION ?= 0.1.0-dev

.PHONY: build test check generate install dist
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

install: build
	mkdir -p "$(PREFIX)/bin"
	install -m 755 bin/arena "$(PREFIX)/bin/arena"

dist:
	$(GO) run ./cmd/release -version "$(VERSION)"
