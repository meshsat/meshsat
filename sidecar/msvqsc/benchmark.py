"""Benchmark tool: compare SMAZ2 vs llama-zip vs MSVQ-SC on Meshtastic messages.

Usage:
    python benchmark.py --msvqsc localhost:50052 [--llamazip localhost:50051]

Sends representative Meshtastic messages through each compressor and reports
compression ratios, latency, and fidelity (exact match for lossless, cosine
similarity for lossy MSVQ-SC).
"""

import argparse
import sys
import time

import grpc

import msvqsc_pb2
import msvqsc_pb2_grpc

# Representative Meshtastic field messages (same as llama-zip benchmark)
SAMPLE_MESSAGES = [
    "Team Alpha at grid ref 51.5074,-0.1278. All clear. Moving to checkpoint 3.",
    "Battery 78%, signal strong. GPS fix 3D. 12 nodes in mesh.",
    "URGENT: Person found at 51.4921,-0.1408. Requesting medical evacuation.",
    "Weather update: wind 15kt NW, visibility 2km, temperature 8C.",
    "Relay: Base camp to Team Bravo - return to staging area by 1600.",
    "Position report: lat 51.5155 lon -0.0922 alt 45m speed 3.2kph heading 270",
    "SOS SOS SOS. Hiker injured at summit. Grid TQ3280. Need helicopter.",
    "All stations: channel 4 compromised. Switch to backup frequency.",
    "Node 0x1a2b3c4d online. Firmware 2.5.6. Battery 92%. SNR 12.5dB.",
    "Message delivery confirmed via Iridium. Round trip 47 seconds.",
    "Roger that. ETA 15 minutes to waypoint Baker.",
    "Low battery warning. Switching to power save mode. 23% remaining.",
    "Net check: Alpha copy, Bravo copy, Charlie no response.",
    "GPS coordinates 48.8566,2.3522. Altitude 35m. Accuracy 2.1m.",
    "Search sector 7 complete. No findings. Moving to sector 8.",
]

# Channel MTU → stages mapping for rate-adaptive testing
CHANNEL_PROFILES = [
    ("zigbee",   100, 2),
    ("cellular", 160, 3),
    ("mesh",     237, 4),
    ("iridium",  340, 6),
    ("webhook",  0,   8),
]


def benchmark_msvqsc(stub, messages, max_stages=0):
    """Benchmark MSVQ-SC encode/decode on message set."""
    results = []
    for msg in messages:
        data = msg.encode("utf-8")
        original_size = len(data)

        # Encode
        enc_resp = stub.Encode(msvqsc_pb2.EncodeRequest(
            data=data, max_stages=max_stages,
        ))
        encoded_size = len(enc_resp.encoded)

        # Decode
        dec_resp = stub.Decode(msvqsc_pb2.DecodeRequest(
            encoded=enc_resp.encoded,
        ))
        decoded = dec_resp.data.decode("utf-8")
        exact_match = decoded == msg

        results.append({
            "message": msg[:55] + "..." if len(msg) > 55 else msg,
            "original": original_size,
            "compressed": encoded_size,
            "ratio": encoded_size / original_size if original_size else 0,
            "savings_pct": (1 - encoded_size / original_size) * 100 if original_size else 0,
            "compress_ms": enc_resp.duration_ms,
            "decompress_ms": dec_resp.duration_ms,
            "stages": enc_resp.stages_used,
            "fidelity": enc_resp.estimated_fidelity,
            "exact": exact_match,
            "decoded": decoded[:55] + "..." if len(decoded) > 55 else decoded,
        })
    return results


def benchmark_smaz2_estimate(messages):
    """Estimate SMAZ2 compression for comparison."""
    results = []
    for msg in messages:
        original_size = len(msg.encode("utf-8"))
        estimated_compressed = int(original_size * 0.55)
        results.append({
            "message": msg[:55] + "..." if len(msg) > 55 else msg,
            "original": original_size,
            "compressed": estimated_compressed,
            "ratio": estimated_compressed / original_size if original_size else 0,
            "savings_pct": (1 - estimated_compressed / original_size) * 100 if original_size else 0,
            "compress_ms": 0,
            "decompress_ms": 0,
            "stages": 0,
            "fidelity": 1.0,
            "exact": True,
            "decoded": msg[:55] + "..." if len(msg) > 55 else msg,
        })
    return results


def print_results(label, results, show_decoded=False):
    """Print benchmark results table."""
    print(f"\n{'=' * 100}")
    print(f"  {label}")
    print(f"{'=' * 100}")
    header = (
        f"{'Message':<58} {'Orig':>5} {'Comp':>5} {'Save%':>6} "
        f"{'CMs':>5} {'DMs':>5} {'Stg':>3} {'Fid':>5} {'Ex':>2}"
    )
    print(header)
    print("-" * 100)

    total_orig = 0
    total_comp = 0
    total_compress_ms = 0
    total_decompress_ms = 0
    total_fidelity = 0.0

    for r in results:
        total_orig += r["original"]
        total_comp += r["compressed"]
        total_compress_ms += r["compress_ms"]
        total_decompress_ms += r["decompress_ms"]
        total_fidelity += r["fidelity"]

        line = (
            f"{r['message']:<58} {r['original']:>5} {r['compressed']:>5} "
            f"{r['savings_pct']:>5.1f}% {r['compress_ms']:>5} {r['decompress_ms']:>5} "
            f"{r['stages']:>3} {r['fidelity']:>5.3f} {'Y' if r['exact'] else 'N':>2}"
        )
        print(line)
        if show_decoded and not r["exact"]:
            print(f"  -> {r['decoded']}")

    avg_savings = (1 - total_comp / total_orig) * 100 if total_orig else 0
    avg_fidelity = total_fidelity / len(results) if results else 0
    print("-" * 100)
    print(
        f"{'TOTAL':<58} {total_orig:>5} {total_comp:>5} "
        f"{avg_savings:>5.1f}% {total_compress_ms:>5} {total_decompress_ms:>5} "
        f"{'':>3} {avg_fidelity:>5.3f}"
    )
    print(
        f"\nAvg compress: {total_compress_ms / len(results):.0f}ms | "
        f"Avg decompress: {total_decompress_ms / len(results):.0f}ms | "
        f"Overall savings: {avg_savings:.1f}% | "
        f"Avg fidelity: {avg_fidelity:.3f}"
    )


def main():
    parser = argparse.ArgumentParser(
        description="Benchmark SMAZ2 vs llama-zip vs MSVQ-SC compression",
    )
    parser.add_argument(
        "--msvqsc", default="localhost:50052",
        help="MSVQ-SC sidecar gRPC address (default: localhost:50052)",
    )
    parser.add_argument(
        "--llamazip", default="",
        help="llama-zip sidecar gRPC address (empty = skip)",
    )
    parser.add_argument(
        "--show-decoded", action="store_true",
        help="Show decoded text for non-exact matches",
    )
    args = parser.parse_args()

    # Connect to MSVQ-SC sidecar
    msvqsc_channel = grpc.insecure_channel(args.msvqsc)
    msvqsc_stub = msvqsc_pb2_grpc.MSVQSCServiceStub(msvqsc_channel)

    try:
        health = msvqsc_stub.Health(msvqsc_pb2.HealthRequest())
        print(
            f"MSVQ-SC sidecar: ready={health.ready} encoder={health.encoder_model} "
            f"stages={health.codebook_stages} K={health.codebook_entries} "
            f"dim={health.embedding_dim} corpus={health.corpus_size} "
            f"device={health.device}"
        )
    except grpc.RpcError as e:
        print(f"ERROR: Cannot connect to MSVQ-SC sidecar at {args.msvqsc}: {e}", file=sys.stderr)
        sys.exit(1)

    print(f"\nBenchmarking {len(SAMPLE_MESSAGES)} representative Meshtastic messages...\n")

    # SMAZ2 estimate
    smaz2_results = benchmark_smaz2_estimate(SAMPLE_MESSAGES)
    print_results("SMAZ2 (estimated, lossless)", smaz2_results)

    # llama-zip (optional)
    if args.llamazip:
        try:
            import compress_pb2
            import compress_pb2_grpc

            lz_channel = grpc.insecure_channel(args.llamazip)
            lz_stub = compress_pb2_grpc.CompressionServiceStub(lz_channel)
            lz_health = lz_stub.Health(compress_pb2.HealthRequest())
            print(f"\nllama-zip sidecar: ready={lz_health.ready} model={lz_health.model_name}")

            lz_results = []
            for msg in SAMPLE_MESSAGES:
                data = msg.encode("utf-8")
                resp = lz_stub.Compress(compress_pb2.CompressRequest(data=data))
                dec = lz_stub.Decompress(compress_pb2.DecompressRequest(compressed=resp.compressed))
                lz_results.append({
                    "message": msg[:55] + "..." if len(msg) > 55 else msg,
                    "original": len(data),
                    "compressed": len(resp.compressed),
                    "ratio": len(resp.compressed) / len(data),
                    "savings_pct": (1 - len(resp.compressed) / len(data)) * 100,
                    "compress_ms": resp.duration_ms,
                    "decompress_ms": dec.duration_ms,
                    "stages": 0,
                    "fidelity": 1.0,
                    "exact": dec.data.decode("utf-8") == msg,
                    "decoded": dec.data.decode("utf-8")[:55],
                })
            print_results("llama-zip (measured, lossless)", lz_results)
            lz_channel.close()
        except Exception as e:
            print(f"\nSkipping llama-zip: {e}")

    # MSVQ-SC at various stage counts (rate adaptation)
    for profile_name, mtu, stages in CHANNEL_PROFILES:
        label = f"MSVQ-SC {stages} stages ({profile_name}, MTU={mtu or 'unlimited'}B, lossy)"
        msvqsc_results = benchmark_msvqsc(msvqsc_stub, SAMPLE_MESSAGES, max_stages=stages)
        print_results(label, msvqsc_results, show_decoded=args.show_decoded)

    # Summary comparison
    print(f"\n{'=' * 100}")
    print("  RATE-ADAPTIVE SUMMARY")
    print(f"{'=' * 100}")
    print(f"  {'Channel':<12} {'MTU':>5} {'Stages':>6} {'Wire':>5} {'Savings':>8} {'Fidelity':>8}")
    print(f"  {'-'*50}")

    smaz2_total_orig = sum(r["original"] for r in smaz2_results)
    smaz2_total_comp = sum(r["compressed"] for r in smaz2_results)
    print(f"  {'smaz2':<12} {'any':>5} {'n/a':>6} {'~55%':>5} "
          f"{(1 - smaz2_total_comp / smaz2_total_orig) * 100:>7.1f}% {'1.000':>8}")

    for profile_name, mtu, stages in CHANNEL_PROFILES:
        results = benchmark_msvqsc(msvqsc_stub, SAMPLE_MESSAGES, max_stages=stages)
        total_orig = sum(r["original"] for r in results)
        total_comp = sum(r["compressed"] for r in results)
        avg_fidelity = sum(r["fidelity"] for r in results) / len(results)
        wire_bytes = 1 + stages * 2
        savings = (1 - total_comp / total_orig) * 100
        print(f"  {'msvqsc':<12} {mtu or 'inf':>5} {stages:>6} {wire_bytes:>4}B "
              f"{savings:>7.1f}% {avg_fidelity:>8.3f}")

    msvqsc_channel.close()


if __name__ == "__main__":
    main()
