# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/gateway .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget tzdata && \
    adduser -D -u 10001 app && \
    mkdir -p /data && chown app:app /data
WORKDIR /app
COPY --from=builder /out/gateway /app/gateway
ENV STATE_DIR=/data LISTEN_ADDR=:9090
EXPOSE 9090
USER app
ENTRYPOINT ["/app/gateway"]
