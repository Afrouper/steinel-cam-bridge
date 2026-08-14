# Steinel L 625 CAM SC — Entwickler- & Agenten-Leitfaden

Dieses Dokument beschreibt die Codebase-Architektur, Designentscheidungen, den Protokollablauf und Richtlinien für zukünftige Entwickler und KI-Agenten.

---

## 1. High-Level Architektur

Die Steinel CAM Bridge ist ein hochperformanter, autarker Go-Daemon, der den proprietären **Nabto Edge P2P WebRTC-Feed** der **Steinel L 625 CAM SC** Kamera in einen standardkonformen **RTSP-Stream** (`rtsp://host:port/steinel`) wandelt – zur Weiternutzung in **Scrypted / Apple HomeKit Secure Video (HKSV)**, **Home Assistant** und **VLC**.

```
┌───────────────────────────────┐
│     Steinel L 625 CAM SC      │
│  (Embedded Linux + SoC DSP)   │
└──────────────┬────────────────┘
               │ 1. mDNS Wake-Up (UDP 5353 / 5592)
               │ 2. Nabto Edge P2P Direct Tunnel (CGo Wrapper)
               │ 3. CoAP /p2p/webrtc-info & /webrtc/tracks
               │ 4. WebRTC SDP & ICE über Nabto Virtual Stream
               ▼
┌───────────────────────────────┐
│   Steinel Bridge Daemon (Go)  │
│  ├── pkg/nabto (CGo Client)   │
│  ├── pkg/webrtc (Pion Engine) │
│  │   ├── RTCP PLI Keyframe Loop (3s GOP)
│  │   └── RTP Silence Watchdog (6s Threshold)
│  └── pkg/rtsp (gortsplib v4)  │
└──────────────┬────────────────┘
               │
               │ RTSP H.264 Passthrough (:8554/steinel)
               ▼
┌───────────────────────────────┐
│ Scrypted / HomeKit HKSV / HA  │
└───────────────────────────────┘
```

---

## 2. Paketstruktur & Verantwortlichkeiten

- **`cmd/steinel-bridge/main.go`**:
  - CLI-Flags (`-ip`, `-qr`, `-key`, `-port`, `-path`, `-res`) und Umgebungsvariablen (`CAMERA_IP`, `QR_CODE`, `KEY_PATH`).
  - Initialisiert den persistenten, integrierten RTSP-Server.
  - Beherbergt den **Supervisor-Loop**: Fängt Verbindungsabbrüche, Session-Beendigungen oder Watchdog-Resets ab und erzwingt einen sauberen **30-Sekunden-Cooldown**, damit neu startende Kameras stabil hochfahren können, ohne das Netzwerk zu fluten.

- **`pkg/nabto/`**:
  - `client.go`: CGo-Bindings für das Nabto Edge Client SDK (`nabto_client.h`).
  - Verwaltet kryptografische Schlüssel (`client.key`), passwortbasiertes IAM-Pairing (`/iam/pairing/password-open`), CoAP-Signaling-Port-Erkennung (`/p2p/webrtc-info`) und Track-Aktivierung (`/webrtc/tracks`).
  - Verwaltet den virtuellen Nabto-Stream (4-Byte Little-Endian Längen-Framing).
  - `qr.go`: Parst die Kamera-Zugangsdaten (`did`, `pid`, `sct`, `pairPwd`) aus dem QR-Code-String der Steinel App.

- **`pkg/webrtc/`**:
  - `bridge.go`: Nutzt **Pion WebRTC v4** (`github.com/pion/webrtc/v4`), um WebRTC-Sessions mit der Kamera über den virtuellen Nabto-Stream auszuhandeln.
  - Sendet initiales SDP-Offer mit `no_trickle: true` und Vanilla ICE Candidates.
  - Hält den **RTCP-PLI Loop**: Sendet einen initialen Burst von Picture Loss Indications (Keyframe-Anforderungen) für sofortigen Bildaufbau und danach periodische PLIs alle 3,0 Sekunden für minimale Live-Latenz (< 1s).
  - **RTP Silence Watchdog (`runWatchdogLoop`)**: Überwacht die Zeitstempel eingehender Videopakete. Bleiben Pakete für > 6s aus, wird die Session abgebrochen und der 30s-Reconnect ausgelöst.
  - **WebRTC State Listeners**: Überwacht `pc.OnConnectionStateChange` und `pc.OnICEConnectionStateChange`, um bei `Failed` oder `Disconnected` sofort den Reset auszulösen.
  - `types.go`: JSON-Serialisierung für Nabto WebRTC Signaling Envelopes.

- **`pkg/rtsp/`**:
  - `server.go`: Integrierter RTSP-Server auf Basis von `github.com/bluenviron/gortsplib/v4`.
  - Stellt H.264 (Payload Type 96, Packetization Mode 1) und PCMU-Audio (G.711u 8000 Hz) bereit.
  - **Zero-Transcoding Passthrough**: Direkte Weiterleitung der RTP-Pakete vom Pion-Receiver in die RTSP-Streams (0 % CPU-Transcoding-Last auf dem Host).

---

## 3. Zentrale Architekturregeln

1. **Zero Transcoding**: Niemals das H.264-Video auf dem Host transkodieren. Reiche rohe NAL-Units direkt an den RTSP-Server weiter. Audio wird nativ durchgereicht (Downstream-Tools wie Scrypted wandeln PCMU bei Bedarf in AAC-ELD für Apple HomeKit um).
2. **24/7 Resilienz**:
   - Die Nabto/WebRTC-Session muss immer in einem abbrechbaren Session-Kontext laufen.
   - Jeder Fehler oder Timeout muss den Supervisor-Loop unblockieren.
   - Die 30-Sekunden-Verzögerung vor Reconnects muss zwingend eingehalten werden.
3. **Keine Binärdateien im Git**:
   - Niemals `.so`, `.dylib`, `.dll`, `.key` oder `.sdk/` ins Git committen.
   - Für die lokale Entwicklung `./scripts/setup-sdk.sh` (macOS/Linux) oder `.\scripts\setup-sdk.ps1` (Windows) nutzen.
   - Das Dockerfile lädt die SDK-Artefakte während des Builds dynamisch direkt von GitHub herunter.

---

## 4. Entwicklungs- & Build-Befehle

```bash
# 1. Lokale SDK-Artefakte herunterladen (macOS / Linux)
./scripts/setup-sdk.sh

# 2. Lokalen Entwicklungs-Build starten
./scripts/run-dev.sh -key data/client.key -ip 192.168.1.100

# 3. Code formatieren und prüfen
go fmt ./...
go vet ./...

# 4. Docker-Container lokal bauen
docker build -t steinel-cam-bridge .
```
