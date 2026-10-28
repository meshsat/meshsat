# Cell broadcast on the MeshSat 2G cell

Two things meet here. The kit's cellular modem **receives** cell broadcasts from whatever network it
is on (NL-Alert, EU-Alert, a test on our own cell) and the Bridge stores them (MESHSAT-1737). The
kit's **own 2G cell** (the Pluto, osmo-cbc, MESHSAT-1578) **sends** cell broadcasts: the 2G cell leg
of the last-mile warning relay (MESHSAT-1734) and the operator's test sends.

A cell broadcast is a one-to-all transmission of the licensed carrier (RDI-2610-00385, 1875.0 to
1879.9 MHz, one location, 500 m). It is logged like a key-up and refused like an ARFCN outside the
block. Nothing in this file changes a kit that has no Pluto: without the cell nothing new happens.

## What a phone shows

3GPP TS 23.041 9.4.1.2.2 gives every channel (message identifier) a meaning:

| Identifier | Meaning | A phone |
|---|---|---|
| 1 to 4351 | ordinary channels (50 is the one the kits use) | shows it only when subscribed to that channel |
| 919, 3471 | NL-Alert on 2G/3G (Regeling NL-Alert art. 2: every message on both, in parallel) | shows it as NL-Alert when subscribed (Dutch phones are) |
| 4352, 4353, 4354, 4356 | ETWS earthquake, tsunami, both, other emergency | official alert, unasked |
| 4355 | ETWS test | only with "Test alerts" on, marked as a test |
| 4370 | EU-Alert level 1 / CMAS presidential (NL-Alert on 4G/5G) | official alert, unasked, cannot be switched off |
| 4371 to 4374 | EU-Alert level 2 / CMAS extreme: observed or likely, immediate or expected | official alert, unasked |
| 4375 to 4378 | EU-Alert level 3 / CMAS severe: the same four | official alert, unasked |
| 4379 | amber (child abduction) | official alert |
| 4380, 4381 | the monthly test, the exercise | only with "Test alerts" on, marked as a test |
| 4382 | operator defined | operator's |
| 4383 to 4395 | the same again in additional languages | as above |
| 4396, 4397 | public safety alert | official |
| 4398, 4399 | state/local test | test |
| 4400 to 6399 | reserved for future public warning | official |

The receive side's severity words are `alert` (919, 3471), `presidential`, `extreme`, `severe`,
`amber`, `test`, `operator`, `info`, `unknown`; they are what `GET /api/cellular/broadcasts` shows
and what the Messages > Broadcasts tab colours.

## Receiving (MESHSAT-1737)

`internal/transport/cbs.go`. What was wrong and is fixed:

- The modem was asked for broadcasts with a bare `AT+CSCB=0`. On the A7670E that is "accept the
  identifiers listed", with nothing listed: reception off (A76XX AT manual 9.2.5; the modem's own
  default reads `+CSCB: 1,(),()`). The Bridge now sends `AT+CSCB=0,"0-65535","0-255"` (the TS 27.005
  form, mode 0 with the whole identifier and coding ranges) and, on a modem that refuses the ranges,
  `AT+CSCB=1` (mode 1, nothing listed: nothing rejected). The bare form is never sent.
- A `+CBM:` header's data is the line after it. The old code queued a separate read for it after the
  line reader had already consumed that line, so the text was lost. The I/O loop now keeps the header
  and takes the next line as its data, in the idle reader and inside an AT response alike.
- Pages. `+CBM: <sn>,<mid>,<dcs>,<page>,<pages>` names the page; the pages of one message (same serial
  number and identifier) are put together, in order, and passed on once, complete. A message still
  missing a page after two minutes goes on as it is, marked `complete: false`, the gap named in the
  text. A repetition (the cell sends each message every few seconds for minutes) is not stored again
  for an hour; an Update has a new serial number and is.
- The data coding scheme (TS 23.038 section 5) is honoured: GSM 7-bit with the language it names
  (0x05 Dutch, 0x01 English, 0x0B Greek, ...), the language-prefix forms 0x10 and 0x11, UCS-2 (0x48
  and the general-coding family), 8-bit. In PDU mode (`+CBM: <length>` and the 88-octet page in hex)
  the Bridge decodes the page itself; in text mode it reads what the modem decoded and decodes a hex
  UCS-2 line itself.
- The severity table was wrong: 919 read as "test", 3471 as unknown, amber was 4390 to 4395 (that is
  "severe, additional languages"; amber is 4379, as `meshsat-cell/core/cbs.sh` has it).

The stored row (`cell_broadcasts`) is unchanged: serial number, identifier, channel, severity, text.
The event the recorder sees carries the DCS, charset, language, pages and `complete`.

## Sending: the allow-list

`internal/cell/cbs.go`, enforced in code, unit-tested, exactly what `meshsat-cell/core/cbs.sh`
refuses:

- **allowed always**: the ordinary channels 1 to 4351, and the designated test identifiers 4355,
  4380, 4381;
- **refused**: everything from 4352 up that is not one of those three, with a clear error naming the
  identifier as a real public-warning identifier;
- **cleared by the owner only**: `MESHSAT_CELL_CBS_REAL_IDS` (default empty) adds real identifiers,
  as identifiers or ranges, "4370,4371-4378", 4352 to 6399 only. It is set in the kit's `.env` (both
  compose files byte for byte) ONLY after step 0 below is on record. The Bridge logs a warning at
  start when it is set.

**Step 0, before any real-identifier key-up** (from the plan): record the RDI clearance in
`meshsat-cell/docs/COMPLIANCE.md` as a new rule replacing the blanket refusal in rule 6: the RDI
reference, the date, which identifiers, and the conditions (place, time window, power, enclosure,
notice). Then open `core/cbs.sh` there and `MESHSAT_CELL_CBS_REAL_IDS` here to exactly those
identifiers and conditions. Nothing in the repos records a clearance today.

## Sending: how a broadcast goes out

`cell.CBC.Broadcast`:

1. a purpose is required (it is the TX-log row's purpose; a test run says `test run:`);
2. the allow-list; a refusal is a `cbs-refused` row and an error;
3. the radio must be keyed up (`cell.Runner.RadioActive`): osmo-cbc would otherwise hold the message
   and send it later, unlogged (`cbs-refused` row, error);
4. the pages: GSM 7-bit when every character is in GSM 03.38, 93 characters a page, padded with CR,
   the DCS naming the language; else UCS-2, 41 characters a page, DCS 0x48; at most 15 pages;
5. the serial number (TS 23.041 9.4.1.2.1): PLMN wide, a message code derived from what the broadcast
   is about (the warning's id, or the text), update number 0; an Update of a warning already on the
   air keeps the message code and adds one to the update number;
6. the `cell_tx_log` row, event `cbs`, before osmo-cbc hears of it: ARFCN and frequency, the setting
   (channel, what a phone makes of it, serial number, code, update, pages, charset, DCS, repetitions),
   the purpose, the operator, the licence;
7. a message already on the channel, and the one an Update replaces, come off first (osmo-cbc holds
   one message per identifier);
8. `POST /api/ecbe/v1/message` on 127.0.0.1:12345 with `payload_encoded` (our DCS and pages, exact);
   an osmo-cbc that answers 400 to that body gets the `payload_decoded` form cbs.sh uses (osmo-cbc
   pages it then); 409 is cleared by a delete once. The answer records which form it took (`form`).

A delete (`DELETE /api/ecbe/v1/message/<identifier>`) writes a `cbs-stop` row. When the radio keys
down everything this Bridge put on the air comes off (`ClearAll`), or it would go out again, unlogged,
on the next key-up. osmo-cbc 0.5.3 has no GET: what is on air is this Bridge's own list plus `show
messages cbs` on osmo-cbc's VTY (4264).

Every new row goes into `meshsat-cell/bench/TX-LOG.md` for the monthly RDI report
(`GET /api/cell/txlog?format=md`).

## The warning relay's 2G cell leg

`internal/warnings/relay.go`, `relayCell`. The relay already puts a verified digest on the mesh
(ALERT_APP and a text) and on APRS (a bulletin). The fourth output, while the kit's cell has its radio
keyed up:

- a **test** digest goes out on **4380** (a phone with "Test alerts" on shows it, marked as a test);
- a real **Extreme** digest on **4371 to 4374**, a real **Severe** one on **4375 to 4378**, the four of
  each by certainty (observed, else likely) and urgency (immediate, else expected), as the TS 23.041
  table lays them out; the allow-list refuses these until the owner clears them, and then the
  **ordinary channel 50** carries the warning instead (logged: the refusal row, then the channel 50
  row);
- a real **Moderate** or milder digest, which has no alert identifier, on channel 50;
- the text is the mesh text (the words, then the label "Relay of <sender> warning, issued <time> UTC
  via MeshSat, not an official NL-Alert"), at most 200 bytes, two or three pages;
- it repeats about once a minute (32 x 1.883 s) until the digest expires, 10 times at least, a day at
  most, 60 times when the digest names no expiry;
- an **Update** replaces the earlier broadcast under the same message code with the update number
  plus one (T7 tells whether the phone rings again);
- a **Cancel** takes the referenced broadcast off the air, radio up or not (osmo-cbc holds it either
  way).

The warning's row lists `cell` (or `cell-cancel`) among its bearers when the leg went. Each digest is
handled once (a seen digest is not relayed again), so the cell is asked once per digest like every
other bearer.

## API

| Route | What |
|---|---|
| `POST /api/cell/cbs/test` `{channel, text, language, broadcasts, period, purpose}` | the operator's test send through the same client: channel default 4380, 30 broadcasts every 9.4 s by default; 201 with what went on the air, 403 for a refused identifier, 409 with the radio down, 502 when osmo-cbc refuses, 503 without a cell |
| `GET /api/cell/cbs` | `live`, `on_air` (this Bridge's list), `allowed` (ordinary, test, real), `cbc` (osmo-cbc's VTY list) |
| `DELETE /api/cell/cbs/{channel}` | takes the channel's message off the air, with the closing row |
| `GET /api/cellular/broadcasts` | what the kit's modem received |
| `GET /api/cell/txlog?format=md` | the rows for TX-LOG.md |

## The owner's tests at the licensed site

Nothing below runs anywhere but inside the 500 m of the licence, with the owner present, a kit with
its Pluto, and a phone with one of the cell's test SIMs (1001 to 4004). Copy every new TX-log row into
`meshsat-cell/bench/TX-LOG.md` afterwards.

**Before any test**: `GET /api/cell/status` shows the core up and the radio active (auto key-up on
adoption), or `POST /api/cell/radio {"action":"start","purpose":"MESHSAT-1734 CBS tests T1-T7"}`.
On the phone: Settings > Safety & emergency (or Notifications) > Wireless emergency alerts > "Test
alerts" on (on a Pixel the toggle appears after dialling `*#*#2627#*#*`); and under its cell broadcast
settings subscribe to channel 50 for T2.

- **T1 forward**: a Hub test alert (CAP status Test/Exercise; the injection curl in the MESHSAT-1734
  comment) -> signed digest over IMT or MQTT -> the Bridge verifies (`warnings: digest taken` in the
  log) -> osmo-cbc on 4380 (`warnings: broadcast on the cell`, channel 4380; `GET /api/cell/cbs` lists
  it; a `cbs` row in the TX log) -> the phone shows it, marked as a test. The same without the Hub:
  `POST /api/cell/cbs/test {"channel":4380,"text":"TEST MeshSat relay on 4380, not an official
  alert","language":"en"}`.
- **T2 matrix**: the same on 4381 and 4355 and on channel 50; record per phone (Android, iPhone, a
  feature phone) which shows which, and the text length, pages and encoding that survive (a Greek
  text takes the UCS-2 path: `charset: ucs2` in the answer).
- **T3 limited service**: a phone with no SIM (TS 23.041 9.1.3.1: no USIM, warnings from any network
  are accepted) and "Test alerts" on, beside the cell: does it camp on ours and show 4380? This
  decides whether the cell reaches ordinary phones in an outage. Where KPN, VodafoneZiggo or Odido
  coverage exists a phone with its own SIM camps there; note the limit honestly.
- **T4 reverse**: our cell sends on 4380 or 50; the kit's own A7670E (or a spare modem with our SIM)
  receives it: `GET /api/cellular/broadcasts` shows it with the right severity, language, text and
  `complete`, and the relay to the mesh follows the rules. The receive fixes above are what this needs.
- **T5 cancel**: a CAP Cancel from the Hub stops the broadcast (`cbs-stop` row, `GET /api/cell/cbs`
  empty, the phone shows nothing new) and reaches the mesh. By hand: `DELETE /api/cell/cbs/4380`.
- **T6 real identifiers** (only as the clearance allows, step 0 on record, `MESHSAT_CELL_CBS_REAL_IDS`
  set to exactly those): the same relayed alert on 4370, 4371 to 4378, 4379 and ETWS 4352 to 4354;
  record per phone whether it shows, with which tone and override of silent mode, with our SIM, a
  foreign SIM and no SIM.
- **T7 update chain**: a CAP Update replaces the message on the same identifier (serial number: same
  message code, update number plus one) without a second alarm where 3GPP says it must not ring again.

What the owner's bench will decide that the code cannot: whether osmo-cbc 0.5.3 takes the
`payload_encoded` body (else the decoded fallback is what goes, `form: decoded` in the answer), what
the A7670E prints for a UCS-2 broadcast in text mode (hex is handled; anything else is stored as it
comes), and whether the A7670E takes the full-range `AT+CSCB` form (the log says
`the modem took no AT+CSCB form` when neither did).

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `MESHSAT_CELL_CBS_REAL_IDS` | `""` | real public-warning identifiers the cell may broadcast on, "4370,4371-4378", 4352 to 6399 only; set after step 0, never before |

The cell's other variables (`MESHSAT_CELL_AUTO_RADIO`, `MESHSAT_CELL_ARFCN`, `MESHSAT_CELL_LICENCE`,
`MESHSAT_CELL_AUTH`, `MESHSAT_CELL_SUBSCRIBERS`) are unchanged.
