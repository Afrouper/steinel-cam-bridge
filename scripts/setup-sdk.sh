#!/usr/bin/env bash
# ==============================================================================
# Setup Nabto Edge Client SDK for Local Development (macOS & Linux)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
SDK_DIR="${ROOT_DIR}/.sdk"
NABTO_TAG="main" # or specific tag like v5.15.4

echo "=================================================="
echo " Setting up Nabto Edge Client SDK (${NABTO_TAG})"
echo " Target directory: ${SDK_DIR}"
echo "=================================================="

mkdir -p "${SDK_DIR}/include/nabto" "${SDK_DIR}/lib" "${SDK_DIR}/tmp"

OS="$(uname -s)"
ARCH="$(uname -m)"

DOWNLOAD_URL="https://github.com/nabto/nabto-client-sdk-releases/archive/refs/heads/${NABTO_TAG}.tar.gz"

echo "[*] Downloading Nabto Client SDK release archive..."
curl -fsSL "${DOWNLOAD_URL}" -o "${SDK_DIR}/tmp/nabto-sdk.tar.gz"

echo "[*] Extracting SDK artifacts..."
tar -xzf "${SDK_DIR}/tmp/nabto-sdk.tar.gz" -C "${SDK_DIR}/tmp"

EXTRACTED_DIR="$(find "${SDK_DIR}/tmp" -maxdepth 1 -type d -name "nabto-client-sdk-releases*" | head -n 1)"

# Copy header files
if [ -d "${EXTRACTED_DIR}/include/nabto" ]; then
    cp -r "${EXTRACTED_DIR}/include/nabto/"* "${SDK_DIR}/include/nabto/"
fi

# Copy matching platform library
if [ "${OS}" = "Darwin" ]; then
    echo "[*] Installing macOS universal dynamic library..."
    cp "${EXTRACTED_DIR}/lib/macos-universal/libnabto_client.dylib" "${SDK_DIR}/lib/"
elif [ "${OS}" = "Linux" ]; then
    if [ "${ARCH}" = "aarch64" ] || [ "${ARCH}" = "arm64" ]; then
        echo "[*] Installing Linux aarch64 (ARM64) shared library..."
        cp "${EXTRACTED_DIR}/lib/linux-aarch64/libnabto_client.so" "${SDK_DIR}/lib/"
    else
        echo "[*] Installing Linux x86_64 shared library..."
        cp "${EXTRACTED_DIR}/lib/linux-x86_64/libnabto_client.so" "${SDK_DIR}/lib/"
    fi
fi

# Cleanup temporary files
rm -rf "${SDK_DIR}/tmp"

echo "=================================================="
echo " [✓] Nabto SDK successfully installed into .sdk/"
echo " Header:  ${SDK_DIR}/include/nabto/nabto_client.h"
echo " Library: ${SDK_DIR}/lib/"
echo "=================================================="
