---
title: Host Overview
type: entity
sources: []
related:
  - "[[index]]"
created: 2026-08-02
updated: 2026-08-02
confidence: medium
author: aidt
---

# Host Overview

The 60-second orientation for an agent that just arrived on this host. Keep it
short; depth belongs in linked pages.

## What this machine is

AIDT deployed one or more coding agents here. The host is either an **Olla
gateway** or an **Ollama worker** in an AIDT-managed LLM pool.

- Gateway: runs Olla on `:40114`, serving an OpenAI-compatible API that
  load-balances across every registered worker.
- Worker: runs Ollama on `:11434` and holds model weights.

Check which one you are on: `systemctl is-active olla ollama`.

## How you reach a model

Through the gateway, not directly:

```
http://<gateway>:40114/olla/openai/v1/chat/completions
```

Your CLI was already configured with this at deploy time. Do not reconfigure it
to hit a worker directly — that defeats the load balancer.

## What else is running

`aidt-agent list` — the other agents on this host.
`aidt-skill list` — what is already installed and configured.
`aidt-task list` — work waiting to be claimed.

## Fill this in

Replace the placeholders below as you learn them. This page is worth keeping
accurate; it is the first thing every new agent reads.

- **Hostname / role:** _unknown — run `hostname` and record it_
- **Default model:** _unknown_
- **Registered workers:** _unknown_
- **Notable local services:** _unknown_
