---
name: vault-wiki
summary: Ingest sources into the shared wiki, query it, and lint it for rot.
status: stable
owner: aidt
requires: []
tags:
  - knowledge
  - core
created: 2026-08-02
updated: 2026-08-02
---

# vault-wiki

## What it does

Maintains the two-layer knowledge base in this vault: `raw/` (immutable
sources) compiled into `wiki/` (interlinked markdown pages). Covers the three
operations defined in `AGENTS.md` §4 — ingest, query, lint.

It does **not** cover the task queue (`task-queue`) or agent identity
(`agent-registry`).

## When to use it

- You were handed a document, URL, transcript, or dataset → **ingest**.
- You need a fact about this host, its models, or prior work → **query**.
- The wiki feels contradictory or stale, or a task asks for a health pass →
  **lint**.

Query first, always. Ingesting something the wiki already covers creates the
duplicate pages that make the wiki useless.

## Prerequisites

Only the vault itself.

```bash
[ -f "$AIDT_AGENT_VAULT/AGENTS.md" ] && echo ok
```

If `$AIDT_AGENT_VAULT` is unset: `export AIDT_AGENT_VAULT="$HOME/Obsidian/AIDT-Agent-Vault"`.

## Usage

### Query

```bash
cat "$AIDT_AGENT_VAULT/wiki/index.md"
grep -ril "least connections" "$AIDT_AGENT_VAULT/wiki/"
```

Follow `[[wikilinks]]` from there. Drop to `raw/` only when the wiki is
demonstrably thin — and if you do, ingest properly afterward.

### Ingest

```bash
# 1. save the source, unmodified, under the right raw/ subdirectory
cp report.pdf "$AIDT_AGENT_VAULT/raw/papers/olla-benchmarks-2026-06-01.pdf"

# 2. does a page already cover this?
grep -i "olla benchmark" "$AIDT_AGENT_VAULT/wiki/index.md"

# 3. write or UPDATE wiki/sources/olla-benchmarks-2026-06-01.md
# 4. lift reusable ideas into wiki/concepts/ and wiki/entities/
# 5. add every new page to wiki/index.md
# 6. append one line to wiki/log.md
```

Frontmatter is mandatory and its shape is fixed — see `AGENTS.md` §3.

### Lint

Read every page under `wiki/` and check for the five rot patterns:
contradictions, orphans (no incoming links), dangling `[[links]]`, stale claims
superseded by newer sources, and unanswered gaps.

```bash
# dangling links: every [[target]] that has no matching file
grep -rho '\[\[[^]]*\]\]' "$AIDT_AGENT_VAULT/wiki/" | tr -d '[]' | sort -u |
  while read -r p; do
    [ -n "$p" ] || continue
    find "$AIDT_AGENT_VAULT/wiki" -name "$p.md" -print -quit | grep -q . ||
      echo "dangling: $p"
  done
```

Write the full pass to `outputs/lint-$(date +%F).md` and log it.

Expected output of a clean pass:

```
dangling: (no output)
```

## Failure modes

| Symptom | Cause | Fix |
| --- | --- | --- |
| Two pages disagree | Concurrent ingest by two agents | Merge into the older page, delete the newer, note the merge in `log.md` |
| Page exists but nobody finds it | Not listed in `wiki/index.md` | Add it; indexing is not optional |
| `[[link]]` resolves to nothing | Page renamed or never written | Create the page or fix the link — never leave it dangling |
| Wiki keeps growing, answers get worse | One-off observations being given pages | Only reusable cross-event knowledge earns a page (`AGENTS.md` §5.3) |
| Your edit vanished | Another agent overwrote the file | Re-read immediately before writing; prefer appending |

## Notes

The vault is per-host. A page written on the gateway is not visible to an agent
on a worker. Say which host you mean, and give hosts their own `entity` pages.
