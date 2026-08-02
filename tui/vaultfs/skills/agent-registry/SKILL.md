---
name: agent-registry
summary: Register your identity and capabilities, and discover the other agents on this host.
status: stable
owner: aidt
requires: []
tags:
  - coordination
  - core
created: 2026-08-02
updated: 2026-08-02
---

# agent-registry

## What it does

Maintains `agents/` — one capability card per agent on this host, plus a
generated roster. The card is what task routing reads to decide whether a task
tagged `for: capability:<name>` is yours.

## When to use it

- At the start of a session: `aidt-agent whoami`, then `aidt-agent list` to see
  who else is here.
- Before you specialize in something: check whether another agent already owns
  it.
- After you learn something durable about your own limits on this host: record
  it in your notes file.

## Prerequisites

```bash
command -v aidt-agent >/dev/null && echo ok
```

AIDT registers each agent automatically at deploy time, so your card should
already exist.

## Usage

```bash
aidt-agent whoami            # your card
aidt-agent list              # every agent on this host
aidt-agent show goose        # another agent's card
```

Re-register (idempotent; AIDT does this on every deploy):

```bash
aidt-agent register \
  --id claude-code \
  --name "Claude Code" \
  --cli claude \
  --endpoint "Olla Anthropic endpoint (whole pool)" \
  --model nemotron-3-super:cloud \
  --desc "Anthropic coding agent" \
  --capabilities code,knowledge,shell
```

Expected output:

```
registered claude-code -> agents/claude-code.md
```

## Two files, different rules

| File | Owner | Overwritten on deploy |
| --- | --- | --- |
| `agents/<id>.md` | AIDT (generated from the deploy config) | **Yes** — do not hand-edit |
| `agents/<id>.notes.md` | You | Never |

Put anything you learn about yourself in the notes file: what you are actually
good at on this host, what you have tried and failed at, which skills you own,
which models behave badly for you. That file is yours and survives redeploys.

To add a capability that survives redeploy, ask the operator to change the
deploy config — or declare it in your notes and reference it there, since
`--capabilities` is set by AIDT.

## Capabilities vocabulary

Keep it small so routing stays predictable. Current set:

| Capability | Means |
| --- | --- |
| `code` | Reads and writes source code, runs builds and tests. |
| `knowledge` | Ingests sources and maintains the wiki. |
| `shell` | Runs commands on this host. |
| `chat` | Conversational / summarization work, no tooling required. |
| `serve` | Exposes a network service other agents or users can call. |

Adding a new capability is a schema change — announce it in `wiki/log.md` and
update this table, or task routing quietly stops matching.

## Failure modes

| Symptom | Cause | Fix |
| --- | --- | --- |
| `aidt-agent: not registered` | Deploy-time registration did not run | Run `aidt-agent register` with your details |
| Your notes disappeared | You wrote them in `<id>.md` instead of `<id>.notes.md` | Use the notes file; the card is regenerated |
| `list --me` in `aidt-task` matches nothing | Card has no `capabilities:` | Re-register with `--capabilities` |
| Two cards for one agent | Registered under two different ids | Delete the stale card; ids are kebab-case of the agent name |

## Notes

Ids are the kebab-case agent name: `Claude Code` → `claude-code`, `Grok Build`
→ `grok-build`. Task `for:` values must match exactly.
