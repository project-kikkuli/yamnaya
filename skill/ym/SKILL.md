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

## Open questions are items, never chat blocks

Never print an open-questions block in chat. File each question that clears the bar:

```
ym item add --kind decision|default|chore --question "<one line>" \
  --rec "<what you recommend>" \
  --option "<label>=<effect>" --option "<label>=<effect>" \
  --assumption "<what you assume meanwhile>" --blocks <n>
```

- `decision`: you need a choice before continuing.
- `default`: you will proceed with `--rec` unless the owner vetoes it.
- `chore`: an action only a human can take (login, approval, payment).
- `--blocks`: how many pieces of your work wait on the answer.

Always give `--rec`, options with their effects, and `--assumption`. The command prints an id; in chat say only that id, e.g. "filed D4". Subagents and teammates file through their lead, or directly; either way the item lands on the lead's session.

## Answers

- When you sit idle, an answer arrives typed into your prompt as `re D4: <answer>`. Act on it.
- At the start of every turn, run `ym item inbox`. It prints `re <id>: <answer>` or `re <id>: dismissed: <reason>` for each answer you have not received, then marks them received. Act on each. A dismissed item is never asked again.
- `ym item inbox` also prints `msg: <text>` for each follow-up the owner sent you with `ym send`. It is the owner speaking, exactly as if typed into your prompt: act on it in this turn, before or alongside your current work.
- `ym item list` shows your project's open items; `--all` adds answered, dismissed and snoozed ones.

## Projects and phases

- Sessions you start from your own shell join your project automatically. For a session started elsewhere, record it: `ym spawn <child-session-id>`.
- Own the phases: `ym phase set a,b,c`, `ym phase next`, `ym phase show`.

## Vibe

Say how your mission is going as a place: `ym vibe "<place>" --why "<one line>"`. North Dakota is rough, Colorado is fine, Hawaii is great. Any place works; go to the extremes when they fit: Taliban-occupied Afghanistan for truly broken, US-Annexed Iceland for exceptional. Set it when a phase changes, when something breaks or lands, and in your final report. Be honest; the board shows it next to the facts. `ym vibe` shows your mission's current vibe and history.
