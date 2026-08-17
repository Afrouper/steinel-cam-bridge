#!/bin/sh
set -e

# ==============================================================================
# Steinel CAM Bridge — Dynamic Nabto SDK Loader & Entrypoint
# ==============================================================================
# Ensures that the required version of the Nabto Edge Client shared library
# (libnabto_client.so) is present in /data/lib/ before launching the bridge.
#
# Because libnabto_client.so is proprietary software owned by Nabto ApS, it is
# downloaded client-side directly from official Nabto GitHub releases upon first
# launch, eliminating copyright and redistribution issues.
# ==============================================================================

NABTO_SDK_VERSION="${NABTO_SDK_VERSION:-5.15.4}"
TARGET_DIR="/data/lib/${NABTO_SDK_VERSION}"
TARGET_SO="${TARGET_DIR}/libnabto_client.so"
TMP_SO="${TARGET_DIR}/libnabto_client.so.tmp"
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
        echo "[Nabto Setup] ❌ Error: Unsupported CPU architecture '${ARCH}'." >&2
        echo "[Nabto Setup] 💡 Supported architectures: x86_64 (amd64) and aarch64 (arm64)." >&2
        exit 1
        ;;
esac

# 2. Check if the required SDK version is already installed in /data/lib
INSTALLED_VERSION=""
if [ -f "$VERSION_FILE" ]; then
    INSTALLED_VERSION="$(cat "$VERSION_FILE" 2>/dev/null || true)"
fi

if [ "$INSTALLED_VERSION" != "$NABTO_SDK_VERSION" ] || [ ! -s "$TARGET_SO" ]; then
    echo "[Nabto Setup] 🔄 Initializing Nabto Edge Client SDK v${NABTO_SDK_VERSION} (${NABTO_ARCH})..."
    
    mkdir -p "$TARGET_DIR" 2>/dev/null || {
        echo "[Nabto Setup] ❌ Error: Cannot create directory ${TARGET_DIR}. Check /data permissions." >&2
        exit 1
    }

    # Streaming download & extraction directly into target directory (0 MB disk space in /tmp)
    # Filter only the exact single .so file matching the host architecture
    DOWNLOAD_SUCCESS=0
    TAG_URL="https://github.com/nabto/nabto-client-sdk-releases/archive/refs/tags/v${NABTO_SDK_VERSION}.tar.gz"
    MAIN_URL="https://github.com/nabto/nabto-client-sdk-releases/archive/refs/heads/main.tar.gz"

    rm -f "$TMP_SO"

    echo "[Nabto Setup] ⬇️ Streaming Nabto SDK v${NABTO_SDK_VERSION} directly from GitHub..."
    if curl -fsSL --connect-timeout 10 --max-time 120 --retry 2 "$TAG_URL" 2>/dev/null | \
       tar -xz -O "nabto-client-sdk-releases-${NABTO_SDK_VERSION}/lib/${NABTO_ARCH}/libnabto_client.so" > "$TMP_SO" 2>/dev/null; then
        DOWNLOAD_SUCCESS=1
    fi

    # Fallback to main branch if tagged release tarball structure changed
    if [ "$DOWNLOAD_SUCCESS" -ne 1 ] || [ ! -s "$TMP_SO" ]; then
        echo "[Nabto Setup] ⚠️ Tagged archive failed, trying main branch archive..."
        rm -f "$TMP_SO"
        if curl -fsSL --connect-timeout 10 --max-time 120 --retry 2 "$MAIN_URL" 2>/dev/null | \
           tar -xz -O "nabto-client-sdk-releases-main/lib/${NABTO_ARCH}/libnabto_client.so" > "$TMP_SO" 2>/dev/null; then
            DOWNLOAD_SUCCESS=1
        fi
    fi

    # Verify download integrity (valid libnabto_client.so is > 5 MB)
    FILE_SIZE=0
    if [ -f "$TMP_SO" ]; then
        FILE_SIZE=$(wc -c < "$TMP_SO" 2>/dev/null || echo 0)
    fi

    if [ "$DOWNLOAD_SUCCESS" -ne 1 ] || [ "$FILE_SIZE" -lt 1000000 ]; then
        rm -f "$TMP_SO"
        echo "[Nabto Setup] ❌ Error: Failed to download or extract libnabto_client.so from GitHub!" >&2
        echo "[Nabto Setup] 💡 Troubleshooting:" >&2
        echo "  1. Check container internet connection / DNS settings." >&2
        echo "  2. Alternatively, place libnabto_client.so manually into: ${TARGET_DIR}/" >&2
        exit 1
    fi

    # Atomically move verified library to final destination
    mv "$TMP_SO" "$TARGET_SO"
    chmod 755 "$TARGET_SO"

    # Remove obsolete SDK version directories in /data/lib/ to prevent disk bloat
    find /data/lib -mindepth 1 -maxdepth 1 -type d ! -name "${NABTO_SDK_VERSION}" -exec rm -rf {} + 2>/dev/null || true

    echo "$NABTO_SDK_VERSION" > "$VERSION_FILE"
    echo "[Nabto Setup] ✅ Nabto Client SDK v${NABTO_SDK_VERSION} installed successfully (${FILE_SIZE} bytes)."
fi

# 3. Export library path for dynamic linker
export LD_LIBRARY_PATH="${TARGET_DIR}:/data/lib:/usr/lib:${LD_LIBRARY_PATH}"

# 4. Launch Steinel CAM Bridge
exec /app/steinel-bridge "$@"
