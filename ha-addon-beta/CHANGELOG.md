# Changelog (Beta)

Alle wichtigen Änderungen für das **Steinel CAM Bridge Beta** Add-on werden hier dokumentiert.

## 1.3.0-beta.7

### 🚀 Automatische Freischaltung des MQTT-Dienstes im Supervisor
- **Auto-Enablement via `POST /services/mqtt`**:
  - Sobald der Supervisor meldet, dass der MQTT-Dienst für das Add-on noch nicht freigeschaltet ist (`Service not enabled`), fordert die Bridge die Aktivierung automatisch an.
  - Ermöglicht eine nahtlose, vollautomatische Anbindung an den Mosquitto-Broker ohne manuelle Broker-Konfiguration oder Neustarts.

## 1.3.0-beta.6

### 🔍 Detaillierte Supervisor-API Fehlerdiagnose
- Exakte Ausgabe des Supervisor-Antworttexts im Log, um eventuelle Konfigurations- oder Dienstprobleme in Home Assistant sofort transparent zu machen.

## 1.3.0-beta.5

### 🛠️ Fix Home Assistant Supervisor MQTT Auto-Discovery & Exact 11-char DeviceID
- **Supervisor Header `X-Supervisor-Token`**:
  - Authentifizierung mit `X-Supervisor-Token` & `Authorization: Bearer` für direkte und automatische Mosquitto-Zugangsdaten-Ermittlung via `mqtt:want`.
- **Exakte 11-Zeichen Nabto ID-Extraktion**:
  - `DeviceId` und `ProductId` werden präzise auf 11 Zeichen (`de-m4yfowbr`, `pr-qtatbtbi`) gecrasht, um bestehende Home Assistant Entitäten 1:1 zu synchronisieren.
- **Explizites Logging bei Kamera-Verbindung**:
  - Bei erfolgreichem Connect werden die ermittelten Kamera-IDs direkt im Log ausgegeben.

## 1.3.0-beta.4

### 🛠️ Home Assistant MQTT Auto-Discovery & Status-Updates
- **Verbesserte Supervisor MQTT Auto-Discovery**:
  - Unterstützung für `172.30.32.2` (Standard-Gateway der Supervisor-API bei `host_network: true`), falls DNS-Namen wie `supervisor` im Host-Netzwerk nicht auflösbar sind.
  - Aussagekräftiges Logging beim Start (`[HA Addon] 📡 Auto-discovered Home Assistant MQTT service` oder entsprechende Hinweismeldung).
- **Automatische DeviceID-Erkennung via CoAP**:
  - Der Pure-Go Treiber ermittelt automatisch die reale `DeviceID` (`de-m4yfowbr`) und `ProductID` der Kamera über CoAP `/iam/pairing`, auch wenn die Kamera nur über IP ohne QR-Code konfiguriert wurde.
  - Der MQTT-Client passt Topics (`steinel/<deviceID>/...`) und Discovery-Konfigurationen dynamisch an, sodass bestehende Home Assistant Entitäten sofort als "Verfügbar" erkannt und aktualisiert werden.
- **Zuverlässiges MCU Status-Parsing**:
  - Alle über den DataChannel eingehenden Base64-Statusmeldungen der Kamera (`tran_report`, `tran_ctl`) werden geparst und synchronisieren Sensoren, Schalter und Helligkeitswerte sofort via MQTT und ONVIF.

## 1.3.0-beta.3

### 🚀 100% Pure-Go Nabto Edge Treiber (CGo & libnabto-Abhängigkeit entfernt)
- **Vollständige Eigenimplementierung des Nabto Edge Protokolls**:
  - **DTLS 1.2 Handshake**: Native Pion/DTLS-Anbindung mit ECC P-256 Schlüsseln, Nabto 16-Byte Paketframing (`0xF0`), CCM Cipher-Suites (`TLS_ECDHE_ECDSA_WITH_AES_128_CCM`) und ALPN `"n5"`.
  - **CoAP RFC 7252 Layer**: Pure-Go REST-Abfragen für `/p2p/webrtc-info`, `/iam/pairing` und `/webrtc/tracks`.
  - **Nabto Streaming Protocol**: Virtueller Multiplex-Stream (`AT_STREAM = 0x05`) für WebRTC-Signaling mit dynamischer Segmentgrößen-Aushandlung (256 Bytes).
- **Stabilität & KeepAlive**:
  - Vollständige 18-Byte Nabto KeepAlive-Paketbehandlung (`0x04 0x02` + Nonce-Echo) und periodischer 5s KeepAlive-Heartbeat zur Verhinderung von Verbindungsabbrüchen.
  - Entfernung künstlicher Read-Timeouts für dauerhaft unterbrechungsfreie Video- und Audio-Streams.
- **Bereinigtes Logging**:
  - Detaillierte Nabto/DTLS-Datagramm- und Stream-Traces werden nur noch im Debug-Modus (`debug: true`) ausgegeben.
- **Keine dynamischen C-Bibliotheken (`libnabto_client.so`/`.dylib`) mehr erforderlich**:
  - Geringerer Speicherbedarf, schnellere Verbindungsaufbauzeiten (< 100ms) und höhere Stabilität.
  - Vollständig kompatibel mit bestehenden Schlüsseldateien (`local_client.key`).
  - Standardmäßig aktiv; bei Bedarf kann über `USE_CGO_NABTO=true` auf den alten Treiber zurückgegriffen werden.

## 1.2.1

### 🧹 Logging-Bereinigung
- **Mute für MQTT Recording JSON Payloads**: Die ausführliche Protokollierung des vollständigen JSON-Payloads bei neuen Aufnahmen wird nur noch im Debug-Modus (`debug: true`) ausgegeben. Im Normalbetrieb genügt eine einzige kompakte Statusmeldung.

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

## 1.2.0-beta.4

### ⚡ Polling- & Log-Optimierung
- **Gezielte Polling-Abfragen (`startTime: lastSeenTime - 10s`)**: Nach dem initialen Sync fragt die Bridge beim 20s-Polling nur noch die Aufnahmen ab, die seit dem letzten erfassten Event hinzugekommen sind. Dadurch werden statt 1800+ Events pro Abfrage nur noch die tatsächlichen neuen Einträge (0 bis 1 Element) über WebRTC übertragen.
- **Sauberes Logging (Debug Level)**: Polling-Anfragen (`[SDCard] 🔍 Requesting...`, `[DataChannel] 📤 Sending...`) werden nur noch im Debug-Modus geloggt. Im normalen Betrieb werden ausschließlich neu gefundene Aufnahmen und MQTT-Publish-Events protokolliert.
- **Lokale Bridge-IP Ermittlung**: `thumbnail_url` und `video_url` verweisen nun automatisch auf die tatsächliche IP der Bridge im LAN statt auf die Kamera-IP.

## 1.2.0-beta.3

### 🔧 Fix Initialer SD-Aufnahme-Sync & Event-Typen
- **Epoch-0 Lookback**: Abfrage der neuesten Aufnahmen ohne 24h-Zeitbeschränkung, damit auch ältere Aufnahmen oder bei Zeitzonenunterschieden zuverlässig gefunden werden.
- **Erweiterte Event-Typen**: Unterstützung für `["motion", "manual", "alarm", "record", "plan", "all"]` bei der Home Assistant `event`-Entität.
- **Detailliertes Sync-Logging**: Protokollierung von gefundenen und publizierten SD-Karten-Aufnahmen im Add-on-Log.

## 1.2.0-beta.2

### 🔄 Automatischer MicroSD Recording Sync & Sofort-Trigger
- **Initialer Sync beim Start**: Lädt beim Start der Bridge sofort die neueste existierende Aufnahme von der MicroSD-Karte und veröffentlicht sie per MQTT nach Home Assistant (behebt den Status *„Unbekannt“* bei der Entität `event.letzte_sd_aufnahme`).
- **Hintergrund-Sync (20s Intervall)**: Erkennt neu auf die SD-Karte geschriebene Aufnahmen automatisch und pusht sie zuverlässig innerhalb weniger Sekunden nach Home Assistant.
- **Sofort-Trigger bei Bewegung/Alarm**: Verknüpfung mit Bewegungsmeldungen (DataChannel / MCU / ONVIF) für unmittelbare Abfrage nach Abschluss des Schreibvorgangs.

## 1.2.0-beta.1

### 💾 Lokaler MicroSD-Speicherabruf & Wiedergabe (#17)
- **Unified Storage Engine**: Einheitliche Schnittstelle zum Abruf von Event-Listen, Metadaten, Snapshots und Videos von der lokalen MicroSD-Karte.
- **REST API Endpunkte**:
  - `GET /api/sdcard/events`: Liefert Liste aller erfassten Bewegungs- und Alarmaufnahmen als JSON.
  - `GET /api/sdcard/events/{id}`: Metadaten einer einzelnen Aufnahme.
  - `GET /api/sdcard/events/{id}/thumbnail.jpg`: Direktes Streaming des JPEG-Vorschaubilds.
  - `GET /api/sdcard/events/{id}/video.mp4`: Direktes Streaming des MP4-Videos mit `Content-Length`.
- **ONVIF Profile G Services**:
  - Vollständige Implementierung von Search (`/onvif/search_service`), Replay (`/onvif/replay_service`) und Recording (`/onvif/recording_service`).
  - Unterstützung für NVR-Systeme (Synology Surveillance Station, QNAP, Milestone, Frigate).
- **Home Assistant MQTT Event-Entität**:
  - Auto-Discovery der Entität `event.steinel_<deviceID>_recording` mit direkten Download- und Thumbnail-Links bei jeder neuen Aufnahme.
- **Kamera-Treiber Unterstützung**:
  - Steinel L 625 CAM SC (Nabto WebRTC Engine)
  - Steinel L 620 CAM / XLED CAM 1 (Xiongmai Sofia Engine via `OPFileQuery`)

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

## 1.1.0-beta.11

- 🧹 **Bereinigung Dämmerungsschwelle (L 625)**: Entfernen des irreführenden statischen Sensors `sensor.lux` (*„Umgebungshelligkeit“*). Vollständiges Mapping der Kamera-Schaltschwelle auf den konfigurierbaren Slider `number.lux_threshold` (*„Dämmerungsschwelle“*, 2 - 1000 lx) mit bidirektionaler Zustandsrückmeldung.
- 🗂️ **Saubere Home Assistant Dashboard-Struktur**: Einstellungsregler (`lux_threshold`, `pir_sensitivity`, `duration`, `lowlight`, `resolution`) in die Kategorie **„Konfiguration“** verschoben; `pir_status` als **„Diagnose“** deklariert.
- 🧹 **Bereinigung Bewegungssensor**: Entfernen der unversorgten Entität `binary_sensor.motion` zur Vermeidung des Status *„Unbekannt“*.
- 🚨 **Echtzeit-Überwachung DataChannel**: Protokollierung eingehender Alarm- oder Bewegungs-Events für zukünftige Firmware-Erweiterungen.

## 1.1.0-beta.10

- 📢 **Fix Home Assistant Sirenen-Steuerung (L 625)**: Unterstützung von JSON-Payloads (`{"state":"ON"}`, `{"state":"ON","volume_level":0.8,"duration":2}`) auf dem MQTT-Topic `siren/set`. Behebt das Problem, dass Home Assistant Sirenen-Befehle fälschlicherweise als `play: false` interpretierte.
- 🔄 **MQTT Sirenen-Zustandsrückmeldung**: Sofortige Veröffentlichung des aktuellen Sirenen-Status (`ON`/`OFF`) auf `siren/state`.

## 1.1.0-beta.9

- 🛑 **MQTT Auto-Discovery Pause für L 620**: Temporäres Ausblenden von nicht funktionalen MQTT-Licht- und Sensor-Entitäten in Home Assistant für die Steinel L 620 CAM, bis das Steuerprotokoll finalisiert ist.
- 🎯 **Fokus auf stabilen RTSP & ONVIF Stream**: Bereitstellung von Video und Ton über RTSP Port 8554 und ONVIF Port 8000.

## 1.1.0-beta.8

- 🎥 **Natives Steinel / MotionEye RTSP-Pfad-Format**: Umstellung des RTSP-Stream-Clients auf das von der Steinel L 620 CAM erwartete Pfadformat (`/user=admin_password=<pwd>_channel=1_stream=0.sdp?real_stream`). Behebt `no authentication methods available` Fehler durch direkte Pfad-Authentifizierung.
- 🔄 **Multi-Pfad Ingest Fallback**: Automatisches Durchprobieren von HD (`stream=0`), SD (`stream=1`) und alternativen RTSP-URIs.
- 🛡️ **Erweiterte RTSP-URL-Maskierung**: Maskiert Passwörter auch in query-basierten RTSP-Pfaden zuverlässig in den Logs.

## 1.1.0-beta.7

- 🛡️ **Resilienter RTSP-Fallback-Modus**: Verhindert Startabbrüche bei Sofia-Auth-Mismatches (`Ret: 124`). Startet direkt den nativen 1080p RTSP-Stream auf Port 554 und übernimmt die vergebene SessionID für Licht- und MCU-Steuerungsversuche.
- ⚡ **Bereinigte Authentifizierungsmatrix**: Reduktion auf die 4 Kern-Authentifizierungsmodi zur Minimierung von Verbindungsverzögerungen.
- 🔒 **Sicheres Debug-Logging**: Maskierte Ausgabe des konfigurierten Passworts mit Zeichenanzahl (`Lin************* (16 chars)`).

## 1.1.0-beta.6

- 🔐 **Multi-Client & Multi-LoginType Authentifizierung**: Automatisches Testen aller LoginType-Varianten (`DVRIP-Web`, `Mobile`, `MobileDVR`, omitted) und alternativer Standard-Nutzer (`admin`, `default`, `root`).
- 🧹 **Automatische Input-Bereinigung**: Automatisches Entfernen von versehentlichen führenden/nachgestellten Leerzeichen oder Umbrüchen in Benutzername und Passwort.
- 📊 **Nummeriertes Kandidaten-Logging**: Detaillierte Protokollierung jedes einzelnen Authentifizierungsversuchs zur schnellen Analyse.

## 1.1.0-beta.5

- 🔑 **Fix DVRIP Login-Paketstruktur**: Umstellung der Login-Anfrage (`MsgID 1000`) auf die native flache DVRIP-Struktur (`EncryptType`, `PassWord`, `LoginType`, `UserName`). Behebt `Ret: 124` Fehler durch Vermeidung von verschachtelten JSON-Wrappern.
- 📡 **Vollständiger Audit aller DVRIP Message-IDs**: Korrektur der Nachrichten-IDs für Konfigurationsbefehle (`MsgConfigSetReq = 1040`, `MsgConfigGetReq = 1042`) und Heartbeat (`MsgKeepAliveReq = 1006`).
- 🛡️ **Graceful Logout & Session-Freigabe**: Senden von `OPUserLogout` (`MsgID 1002`) beim Stoppen des Treibers zur sofortigen Freigabe der Verbindung auf der Kamera.
- 🔍 **Erweitertes Debug-Logging**: Ausgabe der Rohantwort der Kamera im Debug-Modus zur transparenten Diagnose.

## 1.1.0-beta.4

- 🔄 **RTSP-Passwort-Synchronisation**: Automatische Übergabe des beim Sofia-Login bestätigten wirksamen Gerätepassworts an den internen RTSP-Stream-Client auf Port 554.
- 📋 **Detailliertes Auth-Attempt-Logging**: Transparente Protokollierung jedes einzelnen Authentifizierungsversuchs im Add-on-Log zur schnellen Fehlerdiagnose.
- 🌐 **Präzisierte UI-Beschreibungen (DE/EN)**: Klarstellung in den Home Assistant Einstellungsdialogen, dass das lokale Gerätepasswort (ab Werk leer) und nicht das App-Cloud-Passwort gemeint ist.

## 1.1.0-beta.3

- 🔑 **Multi-Varianten Authentifizierungs-Fallback**: Automatisches Durchprobieren aller Authentifizierungsformate bei Return-Code `124`. Behebt Login-Probleme, wenn das lokale Kamera-Passwort ab Werk leer ist (`""`), der Nutzer ein App-Cloud-Passwort eingetragen hat oder die Firmware Plaintext/Hex-MD5/Sofia-Hash erwartet.
- 📝 **Eindeutiges Authentifizierungs-Logging**: Die Bridge loggt transparent im Startlog, welche Authentifizierungsmethode erfolgreich war (`Sofia-Hash`, `empty password`, `Hex-MD5` oder `Plaintext`).

## 1.1.0-beta.2

- 🔐 **Fix Xiongmai Sofia Passwort-Hashing (`Ret: 124`)**: Implementierung des proprietären 8-Zeichen Sofia-Hash-Algorithmus (MD5-Bytepaar-Transformation) zur erfolgreichen Authentifizierung an der Steinel L 620 CAM.
- 💬 **Präzise Fehlerbeschreibungen**: Menschenlesbare Fehlermeldungen bei Login-Problemen (z. B. falsches Passwort, gesperrter Account).

## 1.1.0-beta.1

- 📷 **Steinel L 620 CAM / XLED CAM 1 Unterstützung**: Vollständige lokale Integration der älteren Kameragenerationen (Xiongmai Sofia TCP-Protokoll auf Port 34567).
- ⚡ **Zero-Touch RTSP-Aktivierung**: Automatisches Aktivieren des internen RTSP-Servers auf Port 554 der L 620 CAM beim Start der Bridge.
- 🎙️ **2-Wege-Audio (Gegensprechen)**: Weiterleitung des ONVIF / RTSP Profile T Audio Backchannels aus Apple Home, Scrypted und Home Assistant direkt an den Außenlautsprecher der L 620 CAM via `OPTalk`.
- 💡 **MCU-Steuerung & MQTT Auto-Discovery**: Volle Unterstützung aller Leuchten- und Sensor-Entitäten (Hauptlicht Ein/Aus, Dimmung 10–100%, Grundlicht/Nachtlicht 0–50%, Nachlaufzeit, Dämmerungsschwelle, PIR-Erfassungsreichweite).
- 🛡️ **Sicheres Credential-Masking**: Sichere Maskierung von Passwörtern in RTSP-Stream-URLs in Log-Ausgaben via standardkonformem `net/url.Redacted`.

## 1.0.1-beta.2

- 📦 **Kanonischer Go-Modulpfad (`pkg.go.dev`)**: Umstellung des internen Modulpfads von `steinel-cam-bridge` auf den offiziellen GitHub-Modulpfad `github.com/Afrouper/steinel-cam-bridge` zur Aktivierung der automatischen Dokumentations- und Paket-Indexierung auf [pkg.go.dev](https://pkg.go.dev/github.com/Afrouper/steinel-cam-bridge).

## 1.0.1-beta.1

- ⚡ **Native CGo Cross-Compilation**: Optimiertes Multi-Arch Docker-Build mit nativem Debian-Cross-Compiler (`aarch64-linux-gnu-gcc`) zur Beschleunigung des GitHub Actions Builds von ~9 Minuten auf unter 1 Minute (ohne langsame QEMU-Emulation).

## 1.0.0

- 🔊 **2-Wege-Audio (Gegensprechen)**: Volle Unterstützung für Gegensprechen aus Apple Home & Scrypted direkt auf den Lautsprecher der Steinel-Kamera via RTSP TCP-Interleaved Backchannel.
- 🎯 **Frontdoor-Audio Binding**: Native WebRTC-Bindung (`sendrecv`) an den Kamera-Transceiver `frontdoor-audio` mit `Status: OK` Signalisierungsbestätigung.
- ⏱️ **20ms Frame-Chunking**: Automatische Zerlegung von Audio-Bursts in exakte 20-ms-Frames (160 Bytes @ 8 kHz PCMU) für den internen DSP-Audio-Puffer der Kamera.
- 🔄 **Dynamisches & kollisionsfreies Pairing**: Eindeutige Registrierung als `steinel-bridge-<id>` mit automatischer `409 Conflict`-Auflösung und Retry-Loop.
- 🛡️ **Selbstheilung bei 401**: Automatische Erkennung und Bereinigung ungültiger Schlüsseldateien bei `401 Unauthorized` mit QR-Code-Prüfung.
- 🧹 **Ruhiges Logging**: Minimales Logging im Normalbetrieb (`debug: false`), detaillierte Diagnose bei `debug: true`.

## 0.10.4-beta.3

- 🔊 **2-Wege-Audio (Gegensprechen)**: Volle Unterstützung für Gegensprechen aus Apple Home / Scrypted auf den Lautsprecher der Steinel-Kamera via RTSP TCP Backchannel.
- 🎯 **Frontdoor-Audio Binding**: Lokaler Audio-Send-Track bindet direkt an den Kamera-Transceiver `frontdoor-audio` (`sendrecv`).
- ⏱️ **20ms Frame-Chunking**: Automatische Zerlegung großer Audio-Bursts in standardkonforme 20-ms-Pakete (160 Bytes @ 8 kHz PCMU) für den DSP der Kamera.
- 🔄 **Dynamisches Pairing**: Eindeutige Registrierung als `steinel-bridge-beta-<id>` mit automatischer `409 Conflict`-Auflösung.
- 🛡️ **Selbstheilung bei 401**: Automatische Erkennung und Bereinigung ungültiger Schlüssel bei `401 Unauthorized` mit QR-Code-Prüfung.
- 🧹 **Bereinigtes Logging**: Ruhiges Logging im Standardbetrieb (`debug: false`), detaillierte Diagnose bei `debug: true`.

## 0.10.4-beta.2

- 🔄 **Dynamische Client-Namen**: Wechsel von statischem `steinel-client` zu eindeutigen Präfixen (`steinel-bridge-beta-<id>`).
- 🏷️ **Explizite Beta-Erkennung**: Übergabe von `IS_BETA: "true"` über die Add-on-Umgebung.

## 0.10.4-beta.1

- 🎙️ **RTSP Listener-Interceptor**: Neuer transparenter TCP-Interleaved Audio-Interceptor ohne Upstream-Patches.

## 0.10.3

- 🐛 **Stabilitäts-Fix**: Behebung möglicher Nil-Pointer Panics beim Beenden von RTSP-Sessions.

## 0.10.2

- 🔊 **Gegensprechen TCP-Pipeline**: Erste Version des TCP-Interleaved Backchannels.
- 💡 **MQTT Entitäten**: Umstellung der Helligkeitssteuerung auf `number`-Entitäten.

## 0.10.1

- 🔒 **Sicherheits-Fix**: Berechtigungs- und Pfadkorrekturen für das Google Distroless Runtime-Image.

## 0.10.0

- 🚀 **Nativer Go-Launcher**: Autarkes Single-Binary mit automatischem SDK-Download ohne externe Abhängigkeiten.
