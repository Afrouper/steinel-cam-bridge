# Changelog

Alle wichtigen Änderungen für das **Steinel CAM Bridge** Add-on werden hier dokumentiert.

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
