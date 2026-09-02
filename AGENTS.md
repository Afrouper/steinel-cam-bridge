# Steinel CAM Bridge — Entwickler- & Agenten-Leitfaden

Dieses Dokument beschreibt die Codebase-Architektur, Designentscheidungen, Protokollabläufe und verbindliche Richtlinien für Entwickler und KI-Agenten im Repository `steinel-cam-bridge`.

---

## 1. High-Level Architektur

Die **Steinel CAM Bridge** ist ein hochperformanter, 100 % autarker Go-Daemon, der Steinel Außenleuchten (**L 625 CAM SC**, **L 620 CAM**, **XLED CAM 1/2**, **Spot CAM**) in standardkonforme **ONVIF Profile S/T/G Kameras** mit **RTSP-Streaming**, **2-Wege-Audio (Gegensprechen)**, **lokalem MicroSD-Speicherabruf** und **MQTT Home Assistant Auto-Discovery** wandelt – zur nahtlosen Integration in **Scrypted / Apple HomeKit Secure Video (HKSV)**, **Home Assistant**, **Synology Surveillance Station**, **Frigate** und weitere NVR-Systeme.

### Unterstützte Kameratypen & Protokoll-Treiber

1. **Steinel L 625 CAM SC (Nabto Edge Driver)**:
   - mDNS Wake-Up (UDP 5353 / 5592)
   - Nativer Pure-Go Nabto Edge P2P Direct Tunnel (`pkg/nabtopure`, Standard)
   - CGo-Fallback Wrapper um `libnabto_client` (`pkg/nabto`, optional via `USE_CGO_NABTO=true`)
   - CoAP Signaling (`/p2p/webrtc-info` & `/webrtc/tracks`)
   - WebRTC DTLS/SRTP Media (Pion WebRTC v4) + DataChannel `test` (MCU-Frames & SD-Karten Chunks)
2. **Steinel L 620 CAM / XLED CAM 1 (Xiongmai Sofia Driver)**:
   - Xiongmai Sofia Binärprotokoll auf TCP-Port `34567` (Login, MCU-Lichtsteuerung, Alarme, SD-Karten-Indexierung & Chunk-Download)
   - Nativer lokaler RTSP-Stream-Ingest von der Kamera
   - 2-Wege-Audio Backchannel via Xiongmai Talk-Protokoll

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Steinel Kamera / Außenleuchte                         │
│  ├─ L 625 CAM SC: Nabto Edge P2P (UDP 5592) + WebRTC (H.264/PCMU) + DC      │
│  └─ L 620 CAM / XLED: Xiongmai Sofia (TCP 34567) + RTSP Ingest + XM-Talk    │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ Auto-Detection via Port 34567 Probe
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Steinel Bridge Daemon (Go)                            │
│  ├─ cmd/launcher: Autarker CGo-freier SDK-Downloader & Bootstrap-Starter    │
│  ├─ cmd/steinel-bridge: Haupt-Daemon, Supervisor & CLI/Optionen-Parser      │
│  ├─ pkg/nabtopure: Nativer Pure-Go Nabto Edge Client (DTLS 1.2, CoAP, Stream)│
│  ├─ pkg/nabto: CGo-Fallback Wrapper für Nabto Client SDK                    │
│  ├─ pkg/webrtc: Pion WebRTC v4 Engine, RTCP-PLI, Watchdog & DataChannel     │
│  ├─ pkg/xiongmai: Sofia Protokoll, RTSP-Ingest, MCU & Talk (L 620)          │
│  ├─ pkg/storage: SD-Karten RecordingProvider & Event-Syncer                 │
│  ├─ pkg/audio: G.711u Decoder, Resampler & AAC-LC Echtzeit-Transcoder       │
│  ├─ pkg/mcu: 18-Byte UART Hex Parser & Command Builder                      │
│  ├─ pkg/events: Zentraler Thread-sicherer Pub/Sub Event-Bus                 │
│  ├─ pkg/rtsp: gortsplib v4 Server + Profile T Backchannel & Interceptor     │
│  ├─ pkg/onvif: WS-Discovery (UDP 3702), Profile S/T/G (Media, Events, DevIO)│
│  └─ pkg/mqtt: Home Assistant Auto-Discovery Client                          │
└───────────────────────┬───────────────────────────────┬─────────────────────┘
                        │ RTSP (:8554) & ONVIF (:8000)   │ MQTT (:1883)
                        ▼                               ▼
┌──────────────────────────────────────┐ ┌────────────────────────────────────┐
│ Scrypted / HomeKit HKSV / NVR        │ │ Home Assistant (Auto-Discovery)    │
│ (1080p, 2-Way Audio, Profile G, Snap)│ │ (Licht, Dimmer, Dämmerung, Sirene) │
└──────────────────────────────────────┘ └────────────────────────────────────┘
```

---

## 2. Paketstruktur & Modul-Verantwortlichkeiten

- **`cmd/launcher/`**:
  - `main.go`: Autarker, minimaler Go-Bootstrap-Launcher (`CGO_ENABLED=0`). Dient als Fallback-Downloader für die proprietäre `libnabto_client.so`, falls der CGo-Treiber über `USE_CGO_NABTO=true` aktiviert wird.

- **`cmd/steinel-bridge/main.go`**:
  - Konfigurations-Hierarchie (Precedence):
    1. CLI-Flags (`-ip`, `-type`, `-user`, `-pass`, `-qr`, `-key`, `-port`, `-path`, `-res`, `-audio-codec`, `-onvif`, `-reset-pairing`, `-mqtt-broker`, `-sync-interval`, `-debug`, etc.)
    2. Umgebungsvariablen (`CAMERA_IP`, `CAMERA_TYPE`, `CAMERA_USER`, `CAMERA_PASSWORD`, `QR_CODE`, `KEY_PATH`, `RESOLUTION`, `AUDIO_CODEC`, `RTSP_PORT`, `ONVIF_PORT`, `MQTT_BROKER`, `SDCARD_SYNC_INTERVAL`, `USE_CGO_NABTO`, `DEBUG`, etc.)
    3. Home Assistant Add-on Konfigurationsdatei (`/data/options.json` & Home Assistant Supervisor MQTT Auto-Discovery API via `X-Supervisor-Token`)
    4. Standardwerte (Layer 1)
  - **Modell-Erkennung**: Prüft per `-type` bzw. führt bei `auto` einen schnellen TCP-Probe auf Port `34567` durch, um automatisch zwischen `L 620 CAM` (Xiongmai Sofia) und `L 625 CAM SC` (Nabto Edge) zu unterscheiden.
  - **Treiber-Auswahl (L 625)**: Nutzt standardmäßig den nativen Pure-Go Treiber (`pkg/nabtopure`). Über `USE_CGO_NABTO=true` kann bei Bedarf auf den CGo-Wrapper (`pkg/nabto`) umgeschaltet werden.
  - Initialisiert Server (`rtsp.Server`, `onvif.Server`, `mqtt.Client`, `storage.RecordingSyncer`).
  - **Supervisor-Loop (Nabto)**: Fängt Verbindungsabbrüche, Session-Beendigungen oder Watchdog-Resets ab und erzwingt einen sauberen **30-Sekunden-Cooldown**, damit neu startende Kameras stabil hochfahren können.

- **`pkg/nabtopure/`** *(Neu in v1.3.0)*:
  - `client.go`: 100 % nativer Pure-Go Nabto Edge Client. Verwaltet ECC-Schlüssel (NIST P-256), DTLS 1.2 Handshake via Pion DTLS, KeepAlive-Ping (5s) und 18-Byte Echo (`0x04 0x02` + Nonce) für unterbrechungsfreien Dauerbetrieb.
  - `coap.go`: Integrierter CoAP Client für Nabto Edge Endpunkte (`/p2p/webrtc-info` für Signaling-Port, `/iam/pairing` für Device/Product-ID Extraktion, `/webrtc/tracks` für Track-Aktivierung).
  - `stream.go`: Nabto Stream Transport mit SYN/ACK Handshake, Segmentgrößen-Aushandlung, Sequenznummern und 4-Byte Little-Endian Message Framing für WebRTC-Signaling.
  - `packet_conn.go`: Paket-Demultiplexer und Framer (Typ `0x03` DTLS Record Wrapping über UDP).

- **`pkg/nabto/`**:
  - `client.go`: CGo-Bindings für das Nabto Edge Client SDK (`nabto_client.h`) als konfigurierbarer Fallback (`USE_CGO_NABTO=true`).
  - `interface.go`: Gemeinsame Treiber-Interfaces (`Driver`, `StreamDriver`).
  - `qr.go`: Parst Zugangsdaten (`did`, `pid`, `sct`, `pairPwd`) aus dem QR-Code-String der Steinel App.

- **`pkg/webrtc/`**:
  - `bridge.go`: Nutzt **Pion WebRTC v4** für DTLS/SRTP WebRTC-Sessions über den virtuellen Nabto-Stream (`no_trickle: true`, Vanilla ICE).
  - Hält den **RTCP-PLI Loop** (alle 1,0s für Keyframes) und den **RTP Silence Watchdog** (> 6s Ausfall triggert Reconnect).
  - **2-Way Audio Uplink**: `TrackLocalStaticRTP` für `audio/PCMU` (8000 Hz, PT 0, `a=sendrecv`).
  - **DataChannel 'test'**: Verarbeitet eingehende `tran_report` MCU-Statusframes und leitet SD-Karten Chunks asynchron an `SDCardManager` weiter.
  - `sdcard.go`: Verwaltet das Auslesen der internen SD-Karte (`get_event_list`, `get_snapshot`, `get_event_video`) mit Single-Flight Lock (`sync.Mutex`), Client-Abbruchüberwachung und 10s Watchdog.

- **`pkg/xiongmai/`**:
  - `client.go`: Xiongmai Sofia Binärprotokoll-Client (Port 34567, MD5-Challenge-Response Login, JSON-RPC Messages).
  - `driver.go`: Orchestriert Sofia-Verbindung, RTSP-Ingest, MCU-Synchronisation, SD-Karten-Verwaltung und Talk-Backchannel.
  - `rtsp_ingest.go`: Nimmt den nativen H.264/AAC-RTSP-Stream der L 620 entgegen und füttert den internen RTSP-Server.
  - `mcu.go`: Mappt L 620 spezifische Licht-, Dämmerungs- und Sensorkommandos auf Xiongmai-JSON-Payloads.
  - `sdcard.go`: Xiongmai SD-Karten Dateisystemabfrage (`OPFileQuery`) und Download (`OPFileQuery`).
  - `talk.go`: Rückkanal-Audiokommunikation (G.711u / PCMU) über das Xiongmai Talk-Protokoll.

- **`pkg/storage/`**:
  - `storage.go`: Definiert das gemeinsame Interface `RecordingProvider` für einheitlichen Zugriff auf Aufnahmen (Nabto & Xiongmai).
  - `syncer.go`: Hintergrund-Syncer (`RecordingSyncer`), der neue SD-Karten-Aufnahmen periodisch pollt, bei Bewegung sofort abgleicht und neue Aufnahme-Events an MQTT / Home Assistant publiziert.

- **`pkg/audio/`**:
  - `g711.go`: ITU-T G.711 µ-law Decoder (8-Bit $\rightarrow$ 16-Bit Linear PCM).
  - `resample.go`: Polyphase- / Interpolations-Resampler (8 kHz $\rightarrow$ 16 kHz / 32 kHz).
  - `transcoder.go`: Echtzeit-Audiotranscoder mit persistentem VisualOn AAC-Encoder (`github.com/gen2brain/aac-go`), erzeugt unterbrechungsfreie AAC-LC Access Units für gortsplib.

- **`pkg/rtsp/`**:
  - `server.go`: RTSP-Server auf Basis von `github.com/bluenviron/gortsplib/v4`. Liefert H.264 Video, AAC/PCMU Audio und ONVIF Profile T Audio Backchannel.
  - `interceptor.go`: Dedizierter Non-Blocking UDP-RTP Socket auf Port `8554/udp`, fängt eingehende Backchannel-Audiodaten von Scrypted / HomeKit ab und leitet sie an den aktiven Kamera-Treiber weiter.

- **`pkg/onvif/`**:
  - `discovery.go`: **WS-Discovery Server** auf UDP Multicast `239.255.255.250:3702`.
  - `device.go`: Device Service (`GetDeviceInformation`, `GetCapabilities`, `GetServices`, `GetSystemDateAndTime`).
  - `media.go`: Media Service (`Profile_Main` 1080p, `Profile_Sub` 360p, `GetStreamUri`, `GetSnapshotUri`, `SetVideoEncoderConfiguration`).
  - `events.go`: Event Service (WS-BaseNotification PullPoint für Motion-Events).
  - `deviceio.go`: DeviceIO / Relay / Auxiliary Service für Licht- und Sirenensteuerung.
  - `recording.go`, `replay.go`, `search.go`: **ONVIF Profile G Services** zur standardisierten Suche und Wiedergabe von SD-Karten-Aufnahmen in NVRs.
  - `server.go`: HTTP Server auf Port `8000` (SOAP Dispatcher + `/snapshot.jpg` + REST `/api/status`, `/api/light`, `/api/sdcard/*`).

- **`pkg/mqtt/`**:
  - `client.go`: Home Assistant MQTT Auto-Discovery Client (`paho.mqtt.golang`). Veröffentlicht Entitäten für `light`, `select`, `sensor`, `binary_sensor`, `number`, `siren`, `event`. Zwei-Wege-Sync mit `events.GlobalBus`.

- **`pkg/mcu/`**:
  - `mcu.go`: Parser für 18-Byte (36 Hex-Zeichen) MCU-UART-Frames (`5A0F0F...`) und Command-Builder für Dimmstufen, Nachlaufzeit, Grundlicht, PIR-Sensitivität, Lux-Schwelle und Sirene.

- **`pkg/events/`**:
  - `events.go`: Thread-sicherer zentraler Publish/Subscribe-Event-Bus (`GlobalBus`).

- **`ha-addon/` & `ha-addon-beta/` (Home Assistant Add-ons)**:
  - `config.yaml`: Manifest (Schema, `host_network: true`, `services: ["mqtt:want"]`, `reset_pairing: bool`).
  - `build.yaml`: Verknüpfung mit pre-built Multi-Arch Image `ghcr.io/afrouper/steinel-cam-bridge:{arch}`.
  - `DOCS.md` & `CHANGELOG.md`: In-App Dokumentation und Versionshistorie.
  - `translations/`: Sprachdateien (`de.yaml`, `en.yaml`) für das Home Assistant Einstellungs-Formular.
  - *Regel*: `ha-addon` hält stets die aktuelle Stable-Version (konkreter Tag, kein `latest`). `ha-addon-beta` wird für Tests und Vorabveröffentlichungen genutzt.

- **`scripts/`**:
  - `setup-sdk.sh` / `setup-sdk.ps1`: Lädt Nabto SDK Header und Libraries für lokale Entwicklung herunter.
  - `run-dev.sh` / `run-dev.ps1`: Startskripte für den lokalen Entwicklungsbetrieb.

- **`examples/`**:
  - `docker-compose.yml`: Beispiel-Konfiguration für Standalone-Docker-Setups.

- **`extracts/`**:
  - Nur für Entwickler und Agenten zum Nachschlagen von Reverse-Engineering-Traces, App-Disassembly und Mitschnitten. Darf **nicht** ins Git eingecheckt werden.

- **`tools/`**:
  - `mock-camera`: Mock-Kamera zur Protokollanalyse des Xiongmai Sofia Protokolls (L 620).
  - `test-pure-nabto`: Standalone-Diagnosetool zur schnellen Direktprüfung der Pure-Go Nabto-Verbindung gegen Kameras im LAN.

---

## 3. Zentrale Architektur-, Sicherheits- & Dokumentationsregeln

1. **Aktualisierung von `AGENTS.md`**:
   - Sobald neue Go-Pakete, Dateien mit Kernverantwortlichkeiten, Konfigurationsoptionen (Flags/Env), Protokolle oder Architektur-Patterns hinzugefügt oder modifiziert werden, **muss diese `AGENTS.md` Datei zwingend aktualisiert und erweitert werden**.
   - Neue oder geänderte Schnittstellen müssen synchron in `README.md` und bei Bibliotheksänderungen in `THIRD_PARTY_LICENSES.md` dokumentiert werden.

2. **Hardware-Schonung der Kamera**:
   - Die Kameras besitzen schwache embedded CPUs. **Niemals mehrere parallele WebRTC-Sessions oder gleichzeitige SD-Downloads aufbauen** (Single-Flight Mutex beachten).
   - Nach Verbindungsabbrüchen immer den **30-Sekunden-Cooldown** einhalten.

3. **Zero Transcoding**:
   - Reiche H.264 NAL-Units und Audio-Pakete möglichst direkt weiter (< 0,3 % CPU-Last auf dem Host). Audio-Transcoding (G.711u zu AAC) erfolgt hocheffizient und ohne externe Tools (wie ffmpeg).

4. **Keine Secrets oder realen IPs im Git**:
   - `.key` Dateien, `.sdk/` Verzeichnisse, Traces und Binaries gehören in `.gitignore`.
   - Der QR-Code-String ist ein Secret (`did=...`, `pid=...`, `sct=...`, `pairPwd=...`). In Beispielen, Tests und Doku ausschließlich Platzhalter (`<IP_ADDRESS>`, `<DEVICE_ID>`, etc.) verwenden.
   - Secrets niemals im Klartext loggen oder in Code-Kommentaren hinterlegen.
   - In `go.mod` sind immer die höchsten stabilen Versionen der Dependencies zu verwenden.

5. **Urheberrecht & Keine proprietären Binaries im Container/Git**:
   - `libnabto_client.so` / `.dylib` ist proprietäres geistiges Eigentum der Nabto ApS und darf **nicht** im Git-Repository oder im Docker-Image auf `ghcr.io` bereitgestellt werden.
   - Der Distroless-Container nutzt `cmd/launcher` (`CGO_ENABLED=0`), um beim Erststart die in `$NABTO_SDK_VERSION` definierte Library im RAM-Stream direkt von Nabtos offiziellem GitHub-Repository zu laden und in `/data/lib/` zu cachen.

6. **Hierarchische Scopes**:
   - Alle MQTT-Topics müssen immer unter `<baseTopic>/<deviceID>/...` liegen, um Mehrkamera-Setups ohne Kollisionen zu unterstützen.

7. **Dokumentation**:
   - Code präzise und zielgerichtet kommentieren, besonders bei Protokoll-Decodierung, Bit-Operationen und Concurrency-Locks.
   - Dokumentationssprache ist Deutsch.

8. **Release & Versionierung**:
   - Releases erfolgen über Git-Tags nach Semantic Versioning: `v<MAJOR>.<MINOR>.<PATCH>` (z. B. `v1.0.0`) bzw. `v<MAJOR>.<MINOR>.<PATCH>-beta.<VERSION>` (z. B. `v1.0.0-beta.1`). Ein Push triggert den GitHub Actions Release-Workflow.
   - Ein Tag welches `beta` enthält führt zu einem PreRelease
   - Es ist erst der Build auf GitHub abzuwarten bevor die Metadaten in den HomeAssistant AddOns ergänzt werden
   - Es sind Releasenotes für die HomeAssistant AddOns zu erstellen

9. **Branching**:
  - Es sind Feature Branches nach dem GitHub Standard zu erstellen wenn größere Anpassungen gemacht werden
  - Jede Major oder Minor Version muss in einem Feature Branch erstellt werden
  - Im Zweifel Rückfrage ob es nicht nur ein Patch ist der in einer Patch Version resultiert
  - In den Branches können Tags nach genannten Vorgaben erstellt werden - diese Erzeugen auch ein Release Build und damit ein GitHub Release
    - Tags auf Basis von Branches **müssen** für `beta` Versionen erstellt sein. 

---

## 4. Entwicklungs- & Build-Befehle

```bash
# 1. Lokale SDK-Artefakte herunterladen
# macOS / Linux:
./scripts/setup-sdk.sh
# Windows PowerShell:
.\scripts\setup-sdk.ps1

# 2. Lokalen Entwicklungs-Build starten
# macOS / Linux:
./scripts/run-dev.sh -key data/client.key -ip <IP_ADDRESS> -qr "did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx"
# Windows PowerShell:
.\scripts\run-dev.ps1 -Key data/client.key -IP <IP_ADDRESS> -QR "did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx"

# 3. Unit-Tests ausführen
# macOS:
DYLD_LIBRARY_PATH="$(pwd)/.sdk/lib" go test -v ./...
# Linux:
LD_LIBRARY_PATH="$(pwd)/.sdk/lib" go test -v ./...

# 4. Race-Condition-Detektor ausführen
DYLD_LIBRARY_PATH="$(pwd)/.sdk/lib" go test -race -v ./...

# 5. Native Binaries manuell kompilieren
# CGo-Daemon (steinel-bridge):
CGO_LDFLAGS="-L$(pwd)/.sdk/lib -lnabto_client" CGO_CFLAGS="-I$(pwd)/.sdk/include" go build -o steinel-bridge ./cmd/steinel-bridge
# CGo-freier Launcher (launcher):
CGO_ENABLED=0 go build -o launcher ./cmd/launcher

# 6. Multi-Arch Docker-Container lokal bauen
docker build -t steinel-cam-bridge .
```
