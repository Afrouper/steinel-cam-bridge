# Steinel CAM Bridge — Entwickler- & Agenten-Leitfaden

Dieses Dokument beschreibt die Codebase-Architektur, Designentscheidungen, Protokollabläufe und verbindliche Richtlinien für Entwickler und KI-Agenten im Repository `steinel-cam-bridge`.

---

## 1. High-Level Architektur

Die **Steinel CAM Bridge** ist ein hochperformanter, 100 % autarker Go-Daemon, der Steinel Außenleuchten (**L 625 CAM SC**, **L 620 CAM**, **XLED CAM 1/2**, **Spot CAM**) in standardkonforme **ONVIF Profile S/T/G Kameras** mit **RTSP-Streaming**, **2-Wege-Audio (Gegensprechen)**, **lokalem MicroSD-Speicherabruf** und **MQTT Home Assistant Auto-Discovery** wandelt – zur nahtlosen Integration in **Scrypted / Apple HomeKit Secure Video (HKSV)**, **Home Assistant**, **Synology Surveillance Station**, **Frigate** und weitere NVR-Systeme.

### Unterstützte Kameratypen & Protokoll-Treiber

1. **Steinel L 625 CAM SC (Nabto Edge Driver)**:
   - mDNS Wake-Up (UDP 5353 / 5592)
   - CGo Wrapper um offizielles Nabto Edge Client SDK (`pkg/nabto`, Standard & empfohlen)
   - Nativer Pure-Go Nabto Edge P2P Direct Tunnel (`pkg/nabtopure`, experimentell)
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
│  ├─ cmd/steinel-bridge: Schlanker Daemon-Einstiegspunkt (64 Zeilen)         │
│  ├─ pkg/config: Validierter Parser für CLI-Flags, Env & HA-Optionen        │
│  ├─ pkg/app: Subsystem-Initialisierung & Dependency Injection               │
│  ├─ pkg/supervisor: Autonomer Überwachungs-, Watchdog- & Reconnect-Lifecycle│
│  ├─ pkg/driver: Polymorphes CameraDriver-Interface (L625 & L620 Treiber)   │
│  ├─ pkg/nabto: CGo-Treiber & dynamische Driver-Registry nach Go-Standard    │
│  ├─ pkg/nabtopure: Nativer Pure-Go Nabto Client mit sync.Pool Buffer-Pooling │
│  ├─ pkg/webrtc: Modularisierte WebRTC Engine (Signaling, Media, Backchannel) │
│  ├─ pkg/xiongmai: Sofia Protokoll, RTSP-Ingest, MCU & Talk (L 620)          │
│  ├─ pkg/storage: SD-Karten RecordingProvider, Error-Harmonisierung & Syncer │
│  ├─ pkg/audio: G.711u Decoder, Resampler & AAC-LC Transcoder mit Puffer-Opt │
│  ├─ pkg/rtsp: gortsplib v4 Server, Profile T Backchannel & Zero-Alloc Interc│
│  ├─ pkg/onvif: WS-Discovery (UDP 3702), Profile S/T/G (Media, Events, DevIO)│
│  ├─ pkg/mqtt: Modularisierter HA Auto-Discovery Client (EventBus DI)        │
│  ├─ pkg/events: Instanziierter Thread-sicherer Pub/Sub Event-Bus            │
│  ├─ pkg/mcu: 18-Byte UART Hex Parser & Command Builder                      │
│  └─ pkg/logger: Zentrales hierarchisches log/slog Logging (Trace bis Error) │
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

- **`cmd/steinel-bridge/`**:
  - `main.go`: Schlanker, deklarativer Programmeinstieg (64 Zeilen). Parst Konfiguration via `pkg/config`, initialisiert den Überwachungs-Lifecycle via `pkg/supervisor` und startet die Anwendung via `pkg/app`.

- **`pkg/config/`** *(Neu in Milestone 2)*:
  - `config.go`: Zentrales, validiertes Konfigurationsobjekt (`Config`) mit strenger Präzedenz:
    1. CLI-Flags (`-ip`, `-type`, `-user`, `-pass`, `-bridge-user`, `-bridge-pass`, `-qr`, `-key`, `-port`, `-path`, `-res`, `-audio-codec`, `-onvif`, `-reset-pairing`, `-mqtt-broker`, `-sync-interval`, `-log-level`, etc.)
    2. Umgebungsvariablen (`CAMERA_IP`, `CAMERA_TYPE`, `CAMERA_USER`, `CAMERA_PASSWORD`, `BRIDGE_USER`, `BRIDGE_PASS`, `QR_CODE`, `KEY_PATH`, `RESOLUTION`, `AUDIO_CODEC`, `RTSP_PORT`, `ONVIF_PORT`, `MQTT_BROKER`, `SDCARD_SYNC_INTERVAL`, `USE_CGO_NABTO`, `LOG_LEVEL`, `LOG_FORMAT`, etc.)
    3. Home Assistant Add-on Konfigurationsdatei (`/data/options.json` & Home Assistant Supervisor MQTT Auto-Discovery API via `X-Supervisor-Token`)
    4. Sichere Standardwerte.
  - `probe.go`: Führt bei `camera_type: "auto"` einen schnellen Non-Blocking TCP-Probe auf Port `34567` durch, um automatisch zwischen `L 620 CAM` (Xiongmai Sofia) und `L 625 CAM SC` (Nabto Edge) zu unterscheiden.

- **`pkg/supervisor/`** *(Neu in Milestone 2)*:
  - `supervisor.go`: Robuster, autonomer Überwachungs- und Reconnect-Lifecycle.
  - Verwaltet Driver-Neustarts bei Verbindungsabbrüchen, Timeouts oder Kamera-Reboots.
  - Guarded Goroutine-Draining (max. 3s) stellt sicher, dass zu jedem Zeitpunkt maximal eine Verbindungsinstanz aktiv ist und Sockets sauber freigegeben werden.
  - Hält definierte Cooldown-Phasen ein (15s Retry-Cooldown, 30s Kamera-Reboot-Cooldown).

- **`pkg/app/`** *(Neu in Milestone 2)*:
  - `app.go`: Subsystem-Orchestrierung und saubere Dependency Injection.
  - Instanziiert den isolierten `events.Bus` via `events.NewBus()`.
  - Baut den passenden `driver.CameraDriver` über die Driver-Factory.
  - Initialisiert Server (`rtsp.Server`, `onvif.Server`, `mqtt.Client`, `storage.RecordingSyncer`).
  - Fängt Betriebssystem-Signale (`SIGINT`, `SIGTERM`) für unterbrechungsfreien Graceful Shutdown ab.

- **`pkg/driver/`** *(Neu in Milestone 3)*:
  - `driver.go`: Einheitliches polymorphes `CameraDriver`-Interface (`Run`, `Close`, `GetStatus`, `SetLight`, `TriggerAlarm`, etc.).
  - `l625.go`: Kapselt den autonomen Nabto Edge P2P Lifecycle (mDNS, CoAP, WebRTC-Ingest, MCU-Telemetrie und Watchdog-Handling).
  - `l620.go`: Kapselt Sofia DVRIP TCP-Ingest, RTSP-Relay, MCU-Statusabfrage und Keepalive-Worker.
  - `factory.go`: Dynamische Treiber-Instanziierung über `driver.New(...)` ohne globale Zustände.

- **`pkg/logger/`**:
  - Zentrales, hierarchisches Logging-Framework auf Basis von Go Standardbibliothek `log/slog` (0 externe Abhängigkeiten).
  - Unterstützt Level: `Trace` (-8, Steuersignale), `Debug` (-4), `Info` (0, Standard), `Warn` (4), `Error` (8).
  - Formatierung wahlweise `console` (menschenlesbar mit Zeitstempel & Komponenten-Tag `[Component]`) oder `json`.
  - `logger.FormatBinary`: Hex-Dump für binäre Steuerpakete (MCU-Frames, Sofia-Pakete).
  - Eiserne Restriktion: Reines Durchschleifen von Mediadaten (H.264 NAL-Units, SD-Karten MP4-Videochunks) wird niemals im Log ausgegeben.

- **`pkg/nabto/`**:
  - `registry.go`: Zentrales Nabto Driver Registry & Factory-Pattern nach Go `database/sql`-Standard (`nabto.Register`, `nabto.New`, `nabto.ResolveDriverType`).
  - `client.go`: CGo-Bindings für das offizielle Nabto Edge Client SDK (`nabto_client.h`) als Standard-Treiber (`//go:build cgo`).
  - `stub.go`: Graceful Fallback Stub für reine CGo-freie Builds.
  - `interface.go`: Gemeinsame Treiber-Interfaces (`Driver`, `StreamDriver`).
  - `qr.go`: Parst Zugangsdaten (`did`, `pid`, `sct`, `pairPwd`) aus dem QR-Code-String der Steinel App.

- **`pkg/nabtopure/`**:
  - `client.go`: 100 % nativer Pure-Go Nabto Edge Client. Verwaltet ECC-Schlüssel (NIST P-256), DTLS 1.2 Handshake via Pion DTLS, KeepAlive-Ping (5s) und 18-Byte Echo (`0x04 0x02` + Nonce) für unterbrechungsfreien Dauerbetrieb.
  - `coap.go`: Integrierter CoAP Client für Nabto Edge Endpunkte (`/p2p/webrtc-info` für Signaling-Port, `/iam/pairing` für Device/Product-ID Extraktion, `/webrtc/tracks` für Track-Aktivierung).
  - `stream.go`: Nabto Stream Transport mit SYN/ACK Handshake, Segmentgrößen-Aushandlung und `sync.Pool`-Pufferung (`streamBufPool`) für SYN/ACK-Pakete.
  - `packet_conn.go`: Paket-Demultiplexer und Framer mit `sync.Pool` (`udpBufPool`, 2048 Bytes) für Zero-Allocation UDP-Framing (**0 B/op, 0 allocs/op**).

- **`pkg/webrtc/`**:
  - Modularisiert in vier fokussierte Subsysteme:
    - `signaling.go`: TURN-Exchange, Vanilla-ICE Gathering, SDP Offer/Answer Negotiation & Sanitization.
    - `media.go`: H.264 Video-Ingest, Audio-Ingest & AAC-Transcoder, 6s Silence-Watchdog & PLI-Burst/Intervallschleife.
    - `backchannel.go`: Zwei-Wege-Audio (Kamera-Lautsprecher) mit 160-Byte G.711u Frame-Chunking und SSRC/Timestamp-Management.
    - `mcu_dispatch.go`: DataChannel Message Handler, JSON-RPC Commands, Hex-MCU-Befehle, 30s Status-Polling und 10s PIR/Motion-Reset Timer.
  - `sdcard.go`: Verwaltet das Auslesen der internen SD-Karte (`get_event_list`, `get_event_video`) mit Single-Flight Lock (`sync.Mutex`), Client-Abbruchüberwachung und 10s Watchdog.

- **`pkg/xiongmai/`**:
  - `client.go`: Xiongmai Sofia Binärprotokoll-Client (Port 34567, MD5-Challenge-Response Login mit CWE-312 Credential Protection, JSON-RPC Messages).
  - `driver.go`: Orchestriert Sofia-Verbindung, RTSP-Ingest, MCU-Synchronisation, SD-Karten-Verwaltung und Talk-Backchannel.
  - `rtsp_ingest.go`: Nimmt den nativen H.264/AAC-RTSP-Stream der L 620 entgegen und füttert den internen RTSP-Server.
  - `mcu.go`: Mappt L 620 spezifische Licht-, Dämmerungs- und Sensorkommandos auf Xiongmai-JSON-Payloads.
  - `sdcard.go`: Xiongmai SD-Karten Dateisystemabfrage (`OPFileQuery`) und Download mit konsistenter Thumbnail-Behandlung.
  - `talk.go`: Rückkanal-Audiokommunikation (G.711u / PCMU) über das Xiongmai Talk-Protokoll.

- **`pkg/storage/`**:
  - `storage.go`: Definiert das gemeinsame Interface `RecordingProvider` und kanonische Speicherfehler (`ErrStorageBusy`, `ErrStorageTimeout`, `ErrFeatureDisabled`).
  - `syncer.go`: Hintergrund-Syncer (`RecordingSyncer`), der neue SD-Karten-Aufnahmen periodisch pollt, bei Bewegung sofort abgleicht und neue Aufnahme-Events an MQTT / Home Assistant publiziert.

- **`pkg/audio/`**:
  - `g711.go`: ITU-T G.711 µ-law Decoder (8-Bit $\rightarrow$ 16-Bit Linear PCM).
  - `resample.go`: Polyphase- / Interpolations-Resampler (8 kHz $\rightarrow$ 16 kHz / 32 kHz).
  - `transcoder.go`: Echtzeit-Audiotranscoder mit persistentem VisualOn AAC-Encoder (`github.com/gen2brain/aac-go`), wiederverwendbarem `pcmReader bytes.Reader` via `Reset()` und Puffer-Kompaktierung gegen Speicherfragmentierung.

- **`pkg/rtsp/`**:
  - `server.go`: RTSP-Server auf Basis von `github.com/bluenviron/gortsplib/v4`. Liefert H.264 Video, AAC/PCMU Audio, ONVIF Profile T Audio Backchannel sowie integrierte Digest (MD5/SHA256) & Basic Authentifizierung.
  - `interceptor.go`: Dedizierter UDP-RTP Socket auf Port `8554/udp` sowie TCP-Interleaved Listener (`interceptingConn`) mit vorallokiertem 4-KB Lesepuffer für Zero-Allocation Socket-Reads (**0 Allokationen/Read**).

- **`pkg/onvif/`**:
  - `auth.go`: RFC 2617 HTTP Digest Authentifizierung (zustandsloses HMAC-SHA256 Nonce-Management, MD5 Digest Berechnung mit `qop="auth"` & Legacy-Unterstützung), WS-Security UsernameToken (`PasswordDigest` via SHA-1 Nonce/Timestamp Hash sowie `PasswordText`), HTTP Basic Auth und Timing-Attack-sichere Validierung via `subtle.ConstantTimeCompare`.
  - `discovery.go`: **WS-Discovery Server** auf UDP Multicast `239.255.255.250:3702`.
  - `device.go`: Device Service (`GetDeviceInformation`, `GetCapabilities`, `GetServices`, `GetSystemDateAndTime`, `GetUsers`, `GetScopes`, `<tt:Security>` Capabilities mit `UsernameToken` und `HttpDigest`).
  - `media.go`: Media Service (`Profile_Main` 1080p, `Profile_Sub` 360p, `GetStreamUri`, `SetVideoEncoderConfiguration`).
  - `events.go`: Event Service (WS-BaseNotification PullPoint für Motion-Events).
  - `deviceio.go`: DeviceIO / Relay / Auxiliary Service für Licht- und Sirenensteuerung.
  - `recording.go`, `replay.go`, `search.go`: **ONVIF Profile G Services** zur standardisierten Suche und Wiedergabe von SD-Karten-Aufnahmen in NVRs.
  - `server.go`: HTTP Server auf Port `8000` (SOAP Dispatcher mit Triple-Auth: HTTP Digest, HTTP Basic und WS-Security + HTTP Basic Auth geschützte REST-Endpoints `/api/status`, `/api/light`, `/api/sdcard/*`).

- **`pkg/mqtt/`**:
  - Modularisiert in vier fokussierte Komponenten:
    - `client.go`: Verbindungs-Lifecycle, Auto-Reconnect, Last-Will-and-Testament (LWT).
    - `discovery.go`: Home Assistant Auto-Discovery für alle 10 Entitäten (`light`, `select`, `sensor`, `binary_sensor`, `number`, `siren`, `event`).
    - `state.go`: Status- und Telemetrie-Publizierung sowie Aufnahme-Events (`PublishRecordingEvent`).
    - `command.go`: Sicheres Parsen und Dispatching eingehender MQTT-Kommandos.
  - Vollständige Dependency Injection über den injizierten `events.Bus` (keine Singletons).

- **`pkg/events/`**:
  - `events.go`: Thread-sicherer modularer Publish/Subscribe-Event-Bus (`Bus`), instanziiert in `app.App` und per Dependency Injection an Driver, WebRTC, ONVIF und MQTT übergeben (mit Fallback auf `GlobalBus`).

- **`pkg/mcu/`**:
  - `mcu.go`: Parser für 18-Byte (36 Hex-Zeichen) MCU-UART-Frames (`5A0F0F...`) und Command-Builder für Dimmstufen, Nachlaufzeit, Grundlicht, PIR-Sensitivität, Lux-Schwelle und Sirene.

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

## 3. Lead Developer Patterns & Architektur-Prinzipien

Dieses Repository folgt strengen Software-Engineering- und Clean-Code-Standards. Jeder Entwickler und jeder KI-Agent muss diese Prinzipien zwingend einhalten:

### 1. Lesbarkeit & Modularität VOR maximalem Performancegewinn ("Clarity over Cleverness")
- **Grundsatz:** Lesbarer, sauber strukturierter, verständlicher und wartbarer Code hat **ausnahmslos Vorrang** vor esoterischen Micro-Optimierungen, vorzeitiger Optimierung (*Premature Optimization*), Assembler-Tricks oder unübersichtlichen Monster-Schleifen.
- **Tuning-Grenzen:** Performance- und Speicher-Optimierungen sind ausschließlich dann zulässig, wenn sie die architektonische Klarheit und Lesbarkeit nicht beeinträchtigen (z. B. Standard-Idiome wie `sync.Pool`, vorallokierte Socket-Puffer oder `Reader.Reset()`).
- **Verbot von monolithischen Verschmelzungen:** Logisch getrennte Verarbeitungsstufen (wie Audio-Dekodierung, Resampling und Encoding) dürfen **niemals** in eine einzige unlesbare Schleife gezwungen werden. Jeder Schritt muss modular, isoliert verständlich und separat testbar bleiben.

### 2. Strikte Abstraktion über Interfaces
- **Polymorphismus statt Sonderfall-Abfragen:** Subsysteme kommunizieren ausschließlich über klar definierte Interfaces (`driver.CameraDriver`, `storage.RecordingProvider`, `nabto.Driver`, `events.EventBus`), niemals über konkrete Implementierungstypen.
- **Keine Modellspezifika in höheren Schichten:** Modellspezifische Eigenheiten (z. B. Nabto vs. Sofia, L 625 vs. L 620) gehören strikt und ausschließlich in den jeweiligen Treiber (`pkg/driver/l625.go`, `pkg/driver/l620.go`). Der Supervisor, der RTSP-Server, ONVIF oder MQTT dürfen **niemals** modellspezifische Weichen wie `if isL620` oder `currentXMDriver` enthalten.
- **Compile-Time Interface Assertions:** Jede konkrete Implementierung muss ihre Interface-Konformität zwingend zur Compile-Zeit absichern:
  ```go
  var _ CameraDriver = (*L625Driver)(nil)
  var _ storage.RecordingProvider = (*L620Driver)(nil)
  ```

### 3. Saubere Modularisierung & Single Responsibility Principle (SRP)
- Jedes Paket und jede Quelldatei hat genau eine klar umrissene Verantwortung:
  - `signaling.go`: TURN, ICE & SDP-Aushandlung.
  - `media.go`: Medien-Ingest, Silence-Watchdog & PLI-Bursts.
  - `backchannel.go`: 2-Wege-Audio Chunking.
  - `mcu_dispatch.go`: Befehle & Sensortelemetrie.
- **Dateigrößen-Deckel:** Monolithische Quelldateien (> 400–500 Zeilen) sind zu vermeiden bzw. in fokussierte Module zu zerlegen.
- **Etablierte Go-Muster:**
  - **Factory-Pattern** (`driver.New(...)`, `events.NewBus()`) für saubere Instanziierung.
  - **Registry-Pattern** (`nabto.Register`, `nabto.New`) nach Vorbild der Go-Standardbibliothek (`database/sql`).

### 4. Vollständige Dependency Injection (DI) statt globaler Singletons
- **Verbot globaler Zustände:** Globale Singletons (`var GlobalBus`, globale Bridge-Instanzen) sind im Produktivcode verboten.
- **Explizite Übergabe:** Abhängigkeiten (wie der Event-Bus, Logger, Konfiguration) werden im Konstruktor (`New(...)`) explizit übergeben.
- **Isolierte Testbarkeit:** Jede Komponente muss in Unit-Tests isoliert, ohne globale Nebenwirkungen und ohne gegenseitige Beeinflussung instanziierbar und testbar sein.

### 5. Zukunftssicherheit & Leichte Erweiterbarkeit (Open-Closed-Prinzip)
- Die Architektur muss so aufgebaut sein, dass zukünftige Erweiterungen **ohne Refactoring bestehenden Kerncodes** möglich sind:
  - **Neue Kameramodelle** (z. B. Steinel L 605, Cam Light Solar) werden einfach als neuer `pkg/driver/l605.go` Treiber implementiert und registriert.
  - **Neue Audio-/Video-Codecs** (z. B. Opus, AAC-ELD, H.265) werden als modulare Filter/Transcoder in `pkg/audio` ergänzt, ohne die RTSP- oder WebRTC-Engine umzubauen.

---

## 4. Zentrale Sicherheits-, Concurrency- & Release-Regeln

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

## 5. Entwicklungs- & Build-Befehle

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
