#!/usr/bin/env bash
#
# `make restart` — build curator, put THAT binary on the port, and wait until it
# answers.
#
# Why it exists, with a receipt. After T57–T60 merged, the Library screen still
# showed 29 empty folders and unclickable cards, and the merged code was already
# correct: localhost:8090 was serving a binary built two days earlier. A
# long-running dev server does not pick up a merge, and the symptom is
# indistinguishable from a bug in the code you have just written — it was nearly
# debugged as one (docs/tasks/T62-make-restart.md).
#
# The wait on /healthz is the part that makes this worth having. A restart that
# returns before the process is serving is a restart you go and check by hand,
# which is the thing being replaced.
#
# This is a DEV-LOOP target. At the phase 10 cutover curator is a container on
# the Pi and `docker compose up -d` is the restart there; there is no Go
# toolchain in the deployment and nothing here would run. The Linux branch below
# is so that running from source on a Linux box works, and fails legibly when it
# cannot — not because this is how the Pi is meant to be driven.
#
# It does not build the UI. `make restart` depends on `ui` so the export happens
# before the binary that embeds it (D16); running this script on its own embeds
# whatever internal/web/dist already holds.

set -euo pipefail
cd "$(dirname "$0")/.."

say() { printf '  %s\n' "$*"; }
die() { printf 'make restart: %s\n' "$*" >&2; exit 1; }

# --- the environment the restarted process gets ------------------------------
#
# .env verbatim, because .env IS the configuration and a target that quietly
# drops half of it is a target that lies: the first time somebody used it to
# test a download it would not work, and the reason would not be on screen.
#
# The cost is real and worth naming. .env carries VPN_REQUIRED=true and
# VPN_CONFIG_FILE, cmd/curator/main.go:238 brings the tunnel up *before*
# ListenAndServe at :459, and a tunnel that was configured and did not come up
# is fatal on purpose (main.go:240). So a restart can take tens of seconds and
# can fail for reasons that have nothing to do with the code just built. That is
# what the log tail on failure is for.
#
# The opt-out is an argument rather than a second target:
#
#     make restart VPN_REQUIRED=false TORRENT_BACKEND=qbittorrent
#
# which works because GNU make exports command-line variables into a recipe —
# and only those; a variable set inside the makefile is not exported — and
# because everything already in the environment is put back after .env is
# sourced. Precedence, highest first: the argument, then .env, then the defaults
# below.
if [ -f .env ]; then
	# The names .env would set that the caller has already set, saved as
	# name=value lines. A value containing a newline would not survive this;
	# nothing in .env has one, and an environment that does is its own problem.
	preset=$(
		sed -nE 's/^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=.*/\1/p' .env |
			while IFS= read -r name; do
				if [ -n "${!name+set}" ]; then printf '%s=%s\n' "$name" "${!name}"; fi
			done
	)

	set -a
	# shellcheck disable=SC1091
	. ./.env
	set +a

	while IFS= read -r kv; do
		[ -n "$kv" ] || continue
		# shellcheck disable=SC2163  # a name=value word: this does export it
		export "$kv"
	done <<-EOF
		$preset
	EOF

	env_from=".env, with anything you passed on the command line winning over it"
else
	env_from="no .env here — the defaults only, which is the state CI runs in"
fi

PORT="${PORT:-8090}"
BIN="${BIN:-$HOME/curator-local/curator}"
LOG="${LOG:-$HOME/curator-local/curator-$PORT.log}"

# PORT is exported rather than merely read: the started process and the
# -healthcheck probe below both take it from the environment (config.go:515),
# and a probe that guessed while the server read PORT would call a working
# curator unhealthy.
export PORT

mkdir -p "$(dirname "$BIN")" "$(dirname "$LOG")"
# Absolute and symlink-resolved, because it is compared character for character
# against what the operating system reports for a running pid.
BIN="$(cd "$(dirname "$BIN")" && pwd -P)/$(basename "$BIN")"

# --- who is on the port, and what are they running ---------------------------
#
# There is no portable command. Measured 2026-08-15, on this Mac (macOS 26.6,
# arm64) and on the Pi (Debian 13 trixie, aarch64, iproute2 6.15.0):
#
#   lsof    /usr/sbin/lsof on the Mac,  NOT INSTALLED on the Pi
#   ss      /usr/bin/ss on the Pi,      NOT INSTALLED on the Mac
#   /proc   Linux only — it does not exist on the Mac
#   fuser   on both, and two different programs: macOS's rejects -n outright
#
# and the two disagree about "nothing is listening": `lsof -t` exits 1 with
# empty output, `ss` exits 0 with empty output. Both branches are therefore read
# as EMPTY OUTPUT and never as an exit status.
uname_s=$(uname -s)
case "$uname_s" in
Darwin | Linux) ;;
*) die "$uname_s is not a platform this knows how to find a listening process on (Darwin, Linux)" ;;
esac

# listeners prints the pids listening on $PORT, one per line, and nothing at all
# when the port is free.
listeners() {
	{
		case "$uname_s" in
		Darwin)
			lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null || true
			;;
		Linux)
			# IPv4 and IPv6 are separate sockets, come back as separate rows and
			# carry DIFFERENT pids, so this is a list and not a single value.
			ss -lptnH "sport = :$PORT" 2>/dev/null |
				sed -n 's/.*,pid=\([0-9]*\),.*/\1/p' || true
			;;
		esac
	} | sort -u
}

# occupied says whether anything at all holds the port, regardless of who owns
# it. Linux only, and the reason is measured: `ss -p` leaves the process column
# SILENTLY BLANK for a process you do not own — no error, no warning, exit 0 —
# so an empty pid list does not mean the port is free. `ss -lptn` as a normal
# user against the Pi's Radarr on 7878 prints the LISTEN row with an empty
# Process column; as root the same query names pid 38646, docker-proxy. Every
# Docker-published port on that machine looks like this. On the Mac lsof reports
# this session's own processes, which is the whole of a dev loop, and a bind
# that loses anyway surfaces as "address already in use" in the log tail.
occupied() {
	case "$uname_s" in
	Linux) ss -lntnH "sport = :$PORT" 2>/dev/null || true ;;
	*) printf '' ;;
	esac
}

# exe_of prints the absolute executable of a pid, and nothing when that cannot
# be established — which downstream is a refusal, never a pass.
exe_of() {
	case "$uname_s" in
	Darwin)
		# macOS `ps -o comm=` prints the FULL path and does not truncate,
		# verified against a 221-character path on this machine.
		ps -o comm= -p "$1" 2>/dev/null | sed 's/^[[:space:]]*//'
		;;
	Linux)
		# Linux's `ps -o comm=` is the kernel's 15-character TASK_COMM_LEN name
		# and is a BARE BASENAME — `nordfileshare`, never a path — so it cannot
		# be compared against $BIN at all. /proc/<pid>/exe is the only answer:
		# readable without root for a process you own, empty with exit 1 for one
		# you are not. Plain readlink rather than -f, because the " (deleted)"
		# suffix is the NORMAL case here and not an error — this rebuilds $BIN
		# before it stops the old process, so the running one's image on disk
		# has already been replaced by the time we look.
		readlink "/proc/$1/exe" 2>/dev/null | sed 's/ (deleted)$//'
		;;
	esac
}

# wait_for_port_free polls for up to $1 seconds.
wait_for_port_free() {
	local n=0
	while [ "$n" -lt "$1" ]; do
		if [ -z "$(listeners)" ]; then return 0; fi
		sleep 1
		n=$((n + 1))
	done
	[ -z "$(listeners)" ]
}

# --- build first, so a failed build leaves the running instance alone --------
say "config   $env_from"
say "building $BIN"
go build -o "$BIN" ./cmd/curator

# --- stop, and refuse to signal anything that is not ours --------------------
#
# Ports and processes are global to the machine and a worktree does not isolate
# them (CLAUDE.md, "What a worktree does not fix"). There is a second curator on
# 8099 that belongs to somebody else, and a target that killed whatever held a
# port would eventually kill it. Everything is verified before anything is
# signalled, so a mixed set refuses without having already stopped half of it.
stopped=""
pids=$(listeners)

if [ -z "$pids" ] && [ -n "$(occupied)" ]; then
	die "something is listening on $PORT that this user cannot see the pid of — \
it is another user's, most likely a container. Nothing was signalled. Re-run as \
root to name it, or pick another port with PORT=."
fi

if [ -n "$pids" ]; then
	while IFS= read -r pid; do
		[ -n "$pid" ] || continue
		exe=$(exe_of "$pid")
		if [ -z "$exe" ]; then
			die "port $PORT is held by pid $pid and its executable cannot be read, \
so it cannot be shown to be ours. Nothing was signalled."
		fi
		if [ "$exe" != "$BIN" ]; then
			die "port $PORT is held by pid $pid running
    $exe
  which is not
    $BIN
  Nothing was signalled. Use PORT= for a different port, or BIN= if that really is the one you meant."
		fi
	done <<-EOF
		$pids
	EOF

	while IFS= read -r pid; do
		[ -n "$pid" ] || continue
		say "stopping pid $pid"
		kill "$pid" 2>/dev/null || true
		stopped="$stopped $pid"
	done <<-EOF
		$pids
	EOF

	# curator's own graceful shutdown is bounded at 15s (cmd/curator/main.go:42)
	# because a scan holds the database, so the wait has to be comfortably past
	# that before it concludes anything is wrong.
	if ! wait_for_port_free 25; then
		say "pid$stopped outlived curator's own 15s shutdown bound — sending KILL"
		for pid in $stopped; do kill -9 "$pid" 2>/dev/null || true; done
		wait_for_port_free 5 ||
			die "port $PORT is still held after KILL. Look at pid$stopped by hand."
	fi
fi

# --- start, and do not return until it is actually serving -------------------
printf '\n=== make restart %s ===\n' "$(date '+%Y-%m-%d %H:%M:%S')" >>"$LOG"
nohup "$BIN" >>"$LOG" 2>&1 &
started=$!

# The probe is the binary's own -healthcheck (cmd/curator/main.go:56), which
# reads PORT from the environment exactly as the server did and is the same code
# path the image's HEALTHCHECK runs — rather than a curl this would have to
# assume is installed.
#
# The deadline is 60s because with a tunnel configured the listener does not
# open until the tunnel is up. Measured 2026-08-15 against the real NordLynx
# endpoint that is about 1.4s, so the headroom is for the times it is not.
n=0
until "$BIN" -healthcheck >/dev/null 2>&1; do
	if ! kill -0 "$started" 2>/dev/null; then
		printf '\n'
		tail -20 "$LOG" >&2
		die "pid $started exited during start-up. The last 20 log lines are above; the whole log is $LOG"
	fi
	if [ "$n" -ge 60 ]; then
		printf '\n'
		tail -20 "$LOG" >&2
		die "pid $started is running but /healthz did not answer within 60s. The last 20 log lines are above; the whole log is $LOG"
	fi
	sleep 1
	n=$((n + 1))
done

if [ -n "$stopped" ]; then say "stopped $stopped"; fi
say "started  pid $started — $BIN"
say "serving  http://localhost:$PORT — healthy after ${n}s"
say "log      $LOG"
