# Voice notes and photos over LXMF (MESHSAT-1735)

Status: transport half, Bridge side, since 28 Oct 2026. The phone apps' recording, playback and
the Hub's console are their own issues; this page is the wire and the kit's API.

## What travels

A voice note and a photo ride in an LXMF message's fields, exactly as Sideband and MeshChat
send them, so nothing MeshSat-specific is on the wire and a note from either client plays on
the other side:

| Field | Value | Meaning |
|---|---|---|
| `FIELD_AUDIO` (0x07) | `[mode, bytes]` | mode: Codec2 0x01-0x09 (700C = 0x03, the satellite default), Opus 0x10-0x19 (the bytes are an Ogg Opus stream), 0xFF custom |
| `FIELD_IMAGE` (0x06) | `[format, bytes]` | format: `"webp"` (what every client writes; other lower-case names are carried as given) |

The fields are part of the packed message: a message that outgrows one packet goes as an RNS
resource on the link with the fields inside it, as upstream does. The code is
`internal/lxmf/fields.go` (the same file in meshsat-hub); the proof against Python LXMF 1.2.1 is
`TestInteropLXMFAttachmentsBothWays` in `internal/interop/` (`MESHSAT_INTEROP=1 go test -run
TestInteropLXMFAttachmentsBothWays ./internal/interop/`).

## Caps

- One audio and one image per message, together at most **64 KiB** (`lxmf.MaxAttachmentBytes`).
  A received message over the cap keeps its text and loses the attachments (logged); the API
  refuses a send over it with 413.
- The kit keeps at most `MESHSAT_LXMF_ATTACHMENT_STORE_BYTES` of attachments (default 16 MiB),
  received and sent alike, oldest out first (`lxmf_attachments`, schema v83).

## API

| Route | What |
|---|---|
| `POST /api/lxmf/send` | as before, plus `audio: {mode, data_b64}` and `image: {format, data_b64}`; `content` may be empty when the message carries one. The row goes through the delivery ledger and the `lxmf_0` worker like every LXMF send; the preview of a text-less note is `[audio Codec2 700C 1024 B]`. |
| `GET /api/lxmf/messages/{id}/attachments` | the message's attachments in order (the image first), each with `kind`, `mode` or `format`, `size`, `content_type` and `url`. `{id}` is the message's row id (`messages.id`, what `GET /api/messages` lists) or its LXMF hash (64 hex). |
| `GET /api/lxmf/messages/{id}/attachments/{n}` | the bytes: Codec2 frames as `audio/codec2` with the mode in `X-LXMF-Audio-Mode`, an Opus note as `audio/ogg`, a photo as `image/webp`. |

Example, a note to the Hub's address from a kit:

```sh
curl -s localhost:6050/api/lxmf/send -H 'Content-Type: application/json' -d '{
  "to": "<32 hex lxmf.delivery hash>", "content": "listen",
  "audio": {"mode": 3, "data_b64": "'"$(base64 -w0 note.c2)"'"}}'
curl -s localhost:6050/api/lxmf/messages/<id>/attachments
curl -s -o note.c2 -D - localhost:6050/api/lxmf/messages/<id>/attachments/0
```

## Policy: fields travel on Reticulum links only

Attachments are carried by `lxmf_0`, over whatever Reticulum interface the path to the
destination uses (TCP, UDP, RNode, AutoInterface, the Hub's node, a paid satellite link if the
paid policy allows the destination). No other bearer carries them: an inbound LXMF message meets
the access rules as `lxmf_0` with its text only (SBD, SMS, APRS and LongFast cannot take the
bytes), and every delivery the rules make of that text records what stayed on the kit in the
ledger row's `reason`: `text only; not carried: audio Codec2 700C 1024 B, image webp 2048 B`.
The bytes stay reachable on the kit through the routes above. A rule from `lxmf_0` to `lxmf_0`
(an LXMF relay to other destinations) carries the text only too, for now.

## Not done here

- No codec on the kit: the SPA does not record or play a note yet (Codec2 WASM is the SPA's
  task); the kit stores and serves the bytes.
- A voice note without text is filed and served but not forwarded by the rules (there is no text
  to forward).
- Attachments on a delivery intent's attempts (`intent_worker.go`) take the same body, untested
  on the kits.
