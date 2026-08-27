# Changelog

Alle wichtigen Änderungen für das **Steinel CAM Bridge** Add-on werden hier dokumentiert.

## 1.2.0

### 💾 Lokaler MicroSD-Speicherabruf & ONVIF Profile G (#17)
- **ONVIF Profile G Services**:
  - Vollständige Implementierung von Search (`/onvif/search_service`), Replay (`/onvif/replay_service`) und Recording (`/onvif/recording_service`).
  - Unterstützung für NVR-Systeme (z. B. Synology Surveillance Station, QNAP, Milestone) zum Abrufen, Synchronisieren und Abspielen lokaler Aufnahmen (Edge Storage Retrieval).
- **Unified Recording Engine**:
  - Modellübergreifende Abfrage lokaler SD-Karten-Aufnahmen für Steinel L 625 CAM SC (Nabto RPC) und Steinel L 620 CAM / XLED CAM 1 (Sofia `OPFileQuery`).
- **REST API Endpunkte**:
  - `GET /api/sdcard/events`: Liste der erfassten Aufnahmen (JSON).
  - `GET /api/sdcard/events/{id}/thumbnail.jpg`: Streaming des JPEG-Vorschaubilds.
  - `GET /api/sdcard/events/{id}/video.mp4`: Direktes Streaming des MP4-Videoclips mit `Content-Length`.
- **Home Assistant MQTT Event-Entität**:
  - Neue Entität `event.letzte_sd_aufnahme` (`event.steinel_<deviceID>_recording`) mit Event-Typen `["motion", "manual", "alarm", "record", "plan", "all"]`.
  - Initialer Abgleich beim Start zur sofortigen Befüllung der Entität.
  - Ressourcenschonender Hintergrund-Sync mit differenzieller Zeitabfrage (`start_time: lastSeenTime - 10s`) zur Minimierung der Payload.
  - Konfigurierbares Polling-Intervall (`sdcard_sync_interval`, Standard: 30 Sekunden).
  - Sofort-Trigger bei eingehenden Bewegungs- und Alarm-Signalen.

### 🔧 Verbesserungen & Optimierungen
- **Netzwerk-Adressierung**: Automatische Ermittlung der lokalen Bridge-IP für `thumbnail_url` und `video_url` in MQTT-Events.
- **Log-Bereinigung**: Verlagerung der periodischen Polling-Meldungen auf das Debug-Level (`debug: true`).

## 1.1.0

### 🎥 Steinel L 620 CAM & XLED CAM 1 Unterstützung (Initial Support)
- **RTSP Streaming Proxy & Audio-Transcoding**: Die Bridge fungiert nun als lokaler Streaming-Proxy für die **Steinel L 620 CAM** (Generation 1, Xiongmai-Architektur).
- **Signal-Multiplexing**: Das Kamerasignal wird von der Kamera abgegriffen und stabil für mehrere parallele Clients (z. B. Home Assistant, VLC, Frigate, Scrypted) über den integrierten RTSP-Server bereitgestellt.
- **Audio-Transcoding**: Automatische Enkodierung und Aufbereitung der Tonspur in AAC / PCMU.
- **Automatische Modellerkennung (`camera_type: auto`)**: Erkennt beim Start anhand der Netzwerkports selbstständig, ob eine L 620 (Port 34567) oder L 625 angesprochen wird (manuelle Auswahl über `camera_type: "l620"` oder `"l625"` möglich).
- 🧪 **Experimentelle Funktionen für L 620 CAM (Feedback erwünscht)**:
  - **Home Assistant MQTT Entitäten (Experimentell)**: Erste Implementierung zur Steuerung von Licht, Grundlicht, Dämmerungsschwelle, Nachlaufzeit und PIR-Distanz über den internen MCU-Seriellkanal (`OPTrans` RS232).
  - **2-Wege-Audio / Gegensprechen (Experimentell)**: Erste Unterstützung für den Audio-Rückkanal über RTSP / ONVIF Profile T an den Kameralautsprecher via `OPTalk`.
  - **UDP LAN-Discovery (Experimentell)**: Automatisches Auffinden von L 620 Kameras im lokalen Subnetz über UDP-Port 34569.

### 🚀 Allgemeine Verbesserungen & Fixes
- 🌐 **Flexibler RTSP-Server & Player-Kompatibilität**: Der integrierte RTSP-Server akzeptiert nun auch direkte Verbindungen **ohne Pfad** (`rtsp://<IP>:8554` / `rtsp://<IP>:8554/`) sowie Standard-Pfade (`/live`, `/steinel`, `/cam/realmonitor`). Behebt `404 Not Found`-Abbrüche in VLC, Frigate, go2rtc und QuickTime.
- 🛡️ **Sicherheit & Logging**: Zuverlässige Maskierung von Passwörtern und Authentifizierungs-Tokens in allen Log-Ausgaben und RTSP-Pfaden.

## 1.0.2

- 📢 **Fix Home Assistant Sirenen-Steuerung**: Unterstützung von JSON-Payloads (`{"state":"ON"}`, `{"state":"ON","volume_level":0.8,"duration":2}`) auf dem MQTT-Topic `siren/set` für zuverlässiges Auslösen des Alarms und sofortige Zustandsrückmeldung auf `siren/state`.
- 🧹 **Bereinigung Dämmerungsschwelle**: Entfernen des irreführenden statischen Sensors `sensor.lux` (*„Umgebungshelligkeit“*). Vollständiges Mapping der Kamera-Schaltschwelle auf den konfigurierbaren Slider `number.lux_threshold` (*„Dämmerungsschwelle“*, 2 – 1000 lx) mit bidirektionaler Zustandsrückmeldung.
- 🗂️ **Strukturierte Home Assistant Dashboard-Kategorien**: Einstellungsregler (`lux_threshold`, `pir_sensitivity`, `duration`, `lowlight`, `resolution`) in die Kategorie **„Konfiguration“** verschoben; `pir_status` als **„Diagnose“** deklariert.
- 🧹 **Bereinigung Bewegungssensor**: Entfernen der unversorgten Entität `binary_sensor.motion` zur Vermeidung des Status *„Unbekannt“*.
- 🚨 **Echtzeit-Überwachung DataChannel**: Automatische Erkennung und Protokollierung eingehender Alarm- oder Bewegungs-Events über den lokalen WebRTC-DataChannel.

## 1.0.1

- ⚡ **Native CGo Cross-Compilation**: Optimiertes Multi-Arch Docker-Build mit nativem Debian-Cross-Compiler (`aarch64-linux-gnu-gcc`) zur Beschleunigung des GitHub Actions Builds von ~9 Minuten auf unter 1 Minute (ohne langsame QEMU-Emulation).
- 📦 **Kanonischer Go-Modulpfad (`pkg.go.dev`)**: Offizieller GitHub-Modulpfad `github.com/Afrouper/steinel-cam-bridge` zur Aktivierung der automatischen Dokumentations- und Paket-Indexierung auf [pkg.go.dev](https://pkg.go.dev/github.com/Afrouper/steinel-cam-bridge).
- 🐹 **Go 1.27.0 Upgrade**: Aktualisierung der Go-Toolchain, Abhängigkeiten und GitHub Actions Test-Pipelines auf Go 1.27.0.
- 🏷️ **Automatisches Pre-Release Handling**: Differenzierte Release-Automatisierung für Beta- vs. Stable-Versionen.

## 1.0.0

- 🔊 **2-Wege-Audio (Gegensprechen)**: Volle Unterstützung für Gegensprechen aus Apple Home & Scrypted direkt auf den Lautsprecher der Steinel-Kamera via RTSP TCP-Interleaved Backchannel.
- 🎯 **Frontdoor-Audio Binding**: Native WebRTC-Bindung (`sendrecv`) an den Kamera-Transceiver `frontdoor-audio` mit `Status: OK` Signalisierungsbestätigung.
- ⏱️ **20ms Frame-Chunking**: Automatische Zerlegung von Audio-Bursts in exakte 20-ms-Frames (160 Bytes @ 8 kHz PCMU) für den internen DSP-Audio-Puffer der Kamera.
- 🔄 **Dynamisches & kollisionsfreies Pairing**: Eindeutige Registrierung als `steinel-bridge-<id>` mit automatischer `409 Conflict`-Auflösung und Retry-Loop.
- 🛡️ **Selbstheilung bei 401**: Automatische Erkennung und Bereinigung ungültiger Schlüsseldateien bei `401 Unauthorized` mit QR-Code-Prüfung.
- 🧹 **Ruhiges Logging**: Minimales Logging im Normalbetrieb (`debug: false`), detaillierte Diagnose bei `debug: true`.
- 🏠 **Home Assistant Integration**: Vollständiges MQTT Auto-Discovery aller Licht- und Sensor-Entitäten (`number`, `select`, `sensor`, `binary_sensor`, `siren`).
- 🗄️ **SD-Karten REST API**: Schneller 1:1 Zugriff auf Video-Ereignisse, Snapshots und MP4-Downloads mit Hardware-Überlastungsschutz.

## 0.10.3

- 🐛 **Stabilitäts-Fix**: Behebung möglicher Nil-Pointer Panics beim Beenden von RTSP-Sessions und sauberes Schließen von Hintergrund-Goroutinen.

## 0.10.2

- 💡 **Home Assistant MQTT**: Helligkeitssteuerung für Hauptlicht und Grundlicht auf standardisierte `number`-Entitäten optimiert.
- 🧹 **Throttled Logging**: Gedrosseltes Logging bei Verbindungsabbrüchen.

## 0.10.1

- 🔒 **Sicherheits-Fix**: Berechtigungs- und Pfadkorrekturen für das Google Distroless Runtime-Image.
- ⚠️ **Konfigurationsvalidierung**: Pflichtprüfung der Kamera-IP-Adresse beim Start.

## 0.10.0

- 🚀 **Nativer Go-Launcher**: 100 % autarkes Single-Binary mit automatischem Nabto SDK-Download ohne externe Paketabhängigkeiten.
- 📦 **Google Distroless**: Minimales und gehärtetes Container-Image (Größe auf ca. 56 MB reduziert).

## 0.9.9

- 📡 **MQTT Auto-Discovery**: Automatische Erkennung des Home Assistant Mosquitto MQTT-Brokers über die Supervisor API.

## 0.9.2

- 🗄️ **SD-Karten REST API**: Direkte 1:1 REST-API (Port 8000) für Ereignislisten, Snapshots und Video-Downloads mit Hardware-Überlastungsschutz.
