FROM node:22-alpine AS web-builder

WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit
COPY web/ .
RUN npm run build

FROM golang:1.24-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web-builder /web/dist ./cmd/meshsat/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /meshsat ./cmd/meshsat
RUN gcc -O2 -Wall -static -o /jspr-helper cmd/jspr-helper/main.c

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget coreutils
COPY --from=builder /meshsat /usr/local/bin/meshsat
COPY --from=builder /jspr-helper /usr/local/bin/jspr-helper

EXPOSE 6050

ENTRYPOINT ["meshsat"]
