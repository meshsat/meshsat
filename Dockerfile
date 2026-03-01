FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /meshsat ./cmd/meshsat

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget
COPY --from=builder /meshsat /usr/local/bin/meshsat

EXPOSE 6050

ENTRYPOINT ["meshsat"]
