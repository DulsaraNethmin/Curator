#!/usr/bin/env bash
#
# `make status` — where the build is, derived from the repo rather than declared.
#
# Nothing here is a list somebody has to remember to update. Phases are the
# docs/phase-N.md files that exist; a phase's tasks are the ones its own task
# TABLE names, which is why prose cross-references (phase-3 citing T2, phase-6
# citing T51) do not leak in; a task is "committed" when a commit subject starts
# with its number, which is the convention CLAUDE.md already requires. A second,
# hand-maintained tally would disagree with all of that within a fortnight —
# D14's reasoning about two answers to one question, applied to a status screen.
#
# What it deliberately does NOT do: decide whether anything is *finished*. It
# reports what the repo can prove and points at docs/progress.md for the part
# only a human can write down, which is what is known to be still broken.

set -euo pipefail
cd "$(dirname "$0")/.."

say() { printf '%s\n' "$*"; }
rule() { printf '  %s\n' "----------------------------------------------------------------"; }

# --- git ---------------------------------------------------------------------

git_line() {
	local branch dirty state ahead behind
	branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "not a git repo")
	dirty=$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ')

	if [ "$dirty" = "0" ]; then state="clean"; else state="$dirty uncommitted"; fi

	if git rev-parse --verify --quiet origin/main >/dev/null 2>&1; then
		read -r behind ahead < <(git rev-list --left-right --count origin/main...HEAD 2>/dev/null || echo "0 0")
		if [ "$ahead" != "0" ] || [ "$behind" != "0" ]; then
			state="$state · ahead $ahead, behind $behind of origin/main"
		else
			state="$state · in sync with origin/main"
		fi
	fi
	say "  on $branch · $state"

	# Branches that main does not contain. This is the question that actually
	# gets asked ("did I merge that?"), and it is one nobody can answer from
	# memory after a long day.
	local unmerged
	unmerged=$(git branch --no-merged main --format='%(refname:short)' 2>/dev/null | grep -v '^main$' || true)
	if [ -n "$unmerged" ]; then
		while read -r b; do
			[ -z "$b" ] && continue
			say "  unmerged: $b ($(git rev-list --count main.."$b") commit(s) not in main)"
		done <<<"$unmerged"
	fi
}

# --- phases and tasks --------------------------------------------------------

# tasks_of <phase-doc> — the task numbers named in that document's own tables.
tasks_of() {
	grep -E '^\|' "$1" | grep -oE '\[T[0-9]+\]|\*\*T[0-9]+\*\*' | grep -oE '[0-9]+' | sort -un
}

committed_tasks() {
	git log --format='%s' 2>/dev/null | grep -oE '^T[0-9]+ ' | grep -oE '[0-9]+' | sort -un
}

phases() {
	local committed spec
	committed=$(committed_tasks)

	for doc in docs/phase-*.md; do
		[ -e "$doc" ] || continue
		local n title ids total done_count missing spikes
		n=$(basename "$doc" .md | grep -oE '[0-9]+')
		title=$(head -1 "$doc" | sed -E 's/^# Phase [0-9]+ (—|-) //')
		ids=$(tasks_of "$doc")
		done_count=0
		total=0
		spikes=""
		missing=""
		for id in $ids; do
			if printf '%s\n' "$committed" | grep -qx "$id"; then
				total=$((total + 1))
				done_count=$((done_count + 1))
			elif ! ls docs/tasks/T"$id"-*.md >/dev/null 2>&1; then
				# No commit AND no task file: a spike. Every real task here gets
				# a file; a spike is throwaway code on a branch that is deleted,
				# and only its measurements survive. Counting it as missing work
				# would make phase 6 permanently incomplete.
				spikes="$spikes T$id"
			else
				total=$((total + 1))
				missing="$missing T$id"
			fi
		done

		local mark="  "
		[ "$done_count" = "$total" ] && mark="ok"
		printf '  %-2s %-24s %2s/%-2s  %s%s\n' "$n" "$title" "$done_count" "$total" "$mark" \
			"$([ -n "$spikes" ] && echo "   spikes:$spikes")"
		[ -n "$missing" ] && printf '     no commit for:%s\n' "$missing"
	done

	# Phases that exist in the plan but have no document yet. Absence is the
	# signal: writing docs/phase-N.md is the first act of a phase here.
	spec=""
	for n in $(seq 1 10); do
		[ -e "docs/phase-$n.md" ] || spec="$spec $n"
	done
	[ -n "$spec" ] && say "  not specified yet:$spec  (a phase starts with its doc)"
}

# --- everything else ---------------------------------------------------------

tasks_summary() {
	local files committed orphans
	files=$(ls docs/tasks/ 2>/dev/null | grep -oE '^T[0-9]+' | grep -oE '[0-9]+' | sort -un)
	committed=$(committed_tasks)

	say "  $(printf '%s\n' "$files" | grep -c .) task files · $(printf '%s\n' "$committed" | grep -c .) tasks with a commit"

	orphans=""
	for id in $files; do
		printf '%s\n' "$committed" | grep -qx "$id" || orphans="$orphans T$id"
	done
	if [ -n "$orphans" ]; then
		say "  specified but no T-prefixed commit:$orphans"
		say "  (not the same as undone — a task can land inside another commit)"
	fi
}

decisions() {
	local recorded count last gaps
	recorded=$(grep -oE '^## D[0-9]+' docs/decisions.md | grep -oE '[0-9]+' | sort -un)
	count=$(printf '%s\n' "$recorded" | grep -c .)
	last=$(printf '%s\n' "$recorded" | tail -1)
	say "  $count recorded, up to D$last"

	# The gaps are the interesting part: a decision number is reserved the
	# moment something cites it, so a hole means a record that was promised and
	# has not been written.
	gaps=""
	for n in $(seq 1 "$last"); do
		printf '%s\n' "$recorded" | grep -qx "$n" || gaps="$gaps D$n"
	done
	[ -n "$gaps" ] && say "  reserved but unwritten:$gaps"
}

environment() {
	if [ ! -f .env ]; then
		say "  no .env — defaults apply: embedded engine, VPN required, dispatch refused"
		return
	fi
	# Names and non-secret values only. .env holds a TMDB key, a qBittorrent
	# password and the path to a WireGuard private key; a status screen that
	# printed any of those would be the leak D18 spends a page avoiding.
	local set_keys=""
	for key in TMDB_API_KEY QBIT_USER JELLYFIN_API_KEY VPN_CONFIG_FILE VPN_CONFIG; do
		grep -qE "^${key}=." .env && set_keys="$set_keys $key"
	done
	say "  .env:$([ -n "$set_keys" ] && echo "$set_keys" || echo ' nothing set')"
	for key in TORRENT_BACKEND VPN_REQUIRED DOWNLOADS_DIR LIBRARY_MOVIES; do
		local value
		value=$(grep -E "^${key}=" .env | tail -1 | cut -d= -f2- || true)
		[ -n "$value" ] && printf '  %-16s %s\n' "$key" "$value"
	done
}

# --- output ------------------------------------------------------------------

say ""
say "  curator — $(date '+%Y-%m-%d %H:%M')"
rule
say ""
say "  GIT"
git_line
say ""
say "  PHASES"
phases
say ""
say "  TASKS"
tasks_summary
say ""
say "  DECISIONS"
decisions
say ""
say "  ENVIRONMENT"
environment
say ""
rule
say "  what is still broken is prose, and lives in docs/progress.md"
say "  (\"Still live going out\") — no script can derive that one."
say ""
