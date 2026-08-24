# Changelog (Beta)

Alle wichtigen Änderungen für das **Steinel CAM Bridge Beta** Add-on werden hier dokumentiert.

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
