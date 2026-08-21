# curator
#
# `make check` is the one that matters: it is the per-commit gate CLAUDE.md
# requires, in one command instead of four typed from memory. Every commit in
# this repo has to build, vet, test and cross-compile ON ITS OWN, so that a
# bisect lands on one task rather than on a half-finished phase.

SHELL := /bin/bash
ARM   := GOOS=linux GOARCH=arm64

.DEFAULT_GOAL := help
.PHONY: help status check build ui lists go test race vet cross run restart ui-dev live live-tunnel live-rss clean

help: ## the targets, and what they are for
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk -F':.*?## ' '{printf "  \033[1m%-12s\033[0m %s\n", $$1, $$2}'

status: ## where the build is, derived from the repo
	@./scripts/status.sh

## ---------------------------------------------------------------------------

check: ui lists go vet race cross ## the per-commit gate: build, vet, test -race, cross-compile
	@echo "  ok — this commit stands on its own"

build: ui go ## the UI export, then the binary that embeds it

ui: ## the Next.js static export into internal/web/dist
	npm --prefix web run build

# The release table's ordering rules are TypeScript, and until T100 the gate ran
# none. Both of them — the sections' order (D51) and the filter chips' — are one
# plausible tidy-up away from reversing D11, and the guard used to be a comment
# saying so. Needs no test framework and no dependency: node runs the shipped
# .ts modules directly, against captured answers in testdata/search/.
lists: ## the release-list rules (TypeScript), against captured answers
	node --experimental-strip-types web/scripts/check-lists.mjs

go: ## the Go binary (embeds whatever dist/ currently holds)
	go build ./...

vet: ## go vet
	go vet ./...

test: ## the fast test run
	go test ./...

# -count=1 is the gate, not a preference. Go's test cache is content-keyed and
# machine-global, and actions/setup-go restores it between runs, so without this
# `make check` reports another run's results and passes without executing a
# thing. Measured 2026-08-18: re-running a green `check` on the same commit
# returned `(cached)` for thirteen of twenty packages, internal/engine among
# them, in 1m58s against the 7m15s the run that actually tested took. A gate
# that can be green without running is not a gate. CLAUDE.md already warns about
# this cache for parallel agents; it is the same trap on a runner.
race: ## the test run that counts — the poller and the engine are concurrent
	go test -race -count=1 ./...

cross: ## the arm64 build, which is how this ships
	$(ARM) go build ./...

## ---------------------------------------------------------------------------

# `run` gets none of .env and `restart` gets all of it, which is the whole
# difference between them. Nothing in Go reads .env — it is shell-sourced — and
# a make recipe runs in a fresh non-interactive shell that never sourced it, so
# `run` comes up on the defaults: port 8090, ./curator.db, no TMDB key, no
# tunnel. `restart` sources it deliberately.
run: ## go run on the defaults — a recipe's shell has not sourced .env
	go run ./cmd/curator

# Not a prerequisite of `check` and never will be: the gate builds, vets, tests
# and cross-compiles, and a commit gate with a side effect on a live service is
# not a gate. `ui` first is D16 — the export has to exist before the binary that
# embeds it. PORT, BIN and LOG are overridable, as is anything in .env:
# `make restart PORT=8091 VPN_REQUIRED=false`.
restart: ui ## rebuild, put that binary on PORT (8090), wait until /healthz answers
	@./scripts/restart.sh

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
