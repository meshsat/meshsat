#!/usr/bin/env python3
"""Build the public demo's made-up API answers (internal/demo/fixtures).

The demo (cmd/meshsat-demo, bridge.meshsat.net) serves the Bridge's web app
over these. They start from the accessibility harness's synthetic fixtures
(test/a11y/fixtures/rich) and tell one fictional story on top: a sea rescue
exercise off the coast, a support vessel carrying the Bridge, two rescue
boats, a swimmer team, a command boat and a drift buoy. Every rule of
test/a11y/fixtures.test.mjs holds for them too (positions in open water,
+44 7700 900xxx numbers, !a11ceNNN node ids); the demo server moves every
time in them to now at each request, so they are written at the fixture
epoch, 2026-11-01T10:00:00Z.

Run from the repo root: python3 scripts/demo-fixtures.py  [MESHSAT-1845]
"""
import json
import math
import os
import shutil
from datetime import datetime, timedelta, timezone

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, 'test/a11y/fixtures/rich')
DST = os.path.join(ROOT, 'internal/demo/fixtures')
EPOCH = datetime(2026, 11, 1, 10, 0, 0, tzinfo=timezone.utc)


def iso(minutes_ago):
    return (EPOCH - timedelta(minutes=minutes_ago)).strftime('%Y-%m-%dT%H:%M:%SZ')


def unix(minutes_ago):
    return int((EPOCH - timedelta(minutes=minutes_ago)).timestamp())


# The fleet. num is the node number, user_id its !hex form.
NODES = [
    # user_id, short, long, hw, role, lat, lon, minutes since heard, battery, snr
    ('!a11ce001', 'SV01', 'Support Vessel', 'T_ECHO', 7, 52.052, 3.948, 1, 101, 0),
    ('!a11ce002', 'RB01', 'Rescue Boat 1', 'T_DECK_PRO', 102, 52.081, 3.962, 2, 84, 6.5),
    ('!a11ce003', 'RB02', 'Rescue Boat 2', 'T_DECK_PRO', 102, 52.034, 3.971, 3, 77, 4.0),
    ('!a11ce004', 'SWIM', 'Swimmer Team', 'T_ECHO', 7, 52.074, 3.957, 4, 63, 2.75),
    ('!a11ce005', 'CMD1', 'Command Boat', 'T_ECHO', 7, 52.061, 3.931, 2, 92, 7.25),
    ('!a11ce006', 'BUOY', 'Drift Buoy', 'T_ECHO', 7, 52.088, 3.979, 9, 55, -3.5),
]
OWN = NODES[0][0]


def node_num(uid):
    return int(uid[1:], 16)


def nodes():
    out = []
    for uid, short, long_, hw, hwm, lat, lon, ago, batt, snr in NODES:
        n = {
            'num': node_num(uid), 'user_id': uid, 'short_name': short, 'long_name': long_,
            'hw_model': hwm, 'hw_model_name': hw, 'role': 1 if uid != OWN else 0,
            'latitude': lat, 'longitude': lon, 'altitude': 2, 'sats': 9,
            'last_heard': unix(ago), 'last_heard_str': iso(ago), 'last_readable_at': iso(ago),
            'battery_level': batt, 'voltage': round(3.4 + batt / 100 * 0.8, 3), 'snr': snr,
            'channel_util': 3.1, 'air_util_tx': 1.2, 'other_mesh': False,
            'firmware_version': '2.8.0.47db0e3', 'is_favorite': uid in ('!a11ce002', '!a11ce003'),
            'signal_quality': 'GOOD' if snr > 0 else ('FAIR' if snr > -5 else 'BAD'),
            'uptime_seconds': 3600 * 5,
        }
        if uid != OWN:
            n['hops_away'] = 0 if snr > 0 else 1
        out.append(n)
    return {'count': len(out), 'nodes': out}


def track(uid, start, end, points=14, bend=0.004):
    """A smooth curved track from start to end, one fix every 3 minutes,
    the newest last (the node's current position is end)."""
    out = []
    for i in range(points):
        f = i / (points - 1)
        lat = start[0] + (end[0] - start[0]) * f + math.sin(f * math.pi) * bend
        lon = start[1] + (end[1] - start[1]) * f + math.sin(f * math.pi * 2) * bend / 2
        ago = (points - 1 - i) * 3 + 1
        out.append({'node_id': uid, 'latitude': round(lat, 5), 'longitude': round(lon, 5), 'altitude': 2,
                    'ground_speed': 6 if f < 1 else 0, 'ground_track': 40, 'sats_in_view': 9,
                    'created_at': iso(ago)})
    return out


def positions(rich):
    by = {n[0]: n for n in NODES}
    p = []
    p += track('!a11ce002', (52.046, 3.941), (by['!a11ce002'][5], by['!a11ce002'][6]))
    p += track('!a11ce003', (52.050, 3.950), (by['!a11ce003'][5], by['!a11ce003'][6]), bend=-0.003)
    p += track('!a11ce005', (52.055, 3.915), (by['!a11ce005'][5], by['!a11ce005'][6]), points=10, bend=0.002)
    p += track('!a11ce006', (52.083, 3.966), (by['!a11ce006'][5], by['!a11ce006'][6]), points=12, bend=0.0008)
    # Keep the TAK crews of the rich set: the map shows them as diamonds.
    p += [x for x in rich['positions'] if str(x.get('node_id', '')).startswith('TAK-CREW')]
    for i, x in enumerate(sorted(p, key=lambda x: x['created_at'], reverse=True)):
        x['id'] = 9000 - i
    return {'node_id': '', 'positions': sorted(p, key=lambda x: x['created_at'], reverse=True)}


CHAT = [
    # minutes ago, from, to, text, direction
    (42, OWN, '^all', 'All units: exercise start. Check in on the primary channel.', 'tx'),
    (40, '!a11ce002', '^all', 'RB1 checked in, 3 aboard, fuel 90 percent.', 'rx'),
    (39, '!a11ce003', '^all', 'RB2 checked in, 4 aboard, fuel 80 percent.', 'rx'),
    (37, '!a11ce005', '^all', 'Command on scene. RB1 search sector north, RB2 sector south.', 'rx'),
    (30, '!a11ce006', '^all', 'Buoy deployed, drift reporting every 3 minutes.', 'rx'),
    (22, '!a11ce005', '^all', 'Casualty dummy sighted 300 m east of the buoy.', 'rx'),
    (21, '!a11ce002', '^all', 'RB1 heading to casualty, ETA 4 minutes.', 'rx'),
    (16, '!a11ce004', '^all', 'Swimmer in the water, line attached.', 'rx'),
    (12, '!a11ce002', '^all', 'Casualty recovered, conscious, returning to the support vessel.', 'rx'),
    (11, OWN, '^all', 'Copy. Shore informed over satellite.', 'tx'),
    (6, '!a11ce003', '^all', 'RB2 sector south clear, nothing found.', 'rx'),
    (3, '!a11ce005', '^all', 'All units return to the support vessel for debrief.', 'rx'),
]


def messages(rich):
    out = []
    for i, (ago, frm, to, text, d) in enumerate(CHAT):
        out.append({
            'id': 176000 + i, 'packet_id': 2000 + i, 'channel': 0, 'portnum': 1, 'portnum_name': 'TEXT_MESSAGE_APP',
            'from_node': frm, 'to_node': to, 'decoded_text': text, 'direction': d, 'transport': 'radio',
            'delivery_status': 'delivered' if d == 'tx' else 'received', 'hop_limit': 3, 'hop_start': 3,
            'rx_snr': 0 if d == 'tx' else 5.5, 'rx_time': unix(ago), 'created_at': iso(ago), 'satellite_cost': 0,
        })
    # Keep two telemetry rows from the rich set, re-addressed to the fleet.
    for j, x in enumerate([m for m in rich['messages'] if m.get('portnum_name') == 'TELEMETRY_APP'][:2]):
        x = dict(x)
        x['id'], x['from_node'], x['to_node'] = 175900 + j, '!a11ce006', '^all'
        out.append(x)
    out.sort(key=lambda m: m['created_at'], reverse=True)
    return {'limit': 50, 'messages': out}


def status(rich):
    s = dict(rich)
    s.update({'node_id': OWN, 'node_name': 'Support Vessel', 'num_nodes': len(NODES), 'hw_model': 7,
              'hw_model_name': 'T_ECHO', 'address': 'demo'})
    return s


def warnings(rich):
    texts = ['Exercise message: strong wind expected this afternoon. This is a test, not a real warning.',
             'Exercise message: high water at the harbour mouth. This is a test, not a real warning.']
    out = []
    for i, w in enumerate(rich[:2]):
        w = dict(w)
        w.update({'test': True, 'text': texts[i], 'bearers': 'mesh, post', 'language': 'en',
                  'sent': iso(50 - i * 10), 'received_at': iso(49 - i * 10), 'expires': iso(-180),
                  'msg_type': 'Alert', 'event': 'Wind' if i == 0 else 'Flood', 'severity': 'Moderate'})
        out.append(w)
    return out


def audit():
    out = []
    events = [('post_settings', 'Community Post settings saved'), ('rule_update', 'access rule mesh_0 -> iridium_0 enabled'),
              ('delivery_sent', 'delivery 412 sent on iridium_0'), ('delivery_sent', 'delivery 413 sent on mesh_0'),
              ('gateway_start', 'gateway iridium_0 started')]
    prev = '0' * 64
    for i, (kind, detail) in enumerate(events):
        h = ('a11ce%059d' % (i + 1))[:64]
        out.append({'id': 500 - i, 'timestamp': iso(5 + i * 7), 'event_type': kind, 'detail': detail,
                    'prev_hash': prev, 'hash': h})
        prev = h
    return out


def packets():
    out = []
    for ago, frm, to, text, d in CHAT[-6:]:
        out.append({'time': iso(ago), 'bearer': 'lora', 'dir': d, 'iface': 'mesh_0', 'from': frm,
                    'to': 'broadcast', 'bytes': 24 + len(text), 'rssi': -92 if d == 'rx' else 0,
                    'snr': 5.5 if d == 'rx' else 0, 'hops': 0, 'hop_start': 3, 'channel': 0, 'portnum': 1,
                    'portnum_name': 'TEXT_MESSAGE_APP', 'text': text, 'raw': '', 'path': '', 'msg_ref': ''})
    out.append({'time': iso(11), 'bearer': 'sat', 'dir': 'tx', 'iface': 'iridium_0', 'from': '', 'to': '',
                'bytes': 48, 'rssi': 0, 'snr': 0, 'hops': 0, 'hop_start': 0, 'channel': 0, 'portnum': 0,
                'portnum_name': '', 'text': '', 'raw': '', 'path': 'mo_status=0', 'msg_ref': ''})
    out.sort(key=lambda p: p['time'], reverse=True)
    return {'packets': out}


def packet_rates():
    def b(lora, sat=0):
        return {'lora': {'rx': lora, 'tx': max(1, lora // 4)}, 'aprs': {'rx': 1, 'tx': 0}, 'sms': {'rx': 0, 'tx': 0},
                'sat': {'rx': 0, 'tx': sat}, 'hf': {'rx': 0, 'tx': 0}}
    return {'window_s': 60, 'bearers': b(4), 'long': {'window_s': 300, 'bearers': b(14, 1)}}


def ttc_flow():
    rules = {p: {'id': 900 + i, 'enabled': p == 'imt', 'forward_to': f} for i, (p, f) in
             enumerate([('aprs', 'aprs_0'), ('b2b_sms', 'cellular_0'), ('hub_sms', 'cellular_0'), ('imt', 'iridium_0')])}
    rules['b2b_sms']['contact'] = '+447700900101'
    rules['hub_sms']['contact'] = '+447700900102'
    return {'path': 'imt', 'available': ['aprs', 'b2b_sms', 'hub_sms', 'imt'], 'rules': rules,
            'peer_number': '+447700900101', 'hub_number': '+447700900102',
            'imt': {'interface': 'iridium_0', 'running': True, 'connected': True, 'last_mo_at': iso(11),
                    'last_mo_status': 0, 'recent_mo': True, 'queued': 0, 'dlq_pending': 0},
            'sat': {'modem': 'sbd', 'interface': 'iridium_0', 'running': True, 'connected': True, 'last_mo_at': iso(11)},
            'ready': True}


def main():
    if os.path.isdir(DST):
        shutil.rmtree(DST)
    os.makedirs(DST)
    rich = {}
    for f in sorted(os.listdir(SRC)):
        if f.endswith('.json'):
            with open(os.path.join(SRC, f)) as fh:
                rich[f[:-5]] = json.load(fh)
    out = dict(rich)
    out['nodes'] = nodes()
    out['positions'] = positions(rich['positions'])
    out['messages'] = messages(rich['messages'])
    out['status'] = status(rich['status'])
    out['warnings'] = warnings(rich['warnings'])
    out['audit'] = audit()
    out['audit_signer'] = {'signer_id': 'a11ce' + '0' * 59}
    out['packets'] = packets()
    out['packets_rates'] = packet_rates()
    out['tak_certificates'] = {'enrolled': False, 'certificates': []}
    out['ttc_flow'] = ttc_flow()
    out['zigbee_devices2'] = {'devices': []}
    out['sos_status'] = {'active': False}
    # The kit's UPS: on shore power, nearly full.
    out['system_battery'] = {'ac_present': True, 'charging': True, 'last_update': float(unix(1)), 'soc_percent': 86,
                             'stale': False, 'voltage': 4.06}
    # The passes are made by the server for now (internal/demo Passes).
    out.pop('iridium_passes', None)
    for k, v in sorted(out.items()):
        with open(os.path.join(DST, k + '.json'), 'w') as fh:
            json.dump(v, fh, indent=1, sort_keys=True, ensure_ascii=False)
            fh.write('\n')
    print(f'{len(out)} fixtures written to {os.path.relpath(DST, ROOT)}')


if __name__ == '__main__':
    main()
