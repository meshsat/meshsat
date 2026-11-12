# The public demo

A public demonstration of the Bridge, funded as budget line 3 of the SIDN fonds project (a
reachable Bridge demo that the kit offers and the getting-started guide link to). Two names,
one small process:

| Name | What a visitor gets |
|---|---|
| `bridge.meshsat.net` | The Bridge's own web app, the release bundle the field kits run, over made-up data that always reads as now. A banner says so on every page. |
| `app.safertogether.eu` | The Community Post's phone page (the page a kit offers on its open WiFi), in its demo mode, in English, German, Dutch or Greek. |

Nothing is ever sent anywhere. There are no radios, no modem, no Hub link.

## What runs

`cmd/meshsat-demo`, built by `Dockerfile.demo` into `ghcr.io/meshsat/meshsat:demo-<commit>`
(amd64, distroless, non-root). It is not the Bridge binary and does not contain the Bridge's
API: `cmd/meshsat-demo/deps_test.go` fails the build if it ever links `internal/api`,
`internal/oob`, `internal/relayclient`, `internal/hubreporter`, `internal/pair`,
`internal/federation` or `internal/hubroute`.

- **The web app** (`internal/demo`): `/api/*` is answered from `internal/demo/fixtures`, one
  JSON file per path. Every time in them is moved to now on each request, and the Iridium
  passes are made for the next day, so the dashboard never goes stale. Anything but a read is
  refused with 405 and the header `X-MeshSat-Demo: 1`; a small script put in around the app
  turns that into a short notice. The service worker is not installed. The event streams answer
  204.
- **The phone page** is the real `internal/post` with `Config.Demo` on: a notice on every page,
  a demo volunteer that approves a message after about 20 seconds, a demo satellite that sends
  it a minute later, and an automatic reply, in the page's language, that says it is automatic.
  Everything a visitor writes, the safe-and-well board included, is deleted after an hour; the
  database lives on a memory volume and is empty after every restart. The whole post takes at
  most 60 messages and 30 board lines per 10 minutes and keeps at most 500 rows. One address
  gets at most 6 messages and 6 board lines an hour, 10 unknown pickup codes and 30 name
  searches, whatever cookie it sends.
- **The volunteer screen** (`/post` on `bridge.meshsat.net`) shows the real queue, masked: a
  number as its country code and last two digits (an address as its first letter and its
  top-level domain), no name, the text as its length, and in place of the code a label that
  holds no letter of it. Board lines show initials only. A visitor's words and numbers never
  appear there, and nothing on it opens anyone's pickup page.

## The data

`scripts/demo-fixtures.py` builds `internal/demo/fixtures` from the accessibility run's synthetic
fixtures (`test/a11y/fixtures/rich`) and adds one fictional story: a sea rescue exercise off the
coast, a support vessel carrying the Bridge, two rescue boats, a swimmer team, a command boat
and a drift buoy. `test/a11y/fixtures.test.mjs` holds both sets to the same rules: positions in
open water, phone numbers from the +44 7700 900xxx drama range, no real ids. Warnings in it are
marked TEST and the SOS is never active.

```sh
python3 scripts/demo-fixtures.py
node --test test/a11y/fixtures.test.mjs
go test ./internal/demo/ ./cmd/meshsat-demo/
```

## Where it runs

`k8s/demo/` is synced by Argo CD. One pod in its own namespace:

- PodSecurity `restricted` enforced, its own service account with no token and no role;
- read-only root filesystem, every capability dropped, a memory volume for the database;
- no network out at all: a Kubernetes NetworkPolicy denies both directions and a Cilium policy
  allows only the cluster's ingress controller (and the kubelet's probes) in, and denies all
  egress, DNS included;
- requests per visitor limited at the ingress; the phone page caps every form at 8 KB.

The edge serves both names from the EU edge servers only, and the pipeline pins each new
build's digest in `k8s/demo/kustomization.yaml` (`bump_demo_pin`, a `[skip ci]` commit, so no
field kit is redeployed for it).

## Checking it

- `curl -s https://bridge.meshsat.net/healthz` answers `{"status":"healthy","demo":true}`.
- A write is refused: `curl -si -X POST https://bridge.meshsat.net/api/messages/send` answers
  405 with `X-MeshSat-Demo: 1`.
- On a phone, `https://app.safertogether.eu/` takes a message, shows a code, and the pickup page
  goes from waiting for the volunteer to waiting for the satellite to the automatic reply within
  about three minutes.
