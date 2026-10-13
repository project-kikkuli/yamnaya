# yamnaya

`ym` runs many Claude Code and Codex sessions in tmux. Each project gets a tmux session `ym-<project>` with one manager window and any number of worker windows, plus an open-item tracker and a phase list. It wraps the stock `claude` and `codex` CLIs; nothing is patched.

## Install

    go install github.com/project-kikkuli/yamnaya/cmd/ym@latest

Requires tmux 3.0 or newer. State lives in `~/.local/state/yamnaya/ym.db` (override with `YAMNAYA_STATE_DIR`).

## Commands

    ym new <project> [--cwd .] [--harness claude|codex] [--phases "a,b,c"] [--prompt "..."]
    ym spawn <name> [--harness claude|codex] [--prompt "..."]
    ym item add --kind decision|default|chore --question "..." [--rec "..."] [--option "label=effect"]... [--assumption "..."]
    ym item list [--all] [--json]
    ym item show <id> [--json]
    ym item answer <id> <text>
    ym item dismiss <id> <reason>
    ym item snooze <id>
    ym item reopen <id>
    ym phase set "a,b,c" | next | show
    ym land [project]
    ym kurgan [--json]
    ym ls [--json]
    ym jump <project> [window]
    ym revive <project>
    ym switch <window> --to claude|codex [--min-idle 30s]
    ym board
    ym next
    ym install [--board-key Space] [--next-key N]

Commands run inside a ym window pick up the project from `YM_PROJECT` and the filing window from `YM_WINDOW`; elsewhere pass `-p <project>`.

`revive` recreates a landed or dead project's tmux session and resumes every window's session. `switch` ports an idle window's session to the other harness with `cs port` and resumes it in the same window. `install` links the `ym` skill into `~/.agents`, `~/.claude` and `~/.codex` skills and writes `~/.config/yamnaya/tmux.conf` (prefix+Space opens `ym board`, prefix+N runs `ym next`); source that file from `~/.tmux.conf`.

Item ids are stable per project and never reused: `D` decisions, `V` veto-able defaults, `C` chores, sharing one sequence.

## Environment

- `YAMNAYA_STATE_DIR`: state directory
- `YAMNAYA_TMUX_SOCKET`: tmux socket name (`tmux -L`)
- `YAMNAYA_CLAUDE_BIN`, `YAMNAYA_CODEX_BIN`: harness binaries
- `YAMNAYA_CS_BIN`: the `cs` script `switch` uses to port sessions (default: `cs` on PATH)
- `CODEX_HOME`: where Codex keeps its state; ym reads it to record Codex session ids
