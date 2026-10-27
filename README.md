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
**Needs you** stays empty until the manager hits something only you can decide. When the goal passes verification, its row under **Goals** reads done with the manager's summary.
Press `o` on the goal to open the manager and talk to it like any session.

## How work flows

    you -> goal -> manager -> agents
                    |
                  chiefs (optional, any depth) -> you

- **Goal**: one ask, in your words. States: moving, quiet, adrift (its manager or an agent is busy going nowhere), down (manager died, being relaunched), verifying, stuck, done.
- **Manager**: an interactive Claude session, or Codex with `ym goal --codex` (`tab` on the board), with a persistent identity. If its session dies, ym relaunches it with its prompt, its brief and every event it missed. It starts agents with `ym new`, which links them under it.
- **Context cap**: ym reads each manager's and chief's context size from its latest reply (input plus cache reads and writes on Claude, the turn's input tokens on Codex) and shows it in the selected manager row's details on the board. When one is past the cap (150k tokens by default; `ym goal context-cap [<tokens>|off]` shows or sets it), idle, and a wake is due, ym retires that session and starts a fresh one for the same id that gets the wake. Its prompt is the launch prompt, the latest brief, the current steer revision, the goal's agents and pull requests with CI, and every event since the brief. A stale brief is asked for first and the wake is held until that turn ends. A session past the cap mid-turn keeps its wake until it ends the turn. A manager or chief that is down, past the cap and last replied over an hour ago (its prompt cache gone) starts fresh the same way instead of resuming its transcript. Each restart is a `refreshed: 262k -> fresh` event in the goal's feed (`262k cold` for the down case). Agents are never restarted for their context.
- **Agents**: ordinary Claude or Codex sessions. They ask their manager with `ym item add`, never you. When one goes idle, its last message lands in its manager's inbox once. A reply that adds nothing (no new commit in its worktree, no new ask, mostly the words of its last reply, a short still-waiting note, or one that opens by saying nothing is new) is recorded as `idle-again` and only counted.
- **Chiefs**: a chief groups managers and other chiefs. It reads their briefs, steers them and hears their state changes. Work never waits on a chief. On the board, `c` opens the chief menu: `n` starts one (check its reports in a list of every goal and chief, then type its name and remit), `a` on a goal or chief picks the chief it reports to from a list, and `d` detaches it so it reports to you. From a shell, `ym chief "<name>" --over <id>,<id>` does the same as `c n`.
- **Steering**: `S` on a goal or chief, or `ym goal steer <id>` (opens `$EDITOR`; `--file F` or piped stdin also work), saves a new plan as the next numbered revision. The manager is woken with it and runs `ym goal adopt <id> --revision N`; until then the row shows `steer rN not adopted`. ym nudges it every 10 minutes, twice, then tells whoever steered (a queue row for you, a message for a chief). A chief steers only what is below it. Steering a verifying goal calls verification off; the next verifier judges against the new plan. `ym goal context <id>` prints the revisions.
- **Briefs**: managers and chiefs record where they stand with `ym goal brief`: assessment, what changed, uncertainty, next, waiting on. Each call is a new version. `ym serve` serves them at `http://127.0.0.1:7477/b/<id>` (`/` lists all; `--briefs` or `YAMNAYA_BRIEFS_ADDR` moves it; a ym with another `YAMNAYA_STATE_DIR` serves on a port from 7478 to 8477 derived from that directory; a request whose host is a name other than `localhost` gets 403), and a chief's page rolls up its reports. A brief older than the goal's latest meaningful event (agent done, PR, CI, review, merge, stuck, failed verification, adopted steer) is stale: the manager's prompt has it record a fresh one once it has acted, and a manager still behind 5 minutes later is nudged once, never mid-turn.
- **Authority**: a manager decides everything about its goal. It merges its own pull requests only in repositories cleared by `ym goal authority`; elsewhere a green pull request lands in your merge queue. It writes to other people only with a model-name prefix.

The loop is code in `ym serve`, not instructions in a prompt:

1. **Notice.** ym records goal events without a model: agent finished, asked, stopped, hung or adrift, PR opened, CI red or green, a merge conflict with the base, review thread or decision, merged or closed, stuck, verifying, verified, failed verification.
2. **Act.** Events wait in the manager's inbox. Only what needs it wakes it: an ask, an agent stopped, hung, adrift, swollen or not resumed, CI red, a merge conflict, review comments, a steer, failed verification, a wake it set, or the idle nudge. ym waits a minute after the first of these so a burst lands as one message, which also carries everything else waiting (agents' reports, PRs opened, CI green, merges, resumes, verification passing). With nothing urgent, the rest arrives as one digest at most every 30 minutes. A wake is one line of at most 200 characters per event, `#<n> <kind>: <start>`; `ym goal event <n>` prints one whole and `ym item show` prints an ask. How to act on wakes is in the manager's launch prompt, not repeated in each. A manager that ends a turn with nothing in flight (no busy agent, no PR awaiting CI, no wake set, no ask waiting) is nudged. Managers set their own timers with `ym goal wake 30m "<why>"`. Chiefs get the same: their goals' state changes and their own agents' events, on the same schedule.
3. **Verify.** `ym goal done` moves the goal to verifying. ym checks the facts first, for every PR the manager claims or the goal's agents opened (exited agents' included): merged, CI green on the merge commit, no unresolved review threads. Checks still running are rechecked every minute for up to 2 hours, then the goal shows stuck. Then a fresh session on the manager's harness, not the worker, gets your original ask and the evidence and records a verdict; ym takes it only from that session's own process, so no manager passes its own goal. A fail puts the goal back to active and hands the findings to the manager in its next wake. A verifier that dies, or runs 30 minutes without a verdict, is replaced; after 3 in a row the goal shows stuck. `ym goal show <goal>` prints every attempt with its checks and findings.
4. **Report.** Only a pass reaches you as done. Managers post progress lines with `ym goal update "<line>"`; they show in the goal's feed.

Nothing goes dark. A manager with no turns, brief edits or agent activity for 30 minutes is nudged; after two nudges the goal shows stuck in your queue with evidence. A session busy for 15 minutes with no tool call or reply while the machine is awake counts as hung: a hung manager is relaunched, and a hung or exited agent becomes a goal event, so its manager hears it once in its next wake with the `ym revive` that resumes it, and its chiefs and the goal's feed see it too.

Busy is not moving. ym reads each agent's transcript for signs it is going nowhere: the same failure in 5 of its last 20 tool results over 3 minutes or more with no file changed between (pending CI and empty searches do not count), its context compacted 3 times in a row within 15 minutes each with under 10 tool calls between, or 3 or more files written in a repository other than its own. That agent's row reads `adrift`, its goal stands adrift, and each episode becomes one goal event with the evidence for the manager and its chiefs. It reaches you only if the manager escalates. `h` lists every agent adrift now; `ym health drift` replays this machine's transcripts for past episodes.

## Turning ym off

`ym off` stops ym for a session, a goal or a chief so you can talk to it in its own chat. Run inside a session, `ym off` and `ym on` act on that session; from a shell, `ym off <session|goal|manager|chief>`. On the board, `z` toggles the selected row. A goal's flag covers its manager and every agent under it, including agents started later; a chief's covers the chief and its own agents, not the goals reporting to it; a session's covers it and the sessions it spawns.

While ym is off for a session:

- ym delivers nothing into it: no wakes, nudges, digests, answers, messages, steers or brief prompts. `ym send` and the board's `s` and `r` hold what you send for it.
- ym leaves it alone: no relaunch, revive, idle or quiet nudge, and no stuck row. Its goal stands `ym off` instead of stuck or quiet, and its sessions read `ym off`.
- ym commands run inside it fail with "ym is off for this session; reply in chat instead." These still work, since they only read: `ym`, `ym on`, `ym off`, `ym board`, `ym sessions`, `ym launched`, `ym waiting`, `ym item list`, `ym item show`, `ym goal list`, `ym goal show`, `ym goal feed`, `ym goal context`, `ym drift`, `ym profiles` and `ym phase show`. Commands that never open ym's state, such as `ym slot`, `ym usage` and `ym health`, work as usual.
- Events are still recorded. `ym on` hands each session it reaches again everything held for it as one message: its goal's events, the answers to its items and the messages queued for it.

## What reaches you

The **Needs you** queue holds only what needs you now, and each row leaves on its own once it is settled: an ask answered or dismissed, a pull request merged or closed, a review request withdrawn or done, a discussion resolved, a stuck goal moving again or passing verification, a permission prompt answered. It holds:

- stuck goals
- asks that climbed past every manager and chief
- green pull requests in repositories where managers may not merge
- GitHub review requests addressed to you personally, not to your teams, with the pull request's title and who opened it
- Notion discussions and reviews addressed to you that no goal claims
- permission prompts
- managers' plan revisions you steered that they left unadopted, and relays another host keeps refusing

A session you started yourself that goes idle never lands here, whatever its last message says: its row under **Sessions you started** shows `idle · <its last line>`, with a dot while it has said something since you last opened it with `o` or `O`. When one of your sessions files an ask with `ym item add`, that ask does land here.

It stays quiet because every ask arrives ready to decide:

- ym refuses a decision or default bound for you without a recommendation, and a decision without two options with their effects. Chores and go items need neither.
- ym attaches the goal, its manager, the work's pull requests with CI, recent agent activity and the goal's feed events you have not read, and `ym serve` refreshes them each cycle. The selected row sums it up in one `evidence:` line; `enter` shows what was tried, the evidence and the full context. Answering or dismissing one of the goal's asks, opening the goal or `ym goal feed` marks the news read.
- An ask that climbed to you past a stuck or retired manager or chief without a recommendation or options shows `unprepared`, with what is missing and who was told to fix it with `ym item prepare`. Asks from your own sessions are never marked unprepared.

A done goal never waits in the queue. It stays under **Goals** with its summary for a day after it passes verification, then leaves the board; `x` on its row, or `ym goal ack <goal>`, clears it sooner. Done and stuck also raise a desktop notification; nothing else does.

Each goal row shows its standing, how fresh its brief is, the vibe its manager last recorded, its agents running and idle, its pull requests (open, CI green, merged) and CI state, its spend at API prices, how long since it started, its unread feed count, its ask and the manager's latest `ym goal update` line with its age. The vibe always shows; a narrow screen drops the elapsed time first, then the spend, then CI and pull requests. A chief's row sums the goals below it. `enter` opens the goal with its feed and marks it read.

Every number on a goal or mission row is computed without a model, from transcripts, git, GitHub and ym's database, by the host the goal or mission runs on, each time that host's ym builds its board. A goal or mission on another host shows the numbers from that host's snapshot, so it reads the same here as there. A host still running a ym from before this snapshot format shows `no vibe` and `usage unknown` for its missions until it runs a current ym.

## The board

The footer shows the everyday keys; `?` lists every key.

| Key | Does |
| --- | --- |
| `↑` `↓` | move |
| `tab` / `shift-tab` | next / previous section: Needs you, Goals, Sessions |
| `[` / `]` | previous / next repository group, across goals and sessions |
| `<` `>` (`home` `end`) | top / bottom of the list |
| `enter` / `esc` | open or drill in / go back |
| `r` | reply: answer an item; on a goal or chief, send it a message; on a session, send it a follow-up |
| `g` | go ahead: answer a go item with "PLEASE GO IMPLEMENT THIS" |
| `K` | kill a session, mission or goal, or retire a chief (one y/n naming what dies; see Killing) |
| `c` | chief menu: `a` attach the goal or chief (and the selection) to a chief, or to you; `d` detach it from its chief; `n` new chief over the goals and chiefs you check |
| `b` | show the goal's or chief's brief |
| `n` | new goal: pick the repository first, then type it in your words |
| `/` | filter |
| `?` | every key |

Behind `?`:

| Key | Does |
| --- | --- |
| `N` | new goal in the selected row's repository |
| `+` | new session of your own, no manager |
| `o` / `O` | go to the session's tmux pane / open it in a pane beside the board; `o` on an exited session, or a mission where nothing runs, resumes its latest launched session (on its host too) and goes in |
| `space` | select goal and chief rows; `r`, `s` and `c` then act on every selected one |
| `s` | send a message to the goal, chief or session, like `r`; `tab` widens a chief to its subtree |
| `x` `l` `u` | dismiss, later, reopen an item; `x` also clears a queue row or a done goal |
| `W` / `V` | open the manager's served brief page / its registered artifact |
| `S` | steer a goal or chief: edit its plan in `$EDITOR`, saved as the next revision; on a mission, split it |
| `C` / `D` | new chief / detach from its chief, the same as `c n` / `c d` |
| `R` | relaunch a manager or chief |
| `z` | turn ym off, or back on, for the selected goal, chief, session or mission's session (see Turning ym off) |
| `T` | relaunch an idle session in the other harness |
| `B` | switch the session's login profile |
| `G` | gc old worktrees and merged branches |
| `t` | goals by repository with each chief's reports nested under it, or every goal under its own repository |
| `e` `m` `p` `P` `v` | rename, merge, advance phase, set phases, set the vibe of a mission |
| `I` `U` `E` `h` | every item, usage and limits, everything ym launched, machine slots and load with agents adrift now and weekly health |
| `A` | hide or show the mountains |
| `tab` (while typing a new goal, chief or session) | switch it between claude and codex; each of `n`, `N`, `c n` and `+` starts on the harness it used last |
| `q` | back or quit |

**Goals** groups the goals that report to you by repository and lists every chief, with its reports nested under it, under **chiefs**, so `[` and `]` step through goals and sessions by repository alike.

Sessions you start yourself (`+`, or `ym new` from your own shell) have no manager. They are grouped by repository under **Sessions you started**. An idle one shows `idle · <its last line>` on its own row, behind a dot while that line is unread; `o` or `O` opens it and marks it read, and `r` sends it a follow-up. `ym waiting` still lists the idle ones whose last reply asks you.

A repository is its `origin` remote, so every clone of it, under any directory name and on any host, and every worktree of those clones, shares one repository row and one mountain range, with each session's host shown on its own row. A clone without an `origin` is a repository of its own. `--repo NAME` matches a clone's directory name or its origin's name, and picks the most recently used clone when several clones of one repository match.

`ym new` names the worktree and branch from the prompt, skipping text the agent already reads in its instruction files (`CLAUDE.md`, `AGENTS.md` and what they include) or got in an earlier prompt from the same parent, and ends the name with a random tag so launches in different clones never share a branch. `--slug S` names it exactly. ym discovers a goal's pull requests from its sessions' branches, their upstream branches on origin, and successful pushes, including an explicit refspec such as `HEAD:topic`. The local and remote branch names can differ. A rebased or amended branch keeps its pull request through its upstream or recorded push. An unrelated same-named branch in another clone does not claim it. A goal keeps tracking its pull requests after its agents exit, and pending CI counts as work in flight.

The board's header shows how many sessions run and in what state, how many goals there are, how many rows need you, each Claude and Codex limit window, and today's and this week's spend at API prices across every host. Slots, load and cores are on the health screen (`h`); the header keeps only warnings: throttling, overload and processes holding too much memory.

## Machine slots

Agents share one machine, so ym limits heavy commands to one slot per two available cores and one per 4 GiB of available memory, using whichever gives fewer slots. It first reserves two cores and a quarter of memory (at least 4 GiB) for the host. A 12-core, 24 GiB Mac has 4. `ym slot -- <command> [args...]` waits for a free slot, runs the command at niceness 10 (`--nice N` changes it) in its own process group, and gives the slot back once the command and anything it left running in that group have ended; an interrupt or termination passes to the whole group, and the command's exit status is ym's. Every agent ym starts is told to run test suites, builds and long jobs this way. The slots are the lock files in `~/.local/state/agent-slot` (`$XDG_STATE_HOME/agent-slot`) that the `agent-slot` script also takes, so both draw from one pool; a slot the script holds shows as `agent-slot (no details)`.

Each slot sets its worker budget to `(physical cores - 2) / slots`, rounded down, with a minimum of one. On the 12-core, 24 GiB Mac, four slots each get two workers, leaving four cores and 8 GiB outside the slot budgets. Smaller inherited limits are kept. Commands inherit these settings:

| Tools | Worker settings |
| --- | --- |
| Go | `GOMAXPROCS`, `GOFLAGS=-p=N` |
| Make and CMake | `MAKEFLAGS=-jN`, `CMAKE_BUILD_PARALLEL_LEVEL` |
| Cargo and Rayon | `CARGO_BUILD_JOBS`, `RAYON_NUM_THREADS` |
| pytest-xdist | `PYTEST_XDIST_AUTO_NUM_WORKERS`, for `-n auto` |
| Node | `UV_THREADPOOL_SIZE`, `NODE_OPTIONS=--v8-pool-size=N`, `npm_config_jobs` |
| Native and Python builds | `OMP_NUM_THREADS`, `OPENBLAS_NUM_THREADS`, `MKL_NUM_THREADS`, `NUMEXPR_NUM_THREADS`, `POLARS_MAX_THREADS`, `UV_CONCURRENT_BUILDS`, `MAX_JOBS` |

Existing Go, Make and Node options are retained; inherited Make jobserver descriptors are removed because the slot starts a separate worker budget. These are worker defaults, not a CPU or memory quota. Command-line options and settings inside a shell command can override them. Tools without a worker environment variable need their own flag, such as Jest's `--maxWorkers=2`; use an explicit bounded count for pytest-xdist's `-n` too. The `agent-slot` script shares the locks but does not set these worker limits.

`ym slot --lane yamnaya-test -- nice -n 19 go test -p 2 ./internal/slot/...` also takes the named lane. Commands using the same lane run one at a time across worktrees. A lane waiter appears in `--status` and waits before taking a machine slot, so other lanes can use the pool. Lanes are released when the command and its process group end, including after interruption.

`ym slot --status [--json]` lists the capacity, workers per slot, the load average, each holder with its command, pid, session, how long and where, and each waiter, including their lane names. The board's health screen (`h`) and `ym goal list` show the same with each holder's goal and session name, and `ym goal list` marks the agent holding or waiting for a slot. When the one-minute load average passes one and a half times the physical cores, the header gains a red line naming the load and the three sessions, or programs outside any session, using the most processor time.

`ym serve` also watches load every five seconds, independently of goal reconciliation. When the one-minute load stays above twice the physical cores for a minute, it lowers CPU-consuming child jobs under ym-launched agents to niceness 15, heaviest first, including their job children. Tool jobs beneath a shared Codex app-server belong to a launched session when their `CODEX_THREAD_ID` names it or their working directory is inside its worktree. Agents, shells, the board, ym serve, other terminals and processes outside those agent trees keep their priority. Already lower-priority jobs keep it too. Each affected goal's manager receives one line per overload episode naming its top consumers. Detached jobs whose agent ancestry is gone are outside this control.

During an overload episode, new `ym slot` acquisitions wait without occupying a machine slot and remain visible as waiters. They resume when load falls below 1.25 times the physical cores; the episode clears after three continuous minutes below that level. The header and `ym slot --status [--json]` show observation, holding and recovery. State and notification identities survive serve restarts. Child jobs finish at their lowered priority: macOS does not let an unprivileged process restore a lower nice value, so ym leaves agents and shells unchanged and never kills, stops or restarts a process to recover.

Any process holding more than a quarter of the machine's memory gets a red header line (`memory: script pid 41 holds 26 GiB, 41% of RAM, for 51m, under goal2 w1`), on another host's board too, tagged with that host. When it runs under an agent, ym tells that agent's manager once, with how to end it. ym never stops it itself.

## Mountains

Each repository is a range and each goal a peak. Busy agents climb the west face, red when stopped on you; pull requests are camps on the east face at the gate they reached (open, CI green, reviews resolved, merged). A red flare rises from work that needs you. Clouds mean quiet, a storm a failing check or a stuck goal, an eruption a dead manager, a flag on the summit done. The sky follows your local clock. `A` hides them, `NO_COLOR` leaves them out, `YM_REDUCED_MOTION=1` holds them still. ym sends the mountains only as fast as your terminal takes them in, so they rise from the bottom row up when shown again and a key never waits behind them.

## Remote hosts

    ym remote install <host>
    ym remote <host>

`ym remote install` cross-compiles ym from the source embedded in your binary (needs `go` locally), copies it to `~/.local/bin/ym` on the Linux host and runs `ym install --statusline` there. Every host `ym remote` reaches is remembered: the board, `ym sessions` and `ym waiting` show its sessions and items labelled with the host. `ym item list --all` also lists that host's items answered or dismissed in the last week, each with its history as that host kept it, so you see what became of an answer you sent there. Another host's board is read from the snapshot that host's `ym serve` writes every cycle, so a read never makes a loaded host build a board. The header's `hosts` line says how each host stands: `no answer for 12m (last good 2:04PM)` with ssh's reason when it stops answering, `unreachable` when it never has, and `board 9m old` when it answers but its `ym serve` has fallen behind. None of these holds up the board. Every ssh ym runs to a host shares one connection, kept ten minutes after its last use (its socket is `/tmp/ym-<uid>-<hash>`), so board reads, relays and opening a session skip the ssh handshake. A connection attempt gives up after 3 seconds, a shared connection whose host goes silent ends within about 6, and a read or command that finds a host silent ends that shared connection so the next one starts fresh. Opening, killing, sending and answering run through ssh on that host, and never hold the board: the row says what is running (`killing… 3s`), then how it ended, or `kill failed: <why>`. On a host whose latest read went unanswered, a kill or a send is queued at once instead, and its row says `kill pending since 2:04PM: @host not answering: <why>` until `ym serve` delivers it, within a cycle of the host answering again; opening a session there fails at once with that reason. A kill or send that loses the host partway is queued the same way. Sessions on this host go through the same path and show the same row states. Detaching from a remote session you opened from the board (C-b d) brings you straight back to this host's board. `ym remote forget <host>` stops showing it.

Copying and links work in a session as they do in a plain terminal, on this host's tmux server and on another host's. ym's tmux server takes what an agent copies (the clipboard escape sequence, OSC 52) and what you select with the mouse, and passes links (OSC 8) through to your terminal. On macOS every copy also goes to the pasteboard through `pbcopy`, since Terminal.app ignores OSC 52. A remote session you open from the board or with `ym open` copies to this Mac's pasteboard too, and a link it prints to a port on that host's own localhost, such as a dev server on `http://localhost:3000`, opens here: ym forwards that port over the shared ssh connection when the port is free on this Mac. A session you attach to with your own `ssh host tmux -L ym attach` gets the tmux side only, so on Terminal.app its copies stop at tmux; open it through ym instead. After ym changes this config it applies it to the running server, so sessions already running pick it up. The board's brief page (`b`) wraps long lines and keeps every URL whole and linked rather than cutting it off.

Goals across hosts:

- A goal runs on the host where it starts, supervised by that host's own `ym serve`, which `ym board`, `ym goal`, `ym chief` and `ym new` start there. `ym remote install` also keeps it running across reboots (`ym service keep`, see Background service), so a remote goal moves with no board open anywhere.
- The board shows every host's goal tree as one, each remote manager or chief tagged with its host and standing as that host reads it. A remote goal's feed shows on the board, and its done or stuck notification appears on the host you sit at.
- A remote goal's brief shows on the board and in `ym goal list` with its version and whether it is behind, as that host's snapshot gives it. `b` shows it, and `W` opens its page at `http://127.0.0.1:7477/b/<host>/<id>`, read from that host within five seconds or else from the last snapshot ym read, with its age. Only the latest version comes from another host.
- On a host another host carries relays for, the brief links ym gives its managers name the host, `http://127.0.0.1:7477/b/<host>/<id>`, which opens both there and here, since each `ym serve` shows its own briefs under its own name. Copying an older unqualified link (`/b/<id>`) from a remote session through ym rewrites it to that form, because the same port here belongs to this host's serve.
- A chief can oversee managers and chiefs on other hosts, named `<host>/<id>`: `ym chief attach <host>/mgr3 chief1`, `ym chief attach mgr3 <host>/chief1` or `ym chief --over <host>/mgr3`. Asks climb to it across hosts, its answers come back, and it and every chief above it hear its goals' state changes. `ym goal send`, `relaunch`, `drop`, `ack` and `feed` take remote ids.
- A chief may steer a goal any number of hosts below it. When the chain leaves the goal's host, `ym goal steer` says the steer lands once the next chief's host confirms the steerer is above it; a chief the goal does not report to is told the steer was refused.

Hosts talk through relays:

- Each host keeps every ask, message and command bound for another host until the receiving host acts on it. This host's `ym serve` trades relays with each remembered host every cycle in one ssh round trip, passing on what is bound for a third.
- A relay that fails where it lands (a command that hit a locked database, say) stays with its sender and is sent again every cycle. `ym relay` lists what this host still has to carry, a refused one with `refused there: <why>`; `ym relay drop <seq>` stops carrying one another host keeps refusing.
- A host that cannot be reached keeps working on its own and keeps its relays. The board shows it unreachable with its last known sessions and goals, and what it held takes effect once when it is back.

## Background service

`ym serve` runs the loop above, refreshes pull request status, polls your GitHub review requests every five minutes, sweeps Notion and delivers answers.

- GitHub: pull request status, checks and review threads come from `gh api graphql`, 50 pull requests to a query, at most every two minutes per open PR. A pull request missing from a batch's answer is read alone; a batch that fails ends that cycle's reads. A pull request GitHub answers 404 for shows `pull request not found on GitHub` on its row and is read again hourly. Reads slow as the account's hourly GraphQL budget falls, so your own tools keep their share: full pace above half left, stretching to every 30 minutes at a tenth, and none below a tenth until the budget resets. Pull requests no active goal tracks drop to every 30 minutes below a quarter, so managers keep their CI and review events longest. Every ym instance reads the same account budget. While reads are slowed, stopped or failing, the board's header says so, with the budget left and when it resets.
- Files: ym watches Claude Code's files through kernel notifications (kqueue on macOS), so an untouched file costs nothing, and rereads a session only when one of its files changes. A board whose inputs (processes, files, database, git, launches, remote hosts) did not change is reused for up to ten seconds, so a change only time causes, like a goal going quiet, shows within ten seconds. The transcript scan cache is `ym-scans.db` beside `ym.db`.
- The board starts it detached and leaves it running when you close the board. When the ym executable changes, serve and the board each replace themselves with the new one in the same process. Serve keeps its lock through the switch, so no other ym starts a second serve meanwhile, and the board's message line says `restarted into new ym <commit>` until your next key. A serve started in the background while one already runs exits without a word.
- Once a state directory has had a serve, any `ym` command starts one again if none runs, so a serve that died comes back the next time you or an agent run `ym`. Where `ym service keep` has a launchd agent for the state directory, that command asks launchd to start its serve instead of starting one itself, and a serve started any other way hands over to launchd's at the next binary update, so only launchd's serve runs. A serve sheds any agent session's environment it was started from (its tmux pane, Claude Code's and Codex's session variables). `YAMNAYA_NO_SERVE=1` keeps a command from starting it.
- Killing (`K` on the board, `ym kill` on the command line, the same on this host or another, where it runs through that host's ym over ssh):
  - A session: ends its agent with SIGTERM, closes its tmux pane, removes the worktree ym made for it (an adopted session's directory stays) and drops its row. A session ym did not launch only has its agent ended.
  - A goal: the same for its manager, every agent working for it and a running verifier. The goal shows `killed` on the tree for a day, then leaves it as a done goal does; `x` clears it sooner.
  - A chief: retires the chief alone. Its goals and chiefs keep running and report one level up.
  - One y/n names what dies, such as `3 sessions, 3 worktrees, 2 unpushed commits`.
  - Before removing a worktree, ym bundles the commits no remote or default branch holds and any uncommitted or untracked files into `recovery/<key>.bundle` in the state directory of the session's host; nothing is pushed. The launch record keeps the conversation id, prompt, branch and commit. `ym revive <key>` recreates the worktree, puts the work back and resumes the conversation; `ym revive <goal>` reopens a killed goal (or unretires a chief) and revives every session its kill ended.
  - On the board, counting and teardown run in the background; rows being torn down are marked `x`, and the cursor moves to the next row.
- Exited launches land by themselves once they have nothing to lose, after a day's grace when you killed them or a signal did. `ym serve` does the landing; building the board never runs git. A landing that refuses, such as on uncommitted work, is not tried again until the worktree's HEAD, index or top directory changes. A launch killed before kills removed worktrees is ended as a kill is now, its work bundled first. A launch whose pane went while its agent was running, without `ym kill`, never lands by itself; ym resumes it instead (see Restarts). A launch whose worktree and repository are both gone never lands by itself either; its row says `repo gone: conversation only, no code`.
- A launch counts as starting, not gone, for its first two minutes until its tmux session is up, so a fresh agent is never resumed under itself.
- `ym revive` (and `o` on an exited launched session) recreates the worktree and branch when the repository exists, from the branch, else the recorded commit, else the branch on origin, else the default branch. When the launch's own clone is gone it uses another clone of the same repository on this host. When no clone is left it makes a plain directory at the recorded path and resumes the conversation there, without a checkout, and says so.
- A session you run in a terminal, outside ym's server, gets a launch record while the board shows it, so once it ends `ym revive` resumes it on ym's server and it shows as dormant (`ended outside ym`) for a day.
- `ym gc` keeps a landed record past 14 days while its conversation's transcript is still there.
- ym's worktree Git commands and their `wt` children disable automatic Git maintenance and garbage collection. Starting or restoring sessions therefore does not launch detached `git gc`, `prune` or reflog expiry. Explicit repository maintenance remains a separate command; run it under a slot.
- `ym gc` on the default state directory sweeps the repositories of its launches, of sessions it has seen and every repository under `~/dev` with linked worktrees. On any other state directory (`YAMNAYA_STATE_DIR`) it removes only the worktrees and branches its own launches made, so a drill or test never touches your repositories.
- `ym gc` appends a line to `gc.log` in the state directory for every worktree, branch and launch record it removes: the RFC3339 time, what it removed, then `repo=`, `path=`, `branch=`, `head=` (the commit it pointed at) and `reason=`. The file only grows.
- A new launch never takes the worktree name of a launch ym still keeps a record of, landed or not, so starting another session with the same slug gets `<slug>-2` and the landed one stays revivable.
- A serve on a state directory other than the default (`YAMNAYA_STATE_DIR`) that sees the directory deleted ends the tmux server it launched agents on, unless that is ym's default server, and stops. Your default state directory and ym's default tmux server are never torn down this way.
- A Claude session ym launched that sits at the folder trust dialog in its first ten minutes is trusted again and restarted, since another session's config save can drop the trust ym recorded.

**Restarts.** `ym service keep` keeps `ym serve` running across logouts and reboots. On macOS it writes and loads a LaunchAgent (`~/Library/LaunchAgents/io.yamnaya.ym-serve.plist` for the default state directory, `io.yamnaya.ym-serve-<hash>.plist` for any other) that starts it at login and again whenever it exits, in the login shell's environment, logging to `work-service.log` in the state directory. Each line `ym serve` writes there starts with its RFC3339 time; past 1 MiB the log moves to `work-service.log.1`, replacing the previous one, so the two never grow past about 2 MiB. On Linux it writes a systemd user timer (`ym-serve.timer`, or a name of its own for another state directory). The board and `ym install` set it up for the default state directory, so you never run it. `ym service unkeep` removes it; `ym service status` shows `kept` and `kept_pid` when loaded.

After a restart:

- A manager or chief resumes in its own session and conversation, told it stopped and what it missed. One past the context cap whose last reply is over an hour old starts fresh from its brief instead (see Context cap). If its transcript is gone, it starts over from its prompt and brief, under the same id on Claude and under the new thread Codex records on Codex. `ym goal relaunch` still starts a fresh session.
- Every session ym launched whose agent was running when its pane went, without `ym kill`, resumes in its own conversation instead of landing, whether or not it works for a goal. A goal's manager gets a `resumed` or `resume-failed` event, and `resume-failed` shows in your feed. `ym kill` keeps an agent stopped. A session whose transcript holds no turn starts over on its first prompt.
- Sessions you had running in terminals outside ym's server when the host went down resume on ym's server in their own directories and conversations. `ym serve` keeps the list in `alive.json` in the state directory.
- The default state directory's `ym serve` unloads and removes any other `io.yamnaya.ym-serve-*` LaunchAgent whose ym binary or state directory is gone, as a rehearsal in a deleted scratch directory leaves, so launchd stops retrying it at login.
- Resumes go one at a time, at most one every 10 seconds. Once every launched session found gone has come back or failed, one desktop notification says what resumed, what started over and what could not.

    ym service status     heartbeat and last error
    ym service restart    restart it with this shell's environment
    ym service keep       keep it running across reboots (unkeep undoes it)
    ym serve --wait       wait for a running serve to exit, then take over

Answers reach a Claude session as its next prompt through its messaging socket, busy or idle; otherwise an idle agent in a tmux pane gets them typed in, and anything else reads them with `ym item inbox` on its next turn. `ym serve` retries every cycle: an answer, or a message from the last hour, that is still queued is typed in as soon as its agent sits idle at an empty prompt, never over text you are typing there. When one cannot be delivered to an idle agent, `work-service.log` says why, once per reason.

**Notion.** Create an internal integration at notion.so/profile/integrations with read content, read comments and read user information including email, share your pages with it, then run `ym docs connect <your Notion email>` and paste the token. ym sweeps pages edited in the last 21 days, and every 15 minutes rereads the comments of each of those pages tied to you, edited since or not, and of every page with an ask open, so a new comment, reply or resolution is found even if Notion leaves the page's edit time alone; a page is read in full again only when its edit time or its comments change. A page is tied to you when you created it, wrote in or are mentioned in one of its threads, or it lists you in a reviewer, approver or sign-off people property. A discussion addressed to you becomes an ask; one about a goal's work goes to that goal's manager instead, as one message per document listing its new or changed threads. Each document is one queue row however many threads it has open (`enter` opens the page, or the thread when there is only one; the open row lists each thread with its link); each thread closes on its own when it is resolved or answered, or once the comment asking you is more than 21 days old. A row you clear stays hidden as its threads close and comes back when any of them gets a new comment. `ym docs` shows the last sweep and where each ask went.

**Limits.** Claude Code reports limits only to its statusline. `ym install --statusline` wires it; by hand, add `printf '%s' "$input" | ym limits record >/dev/null 2>&1 &` to your statusline command.

## Commands

| Group | Command | Does |
| --- | --- | --- |
| Board | `ym` | the board |
| | `ym sessions [--local] [--json]` | every live session on this host and remembered hosts |
| | `ym waiting [--json]` | idle sessions whose last reply asks you |
| | `ym board` | this host's latest snapshot as JSON, for another host's ym; `--build` builds it now |
| Goals | `ym goal [--codex] [--repo P] "<ask>"` | start a goal and its manager, on codex with `--codex`, whose verifiers run on the same harness; the ask is one quoted argument, so a subcommand this binary lacks is refused instead of becoming a goal |
| | `ym goal list [--under CHIEF] [--json]` | the goal tree with states and briefs, and under each goal every agent still working for it, sessions its manager started and Claude subagents inside the manager or those sessions: busy, waiting, idle, hung or adrift, its latest tool call, reply or question, its branch's commits and its PRs; the count says how many more finished |
| | `ym goal show <id>` / `feed <id>` | one goal with its verifications / its event feed |
| | `ym goal send [--subtree] <id>[,<id>] <text>` | message managers or chiefs |
| | `ym goal steer [--file F\|-] <id>` / `context [--all] <id>` | change what a goal or chief is about / print its plan revisions |
| | `ym goal relaunch <id>` / `drop <id>` | fresh session from prompt and brief / stop supervising |
| | `ym goal context-cap [<tokens>\|off]` | the context size past which ym restarts an idle manager or chief fresh from its brief |
| | `ym goal ack <goal>` | clear a stuck goal from your queue, or a done goal from the goal tree |
| | `ym goal authority [<repo> --merge\|--no-merge]` | where managers may merge |
| | `ym chief [--codex] [--over ID,ID] [--remit T] "<name>"` | start a chief, on codex with `--codex`; `attach <child> <chief>`, `detach <child>` |
| Manager | `ym goal update "<line>"` | post where the goal stands |
| | `ym goal wake <when> <why>` | wake me at `30m`, `2h`, `15:04` |
| | `ym goal event <n>...` | print events a wake lists, whole |
| | `ym goal done --summary "<line>" [--evidence "<line>"]...` | hand the goal to verification |
| | `ym goal brief --assessment A --changed C --uncertainty U --next N --waiting-on W` | record the next brief version |
| | `ym goal adopt [<id>] --revision N` | record taking a steered revision on |
| | `ym goal artifact <url>` | register a richer brief |
| | `ym goal verdict <goal> --pass\|--fail [--finding "<line>"]...` | (verifier) record the verdict, only from inside the verifier session; `--session` is refused |
| Items | `ym item add --kind decision\|default\|chore\|go --question "..." [--rec "..."] [--option "label=effect"]... [--evidence <link>]... [--tried "..."]` | ask your manager, or the owner when you have none; ym attaches the evidence it can find |
| | `ym item list [--all] [--everywhere] [--to-me]` | open items; `--to-me` is what is routed to the calling manager |
| | `ym item show\|answer\|dismiss\|snooze\|reopen\|go <id>` | act on one item |
| | `ym item prepare <id> [--rec "..."] [--option "label=effect"]... [--evidence <link>]... [--tried "..."] [--assumption "..."]` | (manager) add what an open ask lacks; `--option` replaces its options, `--evidence` appends; refused if an owner-bound ask would still not be ready |
| | `ym item inbox` | answers and messages the calling session has not received |
| | `ym off [<session\|goal\|chief>]` / `ym on [...]` | stop ym for the calling session, or the one named / start it again, handing over what waited in one message |
| Sessions | `ym new [--codex] [--repo P] [--slug S] "<prompt>"` | start an agent in a new worktree on ym's tmux server |
| | `ym open <session\|item>` / `ym send <session> <text>` | go to a session / send it a follow-up |
| | `ym adopt <session>` | move a session from another terminal onto ym's server |
| | `ym kill <session\|goal\|chief> [--yes]` | end it completely, as `K` does; see Killing |
| | `ym launched [--landed]` / `ym revive <key\|goal>` | what ym started / resume an exited or killed one, or a killed goal |
| | `ym slot [--nice N] -- <command>` / `ym slot --status [--json]` | run a heavy command in a shared machine slot / who holds and waits for them |
| | `ym gc [--dry-run]` | remove idle clean worktrees and merged branches |
| | `ym switch <session> --to claude\|codex [--relaunch]` | port a session to the other harness |
| Usage | `ym usage [--top N]` / `ym limits` / `ym health` | spend, limit windows, weekly item health |
| Drift | `ym health drift [--since D] [file...]` | replay this machine's transcripts and list each time an agent was busy going nowhere |
| Hosts | `ym remote install <host>` / `ym remote <host> [args]` / `ym remote forget <host>` | see Remote hosts |
| | `ym relay [drop <seq>]` | what this host still has to carry, or stop carrying a refused one |
| Setup | `ym install [--statusline]` / `ym update` | link the skill / install the latest ym; a running board restarts into it |
| | `ym service status\|restart\|keep\|unkeep` / `ym docs [connect <email>]` | background service / Notion |

Session commands accept a full session ID, the shorthand printed by `ym sessions`, or an unambiguous prefix of at least six characters. UUIDv7 sessions, including Codex threads, display the last 12 characters so sessions started together have distinct labels; Claude UUIDv4 sessions display the first eight. An ambiguous shorthand or prefix returns an error with the full IDs instead of selecting a session. Stored sessions, inboxes, goal links and adopted launch keys use full IDs.

Commands that act for an agent use Codex's full `CODEX_THREAD_ID` in its tool shells, including when the shell's process ancestry names no session or a different one. Otherwise they find the session by walking up the process tree to the nearest `claude` or `codex` process; `--session` overrides. State lives in `~/.local/state/yamnaya/ym.db` (`YAMNAYA_STATE_DIR` overrides).

Messages on ym's tmux server are recorded before a paste starts. ym checks the composer before Enter and checks that it clears afterward. If the paste or submission cannot be confirmed, the session's message list shows "submission uncertain (not replayed)". ym never pastes that message again. Open the session with `ym open <session>` to inspect its composer and conversation before deciding whether the remaining draft needs submission; the message list keeps the original text for comparison. A message for which no paste started stays pending for `ym item inbox` or delivery when the input box becomes available. Pending messages are typed automatically only at an empty, idle prompt; Codex's Working indicator keeps a busy pane out of that path.

ym starts Codex as the platform binary its npm package ships rather than through its node wrapper, since endpoint security such as SentinelOne kills a node process whose command line carries a long prompt, as every manager's does. `YAMNAYA_STATE_DIR`, `YAMNAYA_YM_SOCKET` and `YAMNAYA_NO_SERVE` reach Codex's shells, so `ym` run there acts on the same state and tmux server.

## Login profiles

ym keeps the existing default Claude and Codex login. Register additional native config roots with `ym profiles add claude personal --root /absolute/claude-personal` or `ym profiles add codex personal --root /absolute/codex-personal`. Labels are optional display names. `ym profiles login <harness> <id>` runs the native login inside that root; native provider storage keeps each profile’s credentials separate. On a host without a browser, use `ym remote <host> profiles login codex personal --device-auth` and complete the native device authorization in a browser. A host-local root is a path on that host.

Use `--profile <id>` with `ym new`, `ym goal`, and `ym chief`. The board cycles launch profiles with Shift-Tab. Unspecified launches inherit a usable parent profile or choose deterministically among profiles without a known exhausted pool. Profile and subscription identity are separate: aliases of the same provider principal/workspace share quota; missing identity or quota is shown as unknown.

`ym switch <session-or-launch> --profile <id>` resumes an idle or exited conversation using another login. `--profile auto` chooses a different verified pool. B on the board selects a subscription. Background failover waits for a structured subscription exhaustion plus a settled turn, records attempted pools, and resumes with an instruction to inspect prior tool effects before continuing. It does not replay the previous prompt. Exactly-once tool execution cannot be guaranteed by a native resume.

ym starts managed Codex sessions with `--no-daemon` so each conversation can stop and resume independently. If an external client or shared server cannot be quiesced, ym reports the blocker and keeps the source recoverable. Close that client before switching; unrelated sessions remain running.

An explicitly empty or relative default `CLAUDE_CONFIG_DIR` is unsupported because native Claude resolves its storage against the working directory. Choose a named absolute profile, or intentionally unset that variable or configure an absolute default. Absolute legacy root spelling and the default secure-storage override stay intact; other profiles and providers remain available.

`ym usage`, `ym limits`, and the board separate profile usage and subscription windows. Copied history keeps its original payer; only new destination responses and fresh limit observations belong to the destination. Account changes in an existing root create accounting epochs rather than relabeling older charges. Remote snapshots preserve host scope and deduplicate account-global quota observations; older snapshots report usage unknown.

## Development

    ym slot --lane yamnaya-test -- nice -n 19 go test -p 2 ./internal/<package>/...
    ym slot -- go vet ./...
    staticcheck ./...

`internal/core` holds goals, managers, chiefs, routing, the supervisor, events and verification, and imports no adapter. `internal/harness/claude` and `internal/harness/codex` read each CLI's own files; `internal/host/tmux` runs ym's tmux server; `internal/launch` creates and lands worktrees; `internal/scene` draws the mountains.

`tools/rehearse/rehearse.sh [--host H | --local] [--scenario S] [--ask TEXT] [--ref REF] [--timeout MIN] [--model ID]` runs a real goal end to end against a private proving-ground repository (`$YM_REHEARSAL_REPO`, else a new one per run) with its own state directory and tmux server, logging every change to `timeline.log`. Scenarios inject one fault: `clean`, `kill-manager`, `kill-worker`, `freeze-worker`, `break-ci`, `review-comment`, `kill-serve`. A rehearsal runs on a remote host over ssh so it puts no load on your Mac: `--host H`, else `$YM_REHEARSAL_HOST`, else the one host `ym remote` has reached. It runs on this machine only with `--local`. On the remote host the script runs from a yamnaya clone it keeps in the rehearsal root. Every run lives under `~/tmp/arbiter-work/yamnaya-rehearsal/<stamp>-<scenario>` on the host it runs on, and that root holds a `.ym-rehearsal` file: a ym whose state directory is outside a marked root never shows, records or adopts a session working under it, so your own ym ignores rehearsals while the rehearsal's ym runs them. Every Claude session a rehearsal launches (manager, chief, workers, verifier) runs on `claude-haiku-5-5`; `--model ID` picks another, such as an Opus id, for a run that needs one. Codex sessions run on `gpt-6-luna`. The run's ym reads these from `YAMNAYA_CLAUDE_MODEL` and `YAMNAYA_CODEX_MODEL`, which pass `--model` to every session it starts or resumes (a session record's own model wins on resume); unset, as in every real goal, sessions keep the harness default. Codex runs with `CODEX_HOME` in the run directory, holding copies of your Codex login and config, so its threads stay out of `~/.codex`. Claude runs on your own `~/.claude`, since a separate config directory would leave it logged out.

Any other drill or test that starts claude or codex sets `CLAUDE_CONFIG_DIR` and `CODEX_HOME` to a temporary directory along with `YAMNAYA_STATE_DIR`, so its conversations stay out of `~/.claude` and `~/.codex`, where your ym would find and adopt them. Go tests get both from `agenttest.Run` in their package's `TestMain`. ym never adopts a session working in your home directory.
