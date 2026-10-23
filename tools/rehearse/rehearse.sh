#!/usr/bin/env bash
# Rehearse a real goal end to end: a ym built from origin/main, in its own state dir and tmux server, hands a real claude manager a
# goal on a private repository made for the run from tools/rehearse/seed and records everything that happens; a scenario adds one
# fault and checks it is handled. The repository and the run's checkouts are deleted at the end, leaving the logs.
set -euo pipefail

# Bash reads a script as it runs, so run from a private copy that edits to this file cannot shift under a long rehearsal.
if [ -z "${YM_REHEARSAL_SRC:-}" ]; then
	YM_REHEARSAL_SRC=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
	export YM_REHEARSAL_SRC
	copy=$(mktemp "${TMPDIR:-/tmp}/ym-rehearse.XXXXXX")
	cp "$0" "$copy"
	exec bash "$copy" "$@"
fi

usage() {
	cat <<'EOF'
usage: tools/rehearse/rehearse.sh [--host H | --local] [--scenario S] [--ask TEXT] [--timeout MIN] [--ref REF]

Scenarios: clean, kill-manager, kill-worker, freeze-worker, break-ci, review-comment, kill-serve.
A run goes to a remote host over ssh unless --local: --host H, else $YM_REHEARSAL_HOST, else the one host ym remote
reached. There the script runs from a clone of yamnaya kept in the rehearsal root. Each run builds ym from REF (default
origin/main), creates the private repository <you>/ym-rehearsal-<stamp> from tools/rehearse/seed, runs there, and when it
ends, by timeout, signal or error, ends its tmux server and ym serve, deletes the repository and the run's checkouts, and
keeps timeline.log, serve.log and brief.md in ~/tmp/arbiter-work/yamnaya-rehearsal/<stamp>-<scenario>/ on the host it ran
on. That root holds a .ym-rehearsal file, so the host's own ym never shows, records or adopts a rehearsal's sessions.
YM_REHEARSAL_REPO=owner/name runs on an existing repository instead and keeps it, so concurrent runs can share one.
EOF
}

scenario=clean ask="" timeout=60 ref=origin/main host=${YM_REHEARSAL_HOST:-} local=0
while [ $# -gt 0 ]; do
	case "$1" in
	--host) host=$2; shift 2 ;;
	--local) local=1; shift ;;
	--scenario) scenario=$2; shift 2 ;;
	--ask) ask=$2; shift 2 ;;
	--timeout) timeout=$2; shift 2 ;;
	--ref) ref=$2; shift 2 ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; exit 2 ;;
	esac
done

root=$HOME/tmp/arbiter-work/yamnaya-rehearsal

if [ "$local" = 0 ]; then
	ssh=${YAMNAYA_SSH_BIN:-ssh}
	if [ -z "$host" ]; then
		hosts=$(sqlite3 -readonly "${YAMNAYA_STATE_DIR:-$HOME/.local/state/yamnaya}/ym.db" 'SELECT name FROM ym_hosts ORDER BY first_used' 2>/dev/null || true)
		if [ "$(grep -c . <<<"$hosts")" != 1 ]; then
			echo "pick the host to rehearse on with --host (ym remote knows: ${hosts//$'\n'/ }), or run here with --local" >&2
			exit 2
		fi
		host=$hosts
	fi
	origin=$(git -C "$YM_REHEARSAL_SRC" remote get-url origin)
	remote_args=(--local --scenario "$scenario" --timeout "$timeout" --ref "$ref")
	if [ -n "$ask" ]; then remote_args+=(--ask "$ask"); fi
	script=$(basename "$0")
	# The remote root and the clone are made before the script lands, so it runs from a yamnaya checkout of its own there.
	"$ssh" "$host" "set -e; r=\$HOME/tmp/arbiter-work/yamnaya-rehearsal; mkdir -p \$r; touch \$r/.ym-rehearsal
		[ -d \$r/src/.git ] || git clone -q $(printf %q "$origin") \$r/src; cat >\$r/$script" <"$0"
	echo "rehearsing on $host; its logs stay in ~/tmp/arbiter-work/yamnaya-rehearsal there"
	exec "$ssh" -tt "$host" "r=\$HOME/tmp/arbiter-work/yamnaya-rehearsal; YM_REHEARSAL_SRC=\$r/src YM_REHEARSAL_REPO=$(printf %q "${YM_REHEARSAL_REPO:-}") \
		bash -l \$r/$script$(printf ' %q' "${remote_args[@]}"); s=\$?; rm -f \$r/$script; exit \$s"
fi

src=$YM_REHEARSAL_SRC
stamp=$(date +%Y%m%d-%H%M%S)
ground=${YM_REHEARSAL_REPO:-}
owned=0
if [ -z "$ground" ]; then
	ground=$(gh api user --jq .login)/ym-rehearsal-$stamp
	owned=1
fi
run=$root/$stamp-$scenario
mkdir -p "$run/bin" "$run/state" "$run/src" "$run/snap"
touch "$root/.ym-rehearsal"
log=$run/timeline.log
started=$(date -u +%Y-%m-%dT%H:%M:%SZ)

note() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$log"; }

case "$scenario" in
clean | kill-manager | kill-worker | freeze-worker | break-ci | kill-serve | review-comment) ;;
*) echo "unknown scenario $scenario" >&2; exit 2 ;;
esac

# features are flags the proving ground's cmd/tally may gain, one per line as "flag|ask"; a run takes the first one main
# lacks that no other run has claimed, so every rehearsal is new work.
features='min|Add a -min N flag to cmd/tally that hides words seen fewer than N times (default 1), with table tests in tally for the filtering.
json|Add a -json flag to cmd/tally that prints the counts as a JSON array of {"word", "n"} objects, with a test.
pairs|Add a Bigrams(text string, n int) []Count function to package tally that counts adjacent word pairs joined by a space, with table tests, and a -pairs flag in cmd/tally that uses it.
stop|Add a -stop FILE flag to cmd/tally that ignores the words listed one per line in FILE, with tests in tally for a TopExcept(text, n, stop) function.
len|Add a -len N flag to cmd/tally that ignores words shorter than N letters, with table tests in tally.
reverse|Add a -reverse flag to cmd/tally that prints the least frequent words first, with table tests in tally.
prefix|Add a -prefix P flag to cmd/tally that counts only words starting with P, with table tests in tally.
digits|Add a -digits=false flag to cmd/tally that drops words made only of digits, with table tests in tally.
total|Add a -total flag to cmd/tally that prints one last line with the number of words read, with a test in tally for the count.
unique|Add a -unique flag to cmd/tally that prints only how many distinct words the input has, with table tests in tally.
sep|Add a -sep S flag to cmd/tally that puts S between the count and the word instead of a tab, with a test.
percent|Add a -percent flag to cmd/tally that prints each count as a percentage of all words with one decimal, with table tests in tally.
csv|Add a -csv flag to cmd/tally that prints word,count lines with a header row, with a test.
alpha|Add a -alpha flag to cmd/tally that sorts the printed words alphabetically instead of by count, with table tests in tally.
bars|Add a -bars flag to cmd/tally that prints a bar of # characters scaled so the top word has 40, with table tests in tally.
lines|Add a -lines flag to cmd/tally that counts whole lines instead of words, with table tests in tally.
chars|Add a -chars flag to cmd/tally that counts letters instead of words, with table tests in tally.
suffix|Add a -suffix S flag to cmd/tally that counts only words ending in S, with table tests in tally.
max|Add a -max N flag to cmd/tally that hides words seen more than N times, with table tests in tally.
rank|Add a -rank flag to cmd/tally that prefixes each line with its rank, ties sharing a rank, with table tests in tally.
contains|Add a -contains S flag to cmd/tally that counts only words containing S, with table tests in tally.
upper|Add a -upper flag to cmd/tally that prints the words in upper case, with a test.
dedupe-lines|Add a -dedupe-lines flag to cmd/tally that counts each distinct input line once, with table tests in tally.
first|Add a -first flag to cmd/tally that breaks count ties by first appearance instead of alphabetically, with table tests in tally.
md|Add a -md flag to cmd/tally that prints a Markdown table of word and count, with a test.'

git -C "$src" fetch -q origin
rev=$(git -C "$src" rev-parse "$ref")
git -C "$src" archive "$rev" | tar -x -C "$run/src"
(cd "$run/src" && go build -o "$run/bin/ym" ./cmd/ym)

export YAMNAYA_STATE_DIR=$run/state
# Codex writes its threads under CODEX_HOME; a private one with copies of the owner's login and config keeps the run's out of ~/.codex.
mkdir -p "$run/codex"
for f in auth.json config.toml; do
	if [ -f "$HOME/.codex/$f" ]; then cp "$HOME/.codex/$f" "$run/codex/"; fi
done
export CODEX_HOME=$run/codex
export YAMNAYA_YM_SOCKET=ymr-$stamp
owner_ym=${YM_OWNER_BIN:-$(command -v ym || echo "$HOME/.local/bin/ym")}
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
	if [ -f "$run/frozen" ]; then
		touch "$run/thaw"
		kill -CONT "$(cat "$run/frozen")" 2>/dev/null || true
	fi
	[ -n "$serve_pid" ] && kill "$serve_pid" 2>/dev/null || true
	pkill -f "^$ym serve" 2>/dev/null || true
	tmux -L "$YAMNAYA_YM_SOCKET" kill-server 2>/dev/null || true
	note "ended tmux -L $YAMNAYA_YM_SOCKET and ym serve"
	if [ "$owned" = 1 ]; then
		if out=$(gh repo delete "$ground" --yes 2>&1); then
			note "deleted $ground"
		else
			note "could not delete $ground: $out"
		fi
		rm -rf "$root/claims-${ground##*/}"
	fi
	find "$run" -mindepth 1 -maxdepth 1 ! -name timeline.log ! -name serve.log ! -name brief.md -exec rm -rf {} +
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

if [ "$owned" = 1 ]; then
	gh repo create "$ground" --private --description "Throwaway repository for one yamnaya rehearsal" >/dev/null
	cp -R "$run/src/tools/rehearse/seed" "$run/proving-ground"
	git -C "$run/proving-ground" init -q -b main
	git -C "$run/proving-ground" add -A
	git -C "$run/proving-ground" commit -qm "Count the most frequent words on stdin, with CI"
	git -C "$run/proving-ground" remote add origin "git@github.com:$ground.git"
	git -C "$run/proving-ground" push -q -u origin main
else
	git clone -q "git@github.com:$ground.git" "$run/proving-ground"
fi
if [ -z "$ask" ]; then
	claims=$root/claims-${ground##*/}
	mkdir -p "$claims"
	main_go=$(cat "$run/proving-ground/cmd/tally/main.go")
	while IFS='|' read -r flag text; do
		if ! grep -q "\"$flag\"" <<<"$main_go" && mkdir "$claims/$flag" 2>/dev/null; then
			ask="$text Open a pull request, get CI green, and merge it."
			break
		fi
	done <<<"$features"
	[ -n "$ask" ] || { echo "every feature is on main of $ground or claimed in $claims" >&2; exit 2; }
fi

"$ym" goal authority "${ground##*/}" --merge >/dev/null
start_serve
"$ym" goal --repo "$repo" "$ask" | tee -a "$log"

q() { sqlite3 -readonly -separator ' | ' "$db" "$1" 2>/dev/null || true; }
has_table() { [ -n "$(q "SELECT name FROM sqlite_master WHERE type='table' AND name='$1'")" ]; }
short() { cut -c1-${1:-160} | tr '\n' ' '; }

# prs lists the pull requests holding a commit one of this run's worktrees is on, so concurrent runs on the proving ground stay apart.
prs() {
	local jq=${*: -1} heads
	heads=$(git -C "$repo" worktree list --porcelain | sed -n 's#^HEAD ##p' | jq -R . | jq -sc .)
	gh pr list --repo "$ground" "${@:1:$#-1}" --limit 20 --json commits,createdAt \
		--jq ".[] | select(.createdAt >= \"$started\") | select([.commits[].oid] - (. as \$x | $heads) != [.commits[].oid]) | $jq" 2>/dev/null || true
}

# owner_board is what the owner's own ym shows of this run: its sessions as rows, waiting rows and items from them.
owner_board() {
	env -u YAMNAYA_STATE_DIR -u YAMNAYA_YM_SOCKET YAMNAYA_NO_SERVE=1 "$owner_ym" board 2>/dev/null | jq -r --arg run "$run" '
		([.sessions[]? | select((.cwd // "") | startswith($run)) | .id]) as $ids
		| (.sessions[]? | select((.cwd // "") | startswith($run)) | "owner-row \(.id[0:8]) \(.status) \((.title // "")[0:50])"),
		  (.waiting[]? | select(.session as $s | $ids | index($s)) | "owner-waiting \(.session[0:8]) \((.line // "")[0:120])"),
		  (.items[]? | select(.session as $s | $ids | index($s)) | "owner-item \(.id // .seq) \(.state // "") \((.question // "")[0:120])")' 2>/dev/null || true
}

# snapshot prints one line per fact the timeline tracks; only changed lines are logged.
snapshot() {
	q "SELECT 'goal ' || g.num || ' ' || g.state || ' ev=' || substr(g.evidence,1,120) || ' sum=' || substr(g.summary,1,120) FROM ym_goals g"
	q "SELECT 'manager ' || num || ' ' || role || ' session=' || substr(session,1,8) || ' failures=' || failures || ' nudges=' || nudges || ' err=' || substr(last_error,1,100) FROM ym_managers"
	q "SELECT 'item ' || seq || ' ' || kind || ' ' || state || ' to=' || recipient || ' from=' || substr(session,1,8) || ' q=' || replace(substr(question,1,140),char(10),' ') || ' a=' || replace(substr(answer,1,80),char(10),' ') FROM ym_items"
	q "SELECT 'msg ' || seq || ' ' || state || ' to=' || substr(session,1,8) || ' ' || replace(substr(text,1,170),char(10),' ') FROM ym_messages"
	has_table ym_goal_events && q "SELECT 'event ' || seq || ' goal=' || goal || ' ' || kind || ' delivered=' || (delivered_to_manager IS NOT NULL) || ' ' || replace(substr(payload,1,160),char(10),' ') FROM ym_goal_events"
	has_table ym_goal_timers && q "SELECT 'timer ' || seq || ' ' || manager || ' due=' || due || ' fired=' || (fired IS NOT NULL) || ' ' || replace(substr(why,1,120),char(10),' ') FROM ym_goal_timers"
	has_table ym_goal_verifications && q "SELECT 'verify ' || num || ' attempt=' || attempt || ' verdict=' || verdict || ' verifier=' || substr(verifier_session,1,8) || ' failures=' || failures || ' facts=' || replace(substr(fact_checks,1,100),char(10),' ') || ' findings=' || replace(substr(findings,1,140),char(10),' ') FROM ym_goal_verifications"
	YAMNAYA_NO_SERVE=1 "$ym" sessions --json --local 2>/dev/null | jq -r --arg run "$run" '.[] | select((.cwd // "") | startswith($run)) | "session \(.id[0:8]) \(.status) pid=\(.pid) crew=\(.crew) \((.title // "")[0:60])"' 2>/dev/null || true
	tmux -L "$YAMNAYA_YM_SOCKET" list-panes -a -F 'pane #{session_name} dead=#{pane_dead} status=#{pane_dead_status} pid=#{pane_pid}' 2>/dev/null || true
	if [ $((ticks % 4)) -eq 0 ] || [ ! -f "$run/snap/prs" ]; then
		prs --state all --json number,state,headRefName,reviewDecision,statusCheckRollup,comments,reviews \
		'"pr #\(.number) \(.state) \(.headRefName) checks=\([.statusCheckRollup[]? | (.conclusion // .status)] | join(",")) comments=\(.comments | length) reviews=\(.reviews | length)"' >"$run/snap/prs"
	fi
	cat "$run/snap/prs"
	if [ $((ticks % 4)) -eq 0 ] || [ ! -f "$run/snap/owner" ]; then
		owner_board >"$run/snap/owner"
	fi
	cat "$run/snap/owner"
	for b in "$run"/state/goals/*/brief.md; do [ -f "$b" ] && echo "brief $(basename "$(dirname "$b")") $(cksum <"$b" | cut -d' ' -f1) $(sed -n '1,3p' "$b" | short 100)"; done
	[ -f "$run/state/work-service.json" ] && jq -r '"serve pid=\(.pid) error=" + (.error[0:160])' "$run/state/work-service.json" 2>/dev/null || true
}

prev=$run/snap/prev cur=$run/snap/cur
: >"$prev"
ticks=0
tick() {
	ticks=$((ticks + 1))
	snapshot | sort >"$cur"
	comm -13 "$prev" "$cur" | while IFS= read -r l; do note "+ $l"; done
	comm -23 "$prev" "$cur" | grep -v '^session \|^brief \|^pane ' | while IFS= read -r l; do note "- $l"; done || true
	mv "$cur" "$prev"
}

manager_pid() {
	local s
	s=$(q "SELECT session FROM ym_managers WHERE role='manager' ORDER BY num DESC LIMIT 1")
	YAMNAYA_NO_SERVE=1 "$ym" sessions --json --local 2>/dev/null | jq -r --arg s "$s" '.[] | select(.id == $s) | .pid' | head -1
}
worker_pid() {
	local m
	m=$(q "SELECT group_concat(session, ',') FROM ym_managers")
	YAMNAYA_NO_SERVE=1 "$ym" sessions --json --local 2>/dev/null | jq -r --arg run "$run" --arg m "$m" --arg st "${1:-busy}" \
		'.[] | select((.cwd // "") | startswith($run)) | select((.cwd // "") | test("-wt-verify-") | not) | select(.id as $id | ($m | split(",") | index($id)) | not) | select(.status == $st) | .pid' | head -1
}
first_pr() { prs --state open --json number,headRefName '"\(.number) \(.headRefName)"' | head -1; }
# green_pr is the first open pull request whose checks all passed, so a fault lands on a pull request CI actually runs.
green_pr() {
	prs --state open --json number,headRefName,statusCheckRollup \
		'select((.statusCheckRollup | length) > 0 and all(.statusCheckRollup[]; .conclusion == "SUCCESS")) | "\(.number) \(.headRefName)"' | head -1
}

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
		# tmux continues a pane's process the moment it stops, so keep stopping it until the run ends.
		(while kill -STOP "$pid" 2>/dev/null && [ ! -f "$run/thaw" ]; do :; done) &
		echo "$!" >"$run/freezer"
		note "CHAOS froze busy worker claude pid $pid (SIGSTOP held against tmux's SIGCONT)" && chaos_done=1 && echo "$pid" >"$run/frozen"
		;;
	break-ci)
		read -r pr branch <<<"$(green_pr)"
		[ -n "${pr:-}" ] && [ "$pr" != null ] || return 0
		local w=$run/breaker
		rm -rf "$w"
		git clone -q --branch "$branch" "git@github.com:$ground.git" "$w" || return 0
		printf 'package tally\n\nimport "testing"\n\nfunc TestRehearsalBreaksCI(t *testing.T) { t.Fatal("rehearsal: this test was pushed to break CI; remove it") }\n' >"$w/tally/break_test.go"
		git -C "$w" add -A && git -C "$w" commit -qm "Break CI on purpose for the rehearsal" && git -C "$w" push -q origin "$branch" || return 0
		note "CHAOS pushed a failing test to $branch of PR #$pr" && chaos_done=1
		;;
	kill-serve)
		[ -n "$(worker_pid busy)" ] || return 0
		kill -9 "$serve_pid" && note "CHAOS kill -9 ym serve pid $serve_pid; the rehearsal does not restart it" && chaos_done=1
		;;
	review-comment)
		read -r pr branch <<<"$(first_pr)"
		[ -n "${pr:-}" ] && [ "$pr" != null ] || return 0
		local head
		# GitHub may refuse a call under its rate limits; the fault is left for the next tick then.
		head=$(gh pr view "$pr" --repo "$ground" --json headRefOid --jq .headRefOid) || return 0
		gh api "repos/$ground/pulls/$pr/comments" -f commit_id="$head" -f path=cmd/tally/main.go -f subject_type=file \
			-f body="Rehearsal reviewer: the new flag's usage string should say what happens at its default value." >/dev/null || return 0
		gh pr review "$pr" --repo "$ground" --comment --body "Rehearsal reviewer: please add a one-line mention of the new flag to README.md before this merges." >/dev/null || return 0
		note "CHAOS left a file review thread on cmd/tally/main.go and a top-level review asking for a README line on PR #$pr" && chaos_done=1
		;;
	esac
	return 0
}

deadline=$(($(date +%s) + timeout * 60))
outcome=timeout
while [ "$(date +%s)" -lt "$deadline" ]; do
	tick
	if [ "$scenario" != clean ]; then chaos; fi
	if [ "$scenario" != kill-serve ] && ! kill -0 "$(jq -r .pid "$run/state/work-service.json" 2>/dev/null || echo 0)" 2>/dev/null; then
		note "ym serve is not running; see $run/serve.log"
		start_serve
	fi
	state=$(q "SELECT state FROM ym_goals ORDER BY num DESC LIMIT 1")
	case "$state" in
	done | verified) outcome=$state; break ;;
	esac
	sleep 15
done
tick
if [ -f "$run/frozen" ]; then
	touch "$run/thaw"
	sleep 1
	kill -CONT "$(cat "$run/frozen")" 2>/dev/null || true
fi
note "OUTCOME $outcome after $(( ($(date +%s) - deadline + timeout * 60) / 60 )) min"
pr=$(prs --state all --json number,state,url,headRefName '"\(.url) \(.state)"' | tr '\n' ' ')
note "PRs: ${pr:-none}"
[ "$scenario" = break-ci ] && note "main has break_test.go: $(gh api "repos/$ground/contents/tally/break_test.go" --jq .name 2>/dev/null || echo no)"
cp "$run/state"/goals/*/brief.md "$run/" 2>/dev/null || true
note "final brief: $(ls "$run"/brief.md 2>/dev/null || echo none)"
