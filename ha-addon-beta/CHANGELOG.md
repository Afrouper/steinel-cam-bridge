# Changelog (Beta)

Alle wichtigen Änderungen für das **Steinel CAM Bridge Beta** Add-on werden hier dokumentiert.

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
