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

# 1. Download official Nabto Edge Client SDK artifacts dynamically
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

# 2. Copy dependencies and source code
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 3. Compile stripped static/CGo binary
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /app/steinel-bridge ./cmd/steinel-bridge

# --- Stage 2: Ultra-minimal Distroless runtime image (~35 MB uncompressed, ~15 MB download) ---
FROM gcr.io/distroless/cc-debian12

WORKDIR /app

# Copy binary and runtime library
COPY --from=builder /app/steinel-bridge /app/steinel-bridge
COPY --from=builder /usr/lib/libnabto_client.so /lib/libnabto_client.so

# Default configuration environment variables
ENV CAMERA_IP="192.168.1.100"
ENV QR_CODE=""
ENV KEY_PATH="/data/client.key"
ENV RESOLUTION="1080p"
ENV RTSP_PORT="8554"
ENV RTSP_PATH="steinel"
ENV ONVIF_PORT="8000"
ENV MQTT_BROKER=""
ENV MQTT_USER=""
ENV MQTT_PASSWORD=""
ENV MQTT_TOPIC_PREFIX="steinel"
ENV MQTT_DISCOVERY_PREFIX="homeassistant"

VOLUME ["/data"]

EXPOSE 8554 8554/udp 8000 3702/udp

ENTRYPOINT ["/app/steinel-bridge"]
