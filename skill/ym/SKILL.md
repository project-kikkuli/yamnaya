---
name: ym
description: Use in any Claude Code or Codex session to file open questions for the owner as tracked items with `ym item add`, read the owner's answers, and record phases, vibes and spawned sessions. No setup or environment variables needed.
---

# ym

`ym` finds the session you run in by walking up from its own process to your claude or codex process. Every command below works from any session, in any terminal, with no setup.

## The bar: file rarely

File an item only when the answer changes what gets built, spends money, touches the owner's accounts or permissions, or picks between product directions the owner owns. Anything else you decide and do: implementation details, naming, library or tool choices, and anything with a conventional default. Do not file it, and do not ask about it in chat. If you could pick a default and move on, that is the answer.

Always the owner's, so file them: authorization to build, merging (except where his standing rules delegate it), PR split, fold or retarget, core-capability or parity gaps, deploys outside what he has delegated, and messages to other people on his behalf. Never decide these silently.

`default` is for real product defaults only, never for an engineering choice you could just make. `chore` is only for actions that need the owner's own hands. Before filing one, try the action yourself; file it only if it truly needs the owner's hands (logins, payments, his approvals, anything your safety rules forbid you to do).

## Waiting on the go-ahead is an item

Whenever you wait on the owner's authorization to build ("PLEASE GO IMPLEMENT THIS"), file a `go` item instead of asking in chat:

```
ym item add --kind go --question "<one line naming what you will build>" \
  --rec "<the approach you will take>" --blocks <n>
```

In chat say only its id, e.g. "filed G4", and stop. The owner's go-ahead arrives as `re G4: PLEASE GO IMPLEMENT THIS`; that is the authorization, so start building. A dismissal means do not build.

## Open questions are items, never chat blocks

Never print an open-questions block in chat. File each question that clears the bar:

```
ym item add --kind decision|default|chore|go --question "<one line>" \
  --rec "<what you recommend>" \
  --option "<label>=<effect>" --option "<label>=<effect>" \
  --assumption "<what you assume meanwhile>" --blocks <n>
```

- `decision`: you need a choice before continuing.
- `default`: you will proceed with `--rec` unless the owner vetoes it.
- `chore`: an action only a human can take (login, approval, payment).
- `go`: you wait on the owner's go-ahead to build; see above.
- `--blocks`: how many pieces of your work wait on the answer.

Decision and default items always get `--rec`, options with their effects, and `--assumption`; go items take `--question`, `--rec` and `--blocks` only. The command prints an id; in chat say only that id, e.g. "filed D4". Subagents and teammates file through their lead, or directly; either way the item lands on the lead's session.

## Answers

- An answer can arrive as your next prompt, as `re D4: <answer>` (a Claude session gets it as a cross-session message from ym, after its current turn). Act on it.
- At the start of every turn, run `ym item inbox`. It prints `re <id>: <answer>` or `re <id>: dismissed: <reason>` for each answer you have not received, then marks them received. Act on each. A dismissed item is never asked again.
- `ym item inbox` also prints `msg: <text>` for each follow-up the owner sent you with `ym send`. It is the owner speaking, exactly as if typed into your prompt: act on it in this turn, before or alongside your current work.
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
