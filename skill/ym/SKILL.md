---
name: ym
description: Use in a Yamnaya tmux window (YM_PROJECT is set) to file open questions as tracked items, route them through a manager, and run phases, workers, and landing with the ym CLI.
---

# ym

`YM_PROJECT` and `YM_WINDOW` name your project and window; every `ym` command below uses them. Claude and Codex windows use the same commands.

## Open questions are items, never chat blocks

Never print an open-questions block in chat. File each question:

```
ym item add --kind decision|default|chore --question "<one line>" \
  --rec "<what you recommend>" \
  --option "<label>=<effect>" --option "<label>=<effect>" \
  --assumption "<what you are assuming>"
```

- `decision`: you need a choice before continuing.
- `default`: you will proceed with `--rec` unless the human vetoes it.
- `chore`: an action only a human can take (login, approval, payment).

Always give `--rec`, options with their effects, and `--assumption`. The command prints an id; in chat say only that id, e.g. "filed D4".

## Workers

Route items through your manager: send the question to the manager (SendMessage) and let it file. File directly only when you have no manager or you are a side thread the human started.

## Managers

At the start of every turn run `ym item list --all --json` and act on items answered or dismissed since your last turn. A dismissed item is never asked again.

- Spawn workers: `ym spawn <name> --harness claude|codex --prompt "<task>"`.
- Own the phases: `ym phase set a,b,c`, `ym phase next`, `ym phase show`.
- When the project is done, land it: `ym land`.
