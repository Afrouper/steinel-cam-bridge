# Steinel L 625 CAM SC — Standalone RTSP Bridge

[![CI Test & Build](https://github.com/OWNER/REPO/actions/workflows/ci.yml/badge.svg)](https://github.com/OWNER/REPO/actions/workflows/ci.yml)
[![Docker Image](https://img.shields.io/badge/Docker-GHCR-blue?logo=docker)](https://github.com/OWNER/REPO/pkgs/container/steinel-cam-bridge)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go)](https://go.dev)

Ein hochperformanter, 100 % autarker **Go-Daemon**, der den Kamera-Feed der **Steinel L 625 CAM SC** Außenleuchte in einen standardkonformen **RTSP-Stream** (`rtsp://host:8554/steinel`) wandelt – zur nahtlosen Integration in **Scrypted / Apple HomeKit Secure Video (HKSV)**, **Home Assistant** und **VLC**.

---

## ✨ Features

- **100 % Autarkes Single-Binary**: Kein Python, kein Node.js und kein separater MediaMTX-Server erforderlich.
- **Integrierter RTSP-Server**: Eigener, leichtgewichtiger RTSP-Server auf Basis von `gortsplib/v4` auf Port `8554`.
- **Zero-Transcoding Passthrough**: Direkte Durchleitung des H.264-Videostroms (< 0,3 % CPU-Auslastung auf Host/NAS).
- **24/7 Resilienz & Watchdog**:
  - **RTP-Silence Watchdog**: Erkennt Stream-Abbrüche automatisch und führt gezielte Session-Resets durch.
  - **30s Cooldown**: Wartet 30 Sekunden vor jedem Reconnect, um neu startende Kameras zu schonen und Netzwerklast zu vermeiden.
  - **Automatischer mDNS-Wake-Up**: Weckt die Kamera bei jedem Verbindungsversuch zuverlässig aus dem Ruhezustand auf.
- **Automatisches Pairing**: Erzeugt kryptografische Client-Schlüssel und registriert sich per QR-Code-Payload automatisch im IAM der Kamera.
- **Multi-Arch Docker-Images**: Für **`linux/amd64`** und **`linux/arm64`**.

---

## 🐳 Bereitstellung (Docker & Docker Compose)

Die Steinel Bridge kann als einzelner Docker-Container oder im Verbund mit Scrypted, etc. betrieben werden.
Für die Nutzung es es erforderlich den QR Code der Steinl App zu verwenden (Kamera teilen). Mit einem Standard
QR Code Leser kann der enthaltende String verwendet werden um die Bridge zu pairen.

### Option A: Standalone Docker Run

```bash
docker run -d \
  --name steinel-cam-bridge \
  --restart unless-stopped \
  -p 8554:8554 \
  -e CAMERA_IP="<IP der Kamera>" \
  -e QR_CODE="did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx" \
  -v ./data:/data \
  ghcr.io/Afrouper/steinel-cam-bridge:latest
```

### Option B: Docker Compose (Nur Bridge)

Erstellen Sie eine `docker-compose.yml`:

```yaml
services:
  steinel-cam:
    image: ghcr.io/OWNER/steinel-cam-bridge:latest
    container_name: steinel-cam-bridge
    restart: unless-stopped
    ports:
      - "8554:8554"
      - "8554:8554/udp"
    environment:
      - CAMERA_IP=<IP der Kamera>
      - QR_CODE=did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx
      - RESOLUTION=1080p
    volumes:
      - ./data:/data
```

Starten mit:
```bash
docker compose up -d
```

### Option C: Docker Compose (Komplettstack mit Scrypted für Apple HomeKit / HKSV)

Beispiel für ein Docker Compose Stack mit scrypted integriert um die Kamera z.B. in Apple HomeKit Secure Video zu integrieren
```yaml
services:
  steinel-cam:
    image: ghcr.io/OWNER/steinel-cam-bridge:latest
    container_name: steinel-cam-bridge
    restart: unless-stopped
    network_mode: host
    environment:
      - CAMERA_IP=<IP der Kamera>
      - QR_CODE=did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx
      - RESOLUTION=1080p
    volumes:
      - ./data:/data

  scrypted:
    image: ghcr.io/koush/scrypted:latest
    container_name: scrypted
    restart: unless-stopped
    network_mode: host
    environment:
      - SCRYPTED_WEB_CORSPROXY=true
    volumes:
      - ./scrypted-data:/server/volume
    depends_on:
      - steinel-cam
```

---

## 💻 Lokale Entwicklung

### Voraussetzungen
- Go 1.24+
- C-Compiler (clang/gcc auf macOS/Linux, MinGW auf Windows)

### macOS / Linux
```bash
# 1. Offizielles Nabto Client SDK nach .sdk/ herunterladen (git-ignored)
./scripts/setup-sdk.sh

# 2. Lokal bauen und starten
./scripts/run-dev.sh -ip 192.168.88.89 -qr "did=de-...,pid=pr-...,sct=...,pairPwd=..."
```

### Windows (PowerShell)
```powershell
# 1. Offizielles Nabto Client SDK nach .sdk/ herunterladen (git-ignored)
.\scripts\setup-sdk.ps1

# 2. Lokal bauen und starten
.\scripts\run-dev.ps1 -ip 192.168.88.89 -qr "did=de-...,pid=pr-...,sct=...,pairPwd=..."
```

---

## ⚙️ Konfiguration & Umgebungsvariablen

| Variable / Flag | CLI-Flag | Standard | Beschreibung |
|---|---|---|---|
| `CAMERA_IP` | `-ip` | `192.168.1.2` | Lokale IP-Adresse der Steinel-Kamera |
| `QR_CODE` | `-qr` | `""` | QR-Code Payload aus der Steinel App für automatisches Pairing |
| `KEY_PATH` | `-key` | `data/client.key` | Speicherpfad für den persistenten ECC-Schlüssel |
| `RESOLUTION` | `-res` | `1080p` | Videoauflösung (`1080p`, `720p`, `360p`) |
| `RTSP_PORT` | `-port` | `8554` | Port des integrierten RTSP-Servers |
| `RTSP_PATH` | `-path` | `steinel` | Pfad des RTSP-Streams (`rtsp://host:port/path`) |

---

## 📄 Lizenz & Disclaimer

- **Projektlizenz:** Dieses Projekt ist unter der [MIT License](LICENSE) lizenziert.
- **Third-Party Lizenzen:** Siehe [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md) für Hinweise zum Nabto Edge Client SDK und zu den Go-Bibliotheken.

> [!NOTE]
> Dies ist ein unabhängiges Open-Source-Community-Projekt. Es steht in keiner geschäftlichen Verbindung zur STEINEL GmbH, Nabto ApS oder Apple Inc. Alle Markennamen sind Eigentum der jeweiligen Rechteinhaber.
