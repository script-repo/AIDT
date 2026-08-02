# raw/ — Immutable Sources

Layer 1 of the wiki. **Read only.** Never edit, reformat, summarize in place,
or delete anything here. These files are the evidence every wiki page cites.

| Directory | Holds |
| --- | --- |
| `articles/` | Web pages, blog posts, docs saved as markdown or text. |
| `papers/` | PDFs and paper text. |
| `repos/` | Source snapshots, README dumps, extracted code. |
| `data/` | CSV, JSON, logs, metrics exports. |
| `transcripts/` | Meeting notes, chat logs, console sessions. |
| `assets/` | Images, diagrams, binaries referenced by pages. |

## Naming

`<topic>-YYYY-MM-DD.<ext>` — kebab-case topic, the date the source was
published or captured. Example: `raw/articles/olla-balancer-design-2026-06-01.md`.

## Adding a source

1. Save it here unmodified.
2. Write a `source-summary` page in `wiki/sources/` that cites it in `sources:`.
3. Lift the reusable ideas into `wiki/concepts/` and `wiki/entities/`.
4. Update `wiki/index.md` and append to `wiki/log.md`.

A source with no summary page is invisible. A summary with no source is
unfalsifiable. Always do both.
