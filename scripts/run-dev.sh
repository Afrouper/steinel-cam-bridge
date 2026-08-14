#!/usr/bin/env bash
# ==============================================================================
# Run Steinel Bridge Locally (macOS & Linux)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
SDK_DIR="${ROOT_DIR}/.sdk"

# 1. Ensure SDK is downloaded
if [ ! -f "${SDK_DIR}/include/nabto/nabto_client.h" ] || [ ! -d "${SDK_DIR}/lib" ]; then
    echo "[*] Nabto SDK not found. Running setup script..."
    "${SCRIPT_DIR}/setup-sdk.sh"
fi

cd "${ROOT_DIR}"

echo "=================================================="
echo " Building & Starting Steinel Bridge (Local Dev)"
echo " Output RTSP: rtsp://127.0.0.1:8554/steinel"
echo "=================================================="

# 2. Build Go binary
go build -o steinel-bridge ./cmd/steinel-bridge

# 3. Execute with dynamic library search path
OS="$(uname -s)"
if [ "${OS}" = "Darwin" ]; then
    export DYLD_LIBRARY_PATH="${SDK_DIR}/lib:${DYLD_LIBRARY_PATH:-}"
elif [ "${OS}" = "Linux" ]; then
    export LD_LIBRARY_PATH="${SDK_DIR}/lib:${LD_LIBRARY_PATH:-}"
fi

exec ./steinel-bridge "$@"
