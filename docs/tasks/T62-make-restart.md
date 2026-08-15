# T62 — `make restart` puts the merged code on the running instance

**Owns:** one target in `Makefile`, and the `run` target's doc comment, which is wrong
**Depends on:** nothing
**Status:** specified, not started — queued for the next phase

## Goal

One command rebuilds curator and restarts the instance on **8090**, so that what is merged is what
is running.

**This is a trap with a receipt.** After [T57–T60](T57-library-way-in.md) merged, the Library screen
still showed 29 empty folders and unclickable cards — and the code was already correct. `localhost:8090`
was serving a `go run` binary built **two days earlier**; a long-running dev server does not pick up
a merge. It was nearly debugged as a bug in the new code. The diagnosis is one command
(`ps -o lstart -p <pid>` against the merge time) and the fix is three (`npm --prefix web run build`,
`go build -o …`, kill and restart) — which is exactly the shape that belongs in the Makefile rather
than in somebody's memory, beside `check`.

## Do

1. **`make restart`** — the UI export, then the binary, then swap the running process:

   ```
   npm --prefix web run build        # D16: always before go build
   go build -o $(BIN) ./cmd/curator  # a real path, not a go-build cache path
   stop whatever is serving $(PORT)  # see the safety rule below
   start $(BIN) detached, logging to $(LOG)
   poll /healthz until it answers, and FAIL if it does not
   ```

   `PORT` defaults to 8090 and `BIN` to `~/curator-local/curator`, both overridable. **Waiting on
   `/healthz` is the part that makes it worth having**: a restart that returns before the process is
   serving is a restart you have to verify by hand, which is the thing being replaced.

2. **Refuse to kill a process that is not ours.** `lsof -nP -iTCP:$(PORT) -sTCP:LISTEN -t` gives the
   pid; compare its executable against `$(BIN)` before signalling it, and **stop with an error** if
   they differ. There is a second curator on **8099** that belongs to somebody else, and a target
   that killed whatever held a port would eventually kill it. Nothing listening is not an error —
   `make restart` on a cold machine should just start it.

3. **Fix `run`'s doc comment while you are here.** It says *"run it, reading .env if there is one"*
   and that is false twice over: nothing in Go reads `.env` (it is shell-sourced), and a `make`
   recipe runs in a fresh non-interactive shell that has not sourced it. Say what actually happens,
   or make it true.

## The design question, answer it before writing the recipe

**Which environment does the restarted process get?**

`.env` carries `TORRENT_BACKEND=embedded`, `VPN_REQUIRED=true` and `VPN_CONFIG_FILE`, so sourcing it
verbatim **brings up a WireGuard tunnel**. The 8090 instance running today does not have one: it was
started from a hand-picked subset and logs *"no VPN is configured and VPN_REQUIRED is true: downloads
are refused"*, which is fine for browsing and playing but not for downloading.

- **Source `.env` verbatim** — honest, because `.env` is the configuration, and it makes `make
  restart` produce the real thing. Costs a tunnel bring-up on every restart, which is slow and can
  fail for reasons that have nothing to do with the code you just built.
- **A documented subset, no tunnel** — fast, and matches what 8090 has been all along. But a target
  that silently drops half the config is a target that lies, and the first time somebody uses it to
  test a download it will not work and the reason will not be visible.

**Recommendation: source `.env` verbatim, and let a second target opt out** — `make restart
VPN_REQUIRED=false TORRENT_BACKEND=qbittorrent` for the fast browse-and-play loop. The default should
be the truth; the shortcut should be the thing you ask for.

## Do not

- **Put `restart` in `check`.** The gate builds, vets, tests and cross-compiles; it does not touch a
  running process. A commit gate with a side effect on a live service is not a gate.
- **Kill by name.** `pkill -f curator` matches the other instance, the test binaries, and this
  session's editor if it has the word in a path. Resolve the pid from the port and verify the exe.
- **Write a PID file.** The port plus the executable check answers the same question and survives a
  crash, a kill -9 and a reboot, none of which a stale PID file does.
- **Assume `lsof`.** It is on this Mac; the Pi is Debian and may want `ss -lptn` or `fuser`. Decide
  whether this target is dev-only (fine, say so in the help text) or has to work on the Pi at the
  phase 10 cutover.
- **Make it quiet.** Print the pid it stopped, the pid it started and the log path. The whole point
  is that "is the running thing the merged thing?" stops being a question.

## Verify

- `make restart` twice in a row: the second one stops the first one's process and comes back healthy
- `make restart` with nothing listening on the port: starts it, no error
- `make restart` with a **foreign** process on the port: **refuses**, names the pid and its
  executable, and kills nothing
- after `make restart`, `ps -o lstart` on the new pid is later than the last commit — which is the
  check that this whole task exists to make unnecessary
- `make check` still passes and is unchanged

---

## Reported by

Nethmin, 2026-08-15, after the T57–T60 merge: *"create a make command that restart the application.
my goal is to get the updated features running at 8090."*

**The immediate goal is already met** — 8090 was restarted by hand onto the merged binary and now
serves it from `~/curator-local/curator`, with a scan reporting `removed: 28` and the Library showing
one clickable card. This task is so that the next merge does not need a hand.
