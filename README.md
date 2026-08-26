# Bridge für Steinel CAM Leuchten auf ONVIF, 2-Way Audio & Home Assistant

[![Latest Release](https://img.shields.io/github/v/release/Afrouper/steinel-cam-bridge?logo=github)](https://github.com/Afrouper/steinel-cam-bridge/releases)
[![CI Test & Build](https://github.com/Afrouper/steinel-cam-bridge/actions/workflows/ci.yml/badge.svg)](https://github.com/Afrouper/steinel-cam-bridge/actions/workflows/ci.yml)
[![CodeQL Analysis](https://github.com/Afrouper/steinel-cam-bridge/actions/workflows/codeql.yml/badge.svg)](https://github.com/Afrouper/steinel-cam-bridge/actions/workflows/codeql.yml)
[![golangci-lint](https://img.shields.io/badge/golangci--lint-passing-brightgreen?logo=go)](https://golangci-lint.run/)
[![Go Reference](https://pkg.go.dev/badge/github.com/Afrouper/steinel-cam-bridge.svg)](https://pkg.go.dev/github.com/Afrouper/steinel-cam-bridge)
[![Home Assistant Add-on](https://img.shields.io/badge/Home%20Assistant-Add--on-41BDF5?logo=home-assistant&logoColor=white)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2FAfrouper%2Fsteinel-cam-bridge)
[![Docker Image](https://img.shields.io/badge/Docker-GHCR-blue?logo=docker)](https://github.com/Afrouper/steinel-cam-bridge/pkgs/container/steinel-cam-bridge)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go)](https://go.dev)

Die Anwendung ist ein hochperformanter, 100 % autarker **Go-Daemon**.
Sie verwandelt **Steinel CAM Außenleuchten** (**L 625 CAM SC**, **L 620 CAM**, **XLED CAM 1/2**, **Spot CAM**) in standardkonforme **ONVIF Profile S/T Kameras** mit **RTSP-Streaming**, **2-Wege-Audio (Gegensprechen)** und vollständiger **MQTT Home Assistant Auto-Discovery** – zur nahtlosen Integration in **Home Assistant**, **Scrypted / Apple HomeKit Secure Video (HKSV)**, **Frigate** und weitere.

Das Schwestermodell **XLED CAM2 SC** konnte nicht verprobt werden, könnte aber ebenfalls funktionieren. Über Rückmeldungen würde ich mich freuen.

> [!WARNING]
> Trotz sorgfältiger Entwicklung und Verwendung der Schnittstellen des offiziellen Nabto SDKs und APIs die von der Kamera bereitgestellt werden kann nicht garantiert werden das es zu keinen Komplikationen mit der Hardware der Kamera/Leuchte kommt. Das Projekt oder meine Personen übernehmen keine Gewährleistung oder Sachmängelhaftung für eventuell eintretende Schäden an der Hardware.

---

## ✨ Features & Home Assistant Integration

- **🏠 Home Assistant MQTT Auto-Discovery (Strukturierte Gerätesteuerung)**:
  - **🎮 Steuerung (Hauptansicht)**:
    - **Betriebsmodus (`select.mode`)**: Umschalten zwischen `Sensor (Automatik)`, `Dauerlicht` und `Aus`.
    - **Alarmsirene (`siren.siren`)**: Sofortiges Auslösen und Stoppen des akustischen Alarms der Außenleuchte mit Live-Zustandsrückmeldung (`ON` / `OFF`).
  - **⚙️ Konfiguration (Einstellungsbereich)**:
    - **Dämmerungsschwelle (`number.lux_threshold`)**: Schaltschwelle in Lux (`2`–`1000 lx`), ab welcher Umgebungsdunkelheit das Licht bei Bewegung schaltet.
    - **Hauptlicht Helligkeit (`number.highlight`)**: Maximale Leuchtstärke des Flutlichts (`10`–`100 %`).
    - **Grundlicht Helligkeit (`number.lowlight`)**: Dauerhafte Nachtlicht-Helligkeit (`0`–`50 %`).
    - **Nachlaufzeit (`number.duration`)**: Einschaltdauer des Hauptlichts nach Bewegung (`5`–`900 s`).
    - **PIR-Empfindlichkeit (`number.pir_sensitivity`)**: Reichweite / Sensitivität des Bewegungsmelders (`0`–`100 %`).
    - **Videoauflösung (`select.resolution`)**: Live-Umschaltung der Kameraauflösung (`1080p`, `720p`, `360p`).
  - **🩺 Diagnose**:
    - **PIR-Status (`binary_sensor.pir_status`)**: Zeigt den Betriebszustand des PIR-Sensors an (`running`).

- **💡 Hinweise zu Hardware-Grenzen & Bewegungserkennung**:
  - **Kein kontinuierlicher Luxmeter-Sensor (`sensor.lux`)**: Die Steinel-Kamera besitzt keinen digitalen Helligkeitsmesser (wie eine Wetterstation), sondern einen analogen Photowiderstand (LDR), der lediglich mit der eingestellten Dämmerungsschwelle abgeglichen wird.
  - **Kein lokaler Hardware-PIR-Push (`binary_sensor.motion`)**: Die Kamera-Firmware meldet Bewegungsevents ab Werk ausschließlich über das Cloud-Gateway des Herstellers an die Steinel-Smartphone-App. Auf der lokalen Schnittstelle wird der 1080p-Live-Stream bereitgestellt.
  - **Bewegungserkennung & Apple HomeKit Secure Video (HKSV)**: Die Bewegungserkennung wird in Smart-Home-Umgebungen standardmäßig per Video-Bildanalyse realisiert:
    - **In Home Assistant**: Über **Frigate** oder **MotionEye** für präzise KI-Objekterkennung (Personen, Fahrzeuge, Tiere).
    - **In Scrypted**: Über das offizielle Plugin **`OpenCV Motion Detector`** (`@scrypted/opencv`) für latenzfreie HomeKit-Mitteilungen und HKSV-Cloud-Aufzeichnungen.

- **Standardisierter ONVIF Profile S & Profile T Server**:
  - **WS-Discovery (UDP 3702)**: Automatische Erkennung im lokalen Netzwerk.
  - **Native Live-Snapshots**: NVRs und Clients (z. B. Scrypted Prebuffer, Home Assistant) generieren hochauflösende Live-Standbilder direkt aus dem H.264-Videostream ohne Dummy-Platzhalter.

- **🔊 Volles 2-Way Audio (Gegensprechen) & Natives AAC-Audio**:
  - **Natives AAC-Audio (Standard)**: Automatisches Realtime-Transcoding des Kamera-Mikrofons (G.711u 8 kHz $\rightarrow$ AAC-LC 16 kHz). Keine extra Konfiguration für Transcoding des Audiosignals erforderlich. Wahlweise umschaltbar: `AUDIO_CODEC="aac"` (Standard) oder `AUDIO_CODEC="pcmu"` (Raw Passthrough).
  - **RTSP Audio Backchannel**: Durchleitung von HomeKit/Scrypted-Sprachdaten direkt an den Lautsprecher der Steinel-Leuchte (PCMU / G.711u 8000 Hz).

- **Autarkes Single-Binary**: Kein Python, kein Node.js und kein separater MediaMTX-Server erforderlich.
  - Im Betrieb als HomeAssistant AddOn Image mit minimalsten Abhängigkeiten.
- **24/7 Resilienz & Watchdog**: RTP-Silence Watchdog, 30s Cooldown, mDNS-Wakeup.

---

## 🐳 Bereitstellung (Home Assistant Add-on & Docker)

### Option A: Home Assistant Add-on (Empfohlen für Home Assistant Nutzer)

[![Open your Home Assistant instance and show the add-on store with a specific repository enabled.](https://my.home-assistant.io/badges/supervisor_add_addon_repository.svg)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2FAfrouper%2Fsteinel-cam-bridge)

1. Klicken Sie auf das **"Open in Home Assistant" Badge** oben oder fügen Sie in Home Assistant unter **Einstellungen ➔ Add-ons ➔ Add-on Store ➔ Repositories** (Drei-Punkte-Menü) folgende URL hinzu:
   ```text
   https://github.com/Afrouper/steinel-cam-bridge
   ```
2. Wählen Sie **"Steinel CAM Bridge"** aus und klicken Sie auf **Installieren**.
3. Tragen Sie im Reiter **Konfiguration** Ihre `camera_ip` und den `qr_code` ein und klicken Sie auf **Starten**!

---

### Option B: Standalone Docker Compose (Empfohlen für Server / NAS mit Security Hardening)

Eine fertige Vorlage finden Sie unter [`examples/docker-compose.yml`](examples/docker-compose.yml).
Starten mit:
```bash
docker compose up -d
```

### Option C: Standalone Docker Run (Gehärtet)

```bash
docker run -d \
  --name steinel-cam-bridge \
  --restart unless-stopped \
  --net=host \
  --user "1000:1000" \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  --cap-add NET_BIND_SERVICE \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=64M \
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
Wird kein separater MQTT Broker konfiguriert wird automatisch der in HomeAssistant integrierte [Standard Broker]()https://github.com/home-assistant/addons/tree/master/mosquitto verwendet. In Home Assistant unter **Einstellungen ➔ Geräte & Dienste ➔ MQTT** erscheint automatisch das Gerät **"Steinel L 625 CAM SC"** mit allen Licht-, Sensor- und Steuerungsentitäten:

<p align="center">
  <img src="docs/HomeAssistant%20Controles.png" alt="Home Assistant Steuerung und Sensoren" width="400">
</p>

### 2. Scrypted (Home Assistant Add-on oder Standalone)
Egal ob Scrypted als **Home Assistant Add-on** oder als eigenständige Instanz läuft:
1. Im Scrypted **ONVIF Plugin** auf *Add Camera* klicken (IP-Adresse des Bridge-Servers, Port `8000`).
2. Scrypted erkennt automatisch **1080p Video, natives AAC-Mikrofon und Gegensprechanlage**.
3. **Natives Audio**: Die Bridge liefert standardmäßig natives **AAC-Audio (16 kHz)**. In Scrypted ist **kein manuelles Audio-Transcoding im HomeKit-DEBUG-Reiter erforderlich**.
4. **Bewegungserkennung für HKSV aktivieren**:
   - In Scrypted unter **Plugins** das Plugin **`OpenCV Motion Detector`** (`@scrypted/opencv`) installieren.
   - Auf der Steinel-Kamera im Reiter **Extensions** das Plugin **OpenCV Motion Detector** aktivieren.
5. Im **HomeKit Plugin** die Kamera aktivieren ➔ Live-Streaming & HKSV-Aufnahmen in Apple Home laufen vollautomatisch!

<p align="center">
  <img src="docs/Scrypted%20Kamera.png" alt="Scrypted ONVIF Kamera Integration" width="800">
</p>

---

## 🗄️ SD-Karten REST API (Ereignisse, Snapshots & Video-Download)

Die Bridge stellt auf Port `8000` eine direkte 1:1 REST-API bereit, um Aufnahmen der internen SD-Karte abzufragen und ohne Umwege per HTTP-Stream herunterzuladen (Zero-Disk I/O).

| Endpunkt | Methode | Beschreibung |
|---|---|---|
| `/api/sdcard/events` | `GET` | Liefert die JSON-Liste aller Video-Ereignisse (Query-Parameter: `start`, `end`, `page`, `limit`) |
| `/api/sdcard/events/{timestamp}/snapshot.jpg` | `GET` | Liefert das JPEG-Vorschaubild der Aufnahme direkt aus dem Kameraspeicher |
| `/api/sdcard/events/{timestamp}/video.mp4` | `GET` | Streamt die vollständige MP4-Aufnahme als Binärstream (inkl. Hardware-Überlastungsschutz) |

> [!TIP]
> **Eingebauter Hardware-Schutz (Concurrency = 1)**: Um die kleine Embedded-CPU der Steinel-Kamera vor Überlastung zu schützen, erlaubt die Bridge immer nur **genau einen aktiven Download gleichzeitig**. Parallele Abfragen werden mit `HTTP 429 Too Many Requests` beantwortet. Bricht ein Client den Download vorzeitig ab, stoppt die Bridge den Kamera-Transfer sofort.

---

## 💻 Lokale Entwicklung (macOS & Linux)

Die Bridge kann vollständig nativ auf einem Entwickler-Rechner (macOS / Linux) ohne Docker gebaut, getestet und ausgeführt werden:

### 1. Nabto SDK vorbereiten
Laden Sie die passende native Nabto Client SDK-Bibliothek (`libnabto_client.dylib` bzw. `.so`) einmalig in das lokale `.sdk/`-Verzeichnis herunter:
```bash
./scripts/setup-sdk.sh
```

### 2. Lokale Tests ausführen
```bash
# macOS:
DYLD_LIBRARY_PATH="$(pwd)/.sdk/lib" go test -v ./...

# Linux:
LD_LIBRARY_PATH="$(pwd)/.sdk/lib" go test -v ./...
```

### 3. Natives Binary kompilieren
```bash
# macOS:
CGO_LDFLAGS="-L$(pwd)/.sdk/lib -lnabto_client" CGO_CFLAGS="-I$(pwd)/.sdk/include" go build -o steinel-bridge ./cmd/steinel-bridge

# Linux:
CGO_LDFLAGS="-L$(pwd)/.sdk/lib -lnabto_client" CGO_CFLAGS="-I$(pwd)/.sdk/include" go build -o steinel-bridge ./cmd/steinel-bridge
```

### 4. Bridge lokal starten
```bash
# macOS:
DYLD_LIBRARY_PATH="$(pwd)/.sdk/lib" ./steinel-bridge \
  -ip 192.168.1.100 \
  -qr "did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx" \
  -key ./data/local_client.key \
  -debug

# Linux:
LD_LIBRARY_PATH="$(pwd)/.sdk/lib" ./steinel-bridge \
  -ip 192.168.1.100 \
  -qr "did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx" \
  -key ./data/local_client.key \
  -debug
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
| `DEBUG` | `-debug` | `false` | Ausführliches Debug-Logging für RTSP, WebRTC, Signaling & MCU |
| `RESET_PAIRING` | `-reset-pairing` | `false` | Löscht den gespeicherten Private Key und erzwingt ein erneutes Pairing mit dem angegebenen QR-Code |

---

## 📄 Lizenz & Disclaimer

- **Projektlizenz:** Dieses Projekt ist unter der [MIT License](LICENSE) lizenziert.
- **Third-Party Lizenzen:** Siehe [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md) für Hinweise zum Nabto Edge Client SDK und zu den Go-Bibliotheken.

> [!NOTE]
> Das bereitgestellte Docker-Image enthält keinerlei proprietäre Fremdbibliotheken. Da die Nabto Lizenz keine Distribution zulässt, wird beim allerersten Start
> des Containers die benötigte `libnabto_client.so` vollautomatisch direkt von Nabtos offiziellem GitHub-Repository auf das System des Nutzers geladen
> und persistent im Cache (`/data/lib/`) gespeichert.

> [!NOTE]
> Dies ist ein unabhängiges Open-Source-Community-Projekt. Es steht in keiner geschäftlichen Verbindung zur STEINEL GmbH, Nabto ApS oder Apple Inc. Alle Markennamen sind Eigentum der jeweiligen Rechteinhaber.
