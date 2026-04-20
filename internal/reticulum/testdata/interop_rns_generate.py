#!/usr/bin/env python3
"""Generate a Reticulum announce packet using the RNS library.

Usage: interop_rns_generate.py <app_name> <app_data>
Output: JSON with the announce packet in hex.
"""
import hashlib
import json
import os
import struct
import sys
import time

sys.path.insert(0, '/tmp/rns-test/lib/python3.11/site-packages')

from RNS.Cryptography import Ed25519PrivateKey, X25519PrivateKey


def generate_announce(app_name: str, app_data_str: str) -> dict:
    """Generate a Reticulum announce packet the same way RNS does."""
    app_data = app_data_str.encode("utf-8") if app_data_str else None

    # Generate identity keys (same as RNS Identity.__init__)
    enc_prv = X25519PrivateKey.generate()
    sig_prv = Ed25519PrivateKey.generate()

    enc_pub = enc_prv.public_key()
    sig_pub = sig_prv.public_key()

    enc_pub_bytes = enc_pub.public_bytes()
    sig_pub_bytes = sig_pub.public_bytes()
    public_key = enc_pub_bytes + sig_pub_bytes  # 64 bytes

    # Compute hashes (same as RNS Destination)
    name_hash = hashlib.sha256(app_name.encode("utf-8")).digest()[:10]
    identity_hash = hashlib.sha256(public_key).digest()[:16]
    dest_hash = hashlib.sha256(name_hash + identity_hash).digest()[:16]

    # Random hash: 5 random bytes + 5 bytes timestamp (RNS style)
    random_hash = os.urandom(5) + int(time.time()).to_bytes(5, "big")

    # Sign the announce
    signed_data = dest_hash + public_key + name_hash + random_hash
    if app_data:
        signed_data += app_data
    signature = sig_prv.sign(signed_data)

    # Build announce payload
    announce_data = public_key + name_hash + random_hash + signature
    if app_data:
        announce_data += app_data

    # Build Reticulum packet header
    # Flags: header_type=0, context_flag=0, transport_type=0, dest_type=SINGLE(0), packet_type=ANNOUNCE(1)
    flags = (0 << 6) | (0 << 5) | (0 << 4) | (0 << 2) | 0x01
    hops = 0
    context = 0x00  # NONE

    # Header: [flags][hops][dest_hash(16)][context]
    header = struct.pack("!BB", flags, hops) + dest_hash + struct.pack("!B", context)
    packet = header + announce_data

    return {
        "valid": True,
        "announce_hex": packet.hex(),
        "dest_hash": dest_hash.hex(),
        "name_hash": name_hash.hex(),
        "identity_hash": identity_hash.hex(),
        "public_key": public_key.hex(),
        "hops": hops,
        "app_data": app_data.hex() if app_data else "",
    }


if __name__ == "__main__":
    if len(sys.argv) < 3:
        print(json.dumps({"valid": False, "error": "usage: interop_rns_generate.py <app_name> <app_data>"}))
        sys.exit(1)

    result = generate_announce(sys.argv[1], sys.argv[2])
    print(json.dumps(result))
