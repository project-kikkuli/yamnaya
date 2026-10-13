# yamnaya

`ym` is one full-screen terminal app that shows every running Claude Code and Codex session, what needs you, and how each project is going. Run it in any terminal tab. It reads the harnesses' own files and never changes your tmux layout, panes or key bindings.

## Install

    go install github.com/project-kikkuli/yamnaya/cmd/ym@latest
    ym install

`ym install` links the `ym` skill into `~/.agents`, `~/.claude` and `~/.codex`. State lives in `~/.local/state/yamnaya/ym.db` (override with `YAMNAYA_STATE_DIR`).

## The screen

- **Needs you**: permission prompts with the tool and command, then open items with the agent's recommendation, how much they block and which session filed them. Answer (`a`), later (`l`), why (`w`) or dismiss (`x`) inline.
- **Missions**: one row per project, with phases, crew and status. A project is a session plus the sessions it started; subagents and teammates are its crew. Rename (`r`), merge (`m`) or split (`s`) missions.
- `enter` focuses the terminal already running the session: its tmux pane, iTerm session or Terminal tab. Anywhere else ym shows the resume command. It never starts a second copy, and flags a session that runs in two processes.

An answer is typed into the agent's prompt as `re D4: <answer>` when the agent sits idle; otherwise the agent reads it with `ym item inbox` on its next turn.

## Commands

    ym                                  the board
    ym sessions [--json]                every live session with host and project
    ym open <session|item>              focus a session's terminal
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

`internal/core` holds sessions, projects, items and phases and imports no adapter. Harness adapters (`internal/harness/claude`, `internal/harness/codex`) read each CLI's files; host adapters (`internal/host/tmux`, `iterm`, `terminal`) find, focus and type into terminals. An adapter that cannot read something reports it as unknown instead of failing.
