# Steinel L 625 CAM SC — Standalone ONVIF, 2-Way Audio & Home Assistant Bridge

[![CI Test & Build](https://github.com/Afrouper/steinel-cam-bridge/actions/workflows/ci.yml/badge.svg)](https://github.com/Afrouper/steinel-cam-bridge/actions/workflows/ci.yml)
[![Docker Image](https://img.shields.io/badge/Docker-GHCR-blue?logo=docker)](https://github.com/OWNER/REPO/pkgs/container/steinel-cam-bridge)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev)

Die Anwendung ist als hochperformanter, 100 % autarker **Go-Daemon**.
Es soll die **Steinel L 625 CAM SC** Außenleuchte in eine standardkonforme **ONVIF Profile S/T Kamera** mit **RTSP-Streaming**, **2-Wege-Audio (Gegensprechen)** und vollständiger **MQTT Home Assistant Auto-Discovery** verwandelt – zur nahtlosen Integration in **Home Assistant**, **Scrypted / Apple HomeKit Secure Video (HKSV)**, **Synology Surveillance Station** und **Frigate**.

---

## Disclamer
Versuch die Steinl L 625 CAM SC in Smart Home Apps nutzbar zu machen. Dabei soll die Firmware von der Kamera nicht angetastet werden. Die Kommunikation erfolgt über die Standard Protokolle. Eine Nutzung über die offizelle Steinl App ist nach wie vor möglich und nicht beeinträchtigt.
Die Kamera wird im lokalen Netzwerk als ONVIF Kamera zu verfügung gestellt. Damit kann das Videosignal in z.B. Home Assistant eingebunden werden oder über scrypted weiter verarbeitet werden - z.B. in Apple HomeKit Secure Video.

---

## ✨ Features

- **🏠 Volle Home Assistant MQTT Auto-Discovery**:
  - **Hauptlicht (`light`)**: An/Aus und Dimmung (`10`–`100 %`).
  - **Betriebsmodus (`select`)**: Umschalten zwischen `Sensor (Automatik)`, `Dauerlicht` und `Aus`.
  - **Helligkeitssensor (`sensor`)**: Live-Dämmerungswert in Lux (`lx`).
  - **Bewegungsmelder (`binary_sensor`)**: Hardware-PIR Bewegungserkennung (`motion`).
  - **PIR-Status (`binary_sensor`)**: Zeigt an, ob der PIR-Sensor aktiv/scharf ist.
  - **Schieberegler (`number`)**:
    - PIR-Empfindlichkeit (`0`–`100 %`)
    - Dämmerungsschwelle (`2`–`1000 lx`)
    - Nachlaufzeit des Hauptlichts (`5`–`900 s`)
    - Grundlicht Helligkeit (`0`–`50 %`)
  - **Alarmsirene (`siren`)**: Akustischer Alarm der Außenleuchte.
  - **Videoauflösung (`select`)**: Live-Umschaltung (`1080p`, `720p`, `360p`).
- **Standardisierter ONVIF Profile S & Profile T Server**:
  - **WS-Discovery (UDP 3702)**: Automatische Erkennung im lokalen Netzwerk.
  - **Native Live-Snapshots**: NVRs und Clients (z. B. Scrypted Prebuffer, Home Assistant) generieren hochauflösende Live-Standbilder direkt aus dem H.264-Videostream ohne Dummy-Platzhalter.
- **🔊 Volles 2-Way Audio (Gegensprechen) & Natives AAC-Audio**:
  - **Natives AAC-Audio (Standard)**: Automatisches Realtime-Transcoding des Kamera-Mikrofons (G.711u 8 kHz $\rightarrow$ AAC-LC 16 kHz) direkt in Go. Null Konfigurationsaufwand und kein Transcoding in Scrypted, Apple Home oder Home Assistant nötig!
  - **RTSP Audio Backchannel**: Durchleitung von HomeKit/Scrypted-Sprachdaten direkt an den Lautsprecher der Steinel-Leuchte (PCMU / G.711u 8000 Hz).
  - **Wahlweise umschaltbar**: `AUDIO_CODEC="aac"` (Standard) oder `AUDIO_CODEC="pcmu"` (Raw Passthrough).
- **🚨 Bewegungserkennung & Apple HomeKit Secure Video (HKSV)**:
  - **Hersteller-Architektur**: Die Steinel-Kamera übermittelt Bewegungsevents ab Werk ausschließlich an das Cloud-Push-Gateway des Herstellers (für Push-Nachrichten der Steinel App) und stellt lokal im P2P-Modus den reinen Live-Stream bereit.
  - **100 % Lokale HKSV-Aufnahme**: In Scrypted wird über das offizielle Plugin **`OpenCV Motion Detector`** (`@scrypted/opencv`) eine latenzfreie Pixelanalyse des 1080p-RTSP-Streams durchgeführt. Bewegungen von Personen, Fahrzeugen oder Tieren lösen sofort lokale ONVIF-Events und iCloud-Aufnahmen in Apple Home aus.
- **100 % Autarkes Single-Binary**: Kein Python, kein Node.js und kein separater MediaMTX-Server erforderlich.
- **24/7 Resilienz & Watchdog**: RTP-Silence Watchdog, 30s Cooldown, mDNS-Wakeup.

---

## 🐳 Bereitstellung (Docker & Docker Compose)

Die Steinel CAM Bridge läuft autark als einzelner Container bzw. Docker Compose Stack auf Ihrem Server oder NAS. 

### Option A: Standalone Docker Compose (Empfohlen)

Eine fertige Vorlage finden Sie unter [`examples/docker-compose.yml`](examples/docker-compose.yml):

```yaml
services:
  steinel-cam-bridge:
    image: ghcr.io/afrouper/steinel-cam-bridge:latest
    container_name: steinel-cam-bridge
    restart: unless-stopped
    network_mode: host # Empfohlen für WS-Discovery (UDP 3702) und Direktverbindung zur Kamera
    environment:
      # --- Kamera-Verbindung (Ersetzen Sie die Werte durch Ihre Kamera-Daten) ---
      - CAMERA_IP=192.168.1.100                                         # IP-Adresse der Steinel-Kamera im lokalen Netz
      - QR_CODE=did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx       # QR-Code String aus der Steinel App ("Kamera teilen")
      - RESOLUTION=1080p                                                # Standard-Auflösung: 1080p, 720p oder 360p
      - AUDIO_CODEC=aac                                                 # Audio-Codec: aac (Standard, nativ transkodiert) oder pcmu (Raw Passthrough)
      - KEY_PATH=/data/client.key                                       # Pfad zum persistenten Schlüssel

      # --- Server-Ports ---
      - RTSP_PORT=8554
      - ONVIF_PORT=8000
      - RTSP_PATH=steinel

      # --- MQTT / Home Assistant Konfiguration (Optional) ---
      - MQTT_BROKER=tcp://192.168.1.50:1883    # IP oder Hostname Ihres MQTT Brokers (z.B. Home Assistant Mosquitto)
      - MQTT_USER=homeassistant                 # Optional: MQTT Benutzername
      - MQTT_PASSWORD=secretpassword            # Optional: MQTT Passwort
      - MQTT_TOPIC_PREFIX=steinel               # Basis-Topic (Geräte-ID wird automatisch darunter gehängt: steinel/<deviceID>/...)
      - MQTT_DISCOVERY_PREFIX=homeassistant     # Home Assistant Auto-Discovery Prefix
    volumes:
      - ./data:/data
    deploy:
      resources:
        limits:
          cpus: '0.50'
          memory: 256M
        reservations:
          cpus: '0.05'
          memory: 64M
    pids_limit: 100
```

Starten mit:
```bash
docker compose up -d
```

### Option B: Standalone Docker Run

```bash
docker run -d \
  --name steinel-cam-bridge \
  --restart unless-stopped \
  --net=host \
  --memory=256m \
  --cpus=0.5 \
  --pids-limit=100 \
  -e CAMERA_IP="192.168.1.100" \
  -e QR_CODE="did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx" \
  -e AUDIO_CODEC="aac" \
  -e MQTT_BROKER="tcp://192.168.1.50:1883" \
  -e MQTT_USER="homeassistant" \
  -e MQTT_PASSWORD="secretpassword" \
  -v ./data:/data \
  ghcr.io/afrouper/steinel-cam-bridge:latest
```

---

## 📱 Einbindung in Home Assistant & Scrypted

### 1. Home Assistant (MQTT)
Sobald `MQTT_BROKER` konfiguriert ist, verbindet sich die Bridge mit dem Broker. In Home Assistant unter **Einstellungen ➔ Geräte & Dienste ➔ MQTT** erscheint automatisch das Gerät **"Steinel L 625 CAM SC"** mit allen Licht-, Sensor- und Steuerungsentitäten!

### 2. Scrypted (Home Assistant Add-on oder Standalone)
Egal ob Scrypted als **Home Assistant Add-on** oder als eigenständige Instanz läuft:
1. Im Scrypted **ONVIF Plugin** auf *Add Camera* klicken (IP-Adresse des Bridge-Servers, Port `8000`).
2. Scrypted erkennt automatisch **1080p Video, natives AAC-Mikrofon und Gegensprechanlage**.
3. **Natives Audio**: Die Bridge liefert standardmäßig natives **AAC-Audio (16 kHz)**. In Scrypted ist **kein manuelles Audio-Transcoding im HomeKit-DEBUG-Reiter erforderlich**.
4. **Bewegungserkennung für HKSV aktivieren**:
   - In Scrypted unter **Plugins** das Plugin **`OpenCV Motion Detector`** (`@scrypted/opencv`) installieren.
   - Auf der Steinel-Kamera im Reiter **Extensions** das Plugin **OpenCV Motion Detector** aktivieren.
5. Im **HomeKit Plugin** die Kamera aktivieren ➔ Live-Streaming & HKSV-Aufnahmen in Apple Home laufen vollautomatisch!

---

## 💻 Lokale Entwicklung

```bash
# 1. Offizielles Nabto Client SDK nach .sdk/ herunterladen (git-ignored)
./scripts/setup-sdk.sh

# 2. Lokal bauen und starten (optional mit MQTT)
./scripts/run-dev.sh -ip 192.168.1.100 -qr "did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx" -mqtt-broker "tcp://192.168.1.50:1883"
```

---

## ⚙️ Konfiguration & Umgebungsvariablen

| Variable / Flag | CLI-Flag | Standard | Beschreibung |
|---|---|---|---|
| `CAMERA_IP` | `-ip` | `192.168.1.2` | Lokale IP-Adresse der Steinel-Kamera |
| `QR_CODE` | `-qr` | `""` | QR-Code Payload aus der Steinel App für automatisches Pairing |
| `KEY_PATH` | `-key` | `data/client.key` | Speicherpfad für den persistenten ECC-Schlüssel |
| `RESOLUTION` | `-res` | `1080p` | Videoauflösung (`1080p`, `720p`, `360p`) |
| `AUDIO_CODEC` | `-audio-codec` | `aac` | Audio-Codec des RTSP/ONVIF Streams: `aac` (nativ transkodiert) oder `pcmu` (Raw Passthrough) |
| `RTSP_PORT` | `-port` | `8554` | Port des integrierten RTSP-Servers |
| `ONVIF_PORT` | `-onvif` | `8000` | Port des integrierten ONVIF HTTP-Servers |
| `MQTT_BROKER` | `-mqtt-broker` | `""` | MQTT Broker URL (z. B. `tcp://192.168.1.100:1883`) |
| `MQTT_USER` | `-mqtt-user` | `""` | MQTT Benutzername |
| `MQTT_PASSWORD` | `-mqtt-pass` | `""` | MQTT Passwort |
| `MQTT_TOPIC_PREFIX` | `-mqtt-topic` | `steinel` | MQTT Basis-Topic (Geräte-ID wird automatisch angehängt) |
| `MQTT_DISCOVERY_PREFIX` | `-mqtt-disc` | `homeassistant` | Home Assistant MQTT Auto-Discovery Prefix |

---

## 📄 Lizenz & Disclaimer

- **Projektlizenz:** Dieses Projekt ist unter der [MIT License](LICENSE) lizenziert.
- **Third-Party Lizenzen:** Siehe [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md) für Hinweise zum Nabto Edge Client SDK und zu den Go-Bibliotheken.

> [!NOTE]
> Dies ist ein unabhängiges Open-Source-Community-Projekt. Es steht in keiner geschäftlichen Verbindung zur STEINEL GmbH, Nabto ApS oder Apple Inc. Alle Markennamen sind Eigentum der jeweiligen Rechteinhaber.
