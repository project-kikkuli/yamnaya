# yamnaya

`ym` is one full-screen terminal app that shows every running Claude Code and Codex session, what needs you, and how each project is going. Run it in any terminal tab. It reads the harnesses' own files and never changes your tmux layout, panes or key bindings.

## Install

    go install github.com/project-kikkuli/yamnaya/cmd/ym@latest
    ym install

`ym install` links the `ym` skill into `~/.agents`, `~/.claude` and `~/.codex`. State lives in `~/.local/state/yamnaya/ym.db` (override with `YAMNAYA_STATE_DIR`).

## The screen

- **Needs you**: permission prompts with the tool and command, then open items with the agent's recommendation, how much they block and which session filed them. Answer (`a`), later (`l`), why (`w`) or dismiss (`x`) inline.
- **Missions**: one row per project, with phases, crew and status. A project is a session plus the sessions it started; subagents and teammates are its crew. Rename (`r`), merge (`m`) or split (`s`) missions.
- **Fleet line**: each Claude and Codex limit window with its use and reset, and today's and the last seven days' spend at API prices. Each mission shows its own spend, subagents and child threads included.
- `enter` on a need opens its session; on a mission it shows the detail. When ym runs inside tmux and the session is in a tmux pane, ym jumps there with `switch-client` and `select-pane`. Otherwise it says where the session runs (for example `iTerm ttys012`) and never opens windows or tabs. It never starts a second copy, and flags a session that runs in two processes.

Sessions started with `ym new` run on a separate tmux server, `tmux -L ym`, with its own config and the default `C-b` prefix; ym never touches any other tmux server. `enter` on one of them attaches it in the board's terminal, and detaching returns to the board.

Claude Code reports its limits only to the statusline, so pipe the statusline input into ym from your statusline command, backgrounded so it adds no latency:

    printf '%s' "$input" | ym limits record >/dev/null 2>&1 &

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
    ym item add --kind decision|default|chore --question "..." [--rec "..."] [--option "label=effect"]... [--assumption "..."] [--blocks N]
    ym item list [--all] [--everywhere] [--json]
    ym item show|answer|dismiss|snooze|reopen <id> ...
    ym item inbox                       answers the calling session has not received
    ym phase set "a,b,c" | next | show
    ym spawn <child-session>            record that the calling session started another
    ym project rename|merge|split ...
    ym switch <session> --to claude|codex   port an idle session with cs
    ym usage [--top N] [--json]         API-priced spend per session, today and 7d, and limits
    ym limits                           Claude and Codex limit windows
    ym limits record                    save Claude limits from statusline JSON on stdin

Commands that act for an agent find its session by walking up the process tree to the nearest `claude` or `codex` process; pass `--session` to override.

## Layout

`internal/core` holds sessions, projects, items and phases and imports no adapter. Harness adapters (`internal/harness/claude`, `internal/harness/codex`) read each CLI's files; the host adapter (`internal/host/tmux`) finds, focuses and types into panes and runs ym's own server; `internal/launch` creates and trusts worktrees for `ym new` and decides when one can be landed. An adapter that cannot read something reports it as unknown instead of failing.
