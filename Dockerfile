# syntax=docker/dockerfile:1
# ==============================================================================
# Steinel L 625 CAM SC — Standalone Native Go Bridge Dockerfile
# Supported Architectures: linux/amd64 (x86_64 NAS), linux/arm64 (Raspberry Pi / Apple Silicon)
# ==============================================================================

# --- Stage 1: Fast native builder with CGo cross-compilation ---
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETARCH
ARG TARGETOS
ARG BUILDPLATFORM

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    gcc \
    gcc-aarch64-linux-gnu \
    libc6-dev-arm64-cross \
    gcc-x86-64-linux-gnu \
    libc6-dev-amd64-cross \
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
    mkdir -p /usr/include/nabto /usr/lib /usr/lib/x86_64-linux-gnu /usr/lib/aarch64-linux-gnu /usr/aarch64-linux-gnu/lib && \
    cp -r "${EXTRACTED}/include/nabto/"* /usr/include/nabto/ && \
    cp "${EXTRACTED}/lib/linux-x86_64/libnabto_client.so" /usr/lib/x86_64-linux-gnu/libnabto_client.so && \
    cp "${EXTRACTED}/lib/linux-x86_64/libnabto_client.so" /usr/lib/libnabto_client.so && \
    cp "${EXTRACTED}/lib/linux-aarch64/libnabto_client.so" /usr/lib/aarch64-linux-gnu/libnabto_client.so && \
    cp "${EXTRACTED}/lib/linux-aarch64/libnabto_client.so" /usr/aarch64-linux-gnu/lib/libnabto_client.so && \
    rm -rf /tmp/nabto /tmp/nabto.tar.gz

# 2. Copy dependencies and download Go modules (Cached with BuildKit cache mount)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# 3. Copy source code
COPY . .

# 4. Compile pure Go launcher (statically linked, CGO_ENABLED=0) and CGo bridge binary with native cross-compilers
ARG APP_VERSION="dev"
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /app/launcher ./cmd/launcher && \
    if [ "$TARGETARCH" = "arm64" ]; then \
        export CC=aarch64-linux-gnu-gcc; \
    else \
        export CC=x86_64-linux-gnu-gcc; \
    fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${TARGETARCH} CC=${CC} go build -ldflags="-s -w -X main.AppVersion=${APP_VERSION}" -o /app/steinel-bridge ./cmd/steinel-bridge && \
    mkdir -p /data

# --- Stage 2: Ultra-minimal Distroless runtime (~25 MB Image, 0 Byte proprietary binaries) ---
FROM gcr.io/distroless/cc-debian12:latest

ARG APP_VERSION="dev"

WORKDIR /app

# Copy pure Go bootstrap launcher, CGo bridge binary, and data directory
COPY --from=builder /app/launcher /app/launcher
COPY --from=builder /app/steinel-bridge /app/steinel-bridge
COPY --from=builder /data /data

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

ENTRYPOINT ["/app/launcher"]

