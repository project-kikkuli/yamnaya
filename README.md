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

- **Goal**: one ask, in your words. States: moving, quiet, adrift (its manager or an agent is busy going nowhere), down (manager died, being relaunched), verifying, stuck, done.
- **Manager**: an interactive Claude session, or Codex with `ym goal --codex` (`tab` on the board), with a persistent identity. If its session dies, ym relaunches it with its prompt, its brief and every event it missed. It starts agents with `ym new`, which links them under it.
- **Agents**: ordinary Claude or Codex sessions. They ask their manager with `ym item add`, never you. When one goes idle, its last message reaches the manager once.
- **Chiefs**: a chief groups managers and other chiefs. It reads their briefs, steers them and hears their state changes. Work never waits on a chief. On the board, `C` starts one: check its reports in a list of every goal and chief, then type its name and remit. `c` on a goal or chief picks the chief it reports to from a list, and `D` detaches it so it reports to you. From a shell, `ym chief "<name>" --over <id>,<id>` does the same as `C`.
- **Steering**: `S` on a goal or chief, or `ym goal steer <id>` (opens `$EDITOR`; `--file F` or piped stdin also work), saves a new plan as the next numbered revision. The manager is woken with it and runs `ym goal adopt <id> --revision N`; until then the row shows `steer rN not adopted`. ym nudges it every 10 minutes, twice, then tells whoever steered (a queue row for you, a message for a chief). A chief steers only what is below it. Steering a verifying goal calls verification off; the next verifier judges against the new plan. `ym goal context <id>` prints the revisions.
- **Briefs**: managers and chiefs record where they stand with `ym goal brief`: assessment, what changed, uncertainty, next, waiting on. Each call is a new version. `ym serve` serves them at `http://127.0.0.1:7477/b/<id>` (`/` lists all; `--briefs` or `YAMNAYA_BRIEFS_ADDR` moves it; a ym with another `YAMNAYA_STATE_DIR` serves on a port from 7478 to 8477 derived from that directory; a request whose host is a name other than `localhost` gets 403), and a chief's page rolls up its reports. A brief older than the goal's latest meaningful event (agent done, PR, CI, review, merge, stuck, failed verification, adopted steer) is stale: the wake carrying the event asks for a fresh one, and a manager still behind 5 minutes later is nudged once, never mid-turn.
- **Authority**: a manager decides everything about its goal. It merges its own pull requests only in repositories cleared by `ym goal authority`; elsewhere a green pull request lands in your merge queue. It writes to other people only with a model-name prefix.

The loop is code in `ym serve`, not instructions in a prompt:

1. **Notice.** ym records goal events without a model: agent finished, asked, stopped, hung or adrift, PR opened, CI red or green, a merge conflict with the base, review thread or decision, merged or closed, stuck, verifying, verified, failed verification.
2. **Act.** Once per cycle the manager gets everything new in one message. A manager that ends a turn with nothing in flight (no busy agent, no PR awaiting CI, no wake set, no ask waiting) is nudged at once. Managers set their own timers with `ym goal wake 30m "<why>"`.
3. **Verify.** `ym goal done` moves the goal to verifying. ym checks the facts first, for every PR the manager claims or the goal's agents opened (exited agents' included): merged, CI green on the merge commit, no unresolved review threads. Checks still running are rechecked every minute for up to 2 hours, then the goal shows stuck. Then a fresh session on the manager's harness, not the worker, gets your original ask and the evidence and records a verdict; ym takes it only from that session's own process, so no manager passes its own goal. A fail puts the goal back to active and hands the findings to the manager in its next wake. A verifier that dies, or runs 30 minutes without a verdict, is replaced; after 3 in a row the goal shows stuck. `ym goal show <goal>` prints every attempt with its checks and findings.
4. **Report.** Only a pass reaches you as done. Managers post progress lines with `ym goal update "<line>"`; they show in the goal's feed.

Nothing goes dark. A manager with no turns, brief edits or agent activity for 30 minutes is nudged; after two nudges the goal shows stuck in your queue with evidence. A session busy for 15 minutes with no tool call or reply while the machine is awake counts as hung: a hung manager is relaunched, and a hung or exited agent becomes a goal event, so its manager hears it once in its next wake with the `ym revive` that resumes it, and its chiefs and the goal's feed see it too.

Busy is not moving. ym reads each agent's transcript for signs it is going nowhere: the same failure in 5 of its last 20 tool results over 3 minutes or more with no file changed between (pending CI and empty searches do not count), its context compacted 3 times in a row within 15 minutes each with under 10 tool calls between, or 3 or more files written in a repository other than its own. That agent's row reads `adrift`, its goal stands adrift, and each episode becomes one goal event with the evidence for the manager and its chiefs. It reaches you only if the manager escalates. `h` lists every agent adrift now; `ym health drift` replays this machine's transcripts for past episodes.

## What reaches you

The **Needs you** queue holds only:

- goals that are done (passed verification) or stuck
- asks that climbed past every manager and chief
- green pull requests in repositories where managers may not merge
- GitHub review requests addressed to you personally, not to your teams
- Notion discussions and reviews addressed to you that no goal claims
- permission prompts
- one row per session you started yourself that asks you something

It stays quiet because every ask arrives ready to decide:

- ym refuses a decision or default bound for you without a recommendation, and a decision without two options with their effects. Chores and go items need neither.
- ym attaches the goal, its manager, the work's pull requests with CI, recent agent activity and the goal's feed events you have not read, and `ym serve` refreshes them each cycle. The selected row sums it up in one `evidence:` line; `enter` shows what was tried, the evidence and the full context. Answering or dismissing one of the goal's asks, opening the goal or `ym goal feed` marks the news read.
- An ask that climbed to you past a stuck or retired manager or chief without a recommendation or options shows `unprepared`, with what is missing and who was told to fix it with `ym item prepare`. Asks from your own sessions are never marked unprepared.

Done and stuck also raise a desktop notification; nothing else does.

Each goal row shows its unread feed count and newest line. `enter` opens the goal with its feed and marks it read.

## The board

| Key | Does |
| --- | --- |
| `n` / `N` | new goal in the selected row's repository / pick the repository first |
| `+` | new session of your own, no manager |
| `enter` / `esc` | open or drill in / go back |
| `o` / `O` | go to the session's tmux pane / open it in a pane beside the board; `o` on an exited session, or a mission where nothing runs, resumes its latest launched session (on its host too) and goes in |
| `s` | send a message to the goal, chief or session; `space` selects several, `tab` widens a chief to its subtree |
| `a` `g` `l` `x` `w` `u` | answer, go ahead, later, dismiss, show in full, reopen an item |
| `B` / `W` / `V` | show the manager's brief / open its served brief page / open its registered artifact |
| `S` | steer a goal or chief: edit its plan in `$EDITOR`, saved as the next revision; on a mission, split it |
| `C` | new chief: check the goals and chiefs it oversees (selected rows start checked), then type its name and remit |
| `c` / `D` | pick the chief a goal or chief (and the selection) reports to, or you / detach it from its chief (asks y/n) |
| `R` | relaunch a manager or chief |
| `K` | drop a goal, retire a chief, or kill a session (asks y/n) |
| `L` / `F` | land a launched session (remove its worktree when clean and merged) / forget it |
| `T` | relaunch an idle session in the other harness |
| `G` | gc old worktrees and merged branches |
| `t` | goals as the chief tree, each chief's reports indented under it, or by repository |
| `I` `U` `E` `h` | every item, usage and limits, everything ym launched, agents adrift now and weekly health |
| `A` | hide or show the mountains |
| `tab` (while typing a new goal, chief or session) | switch it between claude and codex; each of `n`, `N`, `C` and `+` starts on the harness it used last |
| `/` `?` `q` | filter, all keys, back or quit |

Sessions you start yourself (`+`, or `ym new` from your own shell) have no manager. They are grouped by repository under **Sessions you started**, and an idle one whose last reply asks you shows as a `waiting:` row you can answer with `a`.

A repository is its `origin` remote, so every clone of it, under any directory name and on any host, and every worktree of those clones, shares one repository row and one mountain range, with each session's host shown on its own row. A clone without an `origin` is a repository of its own. `--repo NAME` matches a clone's directory name or its origin's name, and picks the most recently used clone when several clones of one repository match.

`ym new` names the worktree and branch from the prompt, skipping text the agent already reads in its instruction files (`CLAUDE.md`, `AGENTS.md` and what they include) or got in an earlier prompt from the same parent, and ends the name with a random tag so launches in different clones never share a branch. `--slug S` names it exactly. A pull request is a session's when it is on the session's branch, or on the branch it tracks on origin under the same name, so a rebased or amended branch keeps its pull request. A same-named branch of another clone, a fork's branch, or a branch tracking another name does not claim it.

The fleet line shows each Claude and Codex limit window and today's and this week's spend at API prices.

## Machine slots

Agents share one machine, so ym hands out a fixed number of slots for heavy commands: two cores and 4 GiB each, after leaving two cores and a quarter of memory (at least 4 GiB) to everything else. A 12-core, 24 GiB Mac has 4. `ym slot -- <command> [args...]` waits for a free slot, runs the command at niceness 10 (`--nice N` changes it) in its own process group, and gives the slot back once the command and anything it left running in that group have ended; an interrupt or termination passes to the whole group, and the command's exit status is ym's. Every agent ym starts is told to run test suites, builds and long jobs this way. The slots are the lock files in `~/.local/state/agent-slot` (`$XDG_STATE_HOME/agent-slot`) that the `agent-slot` script also takes, so both draw from one pool; a slot the script holds shows as `agent-slot (no details)`.

`ym slot --status [--json]` lists the capacity, the load average, each holder with its command, pid, session, how long and where, and each waiter. The board's header and `ym goal list` show the same with each holder's goal and session name, and `ym goal list` marks the agent holding or waiting for a slot. When the one-minute load average passes one and a half times the physical cores, the header turns red, names the load and the three sessions, or programs outside any session, using the most processor time.

Any process holding more than a quarter of the machine's memory gets a red header line (`memory: script pid 41 holds 26 GiB, 41% of RAM, for 51m, under goal2 w1`), on another host's board too, tagged with that host. When it runs under an agent, ym tells that agent's manager once, with how to end it. ym never stops it itself.

## Mountains

Each repository is a range and each goal a peak. Busy agents climb the west face, red when stopped on you; pull requests are camps on the east face at the gate they reached (open, CI green, reviews resolved, merged). A red flare rises from work that needs you. Clouds mean quiet, a storm a failing check or a stuck goal, an eruption a dead manager, a flag on the summit done. The sky follows your local clock. `A` hides them, `NO_COLOR` leaves them out, `YM_REDUCED_MOTION=1` holds them still. ym sends the mountains only as fast as your terminal takes them in, so they rise from the bottom row up when shown again and a key never waits behind them.

## Remote hosts

    ym remote install <host>
    ym remote <host>

`ym remote install` cross-compiles ym from the source embedded in your binary (needs `go` locally), copies it to `~/.local/bin/ym` on the Linux host and runs `ym install --statusline` there. Every host `ym remote` reaches is remembered: the board, `ym sessions` and `ym waiting` show its sessions and items labelled with the host. `ym item list --all` also lists that host's items answered or dismissed in the last week, each with its history as that host kept it, so you see what became of an answer you sent there. Another host's board is read from the snapshot that host's `ym serve` writes every cycle, so a read never makes a loaded host build a board. The header's `hosts` line says how each host stands: `no answer for 12m (last good 2:04PM)` with ssh's reason when it stops answering, `unreachable` when it never has, and `board 9m old` when it answers but its `ym serve` has fallen behind. None of these holds up the board. Every ssh ym runs to a host shares one connection, kept ten minutes after its last use (its socket is `/tmp/ym-<uid>-<hash>`), so board reads, relays and opening a session skip the ssh handshake. Opening, killing, sending, landing and answering run through ssh on that host. Detaching from a remote session you opened from the board (C-b d) brings you straight back to this host's board. `ym remote forget <host>` stops showing it.

Goals across hosts:

- A goal runs on the host where it starts, supervised by that host's own `ym serve`, which `ym board`, `ym goal`, `ym chief` and `ym new` start there. `ym remote install` also keeps it running across reboots (`ym service keep`, see Background service), so a remote goal moves with no board open anywhere.
- The board shows every host's goal tree as one, each remote manager or chief tagged with its host and standing as that host reads it. A remote goal's feed shows on the board, and its done or stuck notification appears on the host you sit at.
- A remote goal's brief shows on the board and in `ym goal list` with its version and whether it is behind, as that host's snapshot gives it. `B` shows it, and `W` opens its page at `http://127.0.0.1:7477/b/<host>/<id>`, read from that host within five seconds or else from the last snapshot ym read, with its age. Only the latest version comes from another host.
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
- Exited launches land by themselves once they have nothing to lose, after a day's grace when you killed them or a signal did. A launch whose pane went while its agent was running, without `ym kill`, never lands by itself; ym resumes it instead (see Restarts). A launch whose worktree and repository are both gone never lands by itself either; its row says `repo gone: conversation only, no code`.
- A launch counts as starting, not gone, for its first two minutes until its tmux session is up, so a fresh agent is never resumed under itself.
- `ym revive` (and `o` on an exited launched session) recreates the worktree and branch when the repository exists, from the branch, else the recorded commit, else the branch on origin, else the default branch. When the launch's own clone is gone it uses another clone of the same repository on this host. When no clone is left it makes a plain directory at the recorded path and resumes the conversation there, without a checkout, and says so.
- A session you run in a terminal, outside ym's server, gets a launch record while the board shows it, so once it ends `ym revive` resumes it on ym's server and it shows as dormant (`ended outside ym`) for a day.
- `ym gc` keeps a landed record past 14 days while its conversation's transcript is still there.
- `ym gc` on the default state directory sweeps the repositories of its launches, of sessions it has seen and every repository under `~/dev` with linked worktrees. On any other state directory (`YAMNAYA_STATE_DIR`) it removes only the worktrees and branches its own launches made, so a drill or test never touches your repositories.
- `ym gc` appends a line to `gc.log` in the state directory for every worktree, branch and launch record it removes: the RFC3339 time, what it removed, then `repo=`, `path=`, `branch=`, `head=` (the commit it pointed at) and `reason=`. The file only grows.
- A new launch never takes the worktree name of a launch ym still keeps a record of, landed or not, so starting another session with the same slug gets `<slug>-2` and the landed one stays revivable.
- A serve on a state directory other than the default (`YAMNAYA_STATE_DIR`) that sees the directory deleted ends the tmux server it launched agents on, unless that is ym's default server, and stops. Your default state directory and ym's default tmux server are never torn down this way.
- A Claude session ym launched that sits at the folder trust dialog in its first ten minutes is trusted again and restarted, since another session's config save can drop the trust ym recorded.

**Restarts.** `ym service keep` keeps `ym serve` running across logouts and reboots. On macOS it writes and loads a LaunchAgent (`~/Library/LaunchAgents/io.yamnaya.ym-serve.plist` for the default state directory, `io.yamnaya.ym-serve-<hash>.plist` for any other) that starts it at login and again whenever it exits, in the login shell's environment, logging to `work-service.log` in the state directory. Each line `ym serve` writes there starts with its RFC3339 time; past 1 MiB the log moves to `work-service.log.1`, replacing the previous one, so the two never grow past about 2 MiB. On Linux it writes a systemd user timer (`ym-serve.timer`, or a name of its own for another state directory). The board and `ym install` set it up for the default state directory, so you never run it. `ym service unkeep` removes it; `ym service status` shows `kept` and `kept_pid` when loaded.

After a restart:

- A manager or chief resumes in its own session and conversation, told it stopped and what it missed. If its transcript is gone, it starts over from its prompt and brief, under the same id on Claude and under the new thread Codex records on Codex. `ym goal relaunch` still starts a fresh session.
- Every session ym launched whose agent was running when its pane went, without `ym kill` or `ym land`, resumes in its own conversation instead of landing, whether or not it works for a goal. A goal's manager gets a `resumed` or `resume-failed` event, and `resume-failed` shows in your feed. `ym kill` keeps an agent stopped. A session whose transcript holds no turn starts over on its first prompt.
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
| | `ym goal ack <goal>` | clear a done or stuck goal from your queue |
| | `ym goal authority [<repo> --merge\|--no-merge]` | where managers may merge |
| | `ym chief [--codex] [--over ID,ID] [--remit T] "<name>"` | start a chief, on codex with `--codex`; `attach <child> <chief>`, `detach <child>` |
| Manager | `ym goal update "<line>"` | post where the goal stands |
| | `ym goal wake <when> <why>` | wake me at `30m`, `2h`, `15:04` |
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
| Sessions | `ym new [--codex] [--repo P] [--slug S] "<prompt>"` | start an agent in a new worktree on ym's tmux server |
| | `ym open <session\|item>` / `ym send <session> <text>` | go to a session / send it a follow-up |
| | `ym adopt <session>` | move a session from another terminal onto ym's server |
| | `ym kill <session> [--yes]` | SIGTERM its agent; worktree and transcript stay |
| | `ym launched [--landed]` / `ym revive <key>` | what ym started / resume an exited one |
| | `ym slot [--nice N] -- <command>` / `ym slot --status [--json]` | run a heavy command in a shared machine slot / who holds and waits for them |
| | `ym land <key>` / `ym land --all` | remove worktrees that lose nothing |
| | `ym gc [--dry-run]` | remove idle clean worktrees and merged branches |
| | `ym switch <session> --to claude\|codex [--relaunch]` | port a session to the other harness |
| Usage | `ym usage [--top N]` / `ym limits` / `ym health` | spend, limit windows, weekly item health |
| Drift | `ym health drift [--since D] [file...]` | replay this machine's transcripts and list each time an agent was busy going nowhere |
| Hosts | `ym remote install <host>` / `ym remote <host> [args]` / `ym remote forget <host>` | see Remote hosts |
| | `ym relay [drop <seq>]` | what this host still has to carry, or stop carrying a refused one |
| Setup | `ym install [--statusline]` / `ym update` | link the skill / install the latest ym; a running board restarts into it |
| | `ym service status\|restart\|keep\|unkeep` / `ym docs [connect <email>]` | background service / Notion |

Commands that act for an agent find its session by walking up the process tree to the nearest `claude` or `codex` process; `--session` overrides. State lives in `~/.local/state/yamnaya/ym.db` (`YAMNAYA_STATE_DIR` overrides).

ym starts Codex as the platform binary its npm package ships rather than through its node wrapper, since endpoint security such as SentinelOne kills a node process whose command line carries a long prompt, as every manager's does. `YAMNAYA_STATE_DIR`, `YAMNAYA_YM_SOCKET` and `YAMNAYA_NO_SERVE` reach Codex's shells, so `ym` run there acts on the same state and tmux server.

## Development

    ym slot -- go test -p 2 ./internal/<package>/...
    ym slot -- go vet ./...
    staticcheck ./...

`internal/core` holds goals, managers, chiefs, routing, the supervisor, events and verification, and imports no adapter. `internal/harness/claude` and `internal/harness/codex` read each CLI's own files; `internal/host/tmux` runs ym's tmux server; `internal/launch` creates and lands worktrees; `internal/scene` draws the mountains.

`tools/rehearse/rehearse.sh [--host H | --local] [--scenario S] [--ask TEXT] [--ref REF] [--timeout MIN]` runs a real goal end to end against a private proving-ground repository (`$YM_REHEARSAL_REPO`, else a new one per run) with its own state directory and tmux server, logging every change to `timeline.log`. Scenarios inject one fault: `clean`, `kill-manager`, `kill-worker`, `freeze-worker`, `break-ci`, `review-comment`, `kill-serve`. A rehearsal runs on a remote host over ssh so it puts no load on your Mac: `--host H`, else `$YM_REHEARSAL_HOST`, else the one host `ym remote` has reached. It runs on this machine only with `--local`. On the remote host the script runs from a yamnaya clone it keeps in the rehearsal root. Every run lives under `~/tmp/arbiter-work/yamnaya-rehearsal/<stamp>-<scenario>` on the host it runs on, and that root holds a `.ym-rehearsal` file: a ym whose state directory is outside a marked root never shows, records or adopts a session working under it, so your own ym ignores rehearsals while the rehearsal's ym runs them. Codex runs with `CODEX_HOME` in the run directory, holding copies of your Codex login and config, so its threads stay out of `~/.codex`. Claude runs on your own `~/.claude`, since a separate config directory would leave it logged out.

Any other drill or test that starts claude or codex sets `CLAUDE_CONFIG_DIR` and `CODEX_HOME` to a temporary directory along with `YAMNAYA_STATE_DIR`, so its conversations stay out of `~/.claude` and `~/.codex`, where your ym would find and adopt them. Go tests get both from `agenttest.Run` in their package's `TestMain`. ym never adopts a session working in your home directory.
