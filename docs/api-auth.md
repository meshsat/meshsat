# Securing the kit's API

The Bridge's HTTP API (port 6050) is bound on every interface of the host, and until MESHSAT-1738
it answered anyone who could reach the port: the operator dashboard, every setting, the send
endpoints, the host reboot. That is a design for a sealed kit on its own WiFi, and a blocker for a
kit on any network other people are on.

This page describes the API token. **It is off by default**: a kit that runs this code and has no
token set behaves byte for byte as before. Nothing on today's kits changes until an operator sets
one.

## The rules

With a token set, every request under `/api/` needs one of:

| How | What |
|---|---|
| A header | `Authorization: Bearer <token>` |
| The cookie | `meshsat_api_token`, which `POST /api/auth/login` sets (HttpOnly, `Path=/api`, `SameSite=Strict`). The SPA needs it because its event stream is an `EventSource`, which cannot carry a header. |
| The kit itself | A request from `127.0.0.1` or `::1` while `MESHSAT_API_TRUST_LOOPBACK` is `true` (the default). |
| The Hub relay | A request that arrived through the Hub's WebSocket relay (`internal/relayclient`). The Hub verified the phone's Hub-issued certificate before it opened the tunnel, and the tunnel is TLS end to end with the Bridge's own certificate, so the tunnel is the credential. Nothing on the socket can forge the mark: only the relay's own listener feeds the handler that sets it. |

A request with none of these gets `401 {"error":"api token required"}` and
`WWW-Authenticate: Bearer realm="meshsat"`.

What never needs the token:

- `/health` and `/metrics`. They are not under `/api/`. `/metrics` carries counters, not messages;
  if that is too much on a given network, firewall it.
- `GET /api/auth/status` (says whether a token is needed; it reveals nothing a 401 would not) and
  `POST /api/auth/login` (the door).
- The SPA's own files (`/`, `/assets/...`). The page loads, its first API call gets a 401, and the
  page shows the token field.
- The Community Post's portal. It is a separate listener (`MESHSAT_POST_LISTEN`), for strangers'
  phones by design, and this page does not touch it. Its operator side, `/api/post/*`, is behind
  the token like the rest.
- CORS preflights (`OPTIONS`). A browser sends them without credentials; they are answered before
  the token check, as before.

**The loopback rule and its trade-off.** With the trust on (the default), the kiosk browser on the
kit's own panel, the receipt printer, the backup scripts and the operator's `curl` on the kit's
shell keep working with no change, and so does the Bridge's own call to its API (the Hub's
Bluetooth and WiFi commands go through `127.0.0.1:6050`). The price: anyone with a shell on the kit
reaches the API without the token. On a kit that person is already next to the database and the
private keys in it, so the trust gives away nothing the token would protect. Two cases where it
does: a host where other people have accounts, and a reverse proxy on the kit itself in front of
port 6050 (every request then arrives from loopback, and the proxy's clients get in for free).
There, set `MESHSAT_API_TRUST_LOOPBACK=false`. The Bridge's own in-process calls still get in, on a
token that exists only in the process's memory; the kiosk then shows the token field once, like any
other browser, and keeps the token in its `localStorage`.

**CORS.** With no token, `Access-Control-Allow-Origin: *`, as it has always been. With a token, the
header is only sent back to the kit's own origin (the host name or address the browser reached the
kit by, port included); a page on any other origin gets no `Access-Control-Allow-Origin` at all, so
a browser refuses to hand it the answer, token or not.

**What the token is and is not.** It is one shared secret for the whole kit: there are no users,
roles or scopes. It is compared in constant time (both sides as SHA-256 digests). On a kit's plain
HTTP LAN it crosses the wire in clear, like everything else on that LAN; the Hub relay is TLS, the
Community Post is on its own listener, and a kit exposed beyond its own LAN should sit behind TLS
in any case. The token is never written to a log, and the Bridge never keeps it: `system_config`
holds its SHA-256 under `api_token_sha256`, nothing more.

## Setting it

Two ways; the environment wins when both are present.

**In the environment** (the kits: `/srv/meshsat/.env` and the compose file, both kits byte for
byte, as every `MESHSAT_*` variable):

| Variable | Default | Purpose |
|---|---|---|
| `MESHSAT_API_TOKEN` | `""` (off) | The token. Empty = the API is open; the Bridge then also reads `system_config`. |
| `MESHSAT_API_TRUST_LOOPBACK` | `true` | Requests from `127.0.0.1` / `::1` need no token. Only read while a token is set. |

While the environment sets the token, `PUT` and `DELETE /api/auth/token` answer `409`: change the
environment and recreate the container.

**Through the API**, from the kit itself or by a caller that already has the token:

```
# on the kit: make a token (returned once, kept nowhere but as a hash)
curl -s -X PUT http://localhost:6050/api/auth/token
# {"token":"<43 characters>","source":"config"}

# or bring your own (16 characters or more, no spaces)
curl -s -X PUT http://localhost:6050/api/auth/token -H 'Content-Type: application/json' -d '{"token":"..."}'

# rotate from anywhere, with the old token
curl -s -X PUT http://kit:6050/api/auth/token -H 'Authorization: Bearer <old>'

# clear it: the API is open again
curl -s -X DELETE http://kit:6050/api/auth/token -H 'Authorization: Bearer <token>'
```

It applies from the next request on, no restart. While the API is still open, the first `PUT` is
refused from the LAN (`403`): a stranger on the network must not be able to lock the owner out of
an open kit. It is taken from the kit itself (loopback) or through the Hub relay. Every set and
clear is an audit entry (`api_token_set`, `api_token_cleared`, with the remote address and how the
caller got in; never the token).

| Route | What |
|---|---|
| `GET /api/auth/status` | `required`, `source` (`env` or `config`), `loopback_trusted`, `authenticated`, `via` (`bearer`, `cookie`, `loopback`, `relay`, `self`, `open`), `refused_total` |
| `POST /api/auth/login` | The token in the body (`{"token":"..."}`) or as the Bearer; sets the cookie. `204`, or `401`. |
| `POST /api/auth/logout` | Clears the cookie. The token stays. |
| `PUT /api/auth/token` | Set or rotate. Body `{"token":"..."}` or empty to generate. The new token is in this response and nowhere else. |
| `DELETE /api/auth/token` | Clear. |

## Who calls the API, and what the token means for each

| Caller | From | With the token set |
|---|---|---|
| The kiosk browser on the panel (`http://localhost:6050/?kiosk=1&shell=operator`) | the kit, loopback | Works unchanged with the loopback trust on. With it off it shows the token field once. |
| The SPA in a laptop's or phone's browser | the LAN | Shows the token field on the first 401; keeps the token in `localStorage` (`meshsat.apiToken`), sends it as the Bearer, and logs in for the cookie its event stream needs. |
| MeshSat Android and iOS through the Hub relay | the Hub's tunnel | Nothing to do: the tunnel is the credential. |
| MeshSat Android and iOS on the LAN (direct) | the LAN | Need the token. **Neither app has a field for it yet**: that is a follow-up in `meshsat-android` and `meshsat-ios` (Settings > Bridge: an API token field, sent as the Bearer). Until then a LAN-connected app meets a kit with a token only through the Hub relay. |
| The Bridge's own Hub-command shim (`internal/hubreporter/sysmgmt.go`, Bluetooth and WiFi commands from the Hub) | the Bridge itself, `127.0.0.1:6050` | Works: it presents a process-local token whatever the loopback setting. |
| The receipt printer (`deploy/printer/meshsat-receipt-printer`, `MESHSAT_BRIDGE_URL=http://127.0.0.1:6050`) | the kit | Works with the loopback trust on. It follows only its own kit's `/api/events`; its peer link (`MESHSAT_PRINTER_PEER`) is the slip counter on 6051, not the API. |
| The kiosk launcher (`deploy/kiosk/labwc-autostart`) | the kit | Only polls `/health`, which is open. |
| `deploy/backup.sh`, `deploy/restore.sh` | the kit by default (`MESHSAT_URL`) | Work on the kit with the trust on; from elsewhere add `-H 'Authorization: Bearer ...'` (and note the `/api/backup/*` routes they call are not in this Bridge). |
| `scripts/aprs-linktest.sh` | `ssh` into the kit, then `localhost:6050` | Works with the trust on. |
| `scripts/oob-e2e-kits.py`, `scripts/e2e_validate.sh` | the runner, over the LAN | Need the token: an `Authorization` header on their requests (`BRIDGE_TOKEN` env is the natural shape; not done here). |
| The OOB host agent (`deploy/oob/meshsat-oob-agent`) | n/a | Does not call the API: the Bridge calls it, over `/run/meshsat-oob/agent.sock`. |
| The device-health watchdog | n/a | In-process; no HTTP. |
| The Rock7 and Twilio webhook receivers (`/api/webhook/rockblock`, `/api/webhooks/inbound`) | the internet | Behind the token like the rest, and those services cannot send a header. Neither is used on a kit (kits sit behind NAT; the Hub takes those webhooks). A kit that must take one directly is a follow-up. |

## Checking it on a kit

1. In `/srv/meshsat/.env` on the kit: `MESHSAT_API_TOKEN=<43 random characters>` (for instance
   `openssl rand -base64 32 | tr '+/' '-_' | tr -d '='`); the same two variables in both kits'
   compose files, then `docker compose up -d` (never while a deploy runs).
2. The Bridge log says `api token required on /api` with `source=env`.
3. From the LAN: `curl -si http://<kit>:6050/api/status` answers `401`;
   `curl -si -H 'Authorization: Bearer <token>' http://<kit>:6050/api/status` answers `200`;
   `curl -si http://<kit>:6050/health` answers `200` either way.
4. On the kit: `curl -si http://localhost:6050/api/status` answers `200` (loopback trusted); the
   panel still shows the dashboard; `docker logs` shows no 401 from `127.0.0.1`.
5. `GET /api/auth/status` from the LAN without a token: `{"required":true,"source":"env",
   "loopback_trusted":true,"authenticated":false,"refused_total":1}`.
6. A browser on the LAN: the page loads, the token field appears, the token unlocks it, and the
   event stream (the dashboard's live counters) runs after the reload.
7. A phone through the Hub relay (`kit-relay-e2e.py` in meshsat-hub) reaches `/health` and
   `/api/status` with no token.
