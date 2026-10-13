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

Commands run inside a ym window pick up the project from `YM_PROJECT` and the filing window from `YM_WINDOW`; elsewhere pass `-p <project>`.

Item ids are stable per project and never reused: `D` decisions, `V` veto-able defaults, `C` chores, sharing one sequence.

## Environment

- `YAMNAYA_STATE_DIR`: state directory
- `YAMNAYA_TMUX_SOCKET`: tmux socket name (`tmux -L`)
- `YAMNAYA_CLAUDE_BIN`, `YAMNAYA_CODEX_BIN`: harness binaries
- `CODEX_HOME`: where Codex keeps its state; ym reads it to record Codex session ids
