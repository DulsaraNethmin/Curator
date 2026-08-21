#!/usr/bin/env bash
#
# `make deploy-pi` — put the version compose.pi.yaml pins onto the Pi, prove it
# came up, and put the old one back if it did not.
#
# Why it exists. Every deploy before this one was typed by hand: back up two
# files, scp them, pull, `up -d`, then curl /healthz and hope. That is six
# commands with one irreversible step in the middle, and the parts that get
# skipped when it is typed are exactly the parts that matter — checking the
# arm64 half of the manifest actually exists, and keeping a backup you can get
# back to. 0.5.0's deploy was done by hand and this is that sequence, with the
# checks that were done from memory made mandatory.
#
# WHAT IT DELIBERATELY DOES NOT DO. It does not bump the pin and it does not
# touch git. compose.pi.yaml is the input, not the output: the pin moves on a
# `deploy-N-to-the-pi` branch that a human reviews and merges, exactly as
# release-N moves the constant. A script that edited the pin would make the repo
# a record of what a script did rather than a decision somebody made.
#
# It also never touches jellyfin, portainer, watchtower or homepage. Those are
# `/opt/docker`'s compose project; this only ever names `/opt/curator`'s two
# files, and compose.yaml sets `name: curator`, so the blast radius is curator
# and minter (docs/decisions.md D43).
#
# GitHub Actions cannot do this. The Pi is 192.168.1.26 behind NAT with an
# outbound-only NordVPN tunnel — `nordlynx`, not a mesh — so a hosted runner has
# no route to it. The alternative was a self-hosted runner on the Pi, refused
# because this repository is PUBLIC and a fork's pull request can run code on a
# self-hosted runner: that box holds the media and the NordVPN credentials.
#
#   make deploy-pi                      # deploy what compose.pi.yaml pins
#   make deploy-pi PI=pi PORT=8090      # a different host or port
#   make deploy-pi DRY_RUN=1            # run every check, change nothing
#   make deploy-pi SKIP_RELEASE_CHECK=1 # no gh, or deploying an unreleased tag

set -euo pipefail
cd "$(dirname "$0")/.."

say()  { printf '  %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*" >&2; }
die()  { printf 'make deploy-pi: %s\n' "$*" >&2; exit 1; }

PI="${PI:-pi}"
PORT="${PORT:-8090}"
REMOTE="${REMOTE:-/opt/curator}"
PROFILES="${PROFILES:---profile 1337x}"
SERVICE="${SERVICE:-curator}"
CONTAINER="${CONTAINER:-curator-curator-1}"
# How long the new container gets to answer /healthz with the new version before
# this rolls back. The Pi's own measurement is 1 s to answering and 6 s to
# `healthy`; 90 s is a runaway backstop, not a target.
TIMEOUT="${TIMEOUT:-90}"

compose="docker compose -f compose.yaml -f compose.pi.yaml $PROFILES"

# --- what is being deployed --------------------------------------------------
#
# Read from compose.pi.yaml rather than from cmd/curator/version.go, because the
# pin is what the Pi will actually run. The two normally agree and it is worth
# saying when they do not: a release that was cut but whose pin never moved is
# the most likely reason a deploy appears to do nothing.
version="$(sed -nE 's|^[[:space:]]*image:[[:space:]]*ghcr\.io/dulsaranethmin/curator:(.+)$|\1|p' compose.pi.yaml | head -1)"
[ -n "$version" ] || die "no curator image pin found in compose.pi.yaml"

declared="$(sed -n 's/^const Version = "\(.*\)"$/\1/p' cmd/curator/version.go)"
if [ "$version" != "$declared" ]; then
	warn "compose.pi.yaml pins $version but cmd/curator/version.go says $declared."
	warn "That is fine for a rollback or a re-deploy; it is a mistake if you meant to ship $declared."
fi

if [ -n "$(git status --porcelain compose.pi.yaml compose.yaml 2>/dev/null)" ]; then
	warn "compose.pi.yaml or compose.yaml has uncommitted changes — the Pi will get them anyway."
	warn "The pin belongs on a deploy-$version-to-the-pi branch, or the repo and the box drift."
fi

say "deploying curator:$version to $PI"

# --- refuse to pull something that is not there ------------------------------
#
# `docker pull` answers `manifest unknown` for a tag that does not exist AND for
# one whose release run has not finished building, and the two are
# indistinguishable at the Pi. Both are much cheaper to catch here, before the
# running container has been touched. Checking the arm64 half specifically is
# the point: an amd64-only manifest pulls happily on a laptop and fails on the Pi.
if ! manifest="$(docker manifest inspect "ghcr.io/dulsaranethmin/curator:$version" 2>&1)"; then
	die "no manifest for curator:$version at ghcr.io.
  Either the tag does not exist, or its release run has not finished building.
  Check: gh run list --workflow release
  Registry said: $(printf '%s' "$manifest" | head -2 | tr '\n' ' ')"
fi
printf '%s' "$manifest" | grep -q '"architecture": *"arm64"' \
	|| die "curator:$version has no linux/arm64 in its manifest — the Pi is aarch64 and cannot run it."
say "manifest   arm64 present for curator:$version"

# The release run being green is a second, weaker check: the manifest above is
# what actually blocks a broken pull, and this catches a tag that published an
# image while its gate was red. Skipped without gh rather than made fatal —
# not every machine that can reach the Pi has it.
if [ "${SKIP_RELEASE_CHECK:-}" = "1" ]; then
	say "release    check skipped (SKIP_RELEASE_CHECK=1)"
elif ! command -v gh >/dev/null 2>&1; then
	warn "gh not installed — cannot confirm the release run for v$version was green."
else
	conclusion="$(gh run list --workflow release --branch "v$version" --limit 1 --json conclusion -q '.[0].conclusion' 2>/dev/null || true)"
	case "$conclusion" in
		success)  say "release    run for v$version is green" ;;
		"")       warn "no release run found for tag v$version — deploying anyway." ;;
		*)        die "the release run for v$version concluded '$conclusion', not success.
  Deploy refused. Re-run it, or pass SKIP_RELEASE_CHECK=1 if you know better." ;;
	esac
fi

# --- what is there now, so there is something to go back to ------------------
ssh -o ConnectTimeout=10 "$PI" "test -d $REMOTE" \
	|| die "$PI has no $REMOTE — is this the right host?"

previous_version="$(ssh -o ConnectTimeout=10 "$PI" \
	"curl -s -m 5 http://127.0.0.1:$PORT/healthz 2>/dev/null" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p' || true)"
previous_image="$(ssh -o ConnectTimeout=10 "$PI" \
	"docker inspect -f '{{.Config.Image}}' $CONTAINER 2>/dev/null" || true)"
say "currently  ${previous_version:-unknown} (${previous_image:-no container})"

if [ "$previous_version" = "$version" ]; then
	warn "the Pi is already serving $version — this will recreate it with the same image."
fi

# A rollback restores the OLD compose file, which names the old image. If that
# image is no longer on the box — watchtower's --cleanup elsewhere on the Pi
# removes images it replaces — the restore would re-pull it, which needs the
# network the deploy may have just proven is fine. Worth knowing BEFORE, not
# during, so say so rather than discovering it in the recovery path.
if [ -n "$previous_image" ]; then
	if ssh -o ConnectTimeout=10 "$PI" "docker image inspect '$previous_image' >/dev/null 2>&1"; then
		say "rollback   $previous_image is still on the box"
	else
		warn "$previous_image is NOT on the box — a rollback would have to re-pull it."
	fi
fi

# An `up -d` recreates the container, and a curator with downloads in flight is
# the case D44 refused to let watchtower decide about on a timer. It resumes —
# the engine's state is in the database and on disk — but "it resumes" is a
# worse thing to find out during a deploy than before one, so this counts them
# and says so rather than deciding on anybody's behalf.
active="$(ssh -o ConnectTimeout=10 "$PI" "curl -s -m 5 http://127.0.0.1:$PORT/api/downloads 2>/dev/null" \
	| tr ',' '\n' | grep -c '"state":"\(downloading\|queued\|stalled\)"' || true)"
[ "${active:-0}" -gt 0 ] && warn "$active download(s) in flight — recreating curator interrupts them until they resume."

# --- stop here if this is only a rehearsal -----------------------------------
#
# Everything above is read-only: nothing on the Pi has been written to yet. This
# is the line between "would this work" and "do it", and it is worth having
# because the answer to the first question is most wanted at exactly the moment
# the second one is expensive — mid-download, or in front of a release.
if [ "${DRY_RUN:-}" = "1" ]; then
	say ""
	say "DRY RUN — every check passed and nothing was changed."
	say "           would ship compose{,.pi}.yaml, pull curator:$version, recreate $SERVICE,"
	say "           and roll back to ${previous_version:-the previous compose} if it did not answer."
	exit 0
fi

# --- back up, ship, pull -----------------------------------------------------
#
# Named for the version being deployed TO, matching what is already on the box:
# compose.pi.yaml.pre-0.3.0, .pre-0.4.0, .pre-0.5.0 each hold the file as it was
# BEFORE that version. Both files are backed up because both are shipped — the
# Pi's compose.yaml was four months stale at 0.5.0 and nobody had noticed.
say "backup     $REMOTE/compose{.pi,}.yaml.pre-$version"
ssh -o ConnectTimeout=10 "$PI" "set -eu
	cd $REMOTE
	cp -p compose.pi.yaml compose.pi.yaml.pre-$version
	cp -p compose.yaml    compose.yaml.pre-$version"

say "shipping   compose.yaml, compose.pi.yaml"
scp -q -o ConnectTimeout=10 compose.yaml compose.pi.yaml "$PI:$REMOTE/"

ssh -o ConnectTimeout=10 "$PI" "cd $REMOTE && $compose config --quiet" \
	|| die "the shipped compose files do not validate on $PI. Nothing was restarted; the old container is still running."

say "pulling    curator:$version"
ssh -o ConnectTimeout=10 "$PI" "cd $REMOTE && $compose pull $SERVICE" >/dev/null 2>&1 \
	|| die "pull failed on $PI. Nothing was restarted; the old container is still running."

# --- how to undo, defined before anything that needs it ----------------------
#
# Defined here rather than beside the checks that call it, because bash needs
# the definition to have EXECUTED before the call — a function further down the
# file does not exist yet when an earlier line fails, and the recovery path
# would die with "rollback: command not found" at the exact moment it matters.
rollback() {
	warn "ROLLING BACK to ${previous_version:-the previous compose}"
	ssh -o ConnectTimeout=10 "$PI" "set -eu
		cd $REMOTE
		mv compose.pi.yaml.pre-$version compose.pi.yaml
		mv compose.yaml.pre-$version    compose.yaml
		$compose up -d $SERVICE" >/dev/null 2>&1 \
		|| die "THE ROLLBACK ALSO FAILED. $PI needs hands: the backups are $REMOTE/compose*.pre-$version"
	back="$(ssh -o ConnectTimeout=10 "$PI" "curl -s -m 5 http://127.0.0.1:$PORT/healthz 2>/dev/null" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p' || true)"
	die "deploy of $version failed and was rolled back. $PI is serving ${back:-nothing yet}."
}

# --- the irreversible bit ----------------------------------------------------
say "starting   $SERVICE"
ssh -o ConnectTimeout=10 "$PI" "cd $REMOTE && $compose up -d $SERVICE" >/dev/null 2>&1 || {
	warn "up -d failed"
	rollback
}

# --- prove it, or undo it ----------------------------------------------------
#
# Waiting for the VERSION rather than for HTTP 200 is the whole point. The old
# container answers /healthz perfectly well, so a deploy that silently did
# nothing — an unchanged pin, a pull that resolved to the same digest — looks
# identical to a successful one if the check is "does it answer".
say "waiting    for $PORT/healthz to report $version"
served=""
for i in $(seq 1 "$TIMEOUT"); do
	served="$(ssh -o ConnectTimeout=10 "$PI" \
		"curl -s -m 3 http://127.0.0.1:$PORT/healthz 2>/dev/null" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p' || true)"
	[ "$served" = "$version" ] && { say "serving    $version after ${i}s"; break; }
	sleep 1
done
[ "$served" = "$version" ] || {
	warn "after ${TIMEOUT}s $PI is serving '${served:-nothing}', not $version"
	rollback
}

# Answering is not the same as healthy: the container's own healthcheck is what
# compose and watchtower read, and a curator that answers /healthz while its
# healthcheck fails is a box that looks fine and is not.
settled=""
for i in $(seq 1 60); do
	state="$(ssh -o ConnectTimeout=10 "$PI" "docker inspect -f '{{.State.Health.Status}}' $CONTAINER 2>/dev/null" || echo none)"
	case "$state" in
		healthy)   settled=healthy; say "health     healthy after $((i * 2))s"; break ;;
		unhealthy) warn "container reports UNHEALTHY"; rollback ;;
		none)      settled=none; say "health     no healthcheck on $CONTAINER — skipping"; break ;;
	esac
	sleep 2
done
# Neither healthy nor unhealthy after two minutes is its own answer: `starting`
# forever is a healthcheck that never passes, which compose and watchtower will
# both read as not-ready. Not rolled back — it IS serving the right version, and
# undoing a working binary over a slow probe would be the worse mistake — but it
# must not be reported as a clean deploy.
[ -n "$settled" ] || warn "still '$state' after 120s — deployed, but the healthcheck has not passed."

say ""
say "deployed   curator $version on $PI"
say "rollback   ssh $PI 'cd $REMOTE && mv compose.pi.yaml.pre-$version compose.pi.yaml && mv compose.yaml.pre-$version compose.yaml && $compose up -d $SERVICE'"
ssh -o ConnectTimeout=10 "$PI" 'docker ps --format "  {{.Names}}\t{{.Image}}\t{{.Status}}"' 2>/dev/null || true
