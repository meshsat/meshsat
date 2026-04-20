#!/usr/bin/env python3
"""Validate a bridge-generated Reticulum announce packet using the RNS library.

Usage: interop_rns_validate.py <packet_hex> <app_name>
Output: JSON with validation result.
"""
import hashlib
import json
import struct
import sys

sys.path.insert(0, '/tmp/rns-test/lib/python3.11/site-packages')

from RNS.Cryptography import Ed25519PublicKey, X25519PublicKey


def validate_announce(packet_hex: str, app_name: str) -> dict:
    try:
        raw = bytes.fromhex(packet_hex)
    except ValueError as e:
        return {"valid": False, "error": f"bad hex: {e}"}

    if len(raw) < 19:
        return {"valid": False, "error": f"packet too short: {len(raw)} bytes"}

    # Parse header
    flags = raw[0]
    hops = raw[1]

    header_type = (flags & 0b01000000) >> 6
    context_flag = (flags & 0b00100000) >> 5
    transport_type = (flags & 0b00010000) >> 4
    dest_type = (flags & 0b00001100) >> 2
    packet_type = (flags & 0b00000011)

    if packet_type != 0x01:  # ANNOUNCE
        return {"valid": False, "error": f"not an announce packet: type={packet_type}"}

    pos = 2
    if header_type == 1:  # HEADER_2
        if len(raw) < 35:
            return {"valid": False, "error": "header type 2 too short"}
        transport_id = raw[pos:pos+16]
        pos += 16

    dest_hash = raw[pos:pos+16]
    pos += 16

    context = raw[pos]
    pos += 1

    data = raw[pos:]

    # Parse announce payload
    # [64B public_key][10B name_hash][10B random][32B ratchet?][64B signature][app_data]
    min_payload = 64 + 10 + 10 + 64  # 148
    if context_flag:
        min_payload += 32  # ratchet

    if len(data) < min_payload:
        return {"valid": False, "error": f"payload too short: {len(data)} < {min_payload}"}

    p = 0
    public_key = data[p:p+64]
    p += 64

    name_hash = data[p:p+10]
    p += 10

    random_hash = data[p:p+10]
    p += 10

    ratchet = b""
    if context_flag:
        ratchet = data[p:p+32]
        p += 32

    signature = data[p:p+64]
    p += 64

    app_data = data[p:]

    # Verify destination hash
    # name_hash_computed = SHA-256(app_name.encode("utf-8"))[:10]
    name_hash_computed = hashlib.sha256(app_name.encode("utf-8")).digest()[:10]
    if name_hash != name_hash_computed:
        return {"valid": False, "error": f"name hash mismatch: got {name_hash.hex()}, expected {name_hash_computed.hex()}"}

    # identity_hash = SHA-256(public_key)[:16]
    identity_hash = hashlib.sha256(public_key).digest()[:16]

    # dest_hash_computed = SHA-256(name_hash || identity_hash)[:16]
    dest_hash_computed = hashlib.sha256(name_hash + identity_hash).digest()[:16]
    if dest_hash != dest_hash_computed:
        return {"valid": False, "error": f"dest hash mismatch: got {dest_hash.hex()}, expected {dest_hash_computed.hex()}"}

    # Verify signature
    # signed_data = dest_hash + public_key + name_hash + random_hash + ratchet + app_data
    signed_data = dest_hash + public_key + name_hash + random_hash + ratchet + app_data
    sig_pub_bytes = public_key[32:]  # Ed25519 public key (second 32 bytes)

    try:
        sig_pub = Ed25519PublicKey.from_public_bytes(sig_pub_bytes)
        sig_pub.verify(signature, signed_data)
    except Exception as e:
        return {"valid": False, "error": f"signature verification failed: {e}"}

    return {
        "valid": True,
        "dest_hash": dest_hash.hex(),
        "name_hash": name_hash.hex(),
        "public_key": public_key.hex(),
        "signature": signature.hex(),
        "hops": hops,
        "app_data": app_data.hex() if app_data else "",
    }


if __name__ == "__main__":
    if len(sys.argv) < 3:
        print(json.dumps({"valid": False, "error": "usage: interop_rns_validate.py <packet_hex> <app_name>"}))
        sys.exit(1)

    result = validate_announce(sys.argv[1], sys.argv[2])
    print(json.dumps(result))
