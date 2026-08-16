# Steinel L 625 CAM SC — Entwickler- & Agenten-Leitfaden

Dieses Dokument beschreibt die Codebase-Architektur, Designentscheidungen, den Protokollablauf und Richtlinien für zukünftige Entwickler und KI-Agenten im Repository `steinel-cam-bridge`.

---

## 1. High-Level Architektur

Die **Steinel CAM Bridge** ist ein hochperformanter, 100 % autarker Go-Daemon, der die **Steinel L 625 CAM SC** Außenleuchte in eine standardkonforme **ONVIF Profile S/T Kamera** mit **RTSP-Streaming**, **2-Wege-Audio (Gegensprechen)** und **MQTT Home Assistant Auto-Discovery** wandelt – zur nahtlosen Integration in **Scrypted / Apple HomeKit Secure Video (HKSV)**, **Home Assistant**, **Synology Surveillance Station** und **Frigate**.

```
┌─────────────────────────────────────────────────────────────┐
│                 Steinel L 625 CAM SC                        │
│  ├── Linux SoC / IPC: WiFi, Nabto Edge, WebRTC Video/Audio  │
│  └── MCU: 230V Triac, LED Dimmung, PIR Sensor, Lux Sensor   │
└──────────────────────────────┬──────────────────────────────┘
                               │ 1. mDNS Wake-Up (UDP 5353 / 5592)
                               │ 2. Nabto Edge P2P Direct Tunnel (CGo Wrapper)
                               │ 3. CoAP /p2p/webrtc-info & /webrtc/tracks
                               │ 4. WebRTC DTLS/SRTP Media + DataChannel 'test'
                               ▼
┌─────────────────────────────────────────────────────────────┐
│               Steinel Bridge Daemon (Go)                    │
│  ├── pkg/nabto: CGo Wrapper für Nabto Edge Client SDK       │
│  ├── pkg/webrtc: Pion WebRTC Engine (H.264, PCMU, Backchan) │
│  ├── pkg/mcu: 18-Byte UART Hex Parser & Command Builder     │
│  ├── pkg/events: Zentraler Event-Bus (Motion, Lux, Lamp)    │
│  ├── pkg/rtsp: gortsplib v4 RTSP Server + Profile T Backch. │
│  ├── pkg/onvif: WS-Discovery (UDP 3702), Device, Media, Evt │
│  └── pkg/mqtt: Home Assistant MQTT Auto-Discovery Client    │
└──────────────┬──────────────────────────────┬───────────────┘
               │ RTSP (:8554) & ONVIF (:8000) │ MQTT (:1883)
               ▼                              ▼
┌──────────────────────────────┐ ┌────────────────────────────┐
│ Scrypted / HomeKit HKSV      │ │ Home Assistant (Auto-Disc) │
│ (1080p, 2-Way Audio, PIR Evt)│ │ (Licht, Dimmer, PIR, Lux)  │
└──────────────────────────────┘ └────────────────────────────┘
```

---

## 2. Paketstruktur & Modul-Verantwortlichkeiten

- **`cmd/steinel-bridge/main.go`**:
  - Konfigurations-Hierarchie (Precedence):
    1. CLI-Flags (`-ip`, `-qr`, `-key`, `-port`, `-path`, `-res`, `-onvif`, `-mqtt-broker`, etc.)
    2. Umgebungsvariablen (`CAMERA_IP`, `QR_CODE`, `KEY_PATH`, `MQTT_BROKER`, etc.)
    3. Home Assistant Add-on Konfigurationsdatei (`/data/options.json`, falls vorhanden)
    4. Standardwerte
  - Initialisiert Server (`rtsp.Server`, `onvif.Server`, `mqtt.Client`).
  - Beherbergt den **Supervisor-Loop**: Fängt Verbindungsabbrüche, Session-Beendigungen oder Watchdog-Resets ab und erzwingt einen sauberen **30-Sekunden-Cooldown**, damit neu startende Kameras stabil hochfahren können, ohne das Netzwerk zu fluten.

- **`repository.yaml` & `steinel-cam-bridge/` (Home Assistant Add-on)**:
  - `repository.yaml`: Ermöglicht das Hinzufügen dieses GitHub-Repositories als externe Add-on-Quelle in Home Assistant.
  - `steinel-cam-bridge/config.yaml`: Manifest für Home Assistant (Schema für Einstellungs-Formular, `host_network: true`, `services: ["mqtt:want"]` für automatische Mosquitto-Verbindung).
  - `steinel-cam-bridge/build.yaml`: Verknüpft das Add-on direkt mit dem pre-built Multi-Arch Image `ghcr.io/afrouper/steinel-cam-bridge:{arch}`.
  - `steinel-cam-bridge/DOCS.md`: In-App Dokumentation für Home Assistant Benutzer.

- **`pkg/nabto/`**:
  - `client.go`: CGo-Bindings für das Nabto Edge Client SDK (`nabto_client.h`).
  - Verwaltet kryptografische ECC-Schlüssel (`client.key`), automatisches IAM-Pairing (`/iam/pairing/password-open`), CoAP-Signaling-Port-Erkennung (`/p2p/webrtc-info`) und Track-Aktivierung (`/webrtc/tracks`).
  - `qr.go`: Parst die Kamera-Zugangsdaten (`did`, `pid`, `sct`, `pairPwd`) aus dem QR-Code-String der Steinel App.

- **`pkg/audio/`**:
  - `g711.go`: Standard ITU-T G.711 $\mu$-law Decoder (8-Bit $\rightarrow$ 16-Bit Linear PCM).
  - `resample.go`: Polyphase / Interpolations-Resampler von 8.000 Hz auf 16.000 Hz / 32.000 Hz.
  - `transcoder.go`: Echtzeit-Audiotranscoder auf Basis des VisualOn AAC-Encoders (`github.com/gen2brain/aac-go`), erzeugt standardkonforme AAC-LC Access Units (AU) für gortsplib.

- **`pkg/webrtc/`**:
  - `bridge.go`: Nutzt **Pion WebRTC v4** (`github.com/pion/webrtc/v4`) für DTLS/SRTP WebRTC-Sessions über den virtuellen Nabto-Stream.
  - Sendet initiales SDP-Offer mit `no_trickle: true` und Vanilla ICE Candidates.
  - Hält den **RTCP-PLI Loop**: Periodische PLIs alle 1,0 Sekunden für sofortige H.264-Sync-Frames (I-Frames) in Scrypted / Apple Home.
  - **RTP Silence Watchdog (`runWatchdogLoop`)**: Überwacht Video-Pakete. Bei Ausfall > 6s wird die Session abgebrochen und der 30s-Reconnect ausgelöst.
  - **2-Way Audio Uplink**: Eigener `TrackLocalStaticRTP` für `audio/PCMU` (8000 Hz, Payload Typ 0) im WebRTC SDP (`a=sendrecv`) für Gegensprechen.
  - **DataChannel 'test'**: Verarbeitet eingehende `tran_report` MCU-Statusframes und sendet `tran_ctl` Befehle.

- **`pkg/rtsp/`**:
  - `server.go`: Integrierter RTSP-Server auf Basis von `github.com/bluenviron/gortsplib/v4`.
  - H.264 Video (Main Feed), AAC-Audio (oder PCMU, konfigurierbar via `AUDIO_CODEC`) und **ONVIF Profile T Audio Backchannel** (`IsBackChannel: true`).
  - Leitet empfangene Rückkanal-RTP-Pakete von HomeKit/Scrypted verzögerungsfrei an `webrtc.Bridge.WriteAudioBackchannel` weiter.

- **`pkg/onvif/`**:
  - `discovery.go`: **WS-Discovery Server** auf UDP Multicast `239.255.255.250:3702` (reagiert auf `wsdd:Probe`).
  - `device.go`: Device Service (`GetDeviceInformation`, `GetCapabilities`, `GetServices`, `GetSystemDateAndTime`).
  - `media.go`: Media Service (`Profile_Main` 1080p, `Profile_Sub` 360p, `GetStreamUri`, `GetSnapshotUri`, `SetVideoEncoderConfiguration`).
  - `events.go`: Event Service (WS-BaseNotification PullPoint: `CreatePullPointSubscription`, `PullMessages` für `tns1:RuleEngine/CellMotionDetector/Motion`).
  - `deviceio.go`: DeviceIO / Relay / Auxiliary Service für Licht- und Sirenensteuerung.
  - `server.go`: HTTP Server auf Port `8000` (SOAP Dispatcher + Snapshot Endpoint `/snapshot.jpg` + REST `/api/status`).

- **`pkg/mqtt/`**:
  - `client.go`: Home Assistant MQTT Auto-Discovery Client auf Basis von `paho.mqtt.golang`.
  - Veröffentlicht Discovery-Configs für `light`, `select`, `sensor`, `binary_sensor`, `number`, `siren`.
  - **Hierarchische Topic-Struktur**: `steinel/<deviceID>/...` (keine Konflikte bei mehreren Kameras).
  - Zwei-Wege-Synchronisation mit `events.GlobalBus`.

- **`pkg/mcu/`**:
  - `mcu.go`: Parser für die 18-Byte (36 Hex-Zeichen) MCU-UART-Frames `5A0F0F...` und Command-Builder für Dimmstufen, Nachlaufzeit, Grundlicht, PIR-Sensitivität, Dämmerungsschwelle (Lux) und Sirene.

- **`pkg/events/`**:
  - `events.go`: Thread-sicherer zentraler Publish/Subscribe-Event-Bus (`GlobalBus`) zur Entkopplung aller Subsysteme.

- **`extracts/`**: Niemals in git einchecken. Nur für die Agenten zum Nachschlagen von Informationen und Dokumentationen.
  - `AGENTS.MD`: Weitere Informationen und Details zu den Elementen in diesem Ordner.


---

## 3. Zentrale Architektur- & Sicherheitsregeln

1. **Hardware-Schonung der Kamera**:
   - Die Steinel-Kamera hat eine schwache embedded CPU. **Niemals mehrere parallele WebRTC-Sessions aufbauen**.
   - Nach Verbindungsabbrüchen immer den **30-Sekunden-Cooldown** einhalten.
2. **Zero Transcoding**:
   - Reiche H.264 NAL-Units und PCMU Audio-Pakete direkt weiter (< 0,3 % CPU-Last auf dem Host).
3. **Keine Secrets oder reale IPs im Git**:
   - `.key` Dateien, `.sdk/` Verzeichnisse und Binaries gehören in `.gitignore`.
   - Der QR Code zum Pairen und der enthaltende String ist als Secret anzusehen. Immer unkenntlich ablegen und in Beispielen nur die unkenntliche Version verwenden
     - did=<DeviceID>,pid=<ProductID>,sct=<Secret>,pairPwd=<Pairing Passwort>
   - Niemals Secrets und Keys in den Kommentaren oder im Code hinterlegen.
   - Immer nach dem sichersten Vorgehen im Umgang mit Security relevanten Code fragst. z.B. beim Thema Crypto, Secrets und Credentials.
   - In den Dokumentationen niemals Secrets oder Keys nennen. Z.B. immer nur von "DeviceID" oder "ProductKey" sprechen.
   - Keine IP Adressen im Klartext hinterlegen. In Examples immer "<IP_ADDRESS>" verwenden
   - Keine echten Hostnamen hinterlegen und z.B. immer "<HOSTNAME>" verwenden.
   - Immer die aktuellen Versionen der benötigten Bibliothken (Dependencies) und Programmiersprachen verwenden.
     - In der Go-Modul-Datei `go.mod` ist immer die höchste stabile Version zu verwenden, welche nicht als veraltet markiert wurde oder als unstable gilt.
   - `extracts` Ordner ist niemals im git einzuchecken. Es können Informationen und Traces (z.B. Wireshark, Apps, ...) abgelegt werden
4. **Hierarchische Scopes**:
   - Alle MQTT-Topics müssen immer unter `<baseTopic>/<deviceID>/...` liegen, um Mehrkamera-Setups zu unterstützen.
5. **Dokumentation**:
   - Kommentiere den Sourcecode wo komplizierter Code geschrieben wird; ansonsten nicht.
   - Dokumentationssprache ist deutsch, da Steinl Kameras zumeißt im DACH Raum verwendet werden
   - Passe die Dokumentation an neue Features oder Verhaltensweisen an
     - `README.md` Für die Haupt Dokumentation
     - `AGENTS.md` Für Anweisungen an Agenten wenn es neue Elemente gibt
     - `THIRD_PARTY_LICENSES.md` Falls es Anpassungen an den Dependencies gibt.


---

## 4. Entwicklungs- & Build-Befehle

```bash
# 1. Lokale SDK-Artefakte herunterladen (macOS / Linux)
./scripts/setup-sdk.sh

# 2. Lokalen Entwicklungs-Build starten (Beispiel mit Platzhaltern)
./scripts/run-dev.sh -key data/client.key -ip 192.168.1.100 -qr "did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx"

# 3. Unit-Tests ausführen
go test -v ./...

# 4. Multi-Arch Docker-Container lokal bauen
docker build -t steinel-cam-bridge .
```
