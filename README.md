# yamnaya

`ym` is an alpine terminal board for every active Claude Code and Codex session and the decisions that need you. Run it in any terminal tab. It reads the harnesses' own files and never changes your tmux layout, panes or key bindings.

## Install

    go install github.com/project-kikkuli/yamnaya/cmd/ym@latest
    ym install

`ym install` links the `ym` skill into `~/.agents`, `~/.claude` and `~/.codex`. State lives in `~/.local/state/yamnaya/ym.db` (override with `YAMNAYA_STATE_DIR`).

`ym update` runs that `go install` again and prints the version and path it installed. A running board polls its own executable every second and, once a new binary has settled on disk, restores the terminal and execs it in place with the same arguments and environment, unless you are typing or confirming. Sessions on ym's tmux server are untouched.

## The screen

- **Repos and missions**: one row per repository, with its missions under it. Every worktree of a repo (by git common dir) counts as the repo; a mission whose lead works outside any repo goes under the repo its crew writes in. Each row shows live sessions, running agents, what needs you, and pull requests with their CI result.
- **Progress**: `(crew finished + gates passed) / (crew dispatched + 6 × open branches)`. A branch passes six gates: branch, commits, pull request open, CI green, review threads resolved, merged. A subagent killed or failed mid-turn counts in neither part, so it neither inflates the bar nor holds it below 100%; one that ends by handing back its report counts as finished. Agent-set phases are a label and never move the bar. A mission's detail shows the raw counts and each branch's gate strip.
- **Needs you**: permission prompts with the tool and command, then open items with the agent's recommendation, how much they block and which session filed them. Answer (`a`), later (`l`), why (`w`) or dismiss (`x`) inline. Last come idle sessions whose last reply asks you (the same block of questions `h` counts) while no open item of theirs, permission prompt or earlier dismissal of that reply covers it, shown as `waiting:` with the first question of that block: `w` or `enter` shows the whole reply, `a` sends your answer the way `ym send` does and clears the row, and `x` hides it until the session asks again.
- **Fleet line**: each Claude and Codex limit window with its use and reset, and today's and the last seven days' spend at API prices. Every repo, mission, session and agent shows its own spend, subagents and child threads included.
- `enter` drills down and `esc` climbs back: repo, then its missions, then a mission's sessions and crew, then a session (task, latest ask, what it is doing now, pull requests and CI, items, crew), then one agent (its full brief, result, files written and tool trail). Rename (`r`), merge (`m`) or split (`S`) missions, advance (`p`) or set (`P`) their phases, and set their vibe (`v`).
- `h` shows health by week: median and p90 time from item filed to answered, the share of items dismissed or open past a day, assistant messages per day that print a block of questions in chat, and agent-hours per day spent waiting on you (from a turn that ended asking you, or a permission prompt you answered, to your next typed message). It reads transcripts incrementally into per-day rows in the ym database.
- Anything the CLI does for the owner has a key; `?` lists them. `n` starts a new session in the selected row's repository (prompt; the worktree is named from it; `tab` picks claude or codex). `N` picks the repository first, from recent repos then every repo under the roots, then asks for the prompt. `K` kills a live session's agent with SIGTERM after confirmation and removes the stopped session from the active board. On an exited session or mission, `K` archives it without workspace cleanup. Terminals, worktrees, transcripts and items stay; `E` retains launch history and `ym revive` can resume a launched session. Stop intent survives stale discovery snapshots and restarts; a fresh live observation establishes a resume. `L` lands a registered launch, removing its worktree only when clean and merged or pushed. Exited launches can also land automatically once they have nothing to lose, with a day of retention after an owner kill or signal. `G` runs `ym gc`, `T` ends an idle session and relaunches it in the other harness on ym's server, in the same directory, `I` lists every item including answered, dismissed and snoozed ones, `U` shows usage and limits, `E` lists what ym launched.
- `o` goes to a session. When ym runs inside tmux and the session is in a tmux pane, ym jumps there with `switch-client` and `select-pane`. Otherwise it says where the session runs (for example `iTerm ttys012`) and never opens windows or tabs. It never starts a second copy, and flags a session that runs in two processes.
- `O` opens a tmux session beside the board instead: ym splits its own pane and attaches the new pane to a grouped copy of the session's tmux session, so the board stays in view and no other client's window moves. The pane closes when you detach from it. It needs ym running inside tmux.

Everything shown is read from the harnesses' own files: Claude Code transcripts and subagent files, Codex's thread database (including its spawn edges) and rollouts. Agents need not report anything. ym reads each transcript once and then only what was appended, keeping its offsets in its database. Pull request status, checks and review threads come from one `gh api graphql` query, fetched in the background at most every two minutes per open pull request.

Sessions started with `ym new` run on a separate tmux server, `tmux -L ym`, with its own config and the default `C-b` prefix; ym never touches any other tmux server. `enter` on one of them attaches it in the board's terminal, and detaching returns to the board.

Claude Code reports its limits only to the statusline, so pipe the statusline input into ym from your statusline command, backgrounded so it adds no latency:

    printf '%s' "$input" | ym limits record >/dev/null 2>&1 &

Spend covers the transcripts on the host ym runs on, and the board names that host; limits are account-wide.

## Background service

`ym serve` redelivers answers, refreshes pull request status and captures handoffs independently of the terminal. Opening the board starts it detached; closing the board leaves it running. It reloads after a binary update. `ym service status` shows its heartbeat and last error; `ym service restart` adopts the current shell environment.

The background service also captures explicit owner handoffs from the transcripts of live Claude and Codex sessions, including their subagents and child threads. A bounded native semantic pass identifies review requests, proposals, choices, authorization requests and actions that require the originating chat, and files each as one item for that session: choices as decisions, authorization requests as go items, chat actions as chores marked `action in chat`, and review requests and proposals as chores naming the artifact. Items retain the agent, quoted request and transcript location. Repeated summaries match the existing item instead of filing another. Agent prose never records owner approval.

Chat actions display **action in chat**; `o` opens that session and `O` opens it beside the board. Agents can file this directly with `ym item add --kind chore --action chat --question "Enter the login code in this chat"`. Opening a conversation does not complete the action. Structured submissions remain the direct path; background capture recovers omissions without requiring special headings.

Capture uses durable file cursors and source-event identities, so partial writes, restarts and later replies keep outstanding requests. `ym handoff status` reports the backlog; after correcting a reported provider or source failure, `ym handoff resume` reconsiders unresolved records. `ym handoff scan` captures live sessions' transcripts and reconciles extracted requests. One native classifier runs at a time, outside the reconciliation loop, with tools and hooks disabled.

## Validation

Run `agent-slot -- go test ./...`, `agent-slot -- go vet ./...` and `staticcheck ./...`.

## Remote hosts

`ym remote install <host>` cross-compiles ym from the source embedded in the local binary (needs `go` locally), copies it to `~/.local/bin/ym` on the Linux host over ssh, runs `ym install --statusline` there, and touches nothing outside the remote home. `--statusline` adds a statusline that records limits when Claude Code has none, and otherwise adds the record line to the existing statusline script after it reads `$input`. Re-running upgrades; an unchanged binary is not copied again. Without `wt` on the host, `ym new` creates worktrees with `git worktree add`.

Usage is read incrementally from `~/.claude/projects` and `~/.codex/sessions` into the ym database; prices come from LiteLLM, refreshed daily into the state directory, with an embedded snapshot offline.

An answer goes to the session carrying the conversation now, following Claude's continued-in record when a session moved to the background. A Claude session gets `re D4: <answer>` through its own messaging socket (the one cross-session messages use), as its next prompt, busy or idle, wherever it runs, when the session would read it rather than hold it for review (ym marks the owner's words, meaning answers, dismissals and `ym send` run outside any agent session, as sent in bypass mode to a session that bypasses permission prompts, so it reads them; a `crossSessionInbound` setting still decides first), and counts it only when the session sends back no held, refused or dropped receipt within a second, showing an answer the session holds for the owner as held in session; `ym send` messages arrive the same way as `msg: <text>`. Without one, an idle agent in a tmux pane gets it typed in; otherwise it reads it with `ym item inbox` on its next turn.

## Commands

    ym                                  the board
    ym sessions [--json]                every live session with host and project
    ym new [--codex] [--repo P] [--slug S] "<prompt>"
                                        start claude (or codex) on the prompt in a new wt worktree, on ym's own tmux server
    ym waiting [--json]                 idle sessions whose last reply asks you with no item filed; answer with ym send
    ym open <session|item|key>          show a session: ym's own sessions attach in this terminal; detach (C-b d) returns to the board
    ym launched [--landed]              sessions ym started, and whether each agent still runs; --landed adds landed ones
    ym revive <key|session>             resume an exited or landed one in its worktree, recreating the worktree and branch at its
                                        recorded commit (origin's default branch when that commit is gone) if they are gone
    ym land <key|session>               end it and remove its worktree, only when the branch is clean and merged or pushed;
                                        the branch goes too when merged into origin's default branch. The record stays, marked
                                        landed, and each landing is appended to landings.jsonl in ym's state dir
    ym land --all                       land every exited launch that loses nothing, say what stayed
    ym gc [--dry-run]                   remove worktrees of known repos that are clean, on a remote, untouched 3 days and no process's cwd,
                                        then local branches merged into origin's default branch that no worktree uses; lists the dirty and
                                        local-only worktrees it keeps with their sizes, and drops launch records landed over 14 days ago.
                                        The board runs it in the background at most hourly
    ym kill <session> [--yes]           end its claude or codex process with SIGTERM; asks y/n, or needs --yes without a terminal. A
                                        launched session it kills waits a day before landing by itself
    ym item add --kind decision|default|chore|go --question "..." [--rec "..."] [--option "label=effect"]... [--assumption "..."] [--blocks N]
    ym item list [--all] [--everywhere] [--json]
    ym item show|answer|dismiss|snooze|reopen <id> ...
    ym item go <id>                     answer a go item with PLEASE GO IMPLEMENT THIS
    ym item inbox                       answers the calling session has not received
    ym phase set "a,b,c" | next | show
    ym spawn <child-session>            record that the calling session started another
    ym project rename|merge|split ...
    ym switch <session> --to claude|codex [--relaunch]   port an idle session with cs; --relaunch ends it and starts the port on ym's server
    ym usage [--top N] [--json]         API-priced spend per session, today and 7d, and limits
    ym health [--weeks N] [--json]      weekly item answer times, stale items, questions reprinted in chat, agent time blocked on you
    ym limits                           Claude and Codex limit windows
    ym limits record                    save Claude limits from statusline JSON on stdin
    ym update                           go install the latest ym; a running board restarts into it
    ym remote install <host>            build ym for the host, copy it to ~/.local/bin/ym there, link the skill and wire the statusline
    ym remote <host> [ym args...]       run ym on the host over ssh: the board by default

Commands that act for an agent find its session by walking up the process tree to the nearest `claude` or `codex` process; pass `--session` to override.

## Layout

`internal/core` holds sessions, projects, items and phases and imports no adapter. Harness adapters (`internal/harness/claude`, `internal/harness/codex`) read each CLI's files; the host adapter (`internal/host/tmux`) finds, focuses and types into panes and runs ym's own server; `internal/launch` creates and trusts worktrees for `ym new` and decides when one can be landed. An adapter that cannot read something reports it as unknown instead of failing.
