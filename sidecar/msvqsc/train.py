"""MSVQ-SC training pipeline: sentence encoder + residual VQ codebook.

Trains a multi-stage residual vector quantizer on a corpus of SAR/field/
Meshtastic messages. Exports:
  - codebook_v1.bin    — binary LE float32 codebook vectors
  - corpus_index.bin   — pre-embedded corpus texts + embeddings
  - encoder.onnx       — INT8 quantized sentence encoder for sidecar

Usage:
    pip install sentence-transformers vector-quantize-pytorch onnx onnxruntime
    python train.py [--corpus corpus.txt] [--output-dir ./models]
"""

import argparse
import json
import logging
import struct
import time
from pathlib import Path

import numpy as np
import torch

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("msvqsc-train")

# Codebook hyperparameters
EMBED_DIM = 384          # all-MiniLM-L6-v2 output dimension
NUM_STAGES = 8           # max residual VQ stages
CODEBOOK_SIZE = 1024     # entries per stage (K)
MODEL_NAME = "all-MiniLM-L6-v2"

# Built-in training corpus — representative SAR/field/Meshtastic messages
BUILTIN_CORPUS = [
    # SAR/emergency
    "Team Alpha at grid ref 51.5074,-0.1278. All clear. Moving to checkpoint 3.",
    "URGENT: Person found at 51.4921,-0.1408. Requesting medical evacuation.",
    "SOS SOS SOS. Hiker injured at summit. Grid TQ3280. Need helicopter.",
    "Search sector 7 complete. No findings. Moving to sector 8.",
    "Rescue team deployed to coordinates 48.8566,2.3522. ETA 20 minutes.",
    "Missing person last seen near river crossing at grid 51.5155,-0.0922.",
    "Evacuation route clear. All personnel accounted for.",
    "Medical team on standby at base camp. Stretcher required.",
    # Position/GPS
    "Position report: lat 51.5155 lon -0.0922 alt 45m speed 3.2kph heading 270",
    "GPS coordinates 48.8566,2.3522. Altitude 35m. Accuracy 2.1m.",
    "Waypoint Baker reached. Proceeding to waypoint Charlie.",
    "Current position 52.2053,0.1218. Stationary. Awaiting instructions.",
    # Status/telemetry
    "Battery 78%, signal strong. GPS fix 3D. 12 nodes in mesh.",
    "Node 0x1a2b3c4d online. Firmware 2.5.6. Battery 92%. SNR 12.5dB.",
    "Low battery warning. Switching to power save mode. 23% remaining.",
    "Weather update: wind 15kt NW, visibility 2km, temperature 8C.",
    "Signal quality degraded. SNR 3.2dB. Switching to relay mode.",
    "Solar panel output 4.2W. Battery charging. Estimated full in 3 hours.",
    # Relay/comms
    "Relay: Base camp to Team Bravo - return to staging area by 1600.",
    "Message delivery confirmed via Iridium. Round trip 47 seconds.",
    "Roger that. ETA 15 minutes to waypoint Baker.",
    "All stations: channel 4 compromised. Switch to backup frequency.",
    "Net check: Alpha copy, Bravo copy, Charlie no response.",
    "Relay node 7 forwarding traffic. Hop count 3. Latency 2.1s.",
    # Field operations
    "Water sample collected at grid 51.5074,-0.1278. pH 7.2. Turbidity low.",
    "Trail blocked by fallen tree at waypoint Delta. Rerouting via Echo.",
    "Camp established at elevation 1200m. Temperature dropping. Wind increasing.",
    "Supply drop confirmed at LZ Alpha. Contents: water, rations, batteries.",
    "Perimeter check complete. All sensors operational. No intrusions detected.",
    "Vehicle stuck at grid 52.4862,-1.8904. Requesting tow. No injuries.",
    # Short/constrained
    "Copy.",
    "Roger.",
    "Negative.",
    "Stand by.",
    "All clear.",
    "Moving out.",
    "Check in.",
    "Base, Alpha.",
    "Over and out.",
    "Acknowledge.",
    "Requesting backup.",
    "Position confirmed.",
    "Mission complete.",
    "Returning to base.",
    "Need resupply.",
]


def load_corpus(corpus_path: str | None) -> list[str]:
    """Load training corpus from file or use built-in messages."""
    messages = list(BUILTIN_CORPUS)

    if corpus_path and Path(corpus_path).exists():
        with open(corpus_path, "r") as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith("#"):
                    messages.append(line)
        log.info("Loaded %d additional messages from %s", len(messages) - len(BUILTIN_CORPUS), corpus_path)

    log.info("Total training corpus: %d messages", len(messages))
    return messages


def encode_corpus(messages: list[str]) -> np.ndarray:
    """Encode messages to embeddings using sentence-transformers."""
    from sentence_transformers import SentenceTransformer

    log.info("Loading sentence encoder: %s", MODEL_NAME)
    model = SentenceTransformer(MODEL_NAME)

    log.info("Encoding %d messages...", len(messages))
    start = time.monotonic()
    embeddings = model.encode(messages, convert_to_numpy=True, normalize_embeddings=True)
    elapsed = time.monotonic() - start
    log.info("Encoded in %.1fs — shape: %s", elapsed, embeddings.shape)

    return embeddings.astype(np.float32)


def train_residual_vq(embeddings: np.ndarray) -> tuple[np.ndarray, list[np.ndarray]]:
    """Train residual VQ codebook on embeddings.

    Returns:
        codebook_vectors: [stages, K, dim] array
        per_stage_indices: list of [N] arrays with assigned indices per stage
    """
    from vector_quantize_pytorch import ResidualVQ

    log.info(
        "Training ResidualVQ: stages=%d, K=%d, dim=%d on %d samples",
        NUM_STAGES, CODEBOOK_SIZE, EMBED_DIM, len(embeddings),
    )

    rq = ResidualVQ(
        dim=EMBED_DIM,
        num_quantizers=NUM_STAGES,
        codebook_size=CODEBOOK_SIZE,
        decay=0.99,
        commitment_weight=1.0,
        kmeans_init=True,
        kmeans_iters=20,
    )

    # Training loop — multiple passes over the data
    x = torch.from_numpy(embeddings).unsqueeze(0)  # [1, N, D]
    num_epochs = 50
    log.info("Training for %d epochs...", num_epochs)

    start = time.monotonic()
    for epoch in range(num_epochs):
        quantized, indices, commit_loss = rq(x)

        if (epoch + 1) % 10 == 0:
            # Compute reconstruction error
            recon_error = torch.mean((x - quantized) ** 2).item()
            cos_sim = torch.nn.functional.cosine_similarity(
                x.squeeze(0), quantized.squeeze(0), dim=1
            ).mean().item()
            log.info(
                "Epoch %d/%d — MSE: %.6f, cos_sim: %.4f, commit_loss: %.4f",
                epoch + 1, num_epochs, recon_error, cos_sim,
                commit_loss.sum().item() if commit_loss.numel() > 1 else commit_loss.item(),
            )

    elapsed = time.monotonic() - start
    log.info("Training completed in %.1fs", elapsed)

    # Extract codebook vectors: [stages, K, dim]
    codebook_vectors = []
    for i, layer in enumerate(rq.layers):
        cb = layer._codebook.embed.detach().cpu().numpy()  # [1, K, D]
        codebook_vectors.append(cb.squeeze(0))
        log.info("Stage %d codebook: shape %s, norm range [%.3f, %.3f]",
                 i, cb.squeeze(0).shape,
                 np.linalg.norm(cb.squeeze(0), axis=1).min(),
                 np.linalg.norm(cb.squeeze(0), axis=1).max())

    codebook_array = np.stack(codebook_vectors)  # [stages, K, dim]

    # Get final indices for the corpus
    _, final_indices, _ = rq(x)
    per_stage = [final_indices[0, :, s].detach().cpu().numpy() for s in range(NUM_STAGES)]

    return codebook_array, per_stage


def export_codebook_bin(codebook: np.ndarray, output_path: str):
    """Export codebook as binary LE float32.

    Format:
        4B magic: "MSVQ"
        1B version
        1B stages
        2B K (uint16 LE)
        2B dim (uint16 LE)
        Then stages * K * dim float32 values (LE)
    """
    stages, k, dim = codebook.shape
    with open(output_path, "wb") as f:
        f.write(b"MSVQ")                          # magic
        f.write(struct.pack("<B", 1))              # version
        f.write(struct.pack("<B", stages))         # stages
        f.write(struct.pack("<H", k))              # K
        f.write(struct.pack("<H", dim))            # dim
        f.write(codebook.astype(np.float32).tobytes())

    size_mb = Path(output_path).stat().st_size / 1024 / 1024
    log.info("Codebook exported: %s (%.1f MB) — [%d stages, K=%d, dim=%d]",
             output_path, size_mb, stages, k, dim)


def export_codebook_json(codebook: np.ndarray, output_path: str):
    """Export codebook as human-readable JSON."""
    stages, k, dim = codebook.shape
    data = {
        "version": 1,
        "stages": stages,
        "K": k,
        "dim": dim,
        "vectors": codebook.tolist(),
    }
    with open(output_path, "w") as f:
        json.dump(data, f)
    size_mb = Path(output_path).stat().st_size / 1024 / 1024
    log.info("Codebook JSON exported: %s (%.1f MB)", output_path, size_mb)


def export_corpus_index(messages: list[str], embeddings: np.ndarray, output_path: str):
    """Export corpus index as binary for decode nearest-neighbor lookup.

    Format:
        4B magic: "MCIX"
        1B version
        4B num_entries (uint32 LE)
        2B dim (uint16 LE)
        For each entry:
            2B text_len (uint16 LE)
            text_len bytes UTF-8 text
            dim * 4B float32 embedding
    """
    with open(output_path, "wb") as f:
        f.write(b"MCIX")                                    # magic
        f.write(struct.pack("<B", 1))                       # version
        f.write(struct.pack("<I", len(messages)))           # num entries
        f.write(struct.pack("<H", embeddings.shape[1]))     # dim

        for text, emb in zip(messages, embeddings):
            text_bytes = text.encode("utf-8")
            f.write(struct.pack("<H", len(text_bytes)))
            f.write(text_bytes)
            f.write(emb.astype(np.float32).tobytes())

    size_kb = Path(output_path).stat().st_size / 1024
    log.info("Corpus index exported: %s (%.1f KB) — %d entries",
             output_path, size_kb, len(messages))


def export_onnx_encoder(output_path: str):
    """Export sentence encoder as ONNX with INT8 quantization."""
    from sentence_transformers import SentenceTransformer

    log.info("Exporting ONNX encoder...")
    model = SentenceTransformer(MODEL_NAME)

    # Export the underlying transformer to ONNX
    dummy_input = model.tokenize(["test sentence"])
    input_ids = dummy_input["input_ids"]
    attention_mask = dummy_input["attention_mask"]

    # Get the transformer module and wrap it to avoid forward() signature issues
    transformer = model[0].auto_model

    class OnnxWrapper(torch.nn.Module):
        def __init__(self, bert):
            super().__init__()
            self.bert = bert

        def forward(self, input_ids, attention_mask):
            outputs = self.bert(input_ids=input_ids, attention_mask=attention_mask)
            return outputs.last_hidden_state

    wrapper = OnnxWrapper(transformer)
    wrapper.eval()

    torch.onnx.export(
        wrapper,
        (input_ids, attention_mask),
        output_path,
        input_names=["input_ids", "attention_mask"],
        output_names=["last_hidden_state"],
        dynamic_axes={
            "input_ids": {0: "batch", 1: "seq"},
            "attention_mask": {0: "batch", 1: "seq"},
            "last_hidden_state": {0: "batch", 1: "seq"},
        },
        opset_version=14,
    )

    # Quantize to INT8
    try:
        from onnxruntime.quantization import quantize_dynamic, QuantType

        quantized_path = output_path.replace(".onnx", "_int8.onnx")
        quantize_dynamic(
            output_path,
            quantized_path,
            weight_type=QuantType.QInt8,
        )
        # Replace original with quantized
        Path(output_path).unlink()
        Path(quantized_path).rename(output_path)
        log.info("ONNX encoder exported with INT8 quantization: %s", output_path)
    except ImportError:
        log.warning("onnxruntime.quantization not available — exported FP32 ONNX")

    size_mb = Path(output_path).stat().st_size / 1024 / 1024
    log.info("ONNX encoder size: %.1f MB", size_mb)


def main():
    parser = argparse.ArgumentParser(description="MSVQ-SC training pipeline")
    parser.add_argument(
        "--corpus", default=None,
        help="Additional corpus file (one message per line)",
    )
    parser.add_argument(
        "--output-dir", default="./models",
        help="Output directory for codebook and model files",
    )
    parser.add_argument(
        "--skip-onnx", action="store_true",
        help="Skip ONNX export (faster iteration)",
    )
    args = parser.parse_args()

    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    # Step 1: Load corpus
    messages = load_corpus(args.corpus)

    # Step 2: Encode corpus to embeddings
    embeddings = encode_corpus(messages)

    # Step 3: Train residual VQ
    codebook, indices = train_residual_vq(embeddings)

    # Step 4: Export codebook
    export_codebook_bin(codebook, str(output_dir / "codebook_v1.bin"))
    export_codebook_json(codebook, str(output_dir / "codebook_v1.json"))

    # Step 5: Export corpus index
    export_corpus_index(messages, embeddings, str(output_dir / "corpus_index.bin"))

    # Step 6: Export ONNX encoder
    if not args.skip_onnx:
        export_onnx_encoder(str(output_dir / "encoder.onnx"))
    else:
        log.info("Skipping ONNX export (--skip-onnx)")

    # Step 7: Verify round-trip quality
    log.info("=" * 60)
    log.info("Training complete. Verifying reconstruction quality...")

    # Quick verification: encode a few messages, reconstruct via codebook
    test_messages = messages[:5]
    test_embeddings = embeddings[:5]

    for i, (msg, emb) in enumerate(zip(test_messages, test_embeddings)):
        # Simulate reconstruction: sum codebook vectors at assigned indices
        reconstructed = np.zeros(EMBED_DIM, dtype=np.float32)
        for stage in range(NUM_STAGES):
            idx = indices[stage][i]
            reconstructed += codebook[stage, idx]

        # Cosine similarity
        cos_sim = np.dot(emb, reconstructed) / (
            np.linalg.norm(emb) * np.linalg.norm(reconstructed) + 1e-8
        )

        # Find nearest corpus entry
        sims = embeddings @ reconstructed / (
            np.linalg.norm(embeddings, axis=1) * np.linalg.norm(reconstructed) + 1e-8
        )
        best_idx = np.argmax(sims)

        log.info(
            "  [%d] cos_sim=%.4f | orig: %s",
            i, cos_sim, msg[:70],
        )
        log.info(
            "       nearest: %s (sim=%.4f, match=%s)",
            messages[best_idx][:70], sims[best_idx],
            "EXACT" if best_idx == i else f"idx={best_idx}",
        )

    log.info("=" * 60)
    log.info("Output files in %s/", output_dir)


if __name__ == "__main__":
    main()
