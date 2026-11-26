# Native HomeKit & HomeKit Secure Video (HKSV) — Spezifikation, Validierung & Implementierungsplan

Dieses Dokument definiert die validierten technischen Vorgaben von Apple für **HomeKit Secure Video (HKSV)** (sowohl den produktiven **HDS-Bestandsstandard** als auch den **2026 verabschiedeten Open-Source-Standard**) sowie die architekturkonforme Implementierungs-Roadmap für die **Steinel CAM Bridge**.

---

## 1. Kontext & Zielsetzung

* **Ausgangslage:** Die Steinel CAM Bridge übersetzt proprietäre Protokolle von Steinel-Außenleuchten (**L 625 CAM SC** via Nabto/WebRTC sowie **L 620 CAM / XLED** via Xiongmai Sofia) lokal in RTSP, ONVIF und MQTT. Für Apple HomeKit / HKSV war bislang ein externer Node.js-Proxy (**Scrypted**, ca. 300–600 MB RAM-Bedarf auf Raspberry Pi 5) erforderlich.
* **Ziel:** Native Integration von Apple HomeKit (`pkg/homekit`) direkt in den Go-Daemon – inkl. Sensoren/Aktoren (Licht, Dimmer, Dämmerung, PIR, Sirene), Live-Streaming, Gegensprechen und **HKSV Cloud-Recording** für iOS 27 / HomeOS 27 / tvOS 27.
* **Entwicklungs- & Testprinzip:** Isolierte Protokoll-Verifikation zunächst über ein eigenständiges MacBook-Testlabor (`tools/hksv-lab`) mit normgerechten 30-Sekunden-Test-MP4s im Verzeichnis `hksv/`, bevor die Produktiv-Anbindung an `pkg/app` und `pkg/rtsp` erfolgt.

---

## 2. Validierung der Apple-HKSV-Vorgaben (Beide Standards)

Im HomeKit-Ökosystem koexistieren seit Mitte/Ende 2026 zwei Spezifikationsstände. Eine zukunftssichere Architektur muss die Unterschiede exakt kennen:

### 2.1 Standard A: Bestehender Produktions-Standard (HAP + SRTP + HDS)

Dieser Standard bildet die produktive Basis für alle im Feld befindlichen Home Hubs (Apple TV / HomePod inkl. Software 27) und iOS 27 ohne gesonderte Developer-Provisionierung.

| Schicht | Vorgabe / Protokoll | Technische Details |
| :--- | :--- | :--- |
| **Accessory & Pairing** | HAP over IP (`_hap._tcp`, mDNS) | Basis via `github.com/brutella/hap`, TLV8-Konfiguration |
| **Pflicht-Services (HKSV)** | `CameraOperatingMode`<br>`CameraRecordingManagement`<br>`DataStreamTransportManagement`<br>`MotionSensor` | UUIDs:<br>`0000021A-0000-1000-8000-0026BB765291`<br>`00000204-0000-1000-8000-0026BB765291`<br>`00000129-0000-1000-8000-0026BB765291`<br>`00000085-0000-1000-8000-0026BB765291` |
| **Live-Streaming** | `CameraRTPStreamManagement` (`00000110-...`) via SRTP | Crypto: `AES_CM_128_HMAC_SHA1_80` (via `github.com/pion/srtp/v3`)<br>Video: **H.264** (Baseline/Main/High, Packetization Mode 1)<br>Audio: **AAC-ELD** (16/24 kHz) oder **Opus** (16/24 kHz) |
| **2-Wege-Audio (Backchannel)** | Bidirektionales SRTP Audio | iOS sendet **AAC-ELD** oder **Opus**; Kameras erwarten **G.711 µ-law (PCMU, 8 kHz)** |
| **HKSV Transport (HDS)** | HomeKit Data Stream (TCP) | Eigener TCP-Server, Schlüsselaustausch über `SetupDataStreamTransport`, Verschlüsselung mit **ChaCha20-Poly1305** (`golang.org/x/crypto/chacha20poly1305`), Multiplexer-Protokoll (`hello`, `camera.recording.management` / `startRecord`, `dataSend`, `close`) |
| **HKSV Container & Puffer** | Fragmented MP4 (fMP4 / ISO-BMFF) | `ftyp` + `moov` Init-Segment, gefolgt von `moof` + `mdat` Fragmenten (~4000 ms pro Fragment, max. 262.144 Bytes je HDS-Chunk), mindestens **4 s In-Memory Pre-Buffer** vor Motion-Trigger |
| **HKSV Codecs im fMP4** | H.264 + AAC | Video: **H.264** (geschlossene GOP, Keyframes alle 2 s oder 4 s)<br>Audio: **AAC-LC** (16 / 24 / 48 kHz Mono) oder AAC-ELD |

> **Validierungs-Erkenntnis zu Standard A:**
> 1. `github.com/brutella/hap` stellt nur Standard-HAP und `CameraRTPStreamManagement` bereit. **HDS**, **`DataStreamTransportManagement`** und **`CameraRecordingManagement`** sind in Go nicht vorhanden und müssen aus der TypeScript-Referenz (`HAP-NodeJS`) sauber nach Go portiert werden.
> 2. Für **fMP4-Muxing** existiert mit `github.com/bluenviron/mediacommon/pkg/formats/fmp4` bereits ein getestetes Paket in unserer `go.mod`.
> 3. **Audio-Transcoding-Korrektur:** Die oft angenommene *„Zero-Transcoding“*-Aussage gilt **nur für Video** (H.264 Passthrough) und für **HKSV-fMP4-Audio** (unser interner `AAC-LC 16 kHz`-Stream passt). Für **Live-SRTP** und den **Backchannel** wird iOS-seitig **AAC-ELD** bzw. **Opus** verwendet – hier ist ein Codec-Adapter zu/von `PCMU 8 kHz` erforderlich.

---

### 2.2 Standard B: Neuer Apple-Standard 2026 (*HomeKit Secure Video Open Source Compatibility Guide*)

Veröffentlicht im Juni 2026 als Developer Preview / neue Plattform-Architektur in iOS 27 & HomeOS 27:

| Schicht | Vorgabe / Protokoll | Technische Details & Auswirkung auf Steinel |
| :--- | :--- | :--- |
| **Live-Streaming** | `camera-webrtc-stream-management` (UUID `8033`) | Ersetzt legacy SRTP durch **WebRTC** (SDP Offer/Answer & ICE-Kandidaten über HAP-Characteristics, optional SFrame E2EE). Unsere Bridge nutzt bereits `github.com/pion/webrtc/v4`. |
| **Pflicht-Video-Codecs** | **HEVC (H.265)** (`Enum 2`) & **H.264** (`Enum 1`) | HEVC ist neben H.264 vollwertig spezifiziert (`hvc1`). Passt perspektivisch ideal zu **L 620 / XLED** H.265-Streams. |
| **Pflicht-Audio-Codec** | **Opus** (48 kHz) | Ersetzt proprietäres AAC-ELD durch lizenzfreies Opus – deutlich besser in Go/CGo oder Pure-Go abbildbar. |
| **Multi-Tier Streaming** | Mind. 3 parallele Streams (High / Medium / Low) | **Kritische Hardware-Einschränkung:** Steinel-Kameras liefern nur *einen* Live-Stream. Multi-Tier erfordert hostseitiges Video-Downscaling (CPU/GPU-Last) oder Dual-Stream-Synthese. |
| **HKSV Recording** | **CMAF Ingest** über HTTPS/TLS | Upload von fMP4/CMAF-Segmenten an eine `camera-recording-publishing-point`-URL mit Client-Zertifikat-Provisionierung (CSR-Flow). |

> **Strategische Entscheidung für die Steinel CAM Bridge:**
> * **Phase 1–4 (Produktiv-Fokus):** Umsetzung auf Basis von **Standard A (HDS + fMP4 + H.264/AAC)**, da dies auf jedem iOS 27 / Apple TV 27 / HomePod 27 ohne Apple-Developer-mTLS-Provisioning und ohne CPU-intensives Multi-Tier-Re-Encoding sofort funktioniert.
> * **Zukunftssicherheit (Standard B):** Alle Medien-Schnittstellen in `pkg/homekit` und die Test-Assets in `hksv/` werden von Beginn an codec-agnostisch ausgelegt, sodass **HEVC + Opus** sowie spätere WebRTC/CMAF-Adapter ohne Umbau des Kerncodes ergänzt werden können.

---

## 3. Validierung gegen die bestehende Codebase (`AGENTS.md`)

Bei der Prüfung des ursprünglichen Konzept-Entwurfs gegen den aktuellen Codebestand (`main` / `v1.6.0-beta.4`) wurden folgende Architektur-Korrekturen verbindlich festgelegt:

1. **Kein neues `pkg/eventbus`:**  
   Das Paket [`pkg/events/events.go`](pkg/events/events.go) existiert bereits, ist thread-sicher und unterstützt `EventMotion` (inkl. `SetMotionWithSource`), `EventLight`, `EventLux` und `EventDevice`. `pkg/homekit` erhält den zuständigen `*events.Bus` per Dependency Injection.
2. **`pkg/onvif/events.go` bleibt unberührt:**  
   WS-BaseNotification PullPoint ist in [`pkg/onvif/events.go`](pkg/onvif/events.go) bereits vollständig implementiert und am Event-Bus angebunden.
3. **Treiber-Polymorphismus (L 625 *und* L 620/XLED):**  
   `pkg/homekit` darf keine Nabto- oder Xiongmai-Spezifika kennen (z. B. *„für Nabto“*). Befehle (Licht, Sirene) sowie 2-Wege-Audio (`WriteAudioBackchannel(pkt *rtp.Packet)`) laufen ausschließlich über [`driver.CameraDriver`](pkg/driver/driver.go) bzw. den jeweiligen `BridgeManager` aus [`pkg/app/app.go`](pkg/app/app.go).
4. **Multi-Kamera-Unterstützung:**  
   `pkg/app/app.go` instanziiert pro Kamera eine isolierte `cameraInstance` mit eigenem `camBus`, `camBridgeMgr` und `camStream` (`*rtsp.Stream`). Jede Kamera erhält im HomeKit-Subsystem eine eigene, deterministisch abgeleitete MAC/Seriennummer und wird als eigenständiges HAP-Accessory (oder über eine gemeinsame HAP-Bridge) bereitgestellt.
5. **Keyframes & Kamera-Hardware-Schonung:**  
   * **Keine zweite Kamera-Session:** `pkg/homekit` zapft ausschließlich den internen Medienfluss der Bridge (`*rtsp.Stream` bzw. einen neuen Tap/Sink daran) an – niemals eine zweite Verbindung zur Kamera.
   * **Keyframe-Verhalten:** Bei Bewegungsbeginn oder Stream-Start nutzt `pkg/homekit` den bestehenden `OnPlay`-/Keyframe-Mechanismus (`RequestKeyframe()`). Der Ringpuffer schneidet fMP4-Fragmente exakt an realen IDR-Grenzen (anstatt starrer 4,000-s-Timer), damit auch Kameras mit unregelmäßigen GOPs gültige Fragmente liefern.
6. **Clean-Code & Dateigrößen-Deckel (`AGENTS.md`):**  
   Alle Quelldateien unter `pkg/homekit/` bleiben strikt modular (SRP) und unter ~400–500 Zeilen (kein monolithischer 2000-Zeilen-Block).

---

## 4. Test-Assets im Verzeichnis `hksv/`

Um beide Apple-Standards sauber und ohne gegenseitige Demuxer-/Container-Inkompatibilitäten (CMAF schreibt eine Medien-Repräsentation pro Track/Stream vor) testen zu können, liegen in [`hksv/`](hksv/) zwei normgerechte 30-Sekunden-Testdateien mit exakt synchronen **2,000-s-Closed-GOP-Keyframes** (`0.0s, 2.0s, ..., 28.0s`):

| Datei | Ziel-Standard | Video-Spur | Audio-Spur | Besonderheiten |
| :--- | :--- | :--- | :--- | :--- |
| [`hksv/test-legacy-h264-aac.mp4`](hksv/test-legacy-h264-aac.mp4) | **Standard A (HDS / Legacy SRTP)** | H.264 High@L4.0 (`avc1`), `yuv420p`, 1920×1080 @ 25 fps, 1500 kb/s, Closed-GOP `keyint=50`, `bframes=0` | AAC-LC (`mp4a`), 16 kHz Mono, 64 kb/s | Entspricht exakt den internen Parametern des `pkg/rtsp`-Streams; `+faststart` (`moov` am Dateianfang). |
| [`hksv/test-2026-hevc-opus.mp4`](hksv/test-2026-hevc-opus.mp4) | **Standard B (Apple 2026 Guide)** | HEVC Main@L4.0 (`hvc1`), `yuv420p`, 1920×1080 @ 30 fps, 1200 kb/s, Closed-GOP `keyint=60`, `open-gop=0`, `bframes=0`, `repeat-headers=1` | Opus (`Opus`), 48 kHz Mono, 48 kb/s | Apple-konformes `hvc1`-Tagging & In-Band Parameter-Sets für CMAF/WebRTC-Tests. |

---

## 5. Geplante Modul-Struktur

### 5.1 Isoliertes Test-Labor (`tools/hksv-lab/`)
Für schnelle MacBook-Testläufe gegen physische iOS 27 / tvOS 27 / HomePod 27 Geräte ohne Abhängigkeit von der Steinel-Kamera:
```text
tools/hksv-lab/
├── main.go         # CLI-Test-Harness (PIN, HTTP/Terminal-Trigger [Space] für Motion)
└── mp4_source.go   # Endlos-Demuxer für hksv/test-*.mp4 (H.264/AAC & HEVC/Opus)
```

### 5.2 Produktiv-Paket (`pkg/homekit/`)
```text
pkg/homekit/
├── server.go       # HAP-Server Lifecycle, mDNS, FsStore (/data/homekit), Multi-Cam Bridge
├── accessory.go    # Camera, MotionSensor, Lightbulb (Dimmer), LightSensor (Lux), Switch (Sirene)
├── services.go     # Fehlende HKSV HAP-Service/Characteristic-Definitionen (0x129, 0x204, 0x21A)
├── tlv8.go         # TLV8 Encoder/Decoder für RTP- & Recording-Konfigurationen
├── hds.go          # HomeKit Data Stream (TCP-Server, ChaCha20-Poly1305 Framing, Multiplexer)
├── fmp4_buffer.go  # In-Memory GOP-Ringbuffer (4–8 s Pre-Roll) & mediacommon fMP4 Fragmenter
├── recording.go    # HKSV Recording Controller (Motion -> startRecord -> dataSend -> close)
├── srtp_stream.go  # Live-View SRTP Forwarder (pion/srtp/v3) & Snapshot-Handler
└── backchannel.go  # 2-Wege-Audio Adapter -> driver.CameraDriver.WriteAudioBackchannel
```

*(Erweiterungen im Audio-Bereich für Live-Audio & Backchannel werden modular in [`pkg/audio/`](pkg/audio/) als eigene Filter gekapselt.)*

---

## 6. Implementierungsplan (Phasen & Meilensteine)

### Phase 0: Projekt-Setup & Test-Assets *(Abgeschlossen)*
* [x] Feature-Branch `feature/native-hksv-support` angelegt.
* [x] Spezifikation & Architekturvalidierung in `HKSV.md` dokumentiert.
* [x] Normgerechte 30s-Test-MP4s für Standard A (`H.264 + AAC`) und Standard B (`HEVC + Opus`) unter `hksv/` erzeugt und mit `ffprobe` verifiziert.

### Phase 1: MacBook Test-Labor (`tools/hksv-lab`) & HAP-Basis
* [ ] `github.com/brutella/hap` evaluieren, einbinden und `THIRD_PARTY_LICENSES.md` prüfen.
* [ ] Custom HAP Services (`CameraOperatingMode`, `CameraRecordingManagement`, `DataStreamTransportManagement`) + TLV8-Structs in `pkg/homekit` definieren.
* [ ] `tools/hksv-lab` mit `hksv/test-legacy-h264-aac.mp4` starten, über iOS 27 Home App koppeln und die ersten `SelectedCameraRecordingConfiguration`- / `SetupDataStreamTransport`-Requests vom Home Hub im Log verifizieren.

### Phase 2: HDS Transport-Layer (ChaCha20-Poly1305)
* [ ] HDS TCP-Server und Schlüsselableitung aus dem aktiven HAP-Controller-Session-Key portieren.
* [ ] HDS-Framing (4-Byte-Header + AEAD Tag) und HDS-Message-Multiplexer (`hello` Handshake) implementieren.
* [ ] **Meilenstein-Test:** Apple TV 27 / HomePod 27 baut eine stabile HDS-TCP-Session zum MacBook auf und schließt `hello` erfolgreich ab.

### Phase 3: fMP4-Ringpuffer & HKSV Cloud-Recording
* [ ] MP4-Reader für `hksv/*.mp4` an einen In-Memory-GOP-Ringpuffer (über `mediacommon/pkg/formats/fmp4`) anbinden.
* [ ] HKSV State-Machine (`startRecord`, `dataSend` mit 256-KB-Chunking, `close`) umsetzen.
* [ ] **Meilenstein-Test:** Tastendruck im Terminal (`Space`) löst `MotionSensor = true` aus $\rightarrow$ iOS 27 zeigt Push-Mitteilung und speichert den Test-Clip in der iCloud-Timeline.

### Phase 4: Live-Streaming (SRTP) & Audio-Evaluierung
* [ ] `CameraRTPStreamManagement` (`SetupEndpoints`, `SelectedRTPStreamConfiguration`) und SRTP-Forwarder (`pion/srtp/v3`) umsetzen.
* [ ] Audio-Kompatibilität unter iOS 27 verifizieren (Opus vs. AAC-ELD für Live-Audio und Backchannel; Evaluierung eines schlanken Codec-Adapters in `pkg/audio`).
* [ ] Optionaler Spike: Test von `camera-webrtc-stream-management` (UUID `8033`) mit `hksv/test-2026-hevc-opus.mp4` gegen iOS 27.

### Phase 5: Integration in `steinel-cam-bridge`
* [ ] Saubere Tap-Schnittstelle an `rtsp.Stream` (Video-NALs & AAC-Frames ohne Neu-Enkodierung abgreifen).
* [ ] Konfiguration in [`pkg/config`](pkg/config/config.go) und Home Assistant Add-on Manifesten (`HOMEKIT_ENABLED`, `HOMEKIT_PIN`, `HOMEKIT_PORT`, Multi-Cam).
* [ ] Anbindung in [`pkg/app/app.go`](pkg/app/app.go): Pro Kamera Kopplung an `camBus` (`EventMotion`, `EventLux`, `EventDevice`), `camBridgeMgr` (Licht, Dimmer, Sirene, Backchannel) und `camStream`.

### Phase 6: Härtung, Tests & Dokumentation
* [ ] Unit-Tests für TLV8, HDS-Framing, fMP4-Ringpuffer und HKSV-State-Machine (`go test -race ./...`).
* [ ] Aktualisierung von [`AGENTS.md`](AGENTS.md), [`README.md`](README.md) und [`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md).
