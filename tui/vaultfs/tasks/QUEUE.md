# Task Queue

A filesystem work queue shared by every agent on this host. No daemon, no
database — the queue *is* the directory layout, and claiming is an atomic
`mkdir`, so two agents can never hold the same task.

**Read this before touching `tasks/`. Never work a task you have not claimed.**

## Layout

```
tasks/
├── open/       <id>.md         waiting for someone to claim
├── claimed/    <id>/task.md    in progress; <id>/claim.json says by whom
├── done/       <id>/           completed, with result.md
└── failed/     <id>/           gave up, with result.md explaining why
```

## Task file

`tasks/open/<id>.md`:

```yaml
---
id: 2026-08-02-1432-a91c
title: Summarize the Olla balancer design notes into the wiki
created: 2026-08-02T14:32:11Z
by: human
for: any                  # any | <agent-id> | capability:<name>
requires:                 # skills the worker must have; see skills/REGISTRY.md
  - vault-wiki
priority: normal          # low | normal | high
---

## Goal

What done looks like. Be specific enough that another agent can judge it.

## Context

Links, file paths, `[[wiki-pages]]`, prior attempts.

## Acceptance

- [ ] A checkable list.
- [ ] Each item verifiable without asking the requester.
```

## Routing — "is this task for me?"

Check `for:` against your identity in `agents/<your-id>.md`:

| `for:` value | Claim it if |
| --- | --- |
| `any` | Always eligible. |
| `<agent-id>` | It matches your id exactly. |
| `capability:<name>` | `<name>` is in your card's `capabilities:` list. |

Then check `requires:` — every listed skill must be in `skills/REGISTRY.md`
with a status other than `broken`. If a required skill is missing, **do not
claim the task**. Either author the skill first (`AGENTS.md` §6) or leave it
for an agent that has it.

`aidt-task list --me` applies all of this for you and prints only the tasks you
are actually eligible for. Prefer it over eyeballing the directory.

## Protocol

```bash
aidt-task list                 # every open task
aidt-task list --me            # only ones you are eligible for
aidt-task show <id>            # full task file
aidt-task claim <id>           # atomic; fails loudly if someone beat you
aidt-task done <id> "summary"  # move to done/ with a result
aidt-task fail <id> "reason"   # move to failed/ with a reason
aidt-task release <id>         # put it back in open/ — do this if you abandon it
aidt-task new --title "..." [--for any] [--requires a,b] [--priority high]
```

Claiming is a `mkdir` on `claimed/<id>`, which is atomic on every POSIX
filesystem: exactly one caller creates the directory, everyone else gets
`EEXIST` and a non-zero exit. There is no lock file to leak and no window where
two agents both believe they won.

## Rules

1. **Claim first.** An unclaimed task is fair game for anyone; work on it and
   you will collide.
2. **One task at a time**, unless they are plainly independent.
3. **Release what you abandon.** `aidt-task release <id>`. A task stuck in
   `claimed/` with a dead owner blocks the queue.
4. **Finish honestly.** `fail` with a real reason beats `done` with a summary
   that overstates what happened. The next agent reads your result.
5. **Knowledge goes in the wiki, not the result.** `result.md` records what you
   did for this task. Anything reusable belongs in `wiki/` (`AGENTS.md` §4).
6. **Stale claims:** if `claim.json` is older than a few hours and the host
   shows no sign of that agent working, release it and say so in the result.
