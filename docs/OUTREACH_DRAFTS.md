# Outreach Drafts

---

## Thomas Göttgens — VP Meshtastic

Hey Thomas,

Good to reconnect after Embedded World. We've completed the GPLv3 switch you asked about — MeshSat is fully GPLv3 now. Since then we've shipped the full any-to-any routing fabric, and just landed a TAK/CoT adapter and APRS gateway. I'd love to schedule a 30-minute call to discuss multi-constellation satellite support and what an official Meshtastic ecosystem integration could look like — MeshSat is the only project bridging Meshtastic to Iridium SBD with full delivery tracking and access rules.

When works for you?

---

## Michael Mitrev — Ground Control

Hi Michael,

Following up on your LinkedIn comment about MeshSat. We've been running a RockBLOCK 9603N in production for a while now — 200+ MO+MT messages validated, with ISU-aware DLQ backoff, pass scheduling, and 3-tier compression (down to ~40% of raw payload size on satellite links). You mentioned a path to a 9704 evaluation unit — what do you need from our side to move that forward? Happy to share our integration specs or do a live demo.

---

## Angie at Ground Control

Hi Angie,

Just a gentle follow-up on the evaluation unit request. We've been running the 9603N module since early deployment and have validated 200+ MO+MT messages through MeshSat's bridge, including pass scheduling with SGP4 TLE prediction and satellite-aware retry logic. MeshSat is open source (GPLv3) and would make a strong showcase for the Ground Control ecosystem — multi-channel gateway that makes Iridium SBD accessible to Meshtastic and APRS operators. Let me know if there's anything I can provide to move the eval forward.

---

## squadfi — Flaresat

Hey squadfi,

Saw your comment on the MeshSat launch — thanks. We just shipped a TAK/CoT adapter that bridges Meshtastic and Iridium SBD directly to TAK Server (TCP/TLS, CoT XML, full PLI + emergency + telemetry event types). Given Flaresat's TAK expertise and MeshSat's satellite bridge, there's an obvious collaboration angle. Want to jump on a call to discuss? I'm thinking joint TAK-over-satellite demos and potentially shared CoT infrastructure.
