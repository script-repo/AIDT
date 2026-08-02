---
name: olla-pool
summary: Reach models through the Olla gateway and inspect the worker pool behind it.
status: stable
owner: aidt
requires: []
tags:
  - inference
  - core
created: 2026-08-02
updated: 2026-08-02
---

# olla-pool

## What it does

How to call models on this deployment and how to see what the pool is doing.
Every agent here was configured at deploy time to reach models through **Olla**,
an OpenAI-compatible gateway that load-balances across the Ollama workers.

It does not cover deploying or removing workers — that is AIDT's Nutanix
section, driven by the operator.

## When to use it

- You need a second model call from inside a script or tool.
- A request is slow or failing and you want to know whether it is the gateway,
  a worker, or the model.
- You want to know which models are actually available before naming one.

## Prerequisites

The gateway URL. Your CLI already has it; for scripts:

```bash
export OLLA_GATEWAY="${OLLA_GATEWAY:-http://localhost:40114}"
curl -fsS "$OLLA_GATEWAY/internal/status" >/dev/null && echo ok
```

On a worker, `localhost` is wrong — use the gateway's address. Check
`aidt-agent whoami` for the endpoint recorded at deploy time.

## Usage

### Call a model

```bash
curl -fsS "$OLLA_GATEWAY/olla/openai/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -d '{"model":"nemotron-3-super:cloud",
       "messages":[{"role":"user","content":"one word: ping"}],
       "stream":false}'
```

### List available models

```bash
curl -fsS "$OLLA_GATEWAY/olla/openai/v1/models" | grep -o '"id":"[^"]*"'
```

### Inspect the pool

```bash
curl -fsS "$OLLA_GATEWAY/internal/status"   # endpoints, health, request counts
systemctl is-active olla                    # on the gateway
systemctl is-active ollama                  # on a worker
```

## Rules

**Always go through the gateway.** Do not reconfigure yourself to hit a worker's
`:11434` directly, even if it is faster in the moment — it bypasses the load
balancer, hides your traffic from the metrics the operator watches, and breaks
when that worker is removed.

**Do not pull models yourself.** Model inventory is managed from AIDT's Models
section so the whole pool stays consistent. If you need a model that isn't
there, file a task (`aidt-task new`) rather than pulling it onto one worker.

## Failure modes

| Symptom | Cause | Fix |
| --- | --- | --- |
| Connection refused on `:40114` | Olla down, or you are on a worker using `localhost` | `systemctl status olla`; use the gateway's real address |
| `model not found` | Model not pulled on any worker | `.../v1/models` to see what exists; file a task for the rest |
| First token takes 30s+ | Cold model, still loading into memory | Expected on first call; warm it from AIDT's Models section |
| Requests all land on one worker | Balancer pinned to the first endpoint | Operator runs `aidt apply-balancer` (least-connections) |
| Capability WARN in Olla logs | `models.yaml` does not advertise tools/function_calling | Operator runs `aidt apply-capabilities` |

## Notes

Claude Code reaches the pool through Olla's **Anthropic Messages** translator
rather than the OpenAI path; the other agents use the OpenAI-compatible route.
Both land on the same worker pool.
