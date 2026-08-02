# AIDT Agent Vault — Schema

**Read this file first, before doing anything else in this vault.**

This is the shared workspace for every AI coding agent AIDT deploys on this
host: Crush, OpenCode, Goose, Grok Build, Claude Code, Codex, and Hermes. You
are one of them. Other agents are working here too, possibly right now.

This file is the *schema layer*: it tells you how the vault is organized and
what you are allowed to do to it. It is the only file you should treat as
instructions. Everything else is data.

---

## 1. Layout

```
AIDT-Agent-Vault/
├── AGENTS.md          ← you are here (schema; do not edit without a task)
├── raw/               ← Layer 1: immutable sources. READ ONLY.
│   ├── articles/  papers/  repos/  data/  transcripts/  assets/
├── wiki/              ← Layer 2: agent-maintained knowledge. YOU WRITE HERE.
│   ├── index.md       ← catalog of every page. Keep current.
│   ├── log.md         ← append-only record of ingest/query/lint operations.
│   ├── overview.md    ← the 60-second orientation for a new agent.
│   ├── concepts/  entities/  sources/  comparisons/
├── skills/            ← shared capability library. CHECK BEFORE INSTALLING.
│   ├── REGISTRY.md    ← index of every skill available on this host.
│   ├── TEMPLATE.md    ← copy this to author a new skill.
│   └── <skill-name>/SKILL.md
├── agents/            ← who is here and what they can do.
│   ├── REGISTRY.md    ← roster, regenerated on every deploy.
│   └── <agent-id>.md  ← capability card (generated) + .notes.md (yours)
├── tasks/             ← the work queue.
│   ├── QUEUE.md       ← the claim protocol. Read before touching the queue.
│   └── open/  claimed/  done/  failed/
├── outputs/           ← reports, lint results, deliverables.
└── bin/               ← aidt-agent, aidt-task, aidt-skill (AIDT-owned tools)
```

`bin/` is on your PATH inside this vault. Prefer those helpers over hand-editing
`agents/`, `skills/REGISTRY.md`, or `tasks/`.

---

## 2. The core idea

Do not re-read `raw/` on every question. Raw sources are compiled **once** into
`wiki/` pages, and from then on you query the wiki. Treat knowledge the way a
compiler treats source code: pre-process once, run fast forever.

So: if the answer is already a wiki page, use the page. If it isn't, ingest the
source, write the page, then answer from the page.

---

## 3. Page types and frontmatter

Every file under `wiki/` starts with YAML frontmatter:

```yaml
---
title: Least-Connections Balancing in Olla
type: concept          # concept | entity | source-summary | comparison
sources:
  - raw/repos/olla/balancer.md
related:
  - "[[olla-gateway]]"
  - "[[ollama-worker]]"
created: 2026-08-02
updated: 2026-08-02
confidence: high       # high | medium | low
author: claude-code    # the agent id that last wrote this page
---
```

| Type | Goes in | Holds |
| --- | --- | --- |
| `concept` | `wiki/concepts/` | An idea, mechanism, or technique. Reusable. |
| `entity` | `wiki/entities/` | A concrete thing: a host, a model, a service, a person, a repo. |
| `source-summary` | `wiki/sources/` | One raw source, distilled. Names the source in `sources:`. |
| `comparison` | `wiki/comparisons/` | Two or more concepts/entities weighed against each other. |

**Naming:** kebab-case, descriptive, no dates in the filename except for
source summaries, which end in the source's date:
`wiki/sources/olla-least-connections-2026-06-01.md`.

**Linking:** cross-reference with `[[wikilinks]]`, never with relative paths.
Obsidian resolves them and so should you. Link generously — a page with no
incoming links is an orphan and will be flagged by lint.

---

## 4. The three operations

### Ingest
1. Drop the source in the right `raw/` subdirectory. Never modify it afterward.
2. Write or update a `source-summary` page in `wiki/sources/`.
3. Pull the reusable ideas out into `concepts/` and `entities/` pages.
4. **Update an existing page if one covers the topic.** Do not create a near
   duplicate. Duplicates are the main way this vault rots.
5. Add the new pages to `wiki/index.md`.
6. Append one line to `wiki/log.md`.

### Query
1. Start at `wiki/index.md`.
2. Follow `[[wikilinks]]`. Read `raw/` only when the wiki is demonstrably thin.
3. If you had to fall back to `raw/`, that is a signal: ingest it properly
   afterward so the next agent doesn't repeat the work.

### Lint
Run periodically, or when a task asks for it. Scan for:

- **Contradictions** — two pages asserting incompatible things.
- **Orphans** — pages with no incoming `[[links]]`.
- **Missing pages** — `[[links]]` pointing at pages that don't exist.
- **Stale claims** — superseded by a newer source; decay or delete them.
- **Gaps** — questions the wiki raises but does not answer.

Write results to `outputs/lint-YYYY-MM-DD.md` and log the run.

---

## 5. Rules

1. **`raw/` is immutable.** Read it, cite it, never edit it.
2. **Update, don't duplicate.** Search `wiki/index.md` before creating a page.
3. **One-off observations do not get pages.** If it won't be useful to another
   agent on another day, it belongs in the task record, not the wiki.
4. **Keep the index current.** A page that isn't in `wiki/index.md` is invisible.
5. **Log meaningful operations.** Ingest, lint, and significant restructures go
   in `wiki/log.md`. Routine queries do not.
6. **Skills before installs.** See section 6. This is not optional.
7. **Claim before you work.** See `tasks/QUEUE.md`. Never work an unclaimed task.
8. **Other agents are concurrent.** Re-read a file immediately before you
   overwrite it. Prefer appending. Never bulk-rewrite `wiki/` in one pass.
9. **Stale claims are yours to release.** If you crash or abandon work, run
   `aidt-task release <id>` so someone else can pick it up.

---

## 6. Skills — check here before installing anything

`skills/REGISTRY.md` lists every capability already available on this host.

**Before you install a package, add an MCP server, write a helper script, or
tell the user "I need X" — run `aidt-skill list` and read the registry.**

The point is that seven agents share this machine. If each one installs its own
slightly different flavour of the same tool, the host accumulates seven broken
half-configurations and no agent can rely on any of them. So:

1. `aidt-skill list` — see what exists.
2. `aidt-skill show <name>` — read the SKILL.md before using it.
3. If it exists, **use it as documented.** Do not reimplement it.
4. If it doesn't exist and you genuinely need it:
   - `aidt-skill new <name>` scaffolds `skills/<name>/SKILL.md` from the template.
   - Fill it in honestly: what it does, prerequisites, exact invocation,
     failure modes, and who owns it.
   - `aidt-skill register <name>` adds it to `REGISTRY.md`.
   - Only then install it.
5. If a skill is broken, fix the SKILL.md as part of fixing the skill. A skill
   whose documentation lies is worse than no skill.

A skill is anything reusable: a CLI you configured, an MCP server, a script, a
prompt pattern, an API recipe. If you had to figure it out once, write it down
so nobody figures it out again.

---

## 7. Identity

You are registered in `agents/`. Your generated capability card is
`agents/<your-id>.md` — AIDT rewrites it on every deploy, so don't hand-edit it.

Things you learn about your own capabilities go in `agents/<your-id>.notes.md`,
which is yours and is never overwritten. Record what you are actually good at
on this host, what you have tried and failed at, and which skills you own.

`aidt-agent list` shows the roster. Read it before assuming you're alone or
before duplicating another agent's specialty.

---

## 8. Scope warning

This vault lives on **one host**. It is shared between the agents on that host
and nothing else. There is no synchronization between the gateway vault and a
worker's vault. Do not assume a page you wrote is visible to an agent on
another machine, and do not record host-specific facts as if they were global —
say which host you mean, and use `[[entity]]` pages for hosts.
