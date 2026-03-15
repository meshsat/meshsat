FROM python:3.11-slim AS builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends gcc g++ cmake && \
    rm -rf /var/lib/apt/lists/*

COPY sidecar/msvqsc/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

FROM python:3.11-slim

WORKDIR /app

# Copy installed packages from builder
COPY --from=builder /usr/local/lib/python3.11/site-packages /usr/local/lib/python3.11/site-packages
COPY --from=builder /usr/local/bin /usr/local/bin

# Generate Python protobuf stubs from the proto definition
COPY proto/msvqsc/ proto/msvqsc/
RUN python -m grpc_tools.protoc \
    -Iproto/msvqsc \
    --python_out=. \
    --grpc_python_out=. \
    proto/msvqsc/msvqsc.proto

COPY sidecar/msvqsc/server.py .
COPY sidecar/msvqsc/benchmark.py .
COPY sidecar/msvqsc/train.py .

# Model/codebook cache volume
VOLUME /models

ENV MSVQSC_LISTEN=0.0.0.0:50052
ENV MSVQSC_MODEL_DIR=/models
EXPOSE 50052

ENTRYPOINT ["python", "server.py"]
