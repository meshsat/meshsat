# HeMB-NG fixtures, release v1

The shared test vectors of the HeMB-NG stage 2 contract (MESHSAT-1584): the Bridge (meshsat), the Hub (this repo), MeshSat Android and MeshSat iOS test against the same bytes. The decision numbers (C1d, C3, C6, ...) are the contract's, r3 of 18 Oct 2026.

## The release

- This folder is the release. The tag `fixtures-v1.0` marks it. `RELEASE` names it.
- `SHA256SUMS` lists every file but itself.
- `generate.py` writes every file but this README and itself, byte for byte the same on every run (the ES256 signatures are RFC 6979 deterministic). `python3 generate.py --check` regenerates in memory and compares. Each expected value is written by hand in the script and checked against a small reference implementation before anything is written.
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
| `directory_v2.json` | C7 | the v2 envelope (signature over the exact payload bytes, an unknown field), a tampered payload, a rotation chain; test keys only |
| `sos.json` | C10 | the `0x02`, `0x07` and `0x08` frames, the legacy `sos_id` derivation, the state cases |
| `lxmf_mid_field.json` | C2 | the LXMF fields map carrying the `mid` |
| `sms_reports.tsv`, `sms_parts.tsv` | C6a | an SMS delivery report's status by format, and the completion of a multipart send |

Every id, name and number here is made up. No key in this folder protects anything.
