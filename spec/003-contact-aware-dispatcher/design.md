# Design — Contact-aware Dispatcher (Phase 2)

## Goal

Operators today say "send this to Alice" but the dispatcher only understands "send this to channel mesh_0". Phase 2 closes that gap by introducing `SendToRecipient(RecipientRef, SendOptions)` — one call resolves Alice to her N addresses + dispatches per her policy.

## Wire diagram

```
operator REST:                    SPA "Compose":
POST /api/messages/send-to-contact   { contact_id: "alice", body: "...", precedence: "Flash" }
              │                       │
              └───────────────────────┘
                          │
                          ▼
              Dispatcher.SendToRecipient(
                ref, body, opts
              )
                          │
                          ▼
              policy := resolveStrategy(
                opts, contact, group,
                precedence, global
              )  ← REQ-202
                          │
        ┌─────────────────┼─────────────────┐
        │                 │                 │
        ▼                 ▼                 ▼
   PRIMARY_ONLY      ANY_REACHABLE     ORDERED_FALLBACK
   first-row addr    Score>=50 +       iterate bearer_order
                     rate-limit ok
        │                 │                 │
        ▼                 ▼                 ▼
            DeliveryWorker.processBatch
              SELECT ... ORDER BY precedence DESC,
                              queued_at ASC  ← REQ-211
              IF Flash/Override AND saturated:
                preempt lowest-prec queued    ← REQ-212
                audit "delivery.preempted"
              ELSE:
                gw.Forward(msg)
```

## Strategy resolution (REQ-202)

```
function resolveStrategy(opts, contact, group, precedence, global):
  if opts.strategy_override != "": return opts.strategy_override
  if contact has DispatchPolicy:    return contact.DispatchPolicy.strategy
  if group has DispatchPolicy:      return group.DispatchPolicy.strategy
  if precedence-default exists:     return precedence-default.strategy  (REQ-214)
  return global-default.strategy
```

5-tier resolution. Caller wins, falls through to operator config, falls through to system defaults.

## STANAG 4406 precedence-default seeds (REQ-214)

The 6-level precedence drives the strategy when no contact/group/caller override exists:

| Precedence | Strategy | Rationale |
|---|---|---|
| Override | HEMB_BONDED | Highest-criticality → bond every bearer in parallel |
| Flash | HEMB_BONDED | Same — emergency-class traffic |
| Immediate | ANY_REACHABLE | Time-sensitive — first-bearer-OK suffices |
| Priority | ANY_REACHABLE | Same — operator gets a delivery on the first available |
| Routine | PRIMARY_ONLY | Default daily traffic — cheap path |
| Deferred | PRIMARY_ONLY | Operator explicitly OK with batch-class delivery |

## Preemption (REQ-212 + REQ-213)

The DeliveryWorker queue is bounded per-interface. When a Flash/Override message arrives at a saturated interface, the lowest-precedence queued item gets evicted to the tail (NOT dropped — it'll re-enqueue when the saturation drains). Audit entry captures the eviction so operators can debug "why did my Routine message take so long?" — answer: a Flash preempted it 4 times.

The counter `meshsat_delivery_preempted_total{evicted_precedence}` lets Grafana show the preemption pressure per precedence band.

## 30-day key migration grace (REQ-216 + REQ-217)

Phase 1 introduces `contact:<uuid>` key refs. Existing rules use `sms:<addr>`. During the cutover:
- Day 0..30: both forms work; contact wins on overlap.
- Day 30..60: contact wins; sms fallback emits WARN log.
- Day 60+: sms fallback errors.

Operator can extend via `MESHSAT_CONTACT_KEY_GRACE_DAYS` env var.

## Out of scope

- UI for SendToRecipient (covered by `spec/004-ui-reshape/`).
- Bond-group authoring (operator-managed today; not redesigned here).
- Per-address rate limits (existing per-channel limiter still applies).
