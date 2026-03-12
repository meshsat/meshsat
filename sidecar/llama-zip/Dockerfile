FROM python:3.11-slim AS builder

WORKDIR /app

# Install build dependencies for llama-cpp-python (CPU-only)
RUN apt-get update && \
    apt-get install -y --no-install-recommends gcc g++ cmake && \
    rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

FROM python:3.11-slim

WORKDIR /app

# Copy installed packages from builder
COPY --from=builder /usr/local/lib/python3.11/site-packages /usr/local/lib/python3.11/site-packages
COPY --from=builder /usr/local/bin /usr/local/bin

# Generate Python protobuf stubs from the proto definition
COPY proto/ proto/
RUN python -m grpc_tools.protoc \
    -Iproto \
    --python_out=. \
    --grpc_python_out=. \
    proto/compress/compress.proto && \
    mv compress/compress_pb2.py compress/compress_pb2_grpc.py . && \
    rmdir compress

COPY sidecar/llama-zip/server.py .
COPY sidecar/llama-zip/benchmark.py .

# Model cache volume
VOLUME /models

ENV LLAMAZIP_LISTEN=0.0.0.0:50051
ENV LLAMAZIP_MODEL_CACHE=/models
EXPOSE 50051

ENTRYPOINT ["python", "server.py"]
