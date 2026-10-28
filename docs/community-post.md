# Community Post

The Community Post is a kit's no-app front door. Any phone joins the kit's open WiFi, gets a
captive-portal page, writes a short message to a phone number or an email address, and the message
leaves the kit over satellite through the MeshSat Hub's "@" lane. The person's reply comes back the
same way and waits on the kit under a six-character pickup code. Nothing is installed on the phone,
nothing is kept on it but a screenshot of the code.

Phase 0, MESHSAT-1733. **Everything here is off by default**: a kit that runs this code behaves
exactly as before until `MESHSAT_POST_LISTEN` is set and an operator runs `post-up.sh`.

## What it is for

A square, a shelter, a village hall after a storm or a blackout: people have phones, the phones have
no network, and the kit has a satellite link. The post lets them tell someone they are safe, or ask
for something, without an app, an account or a working carrier. The approval queue lets the
operator keep the satellite credit for what matters.

## The flows

**Write.** The phone joins the SSID (`MeshSat Post`, open). The phone's own captive-portal check
opens the portal (Android, iOS, Windows, Ubuntu, Firefox and RFC 8908 clients are all answered).
The page asks for a recipient (a number with its country code, or an email address), an optional
name, the message (at most 150 characters, no links), and a checkbox for an emergency. It is plain
HTML in Greek, Dutch and English (from `Accept-Language`, switchable at the top of every page), no
script, no outside asset, forms that POST.

**Code.** The confirmation page shows the pickup code large, with the instruction to screenshot it.
With the approval queue on (the default) it also says the operator checks every message first.

**Approve.** The operator sees the queue on the kit's own screen (`/post` in the Bridge UI, also in
the kiosk shell) and approves or refuses each post. `auto_approve` in the settings turns the queue
off.

**Send.** An approved post becomes ONE text in the "@" lane's form, queued through the delivery
ledger on the kit's satellite link as a plain-class row (verbatim, outside the kits' sealed chain,
as the lane requires):

```
@+31612345678 Maria: water at the north gate [#K7QW2X]
@+31612345678 SOS Maria: trapped, second floor [#K7QW2X]     (an emergency)
@ops@example.org Nikos: need fuel [#ABCDEF]                   (an email address)
```

The name is optional (`@+31612345678 water at the north gate [#K7QW2X]`). An emergency is queued
Flash and Critical (first in the queue, never expires). The delivery is keyed by the code, so
approving twice sends once.

**Reply.** The Hub, with the kit's modem marked as a post device, keys the conversation by
tenant, modem, address AND the code, so two people at one post writing to the same number never
collide, and delivers the person's answer as:

```
[from +31612345678] #K7QW2X on my way, 20 min
```

The Bridge parses that form (the parser MESHSAT-1692 said was missing) off the satellite link only,
stores the reply under the code, and the pickup page shows it. A reply that names no code goes to
the latest open post to that address. A reply for a code the kit never gave out is dropped without
a word. The Hub's own notice, `[MeshSat Hub] not sent to <address>: <reason>`, marks the latest post
to that address failed and shows the reason on the pickup page.

**Pick up.** The person comes back to the WiFi, opens any page, enters the code (case and spaces
do not matter) and reads the status and the replies. A phone whose browser kept the session cookie
also sees its own posts listed; iOS's captive assistant keeps none, so the code is the real state.

**Safe and well.** A separate, opt-in board: name, status, place; searchable by name; every line
expires after 30 days (`MESHSAT_POST_BOARD_DAYS`). Nothing on it is sent anywhere.

**About.** The operator's name and the data notice (below).

**Close.** When the post is over, the operator closes it (`POST /api/post/close`, the button on
`/post`): the portal takes no new message and no board line and says so, with the day everything is
deleted; replies can still be picked up with a code until then. `MESHSAT_POST_PURGE_DAYS` (30) after
closing, the service's own sweep (every 15 s, in its `Run` loop) purges every post, reply, board line
and per-phone counter in one transaction, once per closing. `POST /api/post/purge` with
`{"confirm": true}` purges now, open or closed; `POST /api/post/open` reopens a closed post and cancels
the purge it scheduled. The board's own 30-day expiry per line is unchanged by any of this. What the
purge does NOT touch: the delivery ledger's rows for the sent texts (`message_deliveries`,
`delivery_submissions`), which are the Bridge's own record and follow its retention
(`MESHSAT_RETENTION_DAYS`, 30); the notice says so.

**Slips.** With `MESHSAT_POST_PRINT=true` and the booth's receipt printer service on the kit
(`scripts/install-receipt-printer.sh`, `deploy/printer/meshsat-receipt-printer`, the only ESC/POS
code there is), an approved post prints a slip: the pickup code double height, the recipient masked
(`+3161****78`, `m***@example.org`), the time (UTC), the operator's name, the portal URL as text and
the pickup page with the code as a QR. A reply prints a second slip with the code and the reply's
first 100 characters; the Hub's "not sent" notice prints as a reply too. The Bridge only says what
goes on the paper, as a `post_slip` event on `/api/events`; the service lays it out (English only, the
paper speaks CP437) and takes no number from the booth sequence for it, so no hat. The service reports
itself on `PUT /api/post/printer` (which printer, where the sequence is, how the last slip went, every
60 s and after each slip); `/post` shows it, and a report older than three minutes shows as no printer.
Approving twice prints once. **The service file is hand-installed on the kits and does not ship with
the image: a kit prints post slips only after `install-receipt-printer.sh` has placed this version.**

## Switching it on

Three deliberate steps, each reversible.

1. **The Bridge.** In the kit's `.env`: `MESHSAT_POST_LISTEN=0.0.0.0:8081` (the portal listener
   inside the container; it serves only the portal, never the API) and `MESHSAT_POST_OPERATOR="..."`.
   In BOTH kits' `/srv/meshsat/docker-compose.yml`, byte for byte: the new variables and a port
   published on the host's loopback only, `127.0.0.1:8081:8081`. Recreate the container. The API's
   `GET /api/post/config` answers 200 (503 before).
2. **The host.** `sudo scripts/install-post.sh` places hostapd, dnsmasq, nftables and the four
   units `meshsat-post-{net,ap,dns,fw}` DISABLED (the units have no `[Install]` section, so they
   cannot start at boot by accident) and writes `/etc/default/meshsat-post` once. It does not touch
   `/etc/netplan`, `wlan0` or the container.
3. **Open the post.** `sudo scripts/post-up.sh` checks the dongle is there, the P2P bearer is not
   using it, and the portal listener answers, then starts the four units. `sudo scripts/post-down.sh`
   closes it.

The host side runs on the MT7612U USB dongle (`wlx*`), the same dongle as the kit-to-kit P2P bearer:
one or the other, and `post-up.sh` refuses while `meshsat-p2p-link` is active. The kit's own `wlan0`
is the only management path to a sealed box and is never named in any file here. The AP is 2.4 GHz
(`MESHSAT_POST_CHANNEL`, default 6), country from `MESHSAT_POST_COUNTRY`, the radio capped at
`MESHSAT_POST_TXPOWER` (15 dBm; the dongle's antennas add about 5 dBi against the Dutch 20 dBm EIRP).

Network: `10.44.0.0/24`, the kit at `10.44.0.1`, DHCP `.10` to `.200`, two-hour leases, every DNS
name answers `10.44.0.1`, DHCP option 114 names the RFC 8908 API, no IPv6, no router advertisements.
The nftables table `meshsat_post` allows 53, 67 and 80 in on the post's interface, DNATs 80 to the
Bridge's loopback-published listener (`route_localnet` on the interface), and forwards nothing. `nmap`
from a phone should show 53 and 80 only.

### Environment variables (all off or harmless by default)

| Variable | Default | Meaning |
|---|---|---|
| `MESHSAT_POST_LISTEN` | `""` (off) | The portal listener inside the container, e.g. `0.0.0.0:8081` |
| `MESHSAT_POST_URL` | `http://10.44.0.1/` | Where the captive detection sends a phone |
| `MESHSAT_POST_OPERATOR` | `""` | The operator's name on the about page (also settable through the API) |
| `MESHSAT_POST_BEARER` | `auto` | `auto` = the kit's satellite link (`iridium_imt_0`, else `iridium_0`), or an interface id |
| `MESHSAT_POST_PER_HOUR` | `3` | Posts per phone per hour (0 = none; also through the API) |
| `MESHSAT_POST_PER_DAY` | `10` | Posts per phone per day (0 = none; also through the API) |
| `MESHSAT_POST_MAX_CHARS` | `150` | Characters a post may carry |
| `MESHSAT_POST_MAX_BYTES` | `240` | Bytes a post may carry (what an SBD MO leaves after the head) |
| `MESHSAT_POST_BOARD_DAYS` | `30` | Days a safe-and-well line stays |
| `MESHSAT_POST_AUTO_APPROVE` | `false` | The approval queue off (also through the API) |
| `MESHSAT_POST_PURGE_DAYS` | `30` | Days after the operator closes the post before everything it stored is purged |
| `MESHSAT_POST_PRINT` | `false` | An approved post and a reply go to the kit's receipt printer as `post_slip` events |

Host side, `/etc/default/meshsat-post`: `MESHSAT_POST_IFACE` (empty = first `wlx*`; never
`wlan0`), `MESHSAT_POST_ADDR` (`10.44.0.1/24`), `MESHSAT_POST_SSID`, `MESHSAT_POST_CHANNEL`,
`MESHSAT_POST_COUNTRY`, `MESHSAT_POST_TXPOWER`, `MESHSAT_POST_BACKEND` (`127.0.0.1:8081`),
`MESHSAT_POST_DHCP_FROM`/`_TO`.

### The operator API (the Bridge's own API, Swagger-annotated)

`GET /api/post/config` (the configuration, the settings, the state: `closed`, `closed_at`,
`purge_at`, `purged_at`, `purge_days`; `print` and `printer`, the receipt printer's last report with
`heard` and its age), `PUT /api/post/config` (`auto_approve`, `posts_per_hour`, `posts_per_day`,
`operator`), `GET /api/post/queue` (pending, recent, counts), `GET /api/post/{code}`,
`POST /api/post/{code}/approve`, `POST /api/post/{code}/refuse`, `POST /api/post/close`,
`POST /api/post/open`, `POST /api/post/purge` (`{"confirm": true}`, 400 without), `PUT /api/post/printer`
(the receipt printer service's report). All answer 503 while the post is not configured.

### The Hub side

The kit's modem is marked a post device on the Hub (`profile: "post"` on the device, through
`PUT /api/devices/{imei}`). For that device alone the lane runs open (any number or address, whatever
the tenant's mode), keys each conversation by the `[#code]` the kit appends, and puts `#<code>` in
front of every reply. Every other device keeps the `[from <address>] <text>` form. The contract is
in meshsat-hub `docs/bridge-hub-protocol.md`, section "Post devices".

## Cost

The plan's figures: a busy post, about 300 messages out and 150 replies a day, costs roughly
USD 20 to 30 a day on Iridium IMT (9704) and USD 135 to 270 a day on SBD (9602/9603). So posts ride
IMT where the kit has it; SBD is for an emergency. The per-phone ceilings (3 an hour, 10 a day), the
150-character cap and the approval queue are what keep a post affordable; the Hub adds its own
ceilings (20 an hour per device, 10 new people a day per device, 120 an hour per tenant) and a
reply budget per conversation.

## Data notice (as shown on the portal)

The two numbers are `MESHSAT_POST_PURGE_DAYS` and `MESHSAT_POST_BOARD_DAYS` (both 30); a closed post
adds the day everything is deleted.

**English.** Kept on this post: your message, the address you wrote to, the name you gave, the
reply, and a counter per phone so one phone cannot use everything. Deleted 30 days after the operator
closes the post, or sooner by the operator; the kit's send log keeps the sent text under its own
retention. Sent onward: only the message, your name and the pickup code. The safe-and-well board is
separate, holds only what you put on it yourself, and a line stays 30 days. No account, no app, no
tracking. The legal ground is the protection of your or another person's vital interests; the
operator named above is responsible for the data.

**Nederlands.** Bewaard op deze post: je bericht, het adres waar je naartoe schreef, de naam die
je opgaf, het antwoord, en een teller per telefoon zodat een telefoon niet alles kan opmaken. Gewist
30 dagen nadat de beheerder de post sluit, of eerder door de beheerder; het verzendlogboek van de kit
bewaart de verzonden tekst volgens zijn eigen termijn. Doorgestuurd: alleen het bericht, je naam en
de ophaalcode. Het bord Veilig en wel staat los hiervan, bevat alleen wat je er zelf op zet, en een
regel blijft 30 dagen staan. Geen account, geen app, geen tracking. De grondslag is de bescherming
van jouw vitale belangen of die van een ander; de hierboven genoemde beheerder is verantwoordelijk
voor de gegevens.

**Ελληνικά.** Φυλάσσονται σε αυτό το σημείο: το μήνυμά σου, η διεύθυνση στην οποία έγραψες, το
όνομα που έδωσες, η απάντηση, και ένας μετρητής ανά τηλέφωνο ώστε να μην εξαντλεί ένα τηλέφωνο τα
πάντα. Διαγράφονται 30 ημέρες αφού ο διαχειριστής κλείσει το σημείο, ή νωρίτερα από τον διαχειριστή·
το αρχείο αποστολών της συσκευής κρατά το σταλμένο κείμενο για τον δικό του χρόνο. Προωθούνται: μόνο
το μήνυμα, το όνομά σου και ο κωδικός παραλαβής. Ο πίνακας Είμαι καλά είναι ξεχωριστός, περιέχει μόνο
ό,τι βάζεις εσύ, και κάθε γραμμή μένει 30 ημέρες. Χωρίς λογαριασμό, χωρίς εφαρμογή, χωρίς
παρακολούθηση. Η νομική βάση είναι η προστασία ζωτικών συμφερόντων, δικών σου ή άλλου· υπεύθυνος για
τα δεδομένα είναι ο διαχειριστής που αναφέρεται παραπάνω.

**Closed (shown on the write page, the board and the About page).** This post is closed. No new
message can be written here. A reply can still be picked up with your code until `<day>`; after
that everything is deleted.

## Open items (from the plan; not claims)

- **GDPR.** The ground is Art. 6(1)(d), vital interests; the organisation running the post is the
  controller and MeshSat the processor; a DPIA is likely needed (a lawyer's call, not ours). The
  30-day retention follows Person Finder's delete-after-crisis rule; the operator action it needs
  exists since the close and purge endpoints (above), and the notice says what they do.
- **McFadden (C-484/14).** An open WiFi's operator is not liable for what passes, but may be
  ordered to secure it; the post forwards nothing and runs no internet, which is the position to
  argue from. Not reviewed by a lawyer.
- **The approval queue is a human in the loop, not an abuse filter.** The link refusal is a word
  list; the ceilings are per phone (per DHCP lease, or per cookie where the browser keeps one).
- **Not proven on hardware.** Nothing in this page has run on a kit: the portal has been exercised
  with `httptest` only, the host files have been syntax-checked only, and no phone has joined the
  SSID. The MT7612U's AP mode and the DNAT-to-loopback path are the two things to prove first.
- **Receipt slips** are wired (`MESHSAT_POST_PRINT`, above) but have not printed on paper: the
  Python side is checked against a fake printer, the Bridge side against a recording printer, and the
  two meet only through the `post_slip` event's fields. The first real slip is a kit test.
- **The delivery ledger** keeps the sent text of a post (the composed "@" line) under the Bridge's
  retention, not the post's: the purge removes the `post_*` rows only. Honest in the notice; a ledger
  purge by source would be a change to the ledger's own history rules.
- **One satellite link per post.** A post leaves on the kit's satellite link only; there is no SMS
  leg, because the Hub's lane takes satellite MOs alone (its provenance rule).

## Proving it on a kit (no hands needed up to the WiFi step)

1. With the Bridge recreated as above: `curl -s http://127.0.0.1:8081/.well-known/captive-portal`
   on the kit answers `{"captive":true,"user-portal-url":"http://10.44.0.1/",...}`;
   `curl -si http://127.0.0.1:8081/generate_204 | head -1` answers `302`; `curl -s http://127.0.0.1:8081/ | grep -c '<form'`
   answers `1`; `curl -s 'http://127.0.0.1:8081/?lang=el' | grep -c 'Κοινοτικό'` answers `1`.
2. Write a post from the kit itself:
   `curl -s -c /tmp/msp -b /tmp/msp -d 'to=%2B31612345678&name=Test&text=hello+from+the+post' 'http://127.0.0.1:8081/send?lang=en' | grep -o 'class="code">[A-Z2-9]*'`;
   `curl -s http://127.0.0.1:6050/api/post/queue` lists it under `pending`.
3. Approve it: `curl -s -X POST http://127.0.0.1:6050/api/post/<code>/approve`; the delivery queue
   (`GET /api/deliveries?status=queued`) shows one plain-class row on the satellite link with the
   text above. **This is where the spend starts**: the row leaves at the next satellite session, the
   Hub sends an SMS from the tenant's number, and the owner's go is needed before it.
4. The reply: answer the SMS with `#<code> ok`; the Hub's MT reaches the kit;
   `curl -s http://127.0.0.1:6050/api/post/<code>` shows it under `replies`, and the pickup page shows
   it too.
5. The WiFi, with hands: `sudo scripts/install-post.sh`, `sudo scripts/post-up.sh`, a phone joins the
   SSID, the portal opens by itself (Android via `generate_204`, iPhone via the captive assistant),
   `nmap 10.44.0.1` from the phone shows 53 and 80 only, and the code reads from a screenshot.
