# syntax=docker/dockerfile:1
# ==============================================================================
# Steinel L 625 CAM SC — Standalone Native Go Bridge Dockerfile
# Supported Architectures: linux/amd64 (x86_64 NAS), linux/arm64 (Raspberry Pi / Apple Silicon)
# ==============================================================================

# --- Stage 1: Build native Go binary with CGo & Nabto SDK ---
FROM golang:1.26-bookworm AS builder

ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    gcc \
    ca-certificates \
    curl \
    tar \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 1. Download official Nabto Edge Client SDK artifacts dynamically (Cached)
ENV NABTO_TAG="main"
RUN curl -fsSL "https://github.com/nabto/nabto-client-sdk-releases/archive/refs/heads/${NABTO_TAG}.tar.gz" -o /tmp/nabto.tar.gz && \
    mkdir -p /tmp/nabto && \
    tar -xzf /tmp/nabto.tar.gz -C /tmp/nabto && \
    EXTRACTED="$(find /tmp/nabto -maxdepth 1 -type d -name 'nabto-client-sdk-releases*' | head -n 1)" && \
    mkdir -p /usr/include/nabto /usr/lib && \
    cp -r "${EXTRACTED}/include/nabto/"* /usr/include/nabto/ && \
    if [ "$TARGETARCH" = "arm64" ]; then \
        cp "${EXTRACTED}/lib/linux-aarch64/libnabto_client.so" /usr/lib/libnabto_client.so; \
    else \
        cp "${EXTRACTED}/lib/linux-x86_64/libnabto_client.so" /usr/lib/libnabto_client.so; \
    fi && \
    ldconfig && \
    rm -rf /tmp/nabto /tmp/nabto.tar.gz

# 2. Copy dependencies and download Go modules (Cached with BuildKit cache mount)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# 3. Copy source code
COPY . .

# 4. Compile stripped static/CGo binary with compiler cache & version injection
ARG APP_VERSION="dev"
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -ldflags="-s -w -X main.AppVersion=${APP_VERSION}" -o /app/steinel-bridge ./cmd/steinel-bridge && \
    mkdir -p /data && chown -R 1000:1000 /data

# --- Stage 2: Slim runtime image with dynamic SDK loader (0 Byte proprietary binaries) ---
FROM debian:bookworm-slim

ARG APP_VERSION="dev"

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    tar \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binary, entrypoint script, and data directory
COPY --from=builder /app/steinel-bridge /app/steinel-bridge
COPY entrypoint.sh /app/entrypoint.sh
COPY --from=builder --chown=1000:1000 /data /data

RUN chmod +x /app/entrypoint.sh

# OCI Image Labels
LABEL org.opencontainers.image.title="steinel-cam-bridge" \
      org.opencontainers.image.description="Standalone ONVIF, 2-Way Audio & Home Assistant Bridge for Steinel L 625 CAM SC" \
      org.opencontainers.image.version="${APP_VERSION}" \
      org.opencontainers.image.authors="Afrouper" \
      org.opencontainers.image.source="https://github.com/Afrouper/steinel-cam-bridge" \
      org.opencontainers.image.licenses="MIT"

# Default configuration environment variables
ENV NABTO_SDK_VERSION="5.15.4" \
    KEY_PATH="/data/client.key"

VOLUME ["/data"]

EXPOSE 8554 8554/udp 8000 3702/udp

ENTRYPOINT ["/app/entrypoint.sh"]
