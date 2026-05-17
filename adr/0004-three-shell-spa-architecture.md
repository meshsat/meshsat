# ADR-0004 — Three-shell SPA architecture: one Vue codebase serving Kiosk + Desktop browser + Android

* Status: Accepted — codified after the fact 2026-05-17. Decision committed 2026-04-18 (`EXECUTION-PLAN.md §0` decision #2 "Android Both mode v1" and the foundational §1 of `UX-MULTI-ACCESS-KIOSK-PAIRING.md`).
* Date: Originally decided 2026-04-18; recorded as ADR 2026-05-17 during the deep-spec audit.
* Deciders: `ufwtqkgz@meshsat.net`
* Source documents: `UX-MULTI-ACCESS-KIOSK-PAIRING.md` (especially §0, §1, §13), `EXECUTION-PLAN.md` §6.7–§6.9

## Context

The bridge has three concurrent access surfaces:

1. **Operators standing at a kit** want a touch UI on the lid of the case — a 7" Pi Touch Display 2 on a Pelican-cased Pi 5 in the field.
2. **Engineers at a desk** want a full keyboard + mouse browser experience for configuration, debugging, and ops-centre fleet oversight.
3. **Operators with the kit in their pocket** want a smartphone-shaped UI — paired Android phones, or browser-as-remote-control via webcam-scanned QR.

The naive answer (three separate UIs — a kiosk UI, a desktop UI, a mobile app UI) triples implementation and review cost, multiplies bug surface, and creates feature-parity drift the moment a single sprint ships one improvement to one shell.

The other naive answer (one UI but with two-of-three shells using a WebView) gives identity confusion ("which 'app' am I actually using?") and treats native capabilities as second-class.

## Decision

**One Vue 3 Single-Page Application** lives in `web/src/`. **Three shells** wrap it:

| Shell | Wrapper | Talks to backend via | Owns natively |
|---|---|---|---|
| **A — Kiosk** | labwc Wayland compositor + Chromium in `--kiosk --app=` mode on a `kiosk` user (NOT `pi`) | `http://localhost:6050` (no auth — physical access = trust) | systemd-user unit, Chromium policy lockdown, backlight, screen blanking, embedded simple-keyboard OSK |
| **B — Desktop browser** | Any Chrome/Firefox; PWA-installable | LAN HTTPS at `https://<bridge>:6050` over JWT + (optional) mTLS | `sessionStorage` for JWT (dies on tab close), non-extractable `CryptoKey` for client private key in IndexedDB |
| **C — Android** | Native Kotlin app — hybrid Compose + WebView | LAN mTLS first, Reticulum link fallback, Hub WS relay third tier (v1 ships all 3) | CameraX+ZXing pair scanner, Android Keystore for cert+JWT, native BLE/SMS bearers, foreground SSE service, native notifications |

The same Vue SPA is rendered by all three shells. Native code on each shell is the *minimum required* to bridge to that platform's particular capabilities (OS-level boot for kiosk; PWA install for browser; hardware-backed Keystore for Android).

## What stays in the SPA

- All views: Dashboard, Compose, Inbox, Map, People, Radios, Settings, Audit, Topology, Help, About.
- All API calls, all SSE subscriptions, all business logic.
- Theme (including NVIS Green A night-mode), routing, localization (Operator/Engineer i18n bundles).
- The on-screen keyboard component (`simple-keyboard` MIT 100 kB, gated on `pointer:coarse` + no hardware keyboard).

## What is native per-shell

| Capability | Kiosk | Browser | Android |
|---|---|---|---|
| Boot / launch | `systemctl --user meshsat-kiosk.service` | User opens URL | App icon |
| Storage of paired-bridge credentials | none (localhost) | `sessionStorage` JWT + IndexedDB CryptoKey | Android Keystore + Room DB `paired_bridges` |
| QR scan for pair-claim | n/a (kiosk shows QR, doesn't scan) | `getUserMedia()` + ZXing-JS | CameraX + ZXing |
| Background message delivery | n/a (full screen always) | n/a (browser-tab-bound) | foreground service holding SSE + local notifications |
| TLS termination + cert pin | Chromium's | Browser + IndexedDB CryptoKey | OkHttp `CertificatePinner` |
| Backlight control | sysfs `/sys/class/backlight/*` via sudoers-scoped tee | n/a | OS-managed |

## Consequences

**Positive**
- One UI codebase = one review surface, one test matrix, one deploy. A bug fix to Compose lights up on the kit lid, in the ops-centre browser, and on the operator's phone simultaneously.
- The PWA service worker caches the shell on every browser that ever loads it — operators lose signal and still see last-known state with an "Offline" banner.
- Android doesn't reimplement Settings in Compose — it wraps the SPA's `/settings` URL in a WebView with the bearer JWT injected. Every Settings change ships to Android automatically (Home Assistant Companion pattern).
- A *fourth shell* (browser-as-remote-control via `/pair/scan` page with webcam) is essentially free — same QR protocol, same SPA, no new code paths.
- Kiosk → Browser migration when an operator transitions to a desk is a URL change, not a context switch.

**Negative**
- The SPA has to be responsive across three breakpoints simultaneously: 400-wide (Android phone portrait), 1280×720 (kiosk landscape), and ≥1200 (desktop). The audit (`UX-MULTI-ACCESS-KIOSK-PAIRING.md §3`) identified 25 P0 fixes required — all in scope of Phase 7.
- Compositor on-screen keyboards (squeekboard / wvkbd) don't reliably stack above fullscreen Chromium on labwc (labwc issue #2926). Workaround: embed simple-keyboard inside the SPA — costs 100 kB JS, eliminates a real bug class.
- The kiosk Chromium creeps RAM ~24-72h in production. Mitigation: nightly `systemctl --user restart meshsat-kiosk.service`. Cheap; costs one SPA reload.
- Android hybrid means native Compose for hot paths (Compose/Inbox/Map/People) + WebView for Settings. Some duplication of mental model, but the schema and API shape match the Vue SPA exactly so there's no duplicate business logic.

**Forward direction**
- The 4th shell (browser-as-remote-control) is the natural extension and is ALREADY drafted (`UX-MULTI-ACCESS-KIOSK-PAIRING.md §6`).
- Pure-native Android (no WebView at all) is NOT a future direction — it would multiply maintenance for marginal native-feel gain. Stay hybrid.

## Alternatives considered

- **Three separate UIs** (kiosk UI, desktop UI, native Android UI): rejected — triples implementation, multiplies bug surface, guarantees feature drift.
- **Single native Android app + headless bridge** (no SPA at all): rejected — desktop ops-centre users have no UI; kit-lid operator has no UI without a paired phone.
- **Cog / WPE embedded browser instead of Chromium**: rejected for now — Chromium has enterprise-policy framework, hardware acceleration, and a working PWA install path. 8 GB Pi 5 has memory headroom for ~800 MB Chromium baseline. Reconsider only if RAM becomes a real problem.
- **Firefox kiosk mode**: rejected — Firefox kiosk is "very basic" per Mozilla's own docs; tabs and address bar can leak through; no enterprise-policy parity with Chromium.
- **labwc-OSK (squeekboard) instead of in-SPA JS OSK**: rejected — squeekboard under Chromium kiosk on labwc has open layering bug as of Q1 2026.

## Compliance

- The SPA MUST work at 720×1280 (kiosk landscape), at 400-wide (Android portrait), AND at ≥1200 (desktop) without per-shell branching of the Vue components.
- The 25 P0 SPA-hardening fixes in `UX-MULTI-ACCESS-KIOSK-PAIRING.md §3.1–§3.5` are required-merge for Phase 7 closure.
- Native Android code is allowed ONLY where the SPA cannot work: QR scanner, Keystore, background service, native bearers (BLE/SMS), notifications. Anything else MUST go in the Vue SPA.
- Chromium policy file `/etc/chromium/policies/managed/meshsat-lockdown.json` MUST restrict URLAllowlist to `http://localhost:6050/*` only; devtools/passwords/autofill/translate disabled.
- Embedded JS OSK auto-shows on `pointer:coarse` + no hardware-keyboard detected, NEVER on desktop.
- Each shell ships its own paired-bridge identity in its own storage; sessions are NOT shared across shells (kiosk = unauthenticated localhost; browser = sessionStorage JWT; Android = Keystore JWT).
