#!/bin/sh
set -e

# ==============================================================================
# Steinel CAM Bridge — Dynamic Nabto SDK Loader & Entrypoint
# ==============================================================================
# This script ensures that the exact required version of the Nabto Edge Client
# shared library (libnabto_client.so) is present in /data/lib/ before launching
# the bridge binary.
#
# Because libnabto_client.so is proprietary software owned by Nabto ApS, it is
# downloaded client-side directly from the official Nabto GitHub releases
# repository upon first launch, eliminating copyright/distribution issues.
# ==============================================================================

NABTO_SDK_VERSION="${NABTO_SDK_VERSION:-5.15.4}"
TARGET_DIR="/data/lib/${NABTO_SDK_VERSION}"
TARGET_SO="${TARGET_DIR}/libnabto_client.so"
VERSION_FILE="/data/lib/installed_version.txt"

# 1. Detect Host Architecture
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)
        NABTO_ARCH="linux-x86_64"
        ;;
    aarch64|arm64)
        NABTO_ARCH="linux-aarch64"
        ;;
    *)
        echo "[Nabto Setup] ❌ Unsupported architecture: ${ARCH}" >&2
        exit 1
        ;;
esac

# 2. Check if the required SDK version is already installed in /data/lib
INSTALLED_VERSION=""
if [ -f "$VERSION_FILE" ]; then
    INSTALLED_VERSION="$(cat "$VERSION_FILE" 2>/dev/null || true)"
fi

if [ "$INSTALLED_VERSION" != "$NABTO_SDK_VERSION" ] || [ ! -s "$TARGET_SO" ]; then
    echo "[Nabto Setup] 🔄 Setting up Nabto Edge Client SDK v${NABTO_SDK_VERSION} (${NABTO_ARCH})..."
    mkdir -p /tmp/nabto "$TARGET_DIR"

    # Download from official Nabto GitHub releases repository
    DOWNLOAD_URL="https://github.com/nabto/nabto-client-sdk-releases/archive/refs/tags/v${NABTO_SDK_VERSION}.tar.gz"
    if ! curl -fsSL "$DOWNLOAD_URL" -o /tmp/nabto/sdk.tar.gz; then
        echo "[Nabto Setup] ⚠️ Tag v${NABTO_SDK_VERSION} not found on GitHub, falling back to main branch..."
        curl -fsSL "https://github.com/nabto/nabto-client-sdk-releases/archive/refs/heads/main.tar.gz" -o /tmp/nabto/sdk.tar.gz
    fi

    tar -xzf /tmp/nabto/sdk.tar.gz -C /tmp/nabto
    EXTRACTED="$(find /tmp/nabto -maxdepth 1 -type d -name 'nabto-client-sdk-releases*' | head -n 1)"

    if [ -f "${EXTRACTED}/lib/${NABTO_ARCH}/libnabto_client.so" ]; then
        cp "${EXTRACTED}/lib/${NABTO_ARCH}/libnabto_client.so" "$TARGET_SO"
    else
        echo "[Nabto Setup] ❌ Could not find ${NABTO_ARCH}/libnabto_client.so in downloaded SDK package!" >&2
        rm -rf /tmp/nabto
        exit 1
    fi

    rm -rf /tmp/nabto

    # Remove obsolete SDK version directories in /data/lib/ to prevent disk bloat
    find /data/lib -mindepth 1 -maxdepth 1 -type d ! -name "${NABTO_SDK_VERSION}" -exec rm -rf {} + 2>/dev/null || true

    echo "$NABTO_SDK_VERSION" > "$VERSION_FILE"
    echo "[Nabto Setup] ✅ Nabto Client SDK v${NABTO_SDK_VERSION} installed successfully."
fi

# 3. Export library path for dynamic linker
export LD_LIBRARY_PATH="${TARGET_DIR}:/data/lib:/usr/lib:${LD_LIBRARY_PATH}"

# 4. Launch Steinel CAM Bridge
exec /app/steinel-bridge "$@"
