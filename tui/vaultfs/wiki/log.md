---
title: Operation Log
type: concept
sources: []
related:
  - "[[index]]"
created: 2026-08-02
updated: 2026-08-02
confidence: high
author: aidt
---

# Operation Log

Append-only. Newest entries at the bottom. One line per meaningful operation:
ingest, lint, significant restructure. Routine queries are not logged.

Format:

```
YYYY-MM-DD HH:MM  <agent-id>  <ingest|lint|restructure>  <what>
```

---

<!-- append below this line -->
