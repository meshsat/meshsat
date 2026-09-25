#!/usr/bin/env python3
"""NumPy reference for the 10 m HF codec (docs/hf_codec.md in CrossTalk),
written from the recipe so the Go implementation in internal/hf10m can be
checked byte for byte. Prints JSON: the worked example's inner frame, its
LDPC-wrapped burst (after the Costas tones), the radix-40 vector and a few
codewords. [MESHSAT-1353]

Usage: hf10m_ref.py [--vectors]   (needs numpy)
"""
import json, sys
import numpy as np

ALPHABET = " ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/-."
H_HEX = [l.strip() for l in open(__file__.replace("scripts/hf10m_ref.py", "internal/hf10m/hmatrix.go")).read().split("`")[1].split() if l.strip()]
assert len(H_HEX) == 64
H = np.zeros((64, 128), dtype=np.uint8)
for r, line in enumerate(H_HEX):
    v = int(line, 16)
    for c in range(128):
        H[r, c] = (v >> (127 - c)) & 1
INFO = list(range(63)) + [67]
PAR = [c for c in range(128) if c not in INFO]


def gf2_solve(A, b):
    """Solve A x = b over GF(2), A square and invertible."""
    A = A.copy() % 2
    b = b.copy() % 2
    n = A.shape[0]
    for col in range(n):
        piv = next(r for r in range(col, n) if A[r, col])
        A[[col, piv]] = A[[piv, col]]
        b[[col, piv]] = b[[piv, col]]
        for r in range(n):
            if r != col and A[r, col]:
                A[r] ^= A[col]
                b[r] ^= b[col]
    return b


def ldpc_encode(info):
    x = np.zeros(128, dtype=np.uint8)
    x[INFO] = info
    rhs = (H[:, INFO] @ info) % 2
    x[PAR] = gf2_solve(H[:, PAR], rhs.astype(np.uint8))
    assert not ((H @ x) % 2).any()
    return x


def crc16(body):
    crc = 0xFFFF
    for byte in body:
        crc ^= byte << 8
        for _ in range(8):
            crc = ((crc << 1) ^ 0x1021) & 0xFFFF if crc & 0x8000 else (crc << 1) & 0xFFFF
    return crc


def radix40(call):
    padded = call.upper().ljust(9)
    v = 0
    for ch in padded:
        v = v * 40 + ALPHABET.index(ch)
    return v.to_bytes(6, "big")


def inner(origin, dest, msg_id, frag_idx, frag_tot, text):
    body = bytes([0x10, 0x00]) + radix40(origin) + dest + msg_id.to_bytes(2, "big") + bytes([(frag_idx << 4) | frag_tot, len(text)]) + text
    return body + crc16(body).to_bytes(2, "big")


def burst(frame):
    bits = np.unpackbits(np.frombuffer(frame, dtype=np.uint8))
    pad = (-len(bits)) % 64
    bits = np.concatenate([bits, np.zeros(pad, dtype=np.uint8)])
    n = len(bits) // 64
    coded = np.concatenate([ldpc_encode(bits[i * 64:(i + 1) * 64]) for i in range(n)])
    inter = coded.reshape(-1, 16).T.ravel()
    payload = np.packbits(inter).tobytes()
    return n, bytes([0x55] * 8) + bytes.fromhex("FD59BB49C5E51840") + bytes([n, n, n]) + payload + bytes([0x55] * 16)


def main():
    dest = bytes.fromhex("0123456789abcdef0123456789abcdef")
    text = b"no internet here. all ok. next check 0900"
    frame = inner("N0CALL", dest, 42, 0, 1, text)
    n, b = burst(frame)
    rng = np.random.default_rng(1)
    cws = []
    for _ in range(4):
        info = rng.integers(0, 2, 64, dtype=np.uint8)
        cws.append({"info": np.packbits(info).tobytes().hex(), "codeword": np.packbits(ldpc_encode(info)).tobytes().hex()})
    print(json.dumps({
        "n0call": radix40("N0CALL").hex(),
        "frame": frame.hex(),
        "crc": frame[-2:].hex(),
        "n_blocks": n,
        "burst": b.hex(),
        "burst_len": len(b),
        "codewords": cws,
    }, indent=1))


if __name__ == "__main__":
    main()
