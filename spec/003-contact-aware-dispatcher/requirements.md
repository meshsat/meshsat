# Requirements — Contact-aware Dispatcher (meshsat Phase 2, EXECUTION-PLAN §6.2)

Source: `EXECUTION-PLAN.md` §6.2 (5 stories S2-01..S2-05). Depends on `spec/002-unified-directory/`. Foundation for Phase 3 UI reshape.

Constitution invariants in scope: Article VI (DeliveryWorker is the sole outbound path), Article VII (Iridium serial-mutex + 3-min backoff applies to bonded paths too).

The current dispatcher fans messages to a fixed set of channels per access rule. Phase 2 introduces `SendToRecipient(RecipientRef, SendOptions)` — one call fans to N bearers per the recipient's `DispatchPolicy` from spec/002. Plus STANAG 4406 6-level precedence-aware queue with FLASH preemption.

## Functional requirements

REQ-200: The system shall introduce a `Dispatcher.SendToRecipient(RecipientRef, SendOptions)` method that fans the message to N bearers per the recipient's `DispatchPolicy`.

REQ-201: The system shall implement dispatch strategies `PRIMARY_ONLY`, `ANY_REACHABLE`, `ORDERED_FALLBACK`, `HEMB_BONDED`, `ALL_BEARERS`.

REQ-202: When `SendToRecipient` is called, the dispatcher shall resolve the strategy in priority order: caller `SendOptions` override → contact-scoped `DispatchPolicy` → group-scoped → precedence-default → global default.

REQ-203: When the resolved strategy is `PRIMARY_ONLY`, the dispatcher shall send to the contact's primary address only (first row of `directory_addresses` ordered by `kind` precedence).

REQ-204: When the resolved strategy is `ANY_REACHABLE`, the dispatcher shall send to the first bearer whose `HealthScorer.Score >= 50` AND whose rate-limit budget allows.

REQ-205: When the resolved strategy is `ORDERED_FALLBACK`, the dispatcher shall iterate the recipient's `bearer_order` list and try each in order until one succeeds OR the list is exhausted.

REQ-206: When the resolved strategy is `HEMB_BONDED`, the dispatcher shall invoke the existing HeMB bond group whose member bearers match the recipient's available addresses.

REQ-207: When the resolved strategy is `ALL_BEARERS`, the dispatcher shall enqueue a delivery for every address in the recipient's address list.

REQ-208: The system shall expose `POST /api/messages/send-to-contact` accepting `{contact_id, body, precedence?, strategy_override?}` and returning per-bearer delivery IDs.

REQ-209: When `POST /api/messages/send-to-contact` is called for a contact with 0 reachable bearers, the system shall return 503 with `{error, per_bearer_status: [...]}` describing the failure of each attempted bearer.

REQ-210: The legacy `POST /api/messages/send` endpoint shall continue to function unchanged for the lifetime of this phase (no removal until v50).

REQ-211: The `DeliveryWorker.processBatch` shall select queued messages by `precedence DESC, queued_at ASC` so higher-precedence messages drain first.

REQ-212: When a message with precedence `Flash` or `Override` arrives at a saturated interface, the dispatcher shall preempt the lowest-precedence queued item by moving it back to the queue tail AND shall write an audit event `delivery.preempted` with the evicted item's ID.

REQ-213: When the dispatcher applies preemption per REQ-212, the system shall increment `meshsat_delivery_preempted_total{evicted_precedence}` Prometheus counter.

REQ-214: When the bridge boots for the first time after this phase, the system shall populate `directory_dispatch_policy` with one `scope_type="default"` row per STANAG 4406 precedence level mapping: `Flash→HEMB_BONDED`, `Override→HEMB_BONDED`, `Immediate→ANY_REACHABLE`, `Priority→ANY_REACHABLE`, `Routine→PRIMARY_ONLY`, `Deferred→PRIMARY_ONLY`.

REQ-215: When a Hub `directory_push` arrives carrying `directory_dispatch_policy` rows, the system shall apply tenant-scoped overrides on top of the precedence defaults.

REQ-216: While the 30-day key migration grace period is active, the transform pipeline shall accept both `sms:<addr>` and `contact:<uuid>` key references and shall prefer the contact reference when both resolve.

REQ-217: When the grace period ends (configurable via `MESHSAT_CONTACT_KEY_GRACE_DAYS`, default 30), the system shall log channel-key fallbacks at WARN level for 30 days before erroring on them at 60 days.

REQ-218: The `SendResult` returned by `SendToRecipient` shall contain `delivery_ids: [...]`, `bearers_attempted: [...]`, `strategy_used`, AND `precedence_used` so the SPA tick-rendering can display per-bearer status.
