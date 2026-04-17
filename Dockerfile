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
      gcc g++ libc6-dev git cmake make pkg-config ca-certificates patch && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      dpkg --add-architecture arm64 && apt-get update -qq && \
      apt-get install -y -qq --no-install-recommends \
        gcc-aarch64-linux-gnu g++-aarch64-linux-gnu libc6-dev-arm64-cross \
        libusb-1.0-0-dev:arm64 \
        libfftw3-dev:arm64 libtclap-dev; \
    else \
      apt-get install -y -qq --no-install-recommends \
        libusb-1.0-0-dev libfftw3-dev libtclap-dev; \
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
# Patch rtl_power to call rtlsdr_reset_buffer before each sync read.
# Without this, rtl_power hangs forever on the Blog V4's R828D tuner
# because librtlsdr's BULK_TIMEOUT is 0 and the un-primed bulk endpoint
# never delivers data. rtl_test (async) has always done this correctly
# — rtl_power doesn't. [MESHSAT-509]
COPY docker-patches/rtl_power-v4-reset-buffer.patch /tmp/rtl_power-v4.patch
RUN cd /src/rtl && patch -p1 < /tmp/rtl_power-v4.patch && \
    # Sanity check: one hunk adds rtlsdr_reset_buffer(d) in retune(),
    # the other adds a second verbose_reset_buffer(dev) in main(). Both
    # match the common "reset_buffer" substring; expect >=3 occurrences
    # (1 original verbose_reset_buffer + 2 additions).
    count=$(grep -c reset_buffer src/rtl_power.c) && \
    [ "$count" -ge 3 ] || { echo "patch sanity failed: only $count reset_buffer lines"; exit 1; }
# Build rtl_power_fftw — async-threaded variant of rtl_power that
# actually works on the RTL-SDR Blog V4. rtl_power's single-URB sync
# read hangs on the V4's R828D regardless of reset_buffer patches;
# rtl_power_fftw uses a multi-buffer async read in a separate thread
# which keeps the bulk endpoint streaming. Same stdout format as
# a two-column freq+power CSV, parsed by the Go scanner. [MESHSAT-509]
RUN git clone --depth=1 https://github.com/AD-Vega/rtl-power-fftw.git /src/rpfftw

RUN mkdir /src/rtl/build && cd /src/rtl/build && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      # Point pkg-config at the arm64 multiarch dir so CMakeLists.txt's
      # pkg_check_modules(LIBUSB libusb-1.0) finds the cross-installed
      # headers+libs. Without this, upstream CMake hits
      # "Package 'libusb-1.0', required by 'virtual:world', not found".
      export PKG_CONFIG_PATH=/usr/lib/aarch64-linux-gnu/pkgconfig && \
      export PKG_CONFIG_LIBDIR=/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig && \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_C_COMPILER=aarch64-linux-gnu-gcc \
        -DCMAKE_SYSTEM_NAME=Linux \
        -DCMAKE_SYSTEM_PROCESSOR=aarch64 \
        -DINSTALL_UDEV_RULES=OFF \
        -DDETACH_KERNEL_DRIVER=ON; \
    else \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DINSTALL_UDEV_RULES=OFF \
        -DDETACH_KERNEL_DRIVER=ON; \
    fi && \
    make -j"$(nproc)" && make install DESTDIR=/out

# Build rpfftw against the librtlsdr-blog we just installed to /out.
# The headers are also pulled from the Blog fork install so rpfftw
# sees V4-aware init code when it links against librtlsdr.
RUN mkdir /src/rpfftw/build && cd /src/rpfftw/build && \
    # PKG_CONFIG_PATH must include /out/usr/local/lib/pkgconfig so
    # rpfftw's cmake finds librtlsdr.pc. The .pc's prefix= says
    # /usr/local (from the librtlsdr cmake install default), but the
    # files are actually at /out/usr/local — so we pass explicit
    # include + lib paths as CXX/LD flags, which win over the .pc's
    # stale prefix hint.
    FLAGS="-I/out/usr/local/include -L/out/usr/local/lib" && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      export PKG_CONFIG_PATH=/out/usr/local/lib/pkgconfig:/usr/lib/aarch64-linux-gnu/pkgconfig && \
      export PKG_CONFIG_LIBDIR=/out/usr/local/lib/pkgconfig:/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig && \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_C_COMPILER=aarch64-linux-gnu-gcc \
        -DCMAKE_CXX_COMPILER=aarch64-linux-gnu-g++ \
        -DCMAKE_SYSTEM_NAME=Linux \
        -DCMAKE_SYSTEM_PROCESSOR=aarch64 \
        -DCMAKE_C_FLAGS="$FLAGS" \
        -DCMAKE_CXX_FLAGS="$FLAGS" \
        -DCMAKE_EXE_LINKER_FLAGS="$FLAGS"; \
    else \
      export PKG_CONFIG_PATH=/out/usr/local/lib/pkgconfig && \
      cmake .. -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_C_FLAGS="$FLAGS" \
        -DCMAKE_CXX_FLAGS="$FLAGS" \
        -DCMAKE_EXE_LINKER_FLAGS="$FLAGS"; \
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
      libusb-1.0-0 libfftw3-single3 && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder   /meshsat                    /usr/local/bin/meshsat
COPY --from=c-builder /jspr-helper                /usr/local/bin/jspr-helper
COPY --chmod=755      cmd/jspr-helper/jspr_helper.py /usr/local/bin/jspr_helper.py

# Bring in the Blog V4-capable rtl_power + librtlsdr. DESTDIR=/out from
# the c-builder stage gives us /out/usr/local/bin/rtl_* and
# /out/usr/local/lib/librtlsdr.so*. We copy the whole tree under
# /usr/local so the SONAME symlinks and binary layout are preserved.
COPY --from=c-builder /out/usr/local/bin/rtl_power       /usr/local/bin/rtl_power
COPY --from=c-builder /out/usr/local/bin/rtl_test        /usr/local/bin/rtl_test
COPY --from=c-builder /out/usr/local/bin/rtl_sdr         /usr/local/bin/rtl_sdr
COPY --from=c-builder /out/usr/local/bin/rtl_power_fftw  /usr/local/bin/rtl_power_fftw
COPY --from=c-builder /out/usr/local/lib/                /usr/local/lib/
RUN ldconfig

EXPOSE 6050

ENTRYPOINT ["meshsat"]
