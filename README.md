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
    ym [home]
    ym board
    ym next
    ym install [--board-key Space] [--next-key N]
    ym adopt <tmux-target> [--name n] [--harness claude|codex]

Commands run inside a ym window pick up the project from `YM_PROJECT` and the filing window from `YM_WINDOW`; elsewhere pass `-p <project>`.

`revive` recreates a landed or dead project's tmux session and resumes every window's session. `switch` ports an idle window's session to the other harness with `cs port` and resumes it in the same window. `install` links the `ym` skill into `~/.agents`, `~/.claude` and `~/.codex` skills and writes `~/.config/yamnaya/tmux.conf` (prefix+Space opens `ym board`, prefix+N runs `ym next`, prefix+Tab moves focus between the sidebar and the stage in `ym home`); source that file from `~/.tmux.conf`.

Item ids are stable per project and never reused: `D` decisions, `V` veto-able defaults, `C` chores, sharing one sequence.

## Home

`ym` (or `ym home`) opens a tmux session `ym`: the board as a narrow sidebar on the left, and a stage on the right. Enter on a window or a discovered session moves its live pane onto the stage with `swap-pane` and focuses it; choosing another window first returns the staged pane to the window it came from. Nothing is restarted or copied. The stage is recorded in the state database, so a restarted sidebar returns a pane left on stage. Killing the `ym` session kills whatever agent is on stage.

Sidebar keys: Enter stages, Tab focuses the stage, `n` starts a new project (name, cwd, harness, phases, first prompt), `k` lists landed projects and Enter revives one, `q` detaches. In a form, Enter moves to the next field and ctrl-u clears a prefilled one. All board keys below work too; `g` and `f` stage the source window instead of switching to it.

## Board

`ym board` shows one line per project: phase dots and how many items ask you. A project expands to its windows when selected or when something in it asks you. Windows read `needs permission` with the requested tool and command when a Claude Code or Codex permission dialog is on screen, else `asks you`, `running`, `idle 12m`, or `dead`; tmux windows ym does not track show as unmanaged, and `a` adopts one.

Keys: ↑/↓ (or `j`) move, Enter jumps to a window, `o` opens the project's items, `t` starts a side thread, `q` quits. In the item list: Enter expands, `g` goes to the source window, `f` goes there and types `re <id>: `, `y`/`n` answer a default, `d` dismisses with a reason, `s` snoozes.

`ym next` jumps to the next window waiting on a permission dialog, then to windows with open items, oldest item first.

## Environment

- `YAMNAYA_STATE_DIR`: state directory
- `YAMNAYA_TMUX_SOCKET`: tmux socket name (`tmux -L`)
- `YAMNAYA_CLAUDE_BIN`, `YAMNAYA_CODEX_BIN`: harness binaries
- `YAMNAYA_CS_BIN`: the `cs` script `switch` uses to port sessions (default: `cs` on PATH)
- `CODEX_HOME`: where Codex keeps its state; ym reads it to record Codex session ids
- `YAMNAYA_START_GRACE`: how long `new`, `spawn` and `switch` watch a fresh harness for an immediate exit (default `2s`)
- `CLAUDE_CONFIG_DIR`: where Claude Code keeps transcripts (default `~/.claude`); the board reads them for idle time
