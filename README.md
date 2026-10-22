# yamnaya

`ym` hands your work to Claude Code managers and keeps you out of the loop until you are actually needed.
You type a goal once. A manager owns it until it is done, runs its own agents, answers their questions and checks its own work.
The board shows every goal as a mountain, with your queue of things only you can settle floating over it.

## Quickstart

    go install github.com/project-kikkuli/yamnaya/cmd/ym@latest
    ym install --statusline
    cd ~/dev/some-repo
    ym goal "make the export endpoint stream instead of buffering"
    ym

1. `ym install` links the `ym` skill into `~/.agents`, `~/.claude` and `~/.codex`; `--statusline` makes Claude Code's statusline record your limits.
2. `ym goal` records the ask in your words and starts a manager session for it on ym's own tmux server (`tmux -L ym`).
3. `ym` opens the board and starts `ym serve` in the background if it is not running.

On the board you see the goal under **Goals**, moving. Its mountain grows climbers as the manager starts agents and camps as pull requests open.
**Needs you** stays empty until the manager hits something only you can decide, or the goal passes verification and is done.
Press `o` on the goal to open the manager and talk to it like any session.

## How work flows

    you -> goal -> manager -> agents
                    |
                  chiefs (optional, any depth) -> you

- **Goal**: one ask, in your words. States: moving, quiet, down (manager died, being relaunched), verifying, stuck, done.
- **Manager**: an interactive Claude session with a persistent identity. If its session dies, ym relaunches it with its prompt, its brief and every event it missed. It starts agents with `ym new`, which links them under it.
- **Agents**: ordinary Claude or Codex sessions. They ask their manager with `ym item add`, never you. When one goes idle, its last message reaches the manager once.
- **Chiefs**: `ym chief "<name>" --over <id>,<id>` groups managers and other chiefs. A chief reads their briefs, steers them and hears their state changes. Work never waits on a chief.
- **Authority**: a manager decides everything about its goal. It merges its own pull requests only in repositories cleared by `ym goal authority`; elsewhere a green pull request lands in your merge queue. It writes to other people only with a model-name prefix.

The loop is code in `ym serve`, not instructions in a prompt:

1. **Notice.** ym records goal events without a model: agent finished, agent asked, PR opened, CI red or green, review thread or decision, merged or closed, stuck, verifying, verified, failed verification.
2. **Act.** Once per cycle the manager gets everything new in one message. A manager that ends a turn with nothing in flight (no busy agent, no PR awaiting CI, no wake set, no ask waiting) is nudged at once. Managers set their own timers with `ym goal wake 30m "<why>"`.
3. **Verify.** `ym goal done` moves the goal to verifying. ym checks the facts first: every claimed PR merged, CI green on the merge commit, no unresolved review threads. Then a fresh Claude session, not the worker, gets your original ask and the evidence and records a verdict. A fail puts the goal back to active with the findings as an event.
4. **Report.** Only a pass reaches you as done. Managers post progress lines with `ym goal update "<line>"`; they show in the goal's feed.

Nothing goes dark. A manager with no turns, brief edits or agent activity for 30 minutes is nudged; after two nudges the goal shows stuck in your queue with evidence. A session busy for 15 minutes with no tool call or reply counts as hung: a hung manager is relaunched, and a hung or exited agent's manager hears it once with the `ym revive` that resumes it.

## What reaches you

The **Needs you** queue holds only:

- goals that are done (passed verification) or stuck
- asks that climbed past every manager and chief
- green pull requests in repositories where managers may not merge
- GitHub review requests addressed to you personally, not to your teams
- Notion discussions and reviews addressed to you that no goal claims
- permission prompts
- one row per session you started yourself that asks you something

It stays quiet because ym refuses a decision or default bound for you without a recommendation, and a decision without two options with their effects. Every ask arrives with the goal, its pull requests and CI, recent agent activity and what changed since you last looked. Done and stuck also raise a desktop notification; nothing else does.

Each goal row shows its unread feed count and newest line. `enter` opens the goal with its feed and marks it read.

## The board

| Key | Does |
| --- | --- |
| `n` / `N` | new goal in the selected row's repository / pick the repository first |
| `+` | new session of your own, no manager; `tab` switches claude and codex |
| `enter` / `esc` | open or drill in / go back |
| `o` / `O` | go to the session's tmux pane / open it in a pane beside the board |
| `s` | send a message to the goal, chief or session; `space` selects several, `tab` widens a chief to its subtree |
| `a` `g` `l` `x` `w` `u` | answer, go ahead, later, dismiss, show in full, reopen an item |
| `B` / `V` | show the manager's brief / open its registered artifact |
| `C` / `c` | new chief over the selection / put the selection under a chief |
| `R` | relaunch a manager or chief |
| `K` | drop a goal, retire a chief, or kill a session (asks y/n) |
| `L` / `F` | land a launched session (remove its worktree when clean and merged) / forget it |
| `T` | relaunch an idle session in the other harness |
| `G` | gc old worktrees and merged branches |
| `t` | goals as the chief tree or by repository |
| `I` `U` `E` `h` | every item, usage and limits, everything ym launched, weekly health |
| `A` | hide or show the mountains |
| `/` `?` `q` | filter, all keys, back or quit |

Sessions you start yourself (`+`, or `ym new` from your own shell) have no manager. They are grouped by repository under **Sessions you started**, and an idle one whose last reply asks you shows as a `waiting:` row you can answer with `a`.

The fleet line shows each Claude and Codex limit window and today's and this week's spend at API prices.

## Mountains

Each repository is a range and each goal a peak. Busy agents climb the west face, red when stopped on you; pull requests are camps on the east face at the gate they reached (open, CI green, reviews resolved, merged). A red flare rises from work that needs you. Clouds mean quiet, a storm a failing check or a stuck goal, an eruption a dead manager, a flag on the summit done. The sky follows your local clock. `A` hides them, `NO_COLOR` leaves them out, `YM_REDUCED_MOTION=1` holds them still.

## Remote hosts

    ym remote install <host>
    ym remote <host>

`ym remote install` cross-compiles ym from the source embedded in your binary (needs `go` locally), copies it to `~/.local/bin/ym` on the Linux host and runs `ym install --statusline` there. Every host `ym remote` reaches is remembered: the board, `ym sessions` and `ym waiting` show its sessions and items labelled with the host. A host that does not answer shows unreachable with ssh's reason and never holds up the board. Opening, killing, sending, landing and answering run through ssh on that host. `ym remote forget <host>` stops showing it.

## Background service

`ym serve` runs the loop above, refreshes pull request status (at most every two minutes per open PR), polls your GitHub review requests every five minutes, sweeps Notion and delivers answers. The board starts it detached and leaves it running when you close the board. It reloads after a binary update.

    ym service status     heartbeat and last error
    ym service restart    restart it with this shell's environment

Answers reach a Claude session as its next prompt through its messaging socket, busy or idle; otherwise an idle agent in a tmux pane gets them typed in, and anything else reads them with `ym item inbox` on its next turn.

**Notion.** Create an internal integration at notion.so/profile/integrations with read content, read comments and read user information including email, share your pages with it, then run `ym docs connect <your Notion email>` and paste the token. ym sweeps pages edited in the last 21 days. A discussion addressed to you becomes an ask; one about a goal's work goes to that goal's manager instead. `ym docs` shows the last sweep and where each ask went.

**Limits.** Claude Code reports limits only to its statusline. `ym install --statusline` wires it; by hand, add `printf '%s' "$input" | ym limits record >/dev/null 2>&1 &` to your statusline command.

## Commands

| Group | Command | Does |
| --- | --- | --- |
| Board | `ym` | the board |
| | `ym sessions [--local] [--json]` | every live session on this host and remembered hosts |
| | `ym waiting [--json]` | idle sessions whose last reply asks you |
| | `ym board` | this host's state as JSON, for another host's ym |
| Goals | `ym goal [--repo P] "<ask>"` | start a goal and its manager |
| | `ym goal list [--under CHIEF] [--json]` | the goal tree with states and briefs |
| | `ym goal show <id>` / `feed <id>` | one goal with its verifications / its event feed |
| | `ym goal send [--subtree] <id>[,<id>] <text>` | message managers or chiefs |
| | `ym goal relaunch <id>` / `drop <id>` | fresh session from prompt and brief / stop supervising |
| | `ym goal authority [<repo> --merge\|--no-merge]` | where managers may merge |
| | `ym chief [--over ID,ID] [--remit T] "<name>"` | start a chief; `attach <child> <chief>`, `detach <child>` |
| Manager | `ym goal update "<line>"` | post where the goal stands |
| | `ym goal wake <when> <why>` | wake me at `30m`, `2h`, `15:04` |
| | `ym goal done --summary "<line>" [--evidence "<line>"]...` | hand the goal to verification |
| | `ym goal artifact <url>` | register a richer brief |
| | `ym goal verdict <goal> --pass\|--fail [--finding "<line>"]...` | (verifier) record the verdict |
| Items | `ym item add --kind decision\|default\|chore\|go --question "..." [--rec "..."] [--option "label=effect"]...` | ask your manager, or the owner when you have none |
| | `ym item list [--all] [--everywhere] [--to-me]` | open items; `--to-me` is what is routed to the calling manager |
| | `ym item show\|answer\|dismiss\|snooze\|reopen\|go <id>` | act on one item |
| | `ym item inbox` | answers and messages the calling session has not received |
| Sessions | `ym new [--codex] [--repo P] [--slug S] "<prompt>"` | start an agent in a new worktree on ym's tmux server |
| | `ym open <session\|item>` / `ym send <session> <text>` | go to a session / send it a follow-up |
| | `ym adopt <session>` | move a session from another terminal onto ym's server |
| | `ym kill <session> [--yes]` | SIGTERM its agent; worktree and transcript stay |
| | `ym launched [--landed]` / `ym revive <key>` | what ym started / resume an exited one |
| | `ym land <key>` / `ym land --all` | remove worktrees that lose nothing |
| | `ym gc [--dry-run]` | remove idle clean worktrees and merged branches |
| | `ym switch <session> --to claude\|codex [--relaunch]` | port a session to the other harness |
| Usage | `ym usage [--top N]` / `ym limits` / `ym health` | spend, limit windows, weekly item health |
| Hosts | `ym remote install <host>` / `ym remote <host> [args]` / `ym remote forget <host>` | see Remote hosts |
| Setup | `ym install [--statusline]` / `ym update` | link the skill / install the latest ym; a running board restarts into it |
| | `ym service status\|restart` / `ym docs [connect <email>]` | background service / Notion |

Commands that act for an agent find its session by walking up the process tree to the nearest `claude` or `codex` process; `--session` overrides. State lives in `~/.local/state/yamnaya/ym.db` (`YAMNAYA_STATE_DIR` overrides).

## Development

    agent-slot -- go test ./...
    agent-slot -- go vet ./...
    staticcheck ./...

`internal/core` holds goals, managers, chiefs, routing, the supervisor, events and verification, and imports no adapter. `internal/harness/claude` and `internal/harness/codex` read each CLI's own files; `internal/host/tmux` runs ym's tmux server; `internal/launch` creates and lands worktrees; `internal/scene` draws the mountains.

`tools/rehearse/rehearse.sh [--scenario S] [--ref REF] [--timeout MIN]` runs a real goal end to end against a private proving-ground repository (`$YM_REHEARSAL_REPO`) with its own state directory and tmux server, logging every change to `timeline.log`. Scenarios inject one fault: `clean`, `kill-manager`, `kill-worker`, `freeze-worker`, `break-ci`, `review-comment`.
