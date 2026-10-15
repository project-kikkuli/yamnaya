# yamnaya

`ym` is one full-screen terminal app that shows every running Claude Code and Codex session, what needs you, and how each project is going. Run it in any terminal tab. It reads the harnesses' own files and never changes your tmux layout, panes or key bindings.

## Install

    go install github.com/project-kikkuli/yamnaya/cmd/ym@latest
    ym install

`ym install` links the `ym` skill into `~/.agents`, `~/.claude` and `~/.codex`. State lives in `~/.local/state/yamnaya/ym.db` (override with `YAMNAYA_STATE_DIR`).

## The screen

- **Repos and missions**: one row per repository, with its missions under it. Every worktree of a repo (by git common dir) counts as the repo; a mission whose lead works outside any repo goes under the repo its crew writes in. Each row shows live sessions, running agents, what needs you, pull requests with their CI result, and a progress bar.
- **Progress**: `(crew finished + gates passed) / (crew dispatched + 6 × open branches)`. A branch passes six gates: branch, commits, pull request open, CI green, review threads resolved, merged. Killed or failed subagents count as finished. Agent-set phases are a label and never move the bar. A mission's detail shows the raw counts and each branch's gate strip.
- **Needs you**: permission prompts with the tool and command, then open items with the agent's recommendation, how much they block and which session filed them. Answer (`a`), later (`l`), why (`w`) or dismiss (`x`) inline.
- **Fleet line**: each Claude and Codex limit window with its use and reset, and today's and the last seven days' spend at API prices. Every repo, mission, session and agent shows its own spend, subagents and child threads included.
- `enter` drills down and `esc` climbs back: repo, then its missions, then a mission's sessions and crew, then a session (task, latest ask, what it is doing now, pull requests and CI, items, crew), then one agent (its full brief, result, files written and tool trail). Rename (`r`), merge (`m`) or split (`S`) missions, advance (`p`) or set (`P`) their phases, and set their vibe (`v`).
- `h` shows health by week: median and p90 time from item filed to answered, the share of items dismissed or open past a day, assistant messages per day that print a block of questions in chat, and agent-hours per day spent waiting on you (from a turn that ended asking you, or a permission prompt you answered, to your next typed message). It reads transcripts incrementally into per-day rows in the ym database.
- Anything the CLI does for the owner has a key; `?` lists them. `n` starts a new session (prompt, then repository defaulting to the selected row's, then an optional worktree name; `tab` picks claude or codex). `K` kills a session's agent process with SIGTERM after a y/n, leaving its terminal, worktree and transcript; a session ym launched then shows exited and `enter` revives it. `L` lands a launched session, `T` switches an idle one to the other harness, `I` lists every item including answered, dismissed and snoozed ones, `U` shows usage and limits, `E` lists what ym launched.
- `o` goes to a session. When ym runs inside tmux and the session is in a tmux pane, ym jumps there with `switch-client` and `select-pane`. Otherwise it says where the session runs (for example `iTerm ttys012`) and never opens windows or tabs. It never starts a second copy, and flags a session that runs in two processes.

Everything shown is read from the harnesses' own files: Claude Code transcripts and subagent files, Codex's thread database (including its spawn edges) and rollouts. Agents need not report anything. ym reads each transcript once and then only what was appended, keeping its offsets in its database. Pull request status, checks and review threads come from one `gh api graphql` query, fetched in the background at most every two minutes per open pull request.

Sessions started with `ym new` run on a separate tmux server, `tmux -L ym`, with its own config and the default `C-b` prefix; ym never touches any other tmux server. `enter` on one of them attaches it in the board's terminal, and detaching returns to the board.

Claude Code reports its limits only to the statusline, so pipe the statusline input into ym from your statusline command, backgrounded so it adds no latency:

    printf '%s' "$input" | ym limits record >/dev/null 2>&1 &

Spend covers the transcripts on the host ym runs on, and the board names that host; limits are account-wide.

## Remote hosts

`ym remote install <host>` cross-compiles ym from the source embedded in the local binary (needs `go` locally), copies it to `~/.local/bin/ym` on the Linux host over ssh, runs `ym install --statusline` there, and touches nothing outside the remote home. `--statusline` adds a statusline that records limits when Claude Code has none, and otherwise adds the record line to the existing statusline script after it reads `$input`. Re-running upgrades; an unchanged binary is not copied again. Without `wt` on the host, `ym new` creates worktrees with `git worktree add`.

Usage is read incrementally from `~/.claude/projects` and `~/.codex/sessions` into the ym database; prices come from LiteLLM, refreshed daily into the state directory, with an embedded snapshot offline.

An answer is typed into the agent's tmux pane as `re D4: <answer>` when the agent sits idle; otherwise the agent reads it with `ym item inbox` on its next turn.

## Commands

    ym                                  the board
    ym sessions [--json]                every live session with host and project
    ym new [--codex] [--repo P] [--slug S] "<prompt>"
                                        start claude (or codex) on the prompt in a new wt worktree, on ym's own tmux server
    ym open <session|item|key>          show a session: ym's own sessions attach in this terminal; detach (C-b d) returns to the board
    ym launched                         sessions ym started, and whether each agent still runs
    ym revive <key|session>             resume an exited one in its worktree, recreating the worktree if it is gone
    ym land <key|session>               end it and remove its worktree, only when the branch is clean and merged or pushed
    ym kill <session> [--yes]           end its claude or codex process with SIGTERM; asks y/n, or needs --yes without a terminal
    ym item add --kind decision|default|chore --question "..." [--rec "..."] [--option "label=effect"]... [--assumption "..."] [--blocks N]
    ym item list [--all] [--everywhere] [--json]
    ym item show|answer|dismiss|snooze|reopen <id> ...
    ym item inbox                       answers the calling session has not received
    ym phase set "a,b,c" | next | show
    ym spawn <child-session>            record that the calling session started another
    ym project rename|merge|split ...
    ym switch <session> --to claude|codex   port an idle session with cs
    ym usage [--top N] [--json]         API-priced spend per session, today and 7d, and limits
    ym health [--weeks N] [--json]      weekly item answer times, stale items, questions reprinted in chat, agent time blocked on you
    ym limits                           Claude and Codex limit windows
    ym limits record                    save Claude limits from statusline JSON on stdin
    ym remote install <host>            build ym for the host, copy it to ~/.local/bin/ym there, link the skill and wire the statusline
    ym remote <host> [ym args...]       run ym on the host over ssh: the board by default

Commands that act for an agent find its session by walking up the process tree to the nearest `claude` or `codex` process; pass `--session` to override.

## Layout

`internal/core` holds sessions, projects, items and phases and imports no adapter. Harness adapters (`internal/harness/claude`, `internal/harness/codex`) read each CLI's files; the host adapter (`internal/host/tmux`) finds, focuses and types into panes and runs ym's own server; `internal/launch` creates and trusts worktrees for `ym new` and decides when one can be landed. An adapter that cannot read something reports it as unknown instead of failing.
