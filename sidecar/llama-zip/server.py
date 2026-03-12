"""llama-zip gRPC compression sidecar for MeshSat.

Provides LLM-based arithmetic coding compression using llama-zip with
SmolLM2-135M-Instruct Q4_K_M as the language model backbone.
"""

import logging
import os
import time
from concurrent import futures
from pathlib import Path

import grpc
from huggingface_hub import hf_hub_download

import compress_pb2
import compress_pb2_grpc

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("llama-zip-sidecar")

# Model configuration
MODEL_REPO = os.environ.get(
    "LLAMAZIP_MODEL_REPO", "bartowski/SmolLM2-135M-Instruct-GGUF"
)
MODEL_FILE = os.environ.get(
    "LLAMAZIP_MODEL_FILE", "SmolLM2-135M-Instruct-Q4_K_M.gguf"
)
MODEL_CACHE = os.environ.get("LLAMAZIP_MODEL_CACHE", "/models")
LISTEN_ADDR = os.environ.get("LLAMAZIP_LISTEN", "0.0.0.0:50051")
MAX_WORKERS = int(os.environ.get("LLAMAZIP_WORKERS", "2"))
# Context window for the model (SmolLM2-135M supports 2048)
N_CTX = int(os.environ.get("LLAMAZIP_N_CTX", "2048"))


def download_model() -> str:
    """Download the GGUF model from HuggingFace Hub if not cached."""
    cache_path = Path(MODEL_CACHE) / MODEL_FILE
    if cache_path.exists():
        log.info("Model already cached: %s", cache_path)
        return str(cache_path)

    log.info("Downloading model %s/%s ...", MODEL_REPO, MODEL_FILE)
    path = hf_hub_download(
        repo_id=MODEL_REPO,
        filename=MODEL_FILE,
        cache_dir=MODEL_CACHE,
        local_dir=MODEL_CACHE,
    )
    log.info("Model downloaded: %s", path)
    return path


class CompressionServicer(compress_pb2_grpc.CompressionServiceServicer):
    """gRPC servicer wrapping llama-zip compress/decompress."""

    def __init__(self, model_path: str):
        self.model_path = model_path
        self.model_size = Path(model_path).stat().st_size
        self._compressor = None
        self._init_compressor()

    def _init_compressor(self):
        """Initialize llama-zip compressor with the GGUF model."""
        from llama_zip import LlamaZip

        log.info("Loading model into llama-zip (n_ctx=%d) ...", N_CTX)
        start = time.monotonic()
        self._compressor = LlamaZip(
            model_path=self.model_path,
            n_ctx=N_CTX,
            use_mmap=True,
            verbose=False,
        )
        elapsed = time.monotonic() - start
        log.info("Model loaded in %.1fs", elapsed)

    def Compress(self, request, context):
        if not request.data:
            return compress_pb2.CompressResponse(
                compressed=b"", original_size=0, duration_ms=0
            )

        text = request.data.decode("utf-8", errors="replace")
        original_size = len(request.data)

        start = time.monotonic()
        try:
            compressed = self._compressor.compress(text)
        except Exception as e:
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(f"compression failed: {e}")
            return compress_pb2.CompressResponse()
        elapsed_ms = int((time.monotonic() - start) * 1000)

        # llama-zip returns a base64 string; convert to raw bytes
        import base64

        compressed_bytes = base64.b64decode(compressed)

        log.info(
            "Compressed %d → %d bytes (%.1f%%) in %dms",
            original_size,
            len(compressed_bytes),
            (1 - len(compressed_bytes) / original_size) * 100 if original_size else 0,
            elapsed_ms,
        )

        return compress_pb2.CompressResponse(
            compressed=compressed_bytes,
            original_size=original_size,
            duration_ms=elapsed_ms,
        )

    def Decompress(self, request, context):
        if not request.compressed:
            return compress_pb2.DecompressResponse(data=b"", duration_ms=0)

        # llama-zip expects base64 input for decompression
        import base64

        b64_input = base64.b64encode(request.compressed).decode("ascii")

        start = time.monotonic()
        try:
            text = self._compressor.decompress(b64_input)
        except Exception as e:
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(f"decompression failed: {e}")
            return compress_pb2.DecompressResponse()
        elapsed_ms = int((time.monotonic() - start) * 1000)

        return compress_pb2.DecompressResponse(
            data=text.encode("utf-8"),
            duration_ms=elapsed_ms,
        )

    def Health(self, request, context):
        return compress_pb2.HealthResponse(
            ready=self._compressor is not None,
            model_name=f"{MODEL_REPO}/{MODEL_FILE}",
            model_size_bytes=self.model_size,
            device="cpu",
        )


def serve():
    model_path = download_model()

    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=MAX_WORKERS),
        options=[
            ("grpc.max_send_message_length", 1 * 1024 * 1024),
            ("grpc.max_receive_message_length", 1 * 1024 * 1024),
        ],
    )
    servicer = CompressionServicer(model_path)
    compress_pb2_grpc.add_CompressionServiceServicer_to_server(servicer, server)
    server.add_insecure_port(LISTEN_ADDR)
    log.info("llama-zip sidecar listening on %s", LISTEN_ADDR)
    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
