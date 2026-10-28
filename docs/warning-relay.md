# Last-mile warning relay, the kit's side (MESHSAT-1734)

The Hub reads the official public warning feeds and hands each warning for this kit's area to
it as a compact signed digest. This Bridge verifies it, keeps it, and puts it on everything a
person near the kit can hear or read when the phone network does not reach them. The contract,
the digest's byte layout, the feeds, who gets what and the test vector are the Hub's
`docs/warning-relay.md`; this document is what the kit does with a digest once it has one. The
code is `internal/warnings` (the decoder `digest.go`, the fan-out `relay.go`), wired in
`cmd/meshsat/main.go` beside the Community Post.

What it is not: an official warning channel. Every surface carries the label **"Relay of
`<source>` warning, issued `<time>` UTC via MeshSat, not an official NL-Alert"** (the source
is `DWD`, `KNMI via MeteoAlarm` or `MeshSat test`), rendered once in `warnings.LabelFor` so the
mesh, the portal and the event stream say the same thing.

## What arrives and what is kept

A digest comes on the kit's own `{root}/bridge/<id>/warn/in` (the Hub alone may publish there)
or as a satellite MT whose first byte is `0x57`, the digest's magic; the satellite gateways hand
such bytes to the relay untouched (`Processor.SetWarningInbound`), so a digest is never a text
for the rules. The relay verifies the ECDSA P-256 signature with the pinned Hub directory key
(`system_config` `hub_directory_signing_pubkey`, the key the directory snapshots are verified
with) and refuses and logs
anything else: no key pinned, a wrong key, a forged signature, a layout it does not know.

Every digest taken is one row in `warnings` (migration v85): id, what it references, message
type, test flag, source, sent, expires, severity, urgency, certainty, category, event, language,
the text, the raw bytes, how it arrived (`hub` or the satellite interface) and what it went out
on (`bearers`, JSON). The same (id, message type) is never relayed twice: a QoS 1 redelivery or
the satellite copy of a digest the Hub link already delivered logs `digest seen before` and does
nothing. An Update marks the row it references cancelled (no longer the current message) and is
relayed as the new one; a Cancel is kept as its own row, marks the row it references cancelled,
and is relayed so the people who heard the warning hear that it is withdrawn, whether or not
this kit held it (the mesh may have). A digest that arrives expired is kept and relayed nowhere.
Rows outlive their arrival by 30 days. `GET /api/warnings` lists them, newest first.

## The bearers

Each send is one row in the delivery ledger (`Dispatcher.QueueDirectSendKeyed`, submission
source `warnings`) under an idempotency key made of the digest's id and the bearer, so a digest
handled twice by mistake could still not queue anything twice. The text on every bearer is the
same: `[TEST ][UPDATE: ]WARNING <event> <severity> until <HH:MM> UTC: <text>`, a newline, the
label (at most 200 bytes, the label kept whole); a Cancel is `CANCELLED: <event>` and the label.

| Bearer | Key suffix | What goes out | Gate |
|---|---|---|---|
| mesh, port 11 ALERT_APP, class `alert`, precedence Immediate | `:alert` | the text with BEL; a node with its alert setting on buzzes | always |
| mesh, text broadcast on the primary channel, class `plain` | `:text` | the same text without BEL, for nodes without an alert setting; one message, not one per node | always |
| APRS, bulletin `BLN<d>` (d = the digest's last id byte mod 10), class `plain` | `:aprs` | at most 67 printable ASCII characters: `[TEST ][UPD ]<EVENT> <SEV> til <HH:MM>: <text> -MeshSat relay`, or `CANCELLED <EVENT> -MeshSat relay` | the APRS gateway runs (`aprs_0`) |
| the kit's own 2G cell, a cell broadcast | (not a ledger row: `cell_tx_log`) | the text, about once a minute until the warning expires, on 4380 for a test, on the EU-Alert identifier its severity, certainty and urgency give where the owner cleared it, on channel 50 otherwise; an Update replaces, a Cancel deletes | the cell's radio is keyed up; `docs/cell-broadcast.md` |
| LXMF, one message per peer, class `lxmf`, interface `lxmf_0` | `:lxmf:<dest>` | the mesh text as the message's content, to each LXMF peer heard recently (below) | the Reticulum node runs with LXMF on |

The row's `bearers` lists what went: `mesh-alert`, `mesh-text`, `aprs`, `cell` (or
`cell-cancel`), `lxmf`. A bearer whose gate is closed is simply absent; the others still go.

**The LXMF leg.** The peers are the `lxmf.delivery` destinations in the Reticulum node's path
table (what `GET /api/lxmf/peers` lists), each with when its identity last announced to this
kit. The relay writes to those heard within the last 24 hours (`LXMFPeerWindow`), the most
recently heard first, at most 50 per digest (`LXMFMaxPeers`), and never to one whose path leaves
this kit on a paid link (`Stack.CrossesPaidLink`: the satellite interface with a paid-link
policy), since every message over it would cost the owner; the kit's LXMF links are LoRa and
TCP. Each send is the row `POST /api/lxmf/send` writes (content the text, no title, five
retries) under the key `warn:<id>:<type>:lxmf:<dest>`, so a redelivered digest sends nothing
twice to anyone. A Cancel and an Update go to the peers heard at that moment, which may differ
from those who got the warning: a peer that joined in between hears the cancel of a warning it
never had, which is harmless. No peer heard recently, no `lxmf` bearer.

## The event stream

Every digest kept, relayed or not, is one event on `GET /api/events` (the Bridge's server-sent
events, `Processor.Emit`), so the web app and a notifier reading the stream (the Linux app's
`events.py`) learn of it without a poll:

```json
{"type":"warning",
 "message":"WARNING Wind Moderate until 10:11 UTC: Kans op windstoten tot 90 km/u",
 "data":{"id":"f161d743dd14e974","msg_type":"Alert","test":false,"source":"meteoalarm-nl",
         "sent":"2026-10-28T10:11:41Z","expires":"2026-10-29T10:11:41Z",
         "severity":"Moderate","urgency":"Expected","certainty":"Observed","category":"Met",
         "event":"Wind","language":"nl","text":"Kans op windstoten tot 90 km/u","via":"hub",
         "received_at":"2026-10-28T12:00:00Z","cancelled":false,
         "bearers":"[\"mesh-alert\",\"mesh-text\",\"aprs\",\"lxmf\"]",
         "label":"Relay of KNMI via MeteoAlarm warning, issued 28 Oct 10:11 UTC via MeshSat, not an official NL-Alert",
         "expired":false},
 "time":"2026-10-28T12:00:00Z"}
```

`data` is the row as `GET /api/warnings` lists it (`bearers` a JSON string there too), plus
`label` and `expired` (true when the digest arrived expired and went nowhere). A Cancel comes
with `msg_type` `Cancel` and `cancelled` true, its `ref` naming the warning withdrawn; a test with
`test` true. A redelivery is no event. There is no Warnings page in the Bridge's web app yet; the
event is there for it and for the notifier.

## The Community Post banner

The Community Post portal (`docs/community-post.md`, `internal/post`) shows the warnings in
force at this kit at the top of every page, above the page's own content, as plain HTML with no
script: the words "Public warning" (`Publieke waarschuwing`, `Δημόσια προειδοποίηση`), `TEST`
(`ΔΟΚΙΜΗ`) for a test, `Update` for an Update, the event and severity as the digest names them
(English, as on the mesh), `until <HH:MM> UTC`, the warning's words, and the label line.

In force (`warnings.InForce`) is: kept, not cancelled and not replaced by an Update, not a
Cancel, and its `expires` ahead; a warning that names no expiry counts for 24 hours after it was
sent (`NoExpiryInForce`). Newest sent first. A cancelled, replaced or expired warning is not
shown at all; a closed post still shows the banner. The portal reads the table on every page
view; there is nothing to configure.

## Owed

A Warnings page in the Bridge's web app (the event and `GET /api/warnings` are its inputs); a
live digest through the LXMF leg on a kit with a peer heard, and through the Post banner on a
kit running the portal (the mesh leg was proven live on 28 Oct 2026; the cell leg's bench tests
T1 to T7 are listed in `docs/cell-broadcast.md`).
