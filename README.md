# yamnaya

`ym` is an alpine terminal board for continuing software work: current assessments, active Claude Code and Codex sessions, and prepared decisions that need you. Run it in any terminal tab. It reads the harnesses' own files and never changes your tmux layout, panes or key bindings.

## Install

    go install github.com/project-kikkuli/yamnaya/cmd/ym@latest
    ym install

`ym install` links the `ym` skill into `~/.agents`, `~/.claude` and `~/.codex`. State lives in `~/.local/state/yamnaya/ym.db` (override with `YAMNAYA_STATE_DIR`).

`ym update` runs that `go install` again and prints the version and path it installed. A running board polls its own executable every second and, once a new binary has settled on disk, restores the terminal and execs it in place with the same arguments and environment, unless you are typing or confirming. Sessions on ym's tmux server are untouched.

## The screen

- **Repos and missions**: one row per repository, with its missions under it. Every worktree of a repo (by git common dir) counts as the repo; a mission whose lead works outside any repo goes under the repo its crew writes in. Each row shows live sessions, running agents, what needs you, and pull requests with their CI result. Continuing mission rows show the manager assessment, live sessions, pending judgments and changes. Native detail retains branch-gate progress.
- **Progress**: `(crew finished + gates passed) / (crew dispatched + 6 × open branches)`. A branch passes six gates: branch, commits, pull request open, CI green, review threads resolved, merged. A subagent killed or failed mid-turn counts in neither part, so it neither inflates the bar nor holds it below 100%; one that ends by handing back its report counts as finished. Agent-set phases are a label and never move the bar. A mission's detail shows the raw counts and each branch's gate strip.
- **Needs you**: permission prompts with the tool and command, then open items with the agent's recommendation, how much they block and which session filed them. Answer (`a`), later (`l`), why (`w`) or dismiss (`x`) inline. Last come idle sessions whose last reply asks you (the same block of questions `h` counts) while no open item of theirs, permission prompt or earlier dismissal of that reply covers it, shown as `waiting:` with the first question of that block: `w` or `enter` shows the whole reply, `a` sends your answer the way `ym send` does and clears the row, and `x` hides it until the session asks again.
- **Fleet line**: each Claude and Codex limit window with its use and reset, and today's and the last seven days' spend at API prices. Every repo, mission, session and agent shows its own spend, subagents and child threads included.
- `enter` drills down and `esc` climbs back: repo, then its missions, then a mission's sessions and crew, then a session (task, latest ask, what it is doing now, pull requests and CI, items, crew), then one agent (its full brief, result, files written and tool trail). Rename (`r`), merge (`m`) or split (`S`) missions, advance (`p`) or set (`P`) their phases, and set their vibe (`v`).
- `h` shows health by week: median and p90 time from item filed to answered, the share of items dismissed or open past a day, assistant messages per day that print a block of questions in chat, and agent-hours per day spent waiting on you (from a turn that ended asking you, or a permission prompt you answered, to your next typed message). It reads transcripts incrementally into per-day rows in the ym database.
- Anything the CLI does for the owner has a key; `?` lists them. `n` starts a new session in the selected row's repository (prompt; the worktree is named from it; `tab` picks claude or codex). `N` picks the repository first, from recent repos then every repo under the roots, then asks for the prompt. `K` kills a session's agent process with SIGTERM after a y/n, leaving its terminal, worktree and transcript; a session ym launched then shows exited and `enter` revives it. `L` lands a launched session (on an exited mission, each of its launches; one whose agent exited or whose pane is gone, or one killed by a signal or by `K` over a day ago, lands by itself once it has nothing to lose), `G` runs `ym gc`, `T` ends an idle one and relaunches it in the other harness on ym's server, in the same directory, `I` lists every item including answered, dismissed and snoozed ones, `U` shows usage and limits, `E` lists what ym launched.
- `o` goes to a session. When ym runs inside tmux and the session is in a tmux pane, ym jumps there with `switch-client` and `select-pane`. Otherwise it says where the session runs (for example `iTerm ttys012`) and never opens windows or tabs. It never starts a second copy, and flags a session that runs in two processes.
- `O` opens a tmux session beside the board instead: ym splits its own pane and attaches the new pane to a grouped copy of the session's tmux session, so the board stays in view and no other client's window moves. The pane closes when you detach from it. It needs ym running inside tmux.

Everything shown is read from the harnesses' own files: Claude Code transcripts and subagent files, Codex's thread database (including its spawn edges) and rollouts. Agents need not report anything. ym reads each transcript once and then only what was appended, keeping its offsets in its database. Pull request status, checks and review threads come from one `gh api graphql` query, fetched in the background at most every two minutes per open pull request.

Sessions started with `ym new` run on a separate tmux server, `tmux -L ym`, with its own config and the default `C-b` prefix; ym never touches any other tmux server. `enter` on one of them attaches it in the board's terminal, and detaching returns to the board.

Claude Code reports its limits only to the statusline, so pipe the statusline input into ym from your statusline command, backgrounded so it adds no latency:

    printf '%s' "$input" | ym limits record >/dev/null 2>&1 &

Spend covers the transcripts on the host ym runs on, and the board names that host; limits are account-wide.

## Continuing work

The repo view stays the entry point. Missions retain their purpose, manager, context, briefs, review obligations and history after their sessions exit. Discovery attaches existing sessions automatically. Use `ym work create "Compatibility" --repo /src/query/.git --purpose "Preserve v2 callers"` for work that starts without a session, then `ym work bind WORK SESSION` and `ym work manager WORK SESSION` to assign independent native sessions. `ym work relate WORK OTHER "depends on the schema migration"` connects work across repositories without changing repo grouping.

- `B` reads the selected project's full manager brief and shows its browser address. On a repo row, `B` and `C` first choose a project. `tab` moves through brief, workstreams, shared context and history. Changes since the last read appear in the brief; assessment freshness is tied to its cited sources and context.
- `R` opens review obligations. `tab` switches ready, preparing, with author and completed. Opening an obligation restores its question, recommendation, evidence and previous feedback. `a` records a judgment; `f` sends feedback into the manager's durable inbox and transfers follow-through to them. Neither action posts to GitHub or Notion. Reading preserves the obligation.
- `C` reads shared context in the terminal; `e` on that panel edits it in `$EDITOR`. Delivery, receipt and each session's explicit adoption of the context revision remain distinct. Context changes invalidate prepared judgments.
- `s` messages the selected workstream or manager. `o` opens its native session. `V` opens the original source from a review, or the stable rich brief from a project. A manager can publish a separate artifact URL.
- `:` runs owner CLI commands with quoted arguments, without shell expansion. Command output opens in a scrollable reader.

`ym serve` runs discovery, source reconciliation, persisted triggers, delivery and the loopback brief server independently of the terminal. Opening the board starts it detached; closing the board leaves it and native agents running. It reloads when its binary changes. `ym service status` shows its heartbeat, address and errors. After changing credentials, `ym service restart` starts it with the current shell's environment. The default brief address is `http://127.0.0.1:7763/brief/WORK`.

Managers receive durable updates through `ym work inbox` (also included in `ym item inbox`). Exited managers can resume through their native Claude or Codex CLI using their original directory and normal configuration. Up to four detached manager resumes run on a host; independently running agents are unrestricted. Execution failures appear in the brief's coverage status and logs under the state directory. This requires the native CLI's own authentication and tool permissions. Workstations and browser access retain their own lifecycle and configuration.

Managers publish `ym work brief WORK` from JSON on stdin:

```json
{"Assessment":"v2 callers remain compatible","Rationale":"Keep one resolver","Changed":"Nested case verified","Uncertainty":"First-cohort traffic","Next":"Check the SDK","Context":2,"Evidence":{"REVIEW":"SOURCE_VERSION"}}
```

Use the actual context revision and exact observed source versions from `ym work show WORK --json`. `Artifact` is an optional browser artifact URL. `ym --session SESSION work adopt WORK --revision 2` records incorporation of changed direction. `ym work trigger WORK RFC3339 "reassess after CI"` persists the next wake request; registered source changes also wake managers.

## Review intake

GitHub review assignments for the authenticated `gh` account are discovered automatically. PRs linked by native sessions are watched provisionally, without treating every linked PR as a request for the owner's judgment. Register human requests and other sources explicitly:

```
ym work watch WORK https://github.com/owner/repo/pull/42 --requester Mira --question "Accept the migration?"
ym work watch WORK https://www.notion.so/PAGE_ID --kind notion --requester Mira --question "Accept the resolver design?"
ym work watch WORK /src/design.html --kind file --requester Mira --question "Approve the proposal?"
```

Notion requires `NOTION_TOKEN` and pages shared with that integration, including read-comment access. Optional `NOTION_REVIEWER_ID` enables explicit mention detection on watched pages. Page blocks and comments are paginated. Coverage is limited to registered pages; Notion has no workspace-wide review-request inbox API. Missing credentials, incomplete GitHub search results and unavailable sources are surfaced as coverage gaps.

Preparation uses `ym work prepare WORK REVIEW` with JSON on stdin:

```json
{"Version":"SOURCE_VERSION","PreparedContext":2,"Question":"Accept the migration?","Recommendation":"Accept","Evidence":"Compatibility and rollback results","Changed":"Nested case added"}
```

A source or context revision removes stale preparation from the ready queue. Feedback remains attached when a revision returns. Judgments recheck registered sources before committing; unavailable or changed evidence must be reconciled first. `ym work history WORK` retains decision rationale and follow-through events. `ym work close WORK "reason"` archives work while keeping outstanding reviews visible; `resume` reactivates it.

`ym work host SSH_ALIAS` brings captured work from an independent host into the same board. Run a compatible `ym serve` there. Messages and judgments queue to their origin and remain visibly pending until applied; an unavailable host retains its last captured state. `ym work list` includes captured hosts, while `work export` exports local work only. Remote history in the local reader is the most recent 20 events; full history remains on its origin.

## Validation

Run `agent-slot -- go test ./...` and `agent-slot -- go vet ./...`. Lifecycle tests replay generated histories against SQLite and an independent obligation ledger, including restarts, duplicate events, stale decisions and context adoption. Process tests run the service, a fixture manager and the HTTP brief server with the terminal absent. `agent-slot -- python3 scripts/check-work-mutants.py` checks that behavioral tests reject deliberately broken implementations. The 1,000-workstream test simulates records and terminal aggregation; it does not run 1,000 autonomous agents or provide formal Go proofs.

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
