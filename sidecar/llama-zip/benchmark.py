"""Benchmark tool: compare SMAZ2 vs llama-zip on Meshtastic messages.

Usage:
    python benchmark.py --sidecar localhost:50051

Sends a set of representative Meshtastic messages through the llama-zip
gRPC sidecar and reports compression ratios and latency.
"""

import argparse
import base64
import sys
import time

import grpc

import compress_pb2
import compress_pb2_grpc

# Representative Meshtastic field messages (typical SAR, position, status)
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


def benchmark_llamazip(stub, messages):
    """Benchmark llama-zip compression on message set."""
    results = []
    for msg in messages:
        data = msg.encode("utf-8")
        original_size = len(data)

        # Compress
        resp = stub.Compress(compress_pb2.CompressRequest(data=data))
        compressed_size = len(resp.compressed)

        # Verify round-trip
        dec_resp = stub.Decompress(
            compress_pb2.DecompressRequest(compressed=resp.compressed)
        )
        decoded = dec_resp.data.decode("utf-8")
        fidelity = decoded == msg

        results.append(
            {
                "message": msg[:60] + "..." if len(msg) > 60 else msg,
                "original": original_size,
                "compressed": compressed_size,
                "ratio": compressed_size / original_size if original_size else 0,
                "savings_pct": (
                    (1 - compressed_size / original_size) * 100 if original_size else 0
                ),
                "compress_ms": resp.duration_ms,
                "decompress_ms": dec_resp.duration_ms,
                "fidelity": fidelity,
            }
        )
    return results


def benchmark_smaz2_estimate(messages):
    """Estimate SMAZ2 compression for comparison.

    SMAZ2 typically achieves 44-55% compression on English text.
    This provides estimated values for comparison — actual SMAZ2 ratios
    should be measured from the Go side.
    """
    results = []
    for msg in messages:
        original_size = len(msg.encode("utf-8"))
        # Conservative estimate: 45% savings for English text
        estimated_compressed = int(original_size * 0.55)
        results.append(
            {
                "message": msg[:60] + "..." if len(msg) > 60 else msg,
                "original": original_size,
                "compressed": estimated_compressed,
                "ratio": estimated_compressed / original_size if original_size else 0,
                "savings_pct": (
                    (1 - estimated_compressed / original_size) * 100
                    if original_size
                    else 0
                ),
                "compress_ms": 0,  # SMAZ2 is <1ms
                "decompress_ms": 0,
                "fidelity": True,
            }
        )
    return results


def print_results(label, results):
    """Print benchmark results table."""
    print(f"\n{'=' * 80}")
    print(f"  {label}")
    print(f"{'=' * 80}")
    print(
        f"{'Message':<63} {'Orig':>5} {'Comp':>5} {'Save%':>6} {'CMs':>5} {'DMs':>5} {'OK':>3}"
    )
    print("-" * 93)

    total_orig = 0
    total_comp = 0
    total_compress_ms = 0
    total_decompress_ms = 0
    all_fidelity = True

    for r in results:
        total_orig += r["original"]
        total_comp += r["compressed"]
        total_compress_ms += r["compress_ms"]
        total_decompress_ms += r["decompress_ms"]
        all_fidelity = all_fidelity and r["fidelity"]

        print(
            f"{r['message']:<63} {r['original']:>5} {r['compressed']:>5} "
            f"{r['savings_pct']:>5.1f}% {r['compress_ms']:>5} {r['decompress_ms']:>5} "
            f"{'Y' if r['fidelity'] else 'N':>3}"
        )

    avg_savings = (1 - total_comp / total_orig) * 100 if total_orig else 0
    print("-" * 93)
    print(
        f"{'TOTAL':<63} {total_orig:>5} {total_comp:>5} "
        f"{avg_savings:>5.1f}% {total_compress_ms:>5} {total_decompress_ms:>5} "
        f"{'Y' if all_fidelity else 'N':>3}"
    )
    print(
        f"\nAvg compress: {total_compress_ms / len(results):.0f}ms | "
        f"Avg decompress: {total_decompress_ms / len(results):.0f}ms | "
        f"Overall savings: {avg_savings:.1f}%"
    )


def main():
    parser = argparse.ArgumentParser(
        description="Benchmark SMAZ2 vs llama-zip compression"
    )
    parser.add_argument(
        "--sidecar",
        default="localhost:50051",
        help="llama-zip sidecar gRPC address (default: localhost:50051)",
    )
    args = parser.parse_args()

    # Health check
    channel = grpc.insecure_channel(args.sidecar)
    stub = compress_pb2_grpc.CompressionServiceStub(channel)

    try:
        health = stub.Health(compress_pb2.HealthRequest())
        print(f"Sidecar: ready={health.ready} model={health.model_name} "
              f"device={health.device} size={health.model_size_bytes / 1024 / 1024:.1f}MB")
    except grpc.RpcError as e:
        print(f"ERROR: Cannot connect to sidecar at {args.sidecar}: {e}", file=sys.stderr)
        sys.exit(1)

    # Benchmark
    print(f"\nBenchmarking {len(SAMPLE_MESSAGES)} representative Meshtastic messages...\n")

    smaz2_results = benchmark_smaz2_estimate(SAMPLE_MESSAGES)
    print_results("SMAZ2 (estimated)", smaz2_results)

    llamazip_results = benchmark_llamazip(stub, SAMPLE_MESSAGES)
    print_results("llama-zip (measured)", llamazip_results)

    # Summary comparison
    smaz2_total_orig = sum(r["original"] for r in smaz2_results)
    smaz2_total_comp = sum(r["compressed"] for r in smaz2_results)
    lz_total_orig = sum(r["original"] for r in llamazip_results)
    lz_total_comp = sum(r["compressed"] for r in llamazip_results)

    smaz2_savings = (1 - smaz2_total_comp / smaz2_total_orig) * 100
    lz_savings = (1 - lz_total_comp / lz_total_orig) * 100

    print(f"\n{'=' * 80}")
    print("  COMPARISON SUMMARY")
    print(f"{'=' * 80}")
    print(f"  SMAZ2 (est):    {smaz2_savings:.1f}% savings, <1ms latency")
    print(
        f"  llama-zip:      {lz_savings:.1f}% savings, "
        f"{sum(r['compress_ms'] for r in llamazip_results) / len(llamazip_results):.0f}ms avg latency"
    )
    print(
        f"  Delta:          {lz_savings - smaz2_savings:+.1f}% additional savings with llama-zip"
    )

    channel.close()


if __name__ == "__main__":
    main()
