# curator
#
# `make check` is the one that matters: it is the per-commit gate CLAUDE.md
# requires, in one command instead of four typed from memory. Every commit in
# this repo has to build, vet, test and cross-compile ON ITS OWN, so that a
# bisect lands on one task rather than on a half-finished phase.

SHELL := /bin/bash
ARM   := GOOS=linux GOARCH=arm64

.DEFAULT_GOAL := help
.PHONY: help status check build ui go test race vet cross run ui-dev live live-tunnel live-rss clean

help: ## the targets, and what they are for
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk -F':.*?## ' '{printf "  \033[1m%-12s\033[0m %s\n", $$1, $$2}'

status: ## where the build is, derived from the repo
	@./scripts/status.sh

## ---------------------------------------------------------------------------

check: ui go vet race cross ## the per-commit gate: build, vet, test -race, cross-compile
	@echo "  ok — this commit stands on its own"

build: ui go ## the UI export, then the binary that embeds it

ui: ## the Next.js static export into internal/web/dist
	npm --prefix web run build

go: ## the Go binary (embeds whatever dist/ currently holds)
	go build ./...

vet: ## go vet
	go vet ./...

test: ## the fast test run
	go test ./...

race: ## the test run that counts — the poller and the engine are concurrent
	go test -race ./...

cross: ## the arm64 build, which is how this ships
	$(ARM) go build ./...

## ---------------------------------------------------------------------------

run: ## run it, reading .env if there is one
	go run ./cmd/curator

ui-dev: ## the UI alone against a running binary; output:'export' has no dev proxy
	NEXT_PUBLIC_API_BASE=http://localhost:8090 npm --prefix web run dev

## ---------------------------------------------------------------------------

live: live-tunnel ## the live checks that are quick enough to run often

live-tunnel: ## bring up the real tunnel and pull a few MB through it
	go test -run 'TestLiveTunnel' -v -timeout 5m ./internal/vpn
	go test -run 'TestLiveEngineOverTunnel' -v -timeout 10m ./internal/engine

live-rss: ## download 755 MB from a real swarm and report peak RSS and heap
	CURATOR_LIVE_TORRENT=1 go test -run TestLiveDownloadPeakRSS -v -timeout 20m ./internal/engine

clean: ## remove build output; leaves .env, the database and the library alone
	rm -rf web/out web/.next internal/web/dist/_next internal/web/dist/*.html
	go clean -cache -testcache
