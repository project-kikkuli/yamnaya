#!/usr/bin/env bash
# Rehearse a real goal end to end: a ym built from origin/main, in its own state dir and tmux server, hands a real claude manager a
# goal on the proving-ground repository and records everything that happens; a scenario adds one fault and checks it is handled.
set -euo pipefail

usage() {
	cat <<'EOF'
usage: tools/rehearse/rehearse.sh [--scenario S] [--ask TEXT] [--timeout MIN] [--ref REF] [--keep]

Scenarios: clean, kill-manager, kill-worker, freeze-worker, break-ci, review-comment.
Each run clones the proving ground afresh, builds ym from REF (default origin/main) and writes
~/tmp/arbiter-work/yamnaya-rehearsal/<stamp>-<scenario>/timeline.log. --keep leaves the run's tmux server and ym serve up.
EOF
}

scenario=clean ask="" timeout=60 ref=origin/main keep=0
while [ $# -gt 0 ]; do
	case "$1" in
	--scenario) scenario=$2; shift 2 ;;
	--ask) ask=$2; shift 2 ;;
	--timeout) timeout=$2; shift 2 ;;
	--ref) ref=$2; shift 2 ;;
	--keep) keep=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; exit 2 ;;
	esac
done

ground=${YM_REHEARSAL_REPO:-rajatscode/yamnaya-proving-ground}
src=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
stamp=$(date +%Y%m%d-%H%M%S)
run=${YM_REHEARSAL_DIR:-$HOME/tmp/arbiter-work/yamnaya-rehearsal}/$stamp-$scenario
mkdir -p "$run/bin" "$run/state" "$run/src" "$run/snap"
log=$run/timeline.log
started=$(date -u +%Y-%m-%dT%H:%M:%SZ)

note() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$log"; }

if [ -z "$ask" ]; then
	case "$scenario" in
	clean) ask="Add a -min N flag to cmd/tally that hides words seen fewer than N times (default 1), with table tests in tally for the filtering. Open a pull request, get CI green, and merge it." ;;
	kill-manager) ask="Add a -json flag to cmd/tally that prints the counts as a JSON array of {word, n} objects, with a test. Open a pull request, get CI green, and merge it." ;;
	kill-worker) ask="Add a Bigrams(text string, n int) []Count function to package tally that counts adjacent word pairs joined by a space, with table tests, and a -pairs flag in cmd/tally that uses it. Open a pull request, get CI green, and merge it." ;;
	freeze-worker) ask="Add a -stop FILE flag to cmd/tally that ignores the words listed one per line in FILE, with tests in tally for a TopExcept(text, n, stop) function. Open a pull request, get CI green, and merge it." ;;
	break-ci) ask="Add a -len N flag to cmd/tally that ignores words shorter than N letters, with table tests in tally. Open a pull request, get CI green, and merge it." ;;
	review-comment) ask="Add a -reverse flag to cmd/tally that prints the least frequent words first, with table tests in tally. Open a pull request, get CI green, and merge it." ;;
	*) echo "unknown scenario $scenario" >&2; exit 2 ;;
	esac
fi

git -C "$src" fetch -q origin
rev=$(git -C "$src" rev-parse "$ref")
git -C "$src" archive "$rev" | tar -x -C "$run/src"
(cd "$run/src" && go build -o "$run/bin/ym" ./cmd/ym)
git clone -q "git@github.com:$ground.git" "$run/proving-ground"

export YAMNAYA_STATE_DIR=$run/state
export YAMNAYA_YM_SOCKET=ymr-$stamp
export PATH=$run/bin:$PATH
unset TMUX TMUX_PANE CLAUDECODE CLAUDE_CODE_ENTRYPOINT YAMNAYA_TMUX_SOCKET
db=$run/state/ym.db
repo=$run/proving-ground
ym=$run/bin/ym

note "rehearsal $scenario: ym $(git -C "$src" log -1 --format='%h %s' "$rev")"
note "run dir $run, tmux -L $YAMNAYA_YM_SOCKET, repo $ground"

serve_pid=""
start_serve() {
	"$ym" serve >>"$run/serve.log" 2>&1 &
	serve_pid=$!
	note "ym serve started pid $serve_pid"
}
cleanup() {
	if [ "$keep" = 1 ]; then
		note "kept: tmux -L $YAMNAYA_YM_SOCKET, ym serve pid $serve_pid; YAMNAYA_STATE_DIR=$run/state"
		return
	fi
	[ -n "$serve_pid" ] && kill "$serve_pid" 2>/dev/null || true
	tmux -L "$YAMNAYA_YM_SOCKET" kill-server 2>/dev/null || true
}
trap cleanup EXIT

"$ym" goal authority yamnaya-proving-ground --merge >/dev/null
start_serve
"$ym" goal --repo "$repo" "$ask" | tee -a "$log"

q() { sqlite3 -readonly -separator ' | ' "$db" "$1" 2>/dev/null || true; }
has_table() { [ -n "$(q "SELECT name FROM sqlite_master WHERE type='table' AND name='$1'")" ]; }
short() { cut -c1-${1:-160} | tr '\n' ' '; }

# prs lists the pull requests whose head is a branch of this run's worktrees, so concurrent runs on the proving ground stay apart.
prs() {
	local jq=${*: -1} branches
	branches=$(git -C "$repo" worktree list --porcelain | sed -n 's#^branch refs/heads/##p' | jq -R . | jq -sc .)
	gh pr list --repo "$ground" "${@:1:$#-1}" --search "created:>=$started" \
		--jq ".[] | select(.headRefName as \$b | $branches | index(\$b)) | $jq" 2>/dev/null || true
}

# snapshot prints one line per fact the timeline tracks; only changed lines are logged.
snapshot() {
	q "SELECT 'goal ' || g.num || ' ' || g.state || ' ev=' || substr(g.evidence,1,120) || ' sum=' || substr(g.summary,1,120) FROM ym_goals g"
	q "SELECT 'manager ' || num || ' ' || role || ' session=' || substr(session,1,8) || ' failures=' || failures || ' nudges=' || nudges || ' err=' || substr(last_error,1,100) FROM ym_managers"
	q "SELECT 'item ' || seq || ' ' || kind || ' ' || state || ' to=' || recipient || ' from=' || substr(session,1,8) || ' q=' || replace(substr(question,1,140),char(10),' ') || ' a=' || replace(substr(answer,1,80),char(10),' ') FROM ym_items"
	q "SELECT 'msg ' || seq || ' ' || state || ' to=' || substr(session,1,8) || ' ' || replace(substr(text,1,170),char(10),' ') FROM ym_messages"
	has_table ym_goal_events && q "SELECT 'event ' || rowid || ' ' || replace(substr(kind || ' ' || payload,1,170),char(10),' ') FROM ym_goal_events"
	has_table ym_goal_timers && q "SELECT 'timer ' || rowid || ' ' || replace(substr(quote(due) || ' ' || quote(why),1,140),char(10),' ') FROM ym_goal_timers"
	has_table ym_goal_verifications && q "SELECT 'verify ' || rowid || ' ' || replace(substr(quote(verdict) || ' ' || quote(findings),1,170),char(10),' ') FROM ym_goal_verifications"
	"$ym" sessions --json --local 2>/dev/null | jq -r --arg run "$run" '.[] | select((.cwd // "") | startswith($run)) | "session \(.id[0:8]) \(.status) pid=\(.pid) crew=\(.crew) \((.title // "")[0:60])"' 2>/dev/null || true
	tmux -L "$YAMNAYA_YM_SOCKET" list-panes -a -F 'pane #{session_name} dead=#{pane_dead} status=#{pane_dead_status} pid=#{pane_pid}' 2>/dev/null || true
	prs --state all --json number,state,headRefName,reviewDecision,statusCheckRollup,comments,reviews \
		'"pr #\(.number) \(.state) \(.headRefName) checks=\([.statusCheckRollup[]? | (.conclusion // .status)] | join(",")) comments=\(.comments | length) reviews=\(.reviews | length)"'
	for b in "$run"/state/goals/*/brief.md; do [ -f "$b" ] && echo "brief $(basename "$(dirname "$b")") $(md5 -q "$b") $(sed -n '1,3p' "$b" | short 100)"; done
	[ -f "$run/state/work-service.json" ] && jq -r '"serve error=" + (.error[0:160])' "$run/state/work-service.json" 2>/dev/null || true
}

prev=$run/snap/prev cur=$run/snap/cur
: >"$prev"
tick() {
	snapshot | sort >"$cur"
	comm -13 "$prev" "$cur" | while IFS= read -r l; do note "+ $l"; done
	comm -23 "$prev" "$cur" | grep -v '^session \|^brief \|^pane ' | while IFS= read -r l; do note "- $l"; done || true
	mv "$cur" "$prev"
}

manager_pid() {
	local s
	s=$(q "SELECT session FROM ym_managers WHERE role='manager' ORDER BY num DESC LIMIT 1")
	"$ym" sessions --json --local 2>/dev/null | jq -r --arg s "$s" '.[] | select(.id == $s) | .pid' | head -1
}
worker_pid() {
	local m
	m=$(q "SELECT group_concat(session, ',') FROM ym_managers")
	"$ym" sessions --json --local 2>/dev/null | jq -r --arg run "$run" --arg m "$m" --arg st "${1:-busy}" \
		'.[] | select((.cwd // "") | startswith($run)) | select(.id as $id | ($m | split(",") | index($id)) | not) | select(.status == $st) | .pid' | head -1
}
first_pr() { prs --state open --json number,headRefName '"\(.number) \(.headRefName)"' | head -1; }

chaos_done=0
chaos() {
	[ "$chaos_done" = 1 ] && return 0
	local pid pr branch
	case "$scenario" in
	kill-manager)
		[ -n "$(worker_pid busy)" ] || return 0
		pid=$(manager_pid)
		[ -n "$pid" ] || return 0
		kill -9 "$pid" && note "CHAOS kill -9 manager claude pid $pid" && chaos_done=1
		;;
	kill-worker)
		pid=$(worker_pid busy)
		[ -n "$pid" ] || return 0
		kill -9 "$pid" && note "CHAOS kill -9 busy worker claude pid $pid" && chaos_done=1
		;;
	freeze-worker)
		pid=$(worker_pid busy)
		[ -n "$pid" ] || return 0
		kill -STOP "$pid" && note "CHAOS SIGSTOP busy worker claude pid $pid" && chaos_done=1 && echo "$pid" >"$run/frozen"
		;;
	break-ci)
		read -r pr branch <<<"$(first_pr)"
		[ -n "${pr:-}" ] && [ "$pr" != null ] || return 0
		local w=$run/breaker
		git clone -q --branch "$branch" "git@github.com:$ground.git" "$w"
		printf 'package tally\n\nimport "testing"\n\nfunc TestRehearsalBreaksCI(t *testing.T) { t.Fatal("rehearsal: this test was pushed to break CI; remove it") }\n' >"$w/tally/break_test.go"
		git -C "$w" add -A && git -C "$w" commit -qm "Break CI on purpose for the rehearsal" && git -C "$w" push -q origin "$branch"
		note "CHAOS pushed a failing test to $branch of PR #$pr" && chaos_done=1
		;;
	review-comment)
		read -r pr branch <<<"$(first_pr)"
		[ -n "${pr:-}" ] && [ "$pr" != null ] || return 0
		gh pr review "$pr" --repo "$ground" --comment --body "Rehearsal reviewer: please add a one-line mention of the new flag to README.md before this merges." >/dev/null
		note "CHAOS left a review comment on PR #$pr asking for a README line" && chaos_done=1
		;;
	esac
	return 0
}

deadline=$(($(date +%s) + timeout * 60))
outcome=timeout
while [ "$(date +%s)" -lt "$deadline" ]; do
	tick
	if [ "$scenario" != clean ]; then chaos; fi
	if ! kill -0 "$serve_pid" 2>/dev/null; then
		note "ym serve pid $serve_pid exited; see $run/serve.log"
		start_serve
	fi
	state=$(q "SELECT state FROM ym_goals ORDER BY num DESC LIMIT 1")
	case "$state" in
	done | verified) outcome=$state; break ;;
	esac
	sleep 15
done
tick
[ -f "$run/frozen" ] && kill -CONT "$(cat "$run/frozen")" 2>/dev/null || true
note "OUTCOME $outcome after $(( ($(date +%s) - deadline + timeout * 60) / 60 )) min"
pr=$(prs --state all --json number,state,url,headRefName '"\(.url) \(.state)"' | tr '\n' ' ')
note "PRs: ${pr:-none}"
[ "$scenario" = break-ci ] && note "main has break_test.go: $(gh api "repos/$ground/contents/tally/break_test.go" --jq .name 2>/dev/null || echo no)"
cp "$run/state"/goals/*/brief.md "$run/" 2>/dev/null || true
note "final brief: $(ls "$run"/brief.md 2>/dev/null || echo none)"
