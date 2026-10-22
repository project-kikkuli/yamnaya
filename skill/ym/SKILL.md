---
name: ym
description: Use in any Claude Code or Codex session to file open questions as tracked items with `ym item add` (they reach your manager, or the owner when you have none), read answers, run a goal as its manager, and record phases, vibes and spawned sessions. No setup or environment variables needed.
---

# ym

`ym` finds the session you run in by walking up from its own process to your claude or codex process. Every command below works from any session, in any terminal, with no setup.

## The bar: file rarely

File an item only when the answer changes what gets built, spends money, touches the owner's accounts or permissions, or picks between product directions the owner owns. Anything else you decide and do: implementation details, naming, library or tool choices, and anything with a conventional default. Do not file it, and do not ask about it in chat. If you could pick a default and move on, that is the answer.

Never yours to decide silently, so file them: authorization to build, merging (except where standing rules delegate it), PR split, fold or retarget, core-capability or parity gaps, deploys outside what was delegated, and messages to other people on the owner's behalf. Under a manager these reach the manager, who owns them for its goal.

`default` is for real product defaults only, never for an engineering choice you could just make. `chore` is only for actions that need the owner's own hands. Before filing one, try the action yourself; file it only if it truly needs the owner's hands (logins, payments, the owner's approvals, anything your safety rules forbid you to do).

## Waiting on the go-ahead is an item

Whenever you wait on authorization to build ("PLEASE GO IMPLEMENT THIS"), file a `go` item instead of asking in chat:

```
ym item add --kind go --question "<one line naming what you will build>" \
  --rec "<the approach you will take>" --blocks <n>
```

In chat say only its id, e.g. "filed G4", and stop. The go-ahead, from your manager or the owner, arrives as `re G4: PLEASE GO IMPLEMENT THIS`; that is the authorization, so start building. A dismissal means do not build.

## Open questions are items, never chat blocks

Never print an open-questions block in chat. File each question that clears the bar:

```
ym item add --kind decision|default|chore|go --question "<one line>" \
  --rec "<what you recommend>" \
  --option "<label>=<effect>" --option "<label>=<effect>" \
  --evidence "<PR, CI run, commit or file>" --tried "<what you tried>" \
  --assumption "<what you assume meanwhile>" --blocks <n>
```

- `decision`: you need a choice before continuing.
- `default`: you will proceed with `--rec` unless the owner vetoes it.
- `chore`: an action only a human can take (login, approval, payment).
- `go`: you wait on the owner's go-ahead to build; see above.
- `--blocks`: how many pieces of your work wait on the answer.

Decision and default items always get `--rec`, options with their effects, and `--assumption`; go items take `--question`, `--rec` and `--blocks` only. Cite what the call rests on with one `--evidence` per pull request, CI run, commit or file, and say what you already tried with `--tried`.

ym attaches the rest itself: the goal and manager the ask comes from, the pull requests of that work with their CI state, its agents' latest activity, and what changed in the goal since the owner last looked. A decision or default that would reach the owner without `--rec`, or a decision without two `--option`s that each state an effect, is refused with the reason; refile it ready to decide, or make the call yourself if it is within your authority. The command prints an id; in chat say only that id, e.g. "filed D4". Subagents and teammates file through their lead, or directly; either way the item lands on the lead's session.

## Who your questions reach

`ym item add` routes the question for you; you file it the same way either way:

- You are an agent a manager started (with `ym new`, or under an agent it started): the item goes to that manager's session as a message, never to the owner. The manager answers it, and the answer reaches you as `re <id>: ...` like any answer.
- You are a manager or chief: the item goes to your chief, or to the owner when you report to no chief. A chief that is stuck or retired is passed over. What reaches the owner must be ready to decide; see above.
- No manager is above you: the item goes to the owner's queue, folded into one row for your session.

`ym item add` prints the id on stdout and, when a manager or chief owns the answer, says so on stderr.

## Running a goal as its manager

The owner starts a goal with `ym goal "<ask>"` (or `n` on the board), and ym starts a manager session for it with a prompt that carries the goal, its authority and its brief path. The manager owns the goal outright: go-aheads, decisions and fixes are its calls, never the owner's sign-off.

- Start agents with `ym new --repo <repo> "<prompt>"`; they report to you.
- Answer what they ask with `ym item answer '<#key>' <answer>`, `ym item go '<#key>'` or `ym item dismiss '<#key>' <reason>`. `ym item list --to-me` lists what waits on you.
- Keep the brief at the path in your prompt current: where the goal stands, what is in flight, decisions and why, what is next. Register a richer page with `ym goal artifact <url>`.
- ym wakes you with one `ym:` message per cycle listing what happened on your goal: agents finishing, stopping or looking hung, asks, pull requests opened, CI red or green, review comments, merges. Do not poll; end your turn when nothing needs you. To be woken for something ym cannot see, run `ym goal wake <when> <why>` (`30m`, `2h`, `15:04` or RFC 3339). Ending a turn with nothing in flight gets you nudged at once.
- When the goal moves, tell the owner in one line with `ym goal update "<line>"`. It lands in the owner's feed and with your chiefs, who also get every state change of the goals below them.
- Merge your own pull requests only in repositories `ym goal authority` lists as "may merge". Elsewhere a green pull request goes to the owner's merge queue by itself.
- Prefix anything you write to a human with your model name, like "Claude: ...". Never speak as the owner.
- When the goal is done, update the brief, then `ym goal done --summary "<one line>" --evidence "<proof>"`, one `--evidence` per pull request URL, commit or result to check. The goal moves to verifying: ym checks that its pull requests merged with green CI and no open threads, then a fresh verifier session checks the work against the owner's ask. A pass makes it done for the owner; a fail reopens it and the findings arrive in your next wake as a failed-verify event, so fix them and run `ym goal done` again.
- If your prompt says you are a goal's verifier, check read-only and finish with `ym goal verdict <goal> --pass --finding "<what you checked>"` or `--fail --finding "<concrete gap>"`, one `--finding` per line.

ym supervises managers: a dead one is relaunched with its prompt and brief, a quiet one is nudged, and one still stuck after its nudges shows the owner one stuck row. `ym goal list [--under <chief>]` shows the tree; `ym goal send <id>[,<id>] <text>` steers managers (`--subtree` reaches everyone below a chief); `ym goal relaunch <id>` replaces a manager's session.

A chief (`ym chief "<name>" --over <id>,<id>`) oversees managers and other chiefs: it reads their briefs, steers them, relaunches stuck ones and keeps a rolled-up brief. Work never waits on a chief.

A Notion discussion or review request addressed to the owner about your goal's work (its page or a pull request it names is in your goal, brief or sessions) reaches you as a `ym:` message with the thread instead of the owner. Act on it in the document itself, prefixing anything you post with your model name; it leaves your queue when the discussion is resolved or answered there. Escalate with `ym item add` only if it is beyond your authority.

## Actions in the conversation

When the owner must act in the native chat, file `ym item add --kind chore --action chat --question "<specific action>" --rec "<necessary context>"`. The board labels it action in chat and opens the originating session with `o` or `O`. Opening it is not an approval or completion. Existing owner answers in the ledger are authoritative even when an agent repeats the question in prose.

## Answers

- An answer can arrive as your next prompt, as `re D4: <answer>` (a Claude session gets it as a cross-session message from ym, after its current turn). Act on it.
- At the start of every turn, run `ym item inbox`. It prints `re <id>: <answer>` or `re <id>: dismissed: <reason>` for each answer you have not received, then marks them received. Act on each. A dismissed item is never asked again.
- `ym item inbox` also prints `msg: <text>` for each follow-up sent you with `ym send` or `ym goal send`. From the owner it is the owner speaking, exactly as if typed into your prompt; from your manager or chief it is their direction. Act on it in this turn, before or alongside your current work.
- `ym item inbox` prints `vibe: none yet, ...` or `vibe: <age> old, ...` while your mission has no fresh vibe. Set one with `ym vibe` in this turn; the line repeats every call until you do.
- `ym item list` shows your project's open items; `--all` adds answered, dismissed and snoozed ones.

## Projects and phases

- Sessions you start from your own shell join your project automatically. For a session started elsewhere, record it: `ym spawn <child-session-id>`.
- Own the phases: `ym phase set a,b,c`, `ym phase next`, `ym phase show`.

## Vibe

Say how your mission is going as a place: `ym vibe "<place>" --why "<one line>"`. Pick a real place whose character fits this exact moment; be creative and don't reuse the places below.

- Broken: Taliban-occupied Afghanistan, Chernobyl, Pyongyang
- Rough: North Dakota, Oklahoma, Newark, the Mojave
- Middling: Delaware, Nebraska, Wisconsin, Ohio
- Good: Vermont, Montana, Lisbon, Kyoto
- Great: Hawaii, US-Annexed Iceland, Patagonia, the Amalfi Coast

The place is just a place; status goes in `--why`. Set it when a phase changes, when something breaks or lands, and in your final report. Be honest; the board shows it next to the facts. `ym vibe` shows your mission's current vibe and history.
