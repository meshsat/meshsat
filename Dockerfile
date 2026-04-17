FROM node:22-alpine AS web-builder

WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit
COPY web/ .
RUN npm run build

# Cross-compile C helper natively (no QEMU).
# Debian has proper aarch64 cross-compilers; QEMU GCC segfaults on Alpine.
FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS c-builder
ARG TARGETARCH
RUN apt-get update -qq && \
    apt-get install -y -qq --no-install-recommends gcc libc6-dev && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      apt-get install -y -qq --no-install-recommends gcc-aarch64-linux-gnu libc6-dev-arm64-cross; \
    fi && \
    rm -rf /var/lib/apt/lists/*
COPY cmd/jspr-helper/main.c /tmp/main.c
RUN if [ "$TARGETARCH" = "arm64" ]; then \
      aarch64-linux-gnu-gcc -O2 -Wall -static -o /jspr-helper /tmp/main.c; \
    else \
      gcc -O2 -Wall -static -o /jspr-helper /tmp/main.c; \
    fi

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

ARG TARGETARCH
ARG TARGETOS=linux

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web-builder /web/dist ./cmd/meshsat/web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /meshsat ./cmd/meshsat

FROM alpine:3.21

# Build the rtl-sdr-blog fork from source. The stock Alpine 'rtl-sdr'
# package wraps upstream librtlsdr, which does NOT correctly tune the
# RTL-SDR Blog V4's R828D silicon at 800-900 MHz — rtl_power hangs
# indefinitely on LoRa EU868 and LTE 800/900 monitoring. The Blog
# fork (https://github.com/rtlsdrblog/rtl-sdr-blog) carries the V4
# tuning patches and PPL/R828D handling required. Observed live on
# parallax01 2026-04-17.
#
# Build in a virtual pkg so the toolchain doesn't bloat the runtime.
RUN apk add --no-cache ca-certificates wget coreutils python3 py3-pyserial libusb && \
    apk add --no-cache --virtual .sdr-build cmake make gcc g++ musl-dev libusb-dev git && \
    git clone --depth=1 https://github.com/rtlsdrblog/rtl-sdr-blog.git /tmp/rtl-sdr-blog && \
    cd /tmp/rtl-sdr-blog && mkdir build && cd build && \
    cmake ../ -DCMAKE_BUILD_TYPE=Release -DINSTALL_UDEV_RULES=OFF -DDETACH_KERNEL_DRIVER=ON && \
    make -j"$(nproc)" && make install && \
    cd / && rm -rf /tmp/rtl-sdr-blog && \
    apk del .sdr-build

COPY --from=builder /meshsat /usr/local/bin/meshsat
COPY --from=c-builder /jspr-helper /usr/local/bin/jspr-helper
COPY --chmod=755 cmd/jspr-helper/jspr_helper.py /usr/local/bin/jspr_helper.py

EXPOSE 6050

ENTRYPOINT ["meshsat"]
