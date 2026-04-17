FROM node:22-alpine AS web-builder

WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit
COPY web/ .
RUN npm run build

# Cross-compile C helpers + librtlsdr-blog natively (no QEMU). Debian has
# proper aarch64 cross-compilers; QEMU GCC segfaults on Alpine during
# any non-trivial C compile (observed on jspr-helper, now on librtlsdr).
FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS c-builder
ARG TARGETARCH
RUN apt-get update -qq && \
    apt-get install -y -qq --no-install-recommends \
      gcc libc6-dev git cmake make pkg-config ca-certificates && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      dpkg --add-architecture arm64 && apt-get update -qq && \
      apt-get install -y -qq --no-install-recommends \
        gcc-aarch64-linux-gnu libc6-dev-arm64-cross \
        libusb-1.0-0-dev:arm64; \
    else \
      apt-get install -y -qq --no-install-recommends libusb-1.0-0-dev; \
    fi && \
    rm -rf /var/lib/apt/lists/*

COPY cmd/jspr-helper/main.c /tmp/main.c
RUN if [ "$TARGETARCH" = "arm64" ]; then \
      aarch64-linux-gnu-gcc -O2 -Wall -static -o /jspr-helper /tmp/main.c; \
    else \
      gcc -O2 -Wall -static -o /jspr-helper /tmp/main.c; \
    fi

# Build the rtl-sdr-blog fork of librtlsdr. The RTL-SDR Blog V4 uses an
# R828D tuner that upstream librtlsdr does not correctly tune in the
# 800-900 MHz range — rtl_power hangs indefinitely on LoRa EU868 and
# LTE 800/900 with the stock Alpine rtl-sdr package. The Blog fork
# (https://github.com/rtlsdrblog/rtl-sdr-blog) carries the V4 tuning
# patches. [MESHSAT-509 — parallax01 RTL-SDR Blog V4 detected 2026-04-17]
RUN git clone --depth=1 https://github.com/rtlsdrblog/rtl-sdr-blog.git /src/rtl
RUN mkdir /src/rtl/build && cd /src/rtl/build && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_C_COMPILER=aarch64-linux-gnu-gcc \
        -DCMAKE_SYSTEM_NAME=Linux \
        -DCMAKE_SYSTEM_PROCESSOR=aarch64 \
        -DCMAKE_FIND_ROOT_PATH_MODE_LIBRARY=ONLY \
        -DCMAKE_FIND_ROOT_PATH_MODE_INCLUDE=ONLY \
        -DINSTALL_UDEV_RULES=OFF \
        -DDETACH_KERNEL_DRIVER=ON \
        -DLIBUSB_LIBRARIES=/usr/lib/aarch64-linux-gnu/libusb-1.0.so \
        -DLIBUSB_INCLUDE_DIR=/usr/include/libusb-1.0; \
    else \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DINSTALL_UDEV_RULES=OFF \
        -DDETACH_KERNEL_DRIVER=ON; \
    fi && \
    make -j"$(nproc)" && make install DESTDIR=/out

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

ARG TARGETARCH
ARG TARGETOS=linux

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web-builder /web/dist ./cmd/meshsat/web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /meshsat ./cmd/meshsat

# Runtime: Debian bookworm-slim (glibc) matches the c-builder stage's
# glibc, so librtlsdr.so and rtl_power copied from there run without
# musl shenanigans. Previous revision used alpine:3.21 but the Blog V4
# driver fork couldn't be built under QEMU on Alpine (GCC segfaults).
# [MESHSAT-509]
FROM debian:bookworm-slim

RUN apt-get update -qq && \
    apt-get install -y -qq --no-install-recommends \
      ca-certificates wget coreutils python3 python3-serial \
      libusb-1.0-0 && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder   /meshsat                    /usr/local/bin/meshsat
COPY --from=c-builder /jspr-helper                /usr/local/bin/jspr-helper
COPY --chmod=755      cmd/jspr-helper/jspr_helper.py /usr/local/bin/jspr_helper.py

# Bring in the Blog V4-capable rtl_power + librtlsdr. DESTDIR=/out from
# the c-builder stage gives us /out/usr/local/bin/rtl_* and
# /out/usr/local/lib/librtlsdr.so*. We copy the whole tree under
# /usr/local so the SONAME symlinks and binary layout are preserved.
COPY --from=c-builder /out/usr/local/bin/rtl_power /usr/local/bin/rtl_power
COPY --from=c-builder /out/usr/local/bin/rtl_test  /usr/local/bin/rtl_test
COPY --from=c-builder /out/usr/local/lib/         /usr/local/lib/
RUN ldconfig

EXPOSE 6050

ENTRYPOINT ["meshsat"]
