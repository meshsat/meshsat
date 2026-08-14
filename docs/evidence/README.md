# Published field evidence

Primary measurement artefacts, published so the claims made for this system
can be checked rather than taken on trust.

| File | What it is |
|---|---|
| `9704-MO-MT-test-report-2026-03-24.md` | RockBLOCK 9704 (Iridium Messaging Transport) mobile-originated + mobile-terminated field test report, 24 March 2026 — real hardware, real constellation. |
| `hemb_stress_20260330_033905.jsonl` | HeMB bonding stress run, 30 March 2026: 1.000 send attempts over 2 h 06 min across two bonded free terrestrial bearers. 830 successes (all sub-second, median 31,3 ms), 170 failures — 168 of them one unbroken run after total bearer loss. Raw, row per attempt. |
| `hemb_stress_final_stats.json` | Final counters for the same run (`bytes_paid: 0` — no satellite bearer participated; the receive/decode path was barely exercised). |

Caveats are part of the data: the stress run covers the local send path over
free bearers only, and its failure mode is included deliberately. Multi-bearer
validation including a paid satellite bearer is future work.

**Redaction note:** the 9704 report is published with the modem IMEI and SIM
ICCID partially masked (`XXXX`) relative to the original capture. No other
edits were made. The originals remain with the maintainers.
