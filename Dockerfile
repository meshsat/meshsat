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
        libfftw3-dev:arm64 libtclap-dev:arm64; \
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
# Pinned to a commit: both patches below are hunks against this source,
# and an unpinned HEAD could stop them applying or change the V4 code
# under us without a commit here. [MESHSAT-1222]
ARG RTLSDR_BLOG_SHA=aed0ea19f3a273370a13c9009b96313c75d54c7b
RUN git init -q /src/rtl && cd /src/rtl && \
    git fetch -q --depth=1 https://github.com/rtlsdrblog/rtl-sdr-blog.git "$RTLSDR_BLOG_SHA" && \
    git checkout -q FETCH_HEAD
# Never USB-reset the dongle from inside rtlsdr_open: on the Blog V4 that
# reset is what left parallax's dongle unable to enumerate on 17 Sep 2026.
# The open fails instead and the bridge escalates to a hub-port power
# cycle. The sanity check fails the build if the reset call survives.
# [MESHSAT-1222]
COPY docker-patches/librtlsdr-no-reset-on-open.patch /tmp/librtlsdr-no-reset.patch
RUN cd /src/rtl && patch -p1 < /tmp/librtlsdr-no-reset.patch && \
    if grep -q libusb_reset_device src/librtlsdr.c; then \
      echo "patch sanity failed: libusb_reset_device still in librtlsdr.c"; exit 1; \
    fi
# rtl_tcp swallowed a SIGTERM that arrived while a client was connected
# (it only ended the session and listened again), so the bridge had to
# SIGKILL it and rtlsdr_close never ran. A real signal now ends the
# process. [MESHSAT-1222]
COPY docker-patches/rtl_tcp-exit-on-signal.patch /tmp/rtl_tcp-exit.patch
RUN cd /src/rtl && patch -p1 < /tmp/rtl_tcp-exit.patch && \
    count=$(grep -c signal_exit src/rtl_tcp.c) && \
    [ "$count" -ge 3 ] || { echo "patch sanity failed: only $count signal_exit lines in rtl_tcp.c"; exit 1; }
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
# Build rtl_power_fftw, now only the rollback scanner
# (MESHSAT_SPECTRUM_READER=rtl_power_fftw). It is NOT asynchronous, as this
# comment used to claim: Rtlsdr::read() calls rtlsdr_reset_buffer and then
# rtlsdr_read_sync with BULK_TIMEOUT 0, so a read the V4 never answers
# blocks forever, and the bridge started one per band per pass. That was
# the stall behind the RTL-SDR drop-offs of 10-18 Sep 2026. The default
# reader is rtl_tcp (rtlsdr_read_async, opened once), copied into the
# runtime image below. [MESHSAT-509, MESHSAT-1222]
ARG RTL_POWER_FFTW_SHA=cee9a22207ea995bd12adbc6bcfbec92521548b1
RUN git init -q /src/rpfftw && cd /src/rpfftw && \
    git fetch -q --depth=1 https://github.com/AD-Vega/rtl-power-fftw.git "$RTL_POWER_FFTW_SHA" && \
    git checkout -q FETCH_HEAD

RUN mkdir /src/rtl/build && cd /src/rtl/build && \
    # Install into the c-builder's own /usr/local so librtlsdr.pc has
    # prefix=/usr/local and rpfftw's pkg_check_modules finds a
    # self-consistent install. The runtime image's /usr/local/lib will
    # also be /usr/local/lib so the linker path baked into rpfftw stays
    # valid post-COPY.
    if [ "$TARGETARCH" = "arm64" ]; then \
      export PKG_CONFIG_PATH=/usr/lib/aarch64-linux-gnu/pkgconfig && \
      export PKG_CONFIG_LIBDIR=/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig && \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/usr/local \
        -DCMAKE_C_COMPILER=aarch64-linux-gnu-gcc \
        -DCMAKE_SYSTEM_NAME=Linux \
        -DCMAKE_SYSTEM_PROCESSOR=aarch64 \
        -DINSTALL_UDEV_RULES=OFF \
        -DDETACH_KERNEL_DRIVER=ON; \
    else \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/usr/local \
        -DINSTALL_UDEV_RULES=OFF \
        -DDETACH_KERNEL_DRIVER=ON; \
    fi && \
    make -j"$(nproc)" && make install && ldconfig 2>/dev/null || true

# Build rpfftw against the librtlsdr-blog we just installed to /out.
# The headers are also pulled from the Blog fork install so rpfftw
# sees V4-aware init code when it links against librtlsdr.
RUN mkdir /src/rpfftw/build && cd /src/rpfftw/build && \
    # librtlsdr is now at /usr/local of the c-builder. Its .pc file's
    # prefix=/usr/local matches the actual install layout — pkg-config
    # works normally and the cross-arch libusb is picked up from its
    # multiarch location.
    if [ "$TARGETARCH" = "arm64" ]; then \
      export PKG_CONFIG_PATH=/usr/local/lib/pkgconfig:/usr/lib/aarch64-linux-gnu/pkgconfig && \
      export PKG_CONFIG_LIBDIR=/usr/local/lib/pkgconfig:/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig && \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/usr/local \
        -DCMAKE_C_COMPILER=aarch64-linux-gnu-gcc \
        -DCMAKE_CXX_COMPILER=aarch64-linux-gnu-g++ \
        -DCMAKE_SYSTEM_NAME=Linux \
        -DCMAKE_SYSTEM_PROCESSOR=aarch64; \
    else \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/usr/local; \
    fi && \
    make -j"$(nproc)" && make install

# Build Direwolf 1.8.x bundled inside the image. Previously ran as a host-side
# systemd unit with udev auto-start; bundling moves APRS TNC lifecycle under
# MeshSat's own supervisor so the field-kit provisioning loses four manual
# steps. ALSA is the only mandatory dependency (HIDAPI/GPSD/hamlib/libgpiod
# are all optional in Direwolf's CMake and unused by our AIOC+RTS-PTT path).
# [MESHSAT-515]
FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS direwolf-builder
ARG TARGETARCH
# libudev-dev + libhidapi-dev are needed for CM108 PTT — the AIOC keys
# the radio's PTT via the CM108/CM109/CM119 HID GPIO chip, NOT via
# serial DTR/RTS. Without these, Direwolf compiles but `PTT CM108` in
# the conf is a silent no-op. [MESHSAT-514, debugged 2026-04-17]
RUN apt-get update -qq && \
    apt-get install -y -qq --no-install-recommends \
      gcc g++ libc6-dev git cmake make pkg-config ca-certificates && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      dpkg --add-architecture arm64 && apt-get update -qq && \
      apt-get install -y -qq --no-install-recommends \
        gcc-aarch64-linux-gnu g++-aarch64-linux-gnu libc6-dev-arm64-cross \
        libasound2-dev:arm64 libudev-dev:arm64 libhidapi-dev:arm64; \
    else \
      apt-get install -y -qq --no-install-recommends \
        libasound2-dev libudev-dev libhidapi-dev; \
    fi && \
    rm -rf /var/lib/apt/lists/*

# Pin to 1.8 branch. We track upstream but don't chase dev builds — field
# kits need the stable KISS/AFSK path, not experimental IL2P changes.
RUN git clone --depth=1 --branch=1.8 https://github.com/wb2osz/direwolf /src/direwolf

# Bind the KISS TCP server to loopback only. Upstream hardcodes
# INADDR_ANY (kissnet.c), and KISSPORT has no IP-bind option in the conf
# grammar. Our containers run with network_mode=host; without this patch
# MeshSat would expose :8001 to the kit's LAN. One-line in-tree edit —
# simpler than maintaining a patch file. [MESHSAT-517]
RUN grep -q 'sin_addr.s_addr = INADDR_ANY' /src/direwolf/src/kissnet.c && \
    sed -i 's|sin_addr.s_addr = INADDR_ANY|sin_addr.s_addr = htonl(INADDR_LOOPBACK)|' /src/direwolf/src/kissnet.c && \
    grep -q 'htonl(INADDR_LOOPBACK)' /src/direwolf/src/kissnet.c

RUN mkdir /src/direwolf/build && cd /src/direwolf/build && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      export PKG_CONFIG_PATH=/usr/lib/aarch64-linux-gnu/pkgconfig && \
      export PKG_CONFIG_LIBDIR=/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig && \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/usr/local \
        -DCMAKE_C_COMPILER=aarch64-linux-gnu-gcc \
        -DCMAKE_CXX_COMPILER=aarch64-linux-gnu-g++ \
        -DCMAKE_SYSTEM_NAME=Linux \
        -DCMAKE_SYSTEM_PROCESSOR=aarch64 \
        -DFORCE_SSE=OFF; \
    else \
      cmake .. \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/usr/local; \
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
      libusb-1.0-0 libfftw3-single3 \
      libasound2 alsa-utils usbutils procps \
      libudev1 libhidapi-hidraw0 \
      # host-ops: allowed-in-standalone
      # BT + WiFi host-ops tooling [MESHSAT-623 / MESHSAT-624].
      # bluez -> bluetoothctl; wpasupplicant -> wpa_cli; iw -> WiFi scan;
      # rfkill -> radio block/unblock; iproute2 -> ip/ss used by helpers;
      # util-linux supplies ns-entering tools used by the HAL-ported
      # handlers (gated by the standalone-mode pragma above).
      bluez wpasupplicant iw rfkill iproute2 util-linux && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder   /meshsat                    /usr/local/bin/meshsat
COPY --from=c-builder /jspr-helper                /usr/local/bin/jspr-helper
COPY --chmod=755      cmd/jspr-helper/jspr_helper.py /usr/local/bin/jspr_helper.py

# Bundled Direwolf + preflight. The script is PreStart'd by the Go supervisor
# in internal/gateway/direwolf_supervisor.go, not by systemd. [MESHSAT-514]
COPY --from=direwolf-builder /out/usr/local/bin/direwolf      /usr/local/bin/direwolf
COPY --from=direwolf-builder /out/usr/local/share/direwolf    /usr/local/share/direwolf
COPY --chmod=755 scripts/direwolf-preflight.sh                /usr/local/bin/direwolf-preflight.sh

# Bring in the Blog V4-capable rtl_power + librtlsdr. DESTDIR=/out from
# the c-builder stage gives us /out/usr/local/bin/rtl_* and
# /out/usr/local/lib/librtlsdr.so*. We copy the whole tree under
# /usr/local so the SONAME symlinks and binary layout are preserved.
COPY --from=c-builder /usr/local/bin/rtl_power       /usr/local/bin/rtl_power
COPY --from=c-builder /usr/local/bin/rtl_test        /usr/local/bin/rtl_test
COPY --from=c-builder /usr/local/bin/rtl_sdr         /usr/local/bin/rtl_sdr
COPY --from=c-builder /usr/local/bin/rtl_power_fftw  /usr/local/bin/rtl_power_fftw
# The spectrum reader: one long-lived async process holds the dongle and
# the bridge retunes it per band over loopback TCP. [MESHSAT-1222]
COPY --from=c-builder /usr/local/bin/rtl_tcp         /usr/local/bin/rtl_tcp
COPY --from=c-builder /usr/local/lib/                /usr/local/lib/
RUN ldconfig

EXPOSE 6050

ENTRYPOINT ["meshsat"]
