# HeMB-NG fixtures, release v1

The shared test vectors of the HeMB-NG stage 2 contract (MESHSAT-1584): the Bridge (meshsat), the Hub (this repo), MeshSat Android and MeshSat iOS test against the same bytes. The decision numbers (C1d, C3, C6, ...) are the contract's, r4 of 18 Oct 2026.

## The release

- This folder is the release; each version is a tag (`fixtures-v1.0`, `fixtures-v1.1`, ...) and `RELEASE` names the one this commit holds. A tag never moves.
- `SHA256SUMS` lists every file but itself.
- `generate.py` writes every file but this README, `directory_v2_rest.json` and itself, byte for byte the same on every run (the ES256 signatures are RFC 6979 deterministic). `directory_v2_rest.json` is written by the Hub's own code (`internal/directory/fixtures_rest_test.go`, `MESHSAT_WRITE_FIXTURES=1`), and the Hub's test checks it on every run: its payloads byte for byte, every signature with the Hub's verifier. `python3 generate.py --check` regenerates in memory and compares. Each expected value is written by hand in the script and checked against a small reference implementation before anything is written.
- Every other repo keeps an exact copy and a `fixtures.lock` with the tag and the SHA-256 of this `SHA256SUMS`. Its test fails when the copy differs from the lock. Taking a new release means changing the lock; a repo cannot change the expected checksum on its own.
- A change to anything here is a new release (`fixtures-v1.1`, ...), never an edit of a tagged one.

## The files

| File | Decision | What it pins |
|---|---|---|
| `ids.json` | C1e | UUIDv7 everywhere; the pinned UUIDv5 namespace; the two v5 exceptions and their validators |
| `content_digest.json` | C1d | the hashed bytes of `content_sha256`, with the preimage of each vector |
| `inbox_cases.json` | C3 | the receiver's commit: repeats, `op_conflict`, `mid_conflict`, `digest_mismatch`, `bad_expiry`, `bad_id`, expiry at commit, `expiry_unjudged`, capacity |
| `hub_ingress_cases.json` | C4 | the Hub's operation key, the replay of every event, `mid_conflict` at the Hub |
| `c6_reducer.json` | C6 | evidence sets and the state each must reduce to in every order and with duplicates; the escalation rule for late evidence |
| `c6a_receipt_kinds.json` | C6a | which kind may carry which class under which authority |
| `directory_v2.json` | C7 | the v2 envelope (signature over the exact payload bytes, an unknown field), a tampered payload, a rotation chain; test keys only. **Envelope and signature coverage only:** its rows use `name` and `members`, not the REST shapes, so a receiver imports no contact or group from them |
| `directory_v2_rest.json` | C7 | (v1.2) the same keys and checks with the rows a receiver imports, made by the Hub's store, snapshot encoder, signer and rotation: a snapshot, a rotation, a snapshot under the new key with the names, addresses and memberships each must hold; refusals (tampered, wrong key, another tenant, not newer); and rows a receiver skips (unknown address or group kind, no display name, unknown member, an address two contacts hold) |
| `sos.json` | C10 | the `0x02`, `0x07` and `0x08` frames, the legacy `sos_id` derivation, the state cases |
| `lxmf_mid_field.json` | C2 | the LXMF fields map carrying the `mid` |
| `sms_reports.tsv`, `sms_parts.tsv` | C6a | an SMS delivery report's status by format, and the completion of a multipart send |

## Changes in v1.2 (after the review of 22 Oct 2026, r40 decision 1)

- New `directory_v2_rest.json`: the C7 cases in the REST shapes (`display_name`, `member_ids`), generated through the Hub's production path, with each receiver's expected contacts (names and addresses), groups (memberships), versions, pinned key and skipped rows. Its `compare` field says how to apply each list and what to compare.
- `directory_v2.json` is unchanged and stays in the release; it is labelled envelope and signature coverage, not contact or group import coverage.
- Nothing else changed: every other file is v1.1's, byte for byte.

## Changes in v1.1 (after the review of 18 Oct 2026 19:34)

- **FIX-R1:** the inbox and Hub cases carry the delivered fields (`recipient`, `content_type`, `mid_expires_at`, `body_hex`) and the references recompute `content_sha256` from them; no case carries a stand-in "recomputed" value any more. The capacity case's last step now has the digest of its new deadline, and a new case keeps the old digest under a changed deadline and is refused `digest_mismatch`.
- **C10-R1:** `0x07` and `0x08` carry `run_start_ms` (8 bytes, big-endian) after the `sos_id`, and so does the MQTT event. A v7 `sos_id` carries that time in its first 48 bits; a v5 one derives from the authenticated bridge id and that time. Migrated start and cancel frames with nonzero milliseconds are decoded and validated, a cancel before its start stays terminal.
- An inbox commit after `expires_at` but within the skew allowance is marked `accepted_within_skew`.
- C6: evidence is valid only from the source its authority has (`local` for the party's own broker PUBACK, the `msg/status` topic for the Hub's and the recipient's). New kind `recipient_busy`, class `unknown`: the retryable `receiver_busy` never reads as a failure.

Every id, name and number here is made up. No key in this folder protects anything.
