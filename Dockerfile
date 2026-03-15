# CI pre-builds arm64 + amd64 binaries with embedded web assets.
# This Dockerfile just picks the right one — no QEMU, no Go, no Node.
FROM alpine:3.21

ARG TARGETARCH=amd64
RUN apk add --no-cache ca-certificates wget coreutils
COPY meshsat-${TARGETARCH} /usr/local/bin/meshsat
RUN chmod +x /usr/local/bin/meshsat

EXPOSE 6050

ENTRYPOINT ["meshsat"]
