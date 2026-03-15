"""MSVQ-SC gRPC sidecar for MeshSat.

Rate-adaptive lossy semantic compression using multi-stage residual
vector quantization (arXiv:2510.02646). Text is encoded to a 384-dim
embedding via a sentence encoder, then quantized through K=1024
codebook stages. The number of stages transmitted controls the
compression/fidelity trade-off.

Encode: text -> ONNX encoder -> embedding -> RVQ indices -> packed wire
Decode: wire -> unpack indices -> sum codebook vectors -> nearest corpus -> text
"""

import logging
import os
import struct
import time
from concurrent import futures
from pathlib import Path

import grpc
import numpy as np

import msvqsc_pb2
import msvqsc_pb2_grpc

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("msvqsc-sidecar")

# Configuration via environment
LISTEN_ADDR = os.environ.get("MSVQSC_LISTEN", "0.0.0.0:50052")
MAX_WORKERS = int(os.environ.get("MSVQSC_WORKERS", "2"))
MODEL_DIR = os.environ.get("MSVQSC_MODEL_DIR", "/models")
ENCODER_PATH = os.environ.get("MSVQSC_ENCODER", "")  # ONNX encoder path
CODEBOOK_PATH = os.environ.get("MSVQSC_CODEBOOK", "")  # codebook_v1.bin
CORPUS_PATH = os.environ.get("MSVQSC_CORPUS", "")  # corpus_index.bin

# Wire format constants
WIRE_VERSION = 1
HEADER_SIZE = 1   # 1 byte: 4-bit stages + 4-bit version
INDEX_SIZE = 2    # 2 bytes per stage index (uint16 LE)


def load_codebook(path: str) -> tuple[np.ndarray, int, int, int]:
    """Load codebook from binary file.

    Returns: (vectors [stages, K, dim], version, stages, K)
    """
    with open(path, "rb") as f:
        magic = f.read(4)
        if magic != b"MSVQ":
            raise ValueError(f"Invalid codebook magic: {magic!r}")
        version = struct.unpack("<B", f.read(1))[0]
        stages = struct.unpack("<B", f.read(1))[0]
        k = struct.unpack("<H", f.read(2))[0]
        dim = struct.unpack("<H", f.read(2))[0]
        data = np.frombuffer(f.read(), dtype=np.float32)
        vectors = data.reshape(stages, k, dim)
    log.info("Codebook loaded: v%d, %d stages, K=%d, dim=%d", version, stages, k, dim)
    return vectors, version, stages, k


def load_corpus_index(path: str) -> tuple[list[str], np.ndarray]:
    """Load corpus index from binary file.

    Returns: (texts, embeddings [N, dim])
    """
    texts = []
    embeddings = []

    with open(path, "rb") as f:
        magic = f.read(4)
        if magic != b"MCIX":
            raise ValueError(f"Invalid corpus index magic: {magic!r}")
        _version = struct.unpack("<B", f.read(1))[0]
        num_entries = struct.unpack("<I", f.read(4))[0]
        dim = struct.unpack("<H", f.read(2))[0]

        for _ in range(num_entries):
            text_len = struct.unpack("<H", f.read(2))[0]
            text = f.read(text_len).decode("utf-8")
            emb = np.frombuffer(f.read(dim * 4), dtype=np.float32).copy()
            texts.append(text)
            embeddings.append(emb)

    embeddings_array = np.stack(embeddings)
    log.info("Corpus index loaded: %d entries, dim=%d", len(texts), dim)
    return texts, embeddings_array


def pack_wire(indices: list[int], version: int = WIRE_VERSION) -> bytes:
    """Pack VQ indices into wire format.

    Format: [1B header: 4-bit stages | 4-bit version] [2B uint16 LE per stage]
    """
    stages = len(indices)
    header = ((stages & 0x0F) << 4) | (version & 0x0F)
    buf = struct.pack("<B", header)
    for idx in indices:
        buf += struct.pack("<H", idx)
    return buf


def unpack_wire(data: bytes) -> tuple[list[int], int, int]:
    """Unpack wire format to indices.

    Returns: (indices, stages, version)
    """
    if len(data) < HEADER_SIZE:
        raise ValueError("Wire data too short")
    header = data[0]
    stages = (header >> 4) & 0x0F
    version = header & 0x0F
    expected_len = HEADER_SIZE + stages * INDEX_SIZE
    if len(data) < expected_len:
        raise ValueError(f"Wire data too short: need {expected_len}, got {len(data)}")
    indices = []
    for s in range(stages):
        offset = HEADER_SIZE + s * INDEX_SIZE
        idx = struct.unpack("<H", data[offset : offset + INDEX_SIZE])[0]
        indices.append(idx)
    return indices, stages, version


class MSVQSCServicer(msvqsc_pb2_grpc.MSVQSCServiceServicer):
    """gRPC servicer for MSVQ-SC encode/decode."""

    def __init__(self, encoder_session, codebook: np.ndarray,
                 corpus_texts: list[str], corpus_embeddings: np.ndarray,
                 tokenizer, max_stages: int):
        self.encoder = encoder_session
        self.codebook = codebook       # [stages, K, dim]
        self.corpus_texts = corpus_texts
        self.corpus_embeddings = corpus_embeddings  # [N, dim]
        self.tokenizer = tokenizer
        self.max_stages = max_stages
        self.dim = codebook.shape[2]
        self.k = codebook.shape[1]

    def _encode_text(self, text: str) -> np.ndarray:
        """Encode text to normalized embedding via ONNX encoder."""
        tokens = self.tokenizer(text, return_tensors="np",
                                padding=True, truncation=True, max_length=128)
        input_ids = tokens["input_ids"].astype(np.int64)
        attention_mask = tokens["attention_mask"].astype(np.int64)

        outputs = self.encoder.run(None, {
            "input_ids": input_ids,
            "attention_mask": attention_mask,
        })
        # Mean pooling over non-masked tokens
        hidden = outputs[0]  # [1, seq_len, dim]
        mask_expanded = attention_mask[:, :, np.newaxis].astype(np.float32)
        summed = np.sum(hidden * mask_expanded, axis=1)
        counts = np.clip(mask_expanded.sum(axis=1), a_min=1e-9, a_max=None)
        embedding = (summed / counts).squeeze(0)
        # L2 normalize
        norm = np.linalg.norm(embedding)
        if norm > 0:
            embedding = embedding / norm
        return embedding.astype(np.float32)

    def _quantize(self, embedding: np.ndarray, max_stages: int) -> tuple[list[int], float]:
        """Residual VQ encode: find nearest codebook entry per stage."""
        stages = min(max_stages, self.max_stages) if max_stages > 0 else self.max_stages
        residual = embedding.copy()
        indices = []

        for s in range(stages):
            # Find nearest codebook entry
            dists = np.linalg.norm(self.codebook[s] - residual, axis=1)
            best_idx = int(np.argmin(dists))
            indices.append(best_idx)
            residual = residual - self.codebook[s, best_idx]

        # Estimate fidelity: cosine similarity between original and reconstructed
        reconstructed = np.zeros(self.dim, dtype=np.float32)
        for s, idx in enumerate(indices):
            reconstructed += self.codebook[s, idx]
        cos_sim = float(np.dot(embedding, reconstructed) / (
            np.linalg.norm(embedding) * np.linalg.norm(reconstructed) + 1e-8
        ))

        return indices, cos_sim

    def _decode_indices(self, indices: list[int]) -> str:
        """Decode codebook indices to nearest corpus text."""
        reconstructed = np.zeros(self.dim, dtype=np.float32)
        for s, idx in enumerate(indices):
            if s < self.max_stages:
                reconstructed += self.codebook[s, idx]

        # Nearest neighbor in corpus
        sims = self.corpus_embeddings @ reconstructed / (
            np.linalg.norm(self.corpus_embeddings, axis=1) *
            np.linalg.norm(reconstructed) + 1e-8
        )
        best_idx = int(np.argmax(sims))
        return self.corpus_texts[best_idx]

    def Encode(self, request, context):
        if not request.data:
            return msvqsc_pb2.EncodeResponse(
                encoded=b"", stages_used=0, estimated_fidelity=0.0,
                original_size=0, duration_ms=0,
            )

        text = request.data.decode("utf-8", errors="replace")
        original_size = len(request.data)
        max_stages = request.max_stages if request.max_stages > 0 else 0

        start = time.monotonic()
        try:
            embedding = self._encode_text(text)
            indices, fidelity = self._quantize(embedding, max_stages)
            wire = pack_wire(indices)
        except Exception as e:
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(f"encode failed: {e}")
            return msvqsc_pb2.EncodeResponse()
        elapsed_ms = int((time.monotonic() - start) * 1000)

        log.info(
            "Encoded %d → %d bytes (%d stages, fidelity=%.3f) in %dms",
            original_size, len(wire), len(indices), fidelity, elapsed_ms,
        )

        return msvqsc_pb2.EncodeResponse(
            encoded=wire,
            stages_used=len(indices),
            estimated_fidelity=fidelity,
            original_size=original_size,
            duration_ms=elapsed_ms,
        )

    def Decode(self, request, context):
        if not request.encoded:
            return msvqsc_pb2.DecodeResponse(data=b"", stages_used=0, duration_ms=0)

        start = time.monotonic()
        try:
            indices, stages, _version = unpack_wire(request.encoded)
            text = self._decode_indices(indices)
        except Exception as e:
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(f"decode failed: {e}")
            return msvqsc_pb2.DecodeResponse()
        elapsed_ms = int((time.monotonic() - start) * 1000)

        return msvqsc_pb2.DecodeResponse(
            data=text.encode("utf-8"),
            stages_used=stages,
            duration_ms=elapsed_ms,
        )

    def Health(self, request, context):
        return msvqsc_pb2.HealthResponse(
            ready=self.encoder is not None,
            encoder_model="all-MiniLM-L6-v2",
            codebook_stages=self.max_stages,
            codebook_entries=self.k,
            embedding_dim=self.dim,
            corpus_size=len(self.corpus_texts),
            device="cpu",
        )


def resolve_path(env_path: str, default_name: str) -> str:
    """Resolve model file path from env or default directory."""
    if env_path:
        return env_path
    return str(Path(MODEL_DIR) / default_name)


def serve():
    import onnxruntime as ort
    from transformers import AutoTokenizer

    # Load models
    codebook_path = resolve_path(CODEBOOK_PATH, "codebook_v1.bin")
    corpus_path = resolve_path(CORPUS_PATH, "corpus_index.bin")
    encoder_path = resolve_path(ENCODER_PATH, "encoder.onnx")

    log.info("Loading codebook from %s", codebook_path)
    codebook, _version, stages, k = load_codebook(codebook_path)

    log.info("Loading corpus index from %s", corpus_path)
    corpus_texts, corpus_embeddings = load_corpus_index(corpus_path)

    log.info("Loading ONNX encoder from %s", encoder_path)
    session = ort.InferenceSession(encoder_path, providers=["CPUExecutionProvider"])

    log.info("Loading tokenizer: all-MiniLM-L6-v2")
    tokenizer = AutoTokenizer.from_pretrained("sentence-transformers/all-MiniLM-L6-v2")

    # Start gRPC server
    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=MAX_WORKERS),
        options=[
            ("grpc.max_send_message_length", 1 * 1024 * 1024),
            ("grpc.max_receive_message_length", 1 * 1024 * 1024),
        ],
    )
    servicer = MSVQSCServicer(
        encoder_session=session,
        codebook=codebook,
        corpus_texts=corpus_texts,
        corpus_embeddings=corpus_embeddings,
        tokenizer=tokenizer,
        max_stages=stages,
    )
    msvqsc_pb2_grpc.add_MSVQSCServiceServicer_to_server(servicer, server)
    server.add_insecure_port(LISTEN_ADDR)
    log.info("MSVQ-SC sidecar listening on %s", LISTEN_ADDR)
    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
