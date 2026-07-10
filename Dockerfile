# ─── Stage 1: build ──────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# git is needed by some go module tools; ca-certificates for TLS in go get
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Download dependencies first (cached layer if go.mod/go.sum unchanged)
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and compile
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/gobot \
    ./cmd/bot

# ─── Stage 2: runtime ────────────────────────────────────────────────────────
FROM alpine:3.21

# TLS root certificates so the bot can reach Discord / AI APIs
RUN apk add --no-cache ca-certificates tzdata

# Run as a non-root user
RUN addgroup -S gobot && adduser -S gobot -G gobot

WORKDIR /app

COPY --from=builder /out/gobot .

# Data directory for SQLite DB (mounted as a volume in production)
RUN mkdir -p /data && chown gobot:gobot /data

USER gobot

ENTRYPOINT ["/app/gobot"]
