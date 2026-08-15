# Steinel L 625 CAM SC — Standalone ONVIF Profile S/T & 2-Way Audio Bridge

[![CI Test & Build](https://github.com/OWNER/REPO/actions/workflows/ci.yml/badge.svg)](https://github.com/OWNER/REPO/actions/workflows/ci.yml)
[![Docker Image](https://img.shields.io/badge/Docker-GHCR-blue?logo=docker)](https://github.com/OWNER/REPO/pkgs/container/steinel-cam-bridge)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go)](https://go.dev)

Ein hochperformanter, 100 % autarker **Go-Daemon**, der die **Steinel L 625 CAM SC** Außenleuchte in eine standardkonforme **ONVIF Profile S/T Kamera** mit **RTSP-Streaming** und **2-Wege-Audio (Gegensprechen)** verwandelt – zur nahtlosen Integration in **Scrypted / Apple HomeKit Secure Video (HKSV)**, **Home Assistant**, **Synology Surveillance Station** und **Frigate**.

---

## ✨ Features

- **Standardisierter ONVIF Profile S & Profile T Server**:
  - **WS-Discovery (UDP 3702)**: Automatische Erkennung im lokalen Netzwerk durch Scrypted, Home Assistant, NVRs.
  - **Dynamische Auflösungssteuerung**: `Profile_Main` (1080p @ 15fps) & `Profile_Sub` (360p @ 10fps) sowie Umschaltung zur Laufzeit via `SetVideoEncoderConfiguration`.
  - **Snapshot-API (`/snapshot.jpg`)**: Bereitstellung von JPEG-Vorschaubildern für Push-Benachrichtigungen.
- **🔊 Volles 2-Way Audio (Gegensprechen)**:
  - **RTSP Audio Backchannel**: Durchleitung von HomeKit/Scrypted-Sprachdaten direkt an den Lautsprecher der Steinel-Leuchte (PCMU / G.711u 8000 Hz).
- **🚨 Hardware-PIR Bewegungserkennung (ONVIF Events)**:
  - Empfang der physischen MCU-Sensorberichte der Leuchte.
  - Weitergabe als **ONVIF Motion Events** (`tns1:RuleEngine/CellMotionDetector/Motion`) an Scrypted/HKSV – **keine Fehlalarme, 0 % Server-CPU-Last**.
- **💡 Integrierte Lampen- & Sensorsteuerung**:
  - Licht ein-/ausschalten, Sensorbetrieb aktivieren (`/api/light?mode=on|off|auto` oder ONVIF Auxiliary/Relay).
  - Statusabfrage via REST (`/api/status`).
- **100 % Autarkes Single-Binary**: Kein Python, kein Node.js und kein separater MediaMTX-Server erforderlich.
- **24/7 Resilienz & Watchdog**:
  - **RTP-Silence Watchdog**: Erkennt Stream-Abbrüche automatisch und führt gezielte Session-Resets durch.
  - **30s Cooldown**: Wartet 30 Sekunden vor jedem Reconnect, um neu startende Kameras zu schonen.
  - **Automatischer mDNS-Wake-Up**: Weckt die Kamera zuverlässig auf.

---

## 🐳 Bereitstellung (Docker & Docker Compose)

### Option A: Standalone Docker Run

```bash
docker run -d \
  --name steinel-cam-bridge \
  --restart unless-stopped \
  --net=host \
  -e CAMERA_IP="192.168.1.100" \
  -e QR_CODE="did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx" \
  -v ./data:/data \
  ghcr.io/Afrouper/steinel-cam-bridge:latest
```

### Option B: Docker Compose (Komplettstack mit Scrypted für Apple HomeKit / HKSV)

```yaml
services:
  steinel-cam:
    image: ghcr.io/OWNER/steinel-cam-bridge:latest
    container_name: steinel-cam-bridge
    restart: unless-stopped
    network_mode: host
    environment:
      - CAMERA_IP=192.168.1.100
      - QR_CODE=did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx
      - RESOLUTION=1080p
      - RTSP_PORT=8554
      - ONVIF_PORT=8000
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

Starten mit:
```bash
docker compose up -d
```

---

## 📱 Einbindung in Scrypted (Apple HomeKit / HKSV)

1. In der Scrypted Management Console das **ONVIF Plugin** installieren.
2. Auf **Add ONVIF Camera** klicken:
   - **IP-Adresse**: `<IP-des-Docker-Hosts>` (Port `8000`)
   - Benutzername / Passwort: leer lassen.
3. Scrypted erkennt automatisch:
   - **Video Stream**: 1080p H.264
   - **Audio Stream**: PCMU Mikrofon
   - **Two-Way Audio**: Gegensprechanlage über ONVIF Profile T
   - **Motion Sensor**: Hardware-PIR der Steinel-Leuchte
4. Im **HomeKit Plugin** die Kamera aktivieren ➔ In Apple Home erscheint die Kamera inkl. Gegensprechen und Aufnahme-Auslöser!

---

## 💻 Lokale Entwicklung

```bash
# 1. Offizielles Nabto Client SDK nach .sdk/ herunterladen (git-ignored)
./scripts/setup-sdk.sh

# 2. Lokal bauen und starten
./scripts/run-dev.sh -ip 192.168.1.100 -qr "did=de-...,pid=pr-...,sct=...,pairPwd=..."
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
| `ONVIF_PORT` | `-onvif` | `8000` | Port des integrierten ONVIF HTTP-Servers |
| `RTSP_PATH` | `-path` | `steinel` | Pfad des RTSP-Streams (`rtsp://host:port/path`) |

---

## 📄 Lizenz & Disclaimer

- **Projektlizenz:** Dieses Projekt ist unter der [MIT License](LICENSE) lizenziert.
- **Third-Party Lizenzen:** Siehe [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md) für Hinweise zum Nabto Edge Client SDK und zu den Go-Bibliotheken.

> [!NOTE]
> Dies ist ein unabhängiges Open-Source-Community-Projekt. Es steht in keiner geschäftlichen Verbindung zur STEINEL GmbH, Nabto ApS oder Apple Inc. Alle Markennamen sind Eigentum der jeweiligen Rechteinhaber.
