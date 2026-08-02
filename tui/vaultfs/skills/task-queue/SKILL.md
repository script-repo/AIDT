---
name: task-queue
summary: Claim, work, and complete tasks from the shared filesystem queue without colliding with other agents.
status: stable
owner: aidt
requires: []
tags:
  - coordination
  - core
created: 2026-08-02
updated: 2026-08-02
---

# task-queue

## What it does

Wraps `tasks/` — a lock-free work queue shared by every agent on this host.
Claiming is an atomic `mkdir`, so exactly one agent can own a task. Covers
listing, eligibility filtering, claiming, completing, failing, and releasing.

Full protocol and task-file schema: `tasks/QUEUE.md`.

## When to use it

- You are idle and want work → `aidt-task list --me`.
- You want to hand work to another agent → `aidt-task new`.
- You are about to start on something someone else might also start.

## Prerequisites

```bash
command -v aidt-task >/dev/null && echo ok
```

If missing, `$AIDT_AGENT_VAULT/bin` is not on your PATH:

```bash
export AIDT_AGENT_VAULT="$HOME/Obsidian/AIDT-Agent-Vault"
export PATH="$AIDT_AGENT_VAULT/bin:$PATH"
```

You must also be registered — `aidt-task list --me` needs `agents/<your-id>.md`
to resolve `for:` and `requires:`. AIDT registers you at deploy time; check with
`aidt-agent whoami`.

## Usage

```bash
aidt-task list --me
aidt-task show 2026-08-02-1432-a91c
aidt-task claim 2026-08-02-1432-a91c
# ... do the work ...
aidt-task done 2026-08-02-1432-a91c "Ingested the notes; added [[olla-balancer]]."
```

Expected output of a successful claim:

```
claimed 2026-08-02-1432-a91c -> tasks/claimed/2026-08-02-1432-a91c
```

A lost race exits non-zero and prints:

```
aidt-task: 2026-08-02-1432-a91c is already claimed by goose
```

That is normal and not an error on your part. Pick another task.

Creating work for someone else:

```bash
aidt-task new --title "Lint the wiki and file gaps" \
              --for capability:knowledge \
              --requires vault-wiki \
              --priority high
```

## Failure modes

| Symptom | Cause | Fix |
| --- | --- | --- |
| `already claimed by <agent>` | Another agent won the race | Expected. Choose a different task. |
| `list --me` shows nothing but `list` shows tasks | Your card lacks the capability, or a `requires:` skill is missing/`broken` | Check `aidt-agent whoami`; author the missing skill (`AGENTS.md` §6) |
| Task stuck in `claimed/` for hours | Owner crashed or was killed | `aidt-task release <id>`, note it in the result |
| `aidt-task: not registered` | Deploy-time registration did not run | `aidt-agent register --id <you> --name <You>` |
| Two agents produced conflicting work | Someone skipped the claim step | Claim first. Always. |

## Notes

The queue is per-host, like the rest of the vault. A task filed on the gateway
is invisible to agents on a worker.

`done`/`fail` move the whole claim directory, so the claim record travels with
the result and you can always see who did what.
