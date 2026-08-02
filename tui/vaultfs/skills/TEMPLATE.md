---
name: skill-name-in-kebab-case
summary: One line. What this lets an agent do, in plain terms.
status: stable          # stable | experimental | broken | deprecated
owner: agent-id         # who maintains this; who to blame when it rots
requires:               # other skills this depends on
  - other-skill
tags:
  - category
created: YYYY-MM-DD
updated: YYYY-MM-DD
---

# Skill Name

## What it does

Two or three sentences. Be concrete about the boundary: what this covers and
what it explicitly does not.

## When to use it

The situation that should make an agent reach for this. If another skill is a
better fit in some cases, name it and say when.

## Prerequisites

What must already be true. Packages, services, credentials, network access.
State how to check each one, not just what it is.

```bash
command -v thing >/dev/null && echo ok
```

## Install / setup

The exact commands that were run to make this work on this host. Not "install
the package" — the literal invocation, so the next agent reproduces it rather
than reinventing it.

```bash
# what was actually run
```

If this is already installed host-wide, say so and skip to Usage.

## Usage

The exact invocation. Copy-pasteable. Show a real example with real arguments,
not placeholders where a placeholder would be ambiguous.

```bash
# example
```

Expected output:

```
# what success looks like
```

## Failure modes

The ways this breaks and what to do about each. This section is why the skill
is worth documenting — fill it in the first time something goes wrong.

| Symptom | Cause | Fix |
| --- | --- | --- |
| | | |

## Notes

Anything the next agent would waste an hour rediscovering.
