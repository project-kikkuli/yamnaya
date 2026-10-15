# yamnaya

`ym` is one full-screen terminal app that shows every running Claude Code and Codex session, what needs you, and how each project is going. Run it in any terminal tab. It reads the harnesses' own files and never changes your tmux layout, panes or key bindings.

## Install

    go install github.com/project-kikkuli/yamnaya/cmd/ym@latest
    ym install

`ym install` links the `ym` skill into `~/.agents`, `~/.claude` and `~/.codex`. State lives in `~/.local/state/yamnaya/ym.db` (override with `YAMNAYA_STATE_DIR`).

## The screen

- **Repos and missions**: one row per repository, with its missions under it. Every worktree of a repo (by git common dir) counts as the repo; a mission whose lead works outside any repo goes under the repo its crew writes in. Each row shows live sessions, running agents, what needs you, and pull requests with their CI result.
- **Needs you**: permission prompts with the tool and command, then open items with the agent's recommendation, how much they block and which session filed them. Answer (`a`), later (`l`), why (`w`) or dismiss (`x`) inline.
- `enter` drills down and `esc` climbs back: repo, then its missions, then a mission's sessions and crew, then a session (task, latest ask, what it is doing now, pull requests and CI, items, crew), then one agent (its full brief, result, files written and tool trail). Rename (`r`), merge (`m`) or split (`s`) missions.
- `o` goes to a session. When ym runs inside tmux and the session is in a tmux pane, ym jumps there with `switch-client` and `select-pane`. Otherwise it says where the session runs (for example `iTerm ttys012`) and never opens windows or tabs. It never starts a second copy, and flags a session that runs in two processes.

Everything shown is read from the harnesses' own files: Claude Code transcripts and subagent files, Codex's thread database (including its spawn edges) and rollouts. Agents need not report anything. ym reads each transcript once and then only what was appended, keeping its offsets in its database. Pull request status comes from `gh pr view`, fetched in the background at most every two minutes per open pull request.

An answer is typed into the agent's tmux pane as `re D4: <answer>` when the agent sits idle; otherwise the agent reads it with `ym item inbox` on its next turn.

## Commands

    ym                                  the board
    ym sessions [--json]                every live session with host and project
    ym open <session|item>              jump to a session's tmux pane, or say where it runs
    ym item add --kind decision|default|chore --question "..." [--rec "..."] [--option "label=effect"]... [--assumption "..."] [--blocks N]
    ym item list [--all] [--everywhere] [--json]
    ym item show|answer|dismiss|snooze|reopen <id> ...
    ym item inbox                       answers the calling session has not received
    ym phase set "a,b,c" | next | show
    ym spawn <child-session>            record that the calling session started another
    ym project rename|merge|split ...
    ym switch <session> --to claude|codex   port an idle session with cs

Commands that act for an agent find its session by walking up the process tree to the nearest `claude` or `codex` process; pass `--session` to override.

## Layout

`internal/core` holds sessions, projects, items and phases and imports no adapter. Harness adapters (`internal/harness/claude`, `internal/harness/codex`) read each CLI's files; the host adapter (`internal/host/tmux`) finds, focuses and types into panes. An adapter that cannot read something reports it as unknown instead of failing.
