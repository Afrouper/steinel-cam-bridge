# Steinel L 625 CAM SC / Spot CAM — Nabto Edge, WebRTC & MCU Protokoll- & Treiber-Leitfaden

Dieses Dokument beschreibt die hardware- und protokollspezifische Architektur der **Steinel L 625 CAM SC** (sowie baugleicher Modelle wie **Spot CAM**), basierend auf dem Nabto Edge P2P Direct Tunnel, WebRTC DTLS/SRTP Media, DataChannel-Kommandos und den Reverse-Engineering-Erkenntnissen der offiziellen Steinel App und Netzwerk-Traces.

---

## 1. High-Level Protokoll-Stack

Im Gegensatz zur **Steinel L 620 CAM** (Xiongmai Sofia Binärprotokoll auf Port 34567 + nativer RTSP-Port 554) basiert die **L 625 CAM SC** auf einer modernen Nabto Edge P2P Plattform mit Embedded Linux SoC und separatem Microcontroller (MCU) für Beleuchtung und Sensoren:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    Steinel L 625 CAM SC Hardware                            │
│  ├─ Video/Audio: WebRTC DTLS/SRTP (1080p Full HD H.264 + PCMU 8kHz)        │
│  ├─ Talk-Backchannel: WebRTC SRTP an 'frontdoor-audio' (20ms PCMU Chunks)   │
│  ├─ DataChannel 'test': Bidirektionale Steuerung (JSON & MCU Base64 Frames) │
│  ├─ MicroSD-Speicher: Lokaler MP4-Download via DataChannel Chunks           │
│  └─ Nabto Edge P2P Tunnel: UDP Port 5592 (DTLS 1.2 + CoAP Signalisierung)   │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ mDNS Wake-Up (UDP 5353) & UDP 5592
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│               Nabto Edge & WebRTC Verbindungs-Lifecycle                     │
│  1. mDNS Wake-Up: Unicast / Multicast weckt Kamera-WLAN aus Power-Save      │
│  2. Nabto P2P Connect: DTLS 1.2 Handshake (Client ECC-Key + SCT Token)      │
│  3. CoAP Setup: GET /p2p/webrtc-info -> Port; POST /webrtc/tracks -> Audio/Video │
│  4. Virtual Stream: Bidirektionaler Nabto-Stream für Framed JSON Signalisierung│
│  5. Vanilla-ICE & SDP Negotiation: Host Candidates & Setup-Passive Fix      │
│  6. WebRTC Ingest: H.264 RTP Passthrough (0% CPU) + G.711u -> AAC Transcoder│
│  7. DataChannel 'test': 18-Byte MCU UART Frames (5A0F...) & JSON Command API│
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Kamera-Pairing, Sicherheit & Nabto Edge P2P Tunnel

Die Kamera stellt im Ruhezustand keine ungeschützten Standardports (wie HTTP oder RTSP) bereit. Der Verbindungsaufbau erfolgt über ein sicheres, kryptografisches Peer-to-Peer-Verfahren:

### 1. QR-Code Zugangsdaten
Der in der Steinel App angezeigte Einrichtungs-QR-Code enthält vier Schlüsselparameter:
```text
did=de-xxxxxxx,pid=pr-xxxxxxx,sct=xxxxxxxxxxxx,pairPwd=xxxxxxxxxxxx
```
- `did` (**Device ID**): Eindeutige Kennung der spezifischen Kamera.
- `pid` (**Product ID**): Produktlinie / Modellserie (z. B. `pr-qtatbtbi`).
- `sct` (**Server Connect Token**): Pre-shared Token zur Autorisierung des Verbindungsaufbaus und zur JWT-Signatur.
- `pairPwd` (**Pairing-Passwort**): Einmaliges oder administratives Passwort für die Registrierung neuer Client-Schlüssel.

### 2. Client-Zertifikat & Kryptografie
- Zur Authentifizierung gegenüber der Kamera generiert die Bridge ein lokales **ECC-Schlüsselpaar** (NIST P-256 / secp256r1) und speichert es in `client.key`.
- Beim ersten Verbindungsaufbau mit einem neuen Schlüssel wird über das Nabto IAM-System (`/iam/pairing/password-open`) ein dynamischer Benutzername (`steinel-bridge-<hex>`) registriert und mit dem Passwort authentifiziert.

### 3. mDNS Wake-Up (Power-Save Aufwecken)
Der WLAN-Chipsatz der Leuchte schaltet die Funkschnittstelle bei Inaktivität in einen aggressiven Stromsparmodus (Power-Save Sleep):
- Vor jedem Verbindungsversuch sendet die Bridge gezielte UDP-Pakete an Port `5353` (Multicast `224.0.0.251` und Unicast an die Kamera-IP).
- Dies weckt den Netzwerk-Stack der Kamera auf und stellt sicher, dass der Nabto-Daemon auf UDP-Port `5592` antwortet.

### 4. DTLS 1.2 Handshake & Legacy-Zertifikat
- Der Nabto Edge Direct Tunnel baut eine DTLS 1.2 Verbindung auf Port `5592` auf.
- **Wichtige Eigenheit:** Die Kamera nutzt ein selbstsigniertes **X.509 v1 Zertifikat**. Strikte v3-Prüfungen moderner TLS-Bibliotheken müssen deaktiviert werden, um Zertifikats-Verwerfungen zu verhindern.
- Für die lokale LAN-Kommunikation werden **Direct Candidates** (`nabto_client_connection_add_direct_candidate`) priorisiert, sodass der gesamte Videoverkehr ohne Cloud-Umweg im lokalen Netzwerk verbleibt.

### 5. CoAP Endpunkte
Über den etablierten Nabto-Kanal werden CoAP-Anfragen (RFC 7252) an die Kamera gerichtet:
- `GET /p2p/webrtc-info`: Liefert den virtuellen Port für den Signalisierungsstream:
  ```json
  {"SignalingStreamPort": 3749993357}
  ```
- `POST /webrtc/tracks`: Schaltet die internen Medienkanäle der Kamera frei (CBOR-Payload):
  ```json
  {"tracks": ["frontdoor-video", "frontdoor-audio"]}
  ```

---

## 3. WebRTC Signalisierung über Nabto Virtual Stream

Nachdem der `SignalingStreamPort` ermittelt wurde, öffnet die Bridge einen virtuellen Nabto-Stream. Über diesen Stream findet der gesamte WebRTC-Handshake im Null-Byte-terminierten (`\0`) JSON-Format statt.

### 1. Signalisierungs-Nachrichtentypen
| Typ-ID | Konstante | Richtung | Bedeutung |
|:---:|:---|:---:|:---|
| **0** | `TYPE_OFFER` | Bridge $\rightarrow$ Kamera / Kamera $\rightarrow$ Bridge | SDP Offer (initial oder Renegotiation) |
| **1** | `TYPE_ANSWER` | Bridge $\leftrightarrow$ Kamera | SDP Answer |
| **2** | `TYPE_ICE_CANDIDATE` | Bridge $\leftrightarrow$ Kamera | ICE Kandidaten (Trickle / Fallback) |
| **3** | `TYPE_TURN_REQUEST` | Bridge $\rightarrow$ Kamera | Anfrage nach lokalen ICE/TURN Konfigurationen |
| **4** | `TYPE_TURN_RESPONSE` | Kamera $\rightarrow$ Bridge | Rückgabe der Host- und TURN-Konfiguration |

### 2. Vanilla-ICE Gathering & SDP-Sanitization
Die Kamera implementiert kein standardkonformes Trickle-ICE über den Stream, sondern erwartet ein vollständiges Offer (**Vanilla-ICE**):
- **Candidate Filtering**: Alle lokalen ICE-Kandidaten werden vor dem Senden gesammelt. IPv6-Kandidaten und Loopback-Adressen (`127.0.0.1`) werden aus dem SDP entfernt, um Verbindungsabbrüche zu vermeiden.
- **DTLS Role Fix**: Im SDP Answer der Bridge wird `a=setup:active` zwingend durch `a=setup:passive` ersetzt (`fixAnswerSDP`), da die Kamera immer als DTLS-Server/Active agieren möchte.
- **Renegotiation Offer der Kamera**: Sobald die Kamera bereit ist, sendet sie ein eigenes `TYPE_OFFER` mit den aktiven Tracks. Die Bridge hängt ihren `audioSendTrack` an den Audio-Transceiver (`frontdoor-audio`) an und beantwortet den Offer mit einem sauberen `TYPE_ANSWER`.

---

## 4. Media Streaming Pipeline (Video, Audio & Backchannel)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Media Streaming Pipeline                              │
│                                                                             │
│  [ Kamera ] ──H.264 RTP (FU-A/STAP-A)──> Zero-Transcoding ──> RTSP Server  │
│                                                               (:8554 H.264) │
│  [ Kamera ] ──PCMU (G.711u 8kHz)───────> Resampler & AAC ───> RTSP Server  │
│                                          Transcoder            (:8554 AAC)  │
│                                                                             │
│  [ Smartphone ] ──RTSP Backchannel ────> 20ms Frame Chunking ─> Kamera      │
│                   (:8554/udp Profile T)   (160 Byte PCMU)       Lautsprecher│
└─────────────────────────────────────────────────────────────────────────────┘
```

### 1. Video-Pipeline (H.264 Zero-Transcoding)
- **Auflösung & Framerate**: 1920 × 1080 (Full HD) @ 15 fps (Baseline Profile, Level 4.0 / 5.1).
- **Zero-Transcoding**: Eingehende RTP-Pakete (STAP-A und FU-A) werden ohne Dekodierung direkt als H.264 NAL-Units an den RTSP-Server (`gortsplib`) übergeben. Dies garantiert minimale Latenz (< 300 ms) und vernachlässigbare CPU-Last (< 0,3 % auf dem Host).
- **Keyframe-Gating**: Zu Beginn der Session wird das erste Keyframe (IDR mit SPS/PPS `0x67` / `0x68`) via RTCP Picture Loss Indication (PLI) angefordert.
- **Steady-State PLI Loop**: Die Bridge sendet periodisch alle 1,0 bis 3,0 Sekunden ein sanftes RTCP PLI an die Video-SSRC der Kamera, um das GOP-Intervall stabil zu halten und neu hinzukommenden RTSP-Clients sofort ein Schlüsselbild bereitzustellen.

### 2. Live Audio-Pipeline (Transcoding G.711u $\rightarrow$ AAC-LC)
- Die Kamera sendet Mikrofonton als **PCMU (G.711 $\mu$-law, 8000 Hz, Mono)**.
- Da Apple HomeKit Secure Video (HKSV / Scrypted) zwingend AAC-Audio verlangt, wandelt die Bridge das Audiosignal in Echtzeit:
  - G.711u Dekodierung zu 16-Bit Linear PCM
  - Polyphase-Interpolation / Resampling (8 kHz $\rightarrow$ 16 kHz)
  - VisualOn AAC-LC Kodierung mit optimiertem Puffer-Pooling (`bytes.Reader.Reset()`)
- Für NVRs ohne AAC-Bedarf kann die Bridge transparent auf PCMU-Passthrough umgeschaltet werden.

### 3. Zwei-Wege-Audio Backchannel (Gegensprechen)
- Eingehendes Audio von RTSP Profile T Clients (z. B. Gegensprechen in Apple Home) wird über den UDP-Interceptor auf Port `8554/udp` empfangen.
- Die Audio-RTP-Pakete werden in exakt getaktete **20ms-Chunks** (160 Bytes PCMU @ 8000 Hz) formatiert.
- Die Chunks werden über SRTP an den `frontdoor-audio` Track der PeerConnection übertragen und direkt über den internen Verstärker und Lautsprecher der Leuchte wiedergegeben.

---

## 5. WebRTC DataChannel `test` (JSON Command API)

Alle übergeordneten Steuerbefehle werden über den bidirektionalen SCTP DataChannel mit dem Namen `test` als JSON-Strings ausgetauscht.

### 1. Nachrichtenformat
**Anfrage an die Kamera**:
```json
{
  "from": "Android",
  "cmd": "<COMMAND_NAME>",
  "msgid": "<UUID>",
  "action": "all",
  "info": { ... }
}
```

**Asynchrone Antwort der Kamera**:
```json
{
  "resp": "<COMMAND_NAME>",
  "info": {
    "code": 200,
    "msg": "success"
  }
}
```

### 2. Wichtigste Steuerbefehle
- **Videoauflösung setzen (`set_video_setting`)**:
  ```json
  {"from":"Android","cmd":"set_video_setting","msgid":"...","action":"all","info":{"quality":{"sub1":{"resolution":"1080p"}}}}
  ```
  *Unterstützt: `"1080p"`, `"720p"`, `"360p"`.*
- **Alarmsirene schalten (`alarm_voice_ctl`)**:
  ```json
  {"from":"Android","cmd":"alarm_voice_ctl","msgid":"...","info":{"play": true}}
  ```
  *Schaltet den integrierten Sirenenton manuell ein (`play: true`) oder aus (`play: false`).*
- **Geräteinformationen abfragen (`get_device_info`)**:
  Liefert Firmware-Version (`FW_version`), Modellname und Hardware-Revision.
- **Kamera-Reboot auslösen (`reboot`)**:
  Startet den Kamera-SoC kontrolliert neu.
- **Push-Benachrichtigungen der Kamera**:
  Die Kamera meldet Ereignisse unaufgefordert als JSON über den DataChannel. Enthält der Payload Schlüsselwörter wie `alarm`, `motion`, `pir`, `event` oder `doorbell`, wird unmittelbar ein Bewegungsevent im EventBus ausgelöst.

---

## 6. MCU Lampen-, Sensor- & Telemetriesteuerung (`tran_ctl`)

Die physischen Hardwarekomponenten der Außenleuchte (LED-Treiber, PIR-Sensor, Dämmerungssensor) werden von einem separaten Microcontroller (MCU) gesteuert. Die Kommunikation zwischen Linux-SoC und MCU erfolgt über eine interne serielle UART-Schnittstelle.

Die Steinel App und die Bridge tunneln diese UART-Pakete über den DataChannel `test` mittels des JSON-Befehls `tran_ctl`, wobei die Binärdaten als **Base64-kodierte Hex-Strings** übertragen werden:
```json
{
  "from": "Android",
  "cmd": "tran_ctl",
  "msgid": "d14a51e6-234b-4b4d-9051-7efc464a9749",
  "info": {
    "data": "WgEPag=="
  }
}
```

### 1. Aufbau des 18-Byte MCU UART Telemetrie-Frames (`5A 0F 0F...`)
Jeder vollständige Statusbericht der Leuchte (`tran_report` oder Antwort auf Statusabfrage) besteht aus exakt **18 Bytes** (36 Hex-Zeichen), beginnend mit dem Magic-Header `5A 0F 0F`:

```
Byte:   0    1    2    3    4    5    6    7    8    9   10   11   12   13   14   15   16   17
Hex:  [5A] [0F] [0F] [HW] [SW] [MD] [ LUX ] [PIR][TX] [PS] [CT] [HL] [ HLT ] [LL] [ LLT ] [CS]
```

| Byte-Index | Hex-Offset | Feld in `mcu.ConfigInfo` | Typ | Bedeutung & Wertebereich |
|:---:|:---:|:---|:---:|:---|
| **0** | `0..1` | Header Magic | Byte | Immer `0x5A` (Steinel UART Frame Start) |
| **1** | `2..3` | Frame-Typ / Länge | Byte | Immer `0x0F` |
| **2** | `4..5` | Frame-Subtyp | Byte | Immer `0x0F` (Status Telemetrie Report) |
| **3** | `6..7` | `HVersion` | Hex | Hardware-Revision des MCU-Boards |
| **4** | `8..9` | `SVersion` | Hex | Software-/Firmware-Version der MCU |
| **5** | `10..11` | `Mode` | uint8 | Betriebsmodus: `0` = Aus, `1` = Dauerlicht AN, `2` = Sensor Auto |
| **6–7** | `12..15` | `Lux` | uint16 | Gemessene Umgebungshelligkeit in Lux ($2$ bis $1000\text{ lx}$, $10000 = \text{Tag}$) |
| **8** | `16..17` | `PIRActive` | bool | PIR-Sensor aktiv (`0` = inaktiv, `1` = aktiv) |
| **9** | `18..19` | `tuxingVal` | Bitmaske | Bewegungserkennung: Bit 0 = PIR-Sensor, Bit 1 = Kamera-Bilderkennung |
| **10** | `20..21` | `PIRSensitivity` | uint8 | PIR-Erfassungsbereich / Empfindlichkeit ($0\ \%$ bis $100\ \%$) |
| **11** | `22..23` | `ColorTemp` | uint8 | Farbtemperatur der Leuchte |
| **12** | `24..25` | `Highlight` | uint8 | Hauptlicht-Helligkeit bei Bewegung ($10\ \%$ bis $100\ \%$) |
| **13–14** | `26..29` | `HighlightTime` | uint16 | Nachlaufzeit des Hauptlichts in Sekunden ($5$ bis $900\text{ s}$) |
| **15** | `30..31` | `Lowlight` | uint8 | Grundlicht-Helligkeit / Nachtlicht ($0\ \%$ = Aus, $10\ \%$ bis $50\ \%$) |
| **16–17** | `32..35` | `LowlightTime` | uint16 | Grundlicht-Dauer (z. B. Zeitwert oder ganztägig) |

### 2. Prüfsummen-Berechnung (`Checksum`)
Steinel verwendet eine einfache 8-Bit Summenprüfsumme über alle Nutzdatenbytes vor dem Anhängen an den Frame:
$$\text{Checksum} = \left( \sum_{i=0}^{n-1} \text{byte}_i \right) \pmod{256}$$

### 3. Exakte MCU-Steuerbefehle
Alle Steuerbefehle beginnen mit Header `0x5A`, gefolgt von der Befehlslänge, dem Befehlscode, Parametern und der Prüfsumme:

| Funktion | Hex-Kommando (ohne CS) | Beispiel-Frame mit CS | Beschreibung |
|:---|:---|:---|:---|
| **Status abfragen** | `5A 01 0F 6A` | `5A010F6AD4` | Fragt vollständigen Telemetriebericht an |
| **Dauerlicht AN** | `5A 02 01 01` | `5A0201015E` | Schaltet das Hauptlicht dauerhaft ein (`Mode 1`) |
| **Sensor Auto (Licht AUS)** | `5A 02 01 02` | `5A0201025F` | Aktiviert Automatikbetrieb mit PIR (`Mode 2`) |
| **Leuchte komplett AUS** | `5A 02 01 00` | `5A0201005D` | Deaktiviert Leuchte und PIR vollständig (`Mode 0`) |
| **Hauptlicht dimmen** | `5A 02 07 <PCT>` | `5A020764C7` | Setzt Helligkeit auf z. B. 100 % (`0x64`) |
| **Nachlaufzeit setzen** | `5A 03 08 <SEC16>` | `5A0308003C9D` | Setzt Nachlaufzeit auf z. B. 60 s (`0x003C`) |
| **Grundlicht dimmen** | `5A 02 0A <PCT>` | `5A020A147A` | Setzt Grundlicht auf z. B. 20 % (`0x14`) |
| **Grundlicht-Dauer** | `5A 03 0B <VAL16>` | `5A030B...` | Setzt Einschaltdauer des Grundlichts |
| **PIR-Sensitivität** | `5A 02 06 <PCT>` | `5A020650B2` | Setzt PIR-Empfindlichkeit auf z. B. 80 % (`0x50`) |
| **Dämmerungsschwelle** | `5A 03 04 <LUX16>` | `5A0304000A6B` | Setzt Dämmerungsschwelle auf z. B. 10 Lux (`0x000A`) |

---

## 7. MicroSD-Karten Edge Storage & Event-Playback

Die Steinel L 625 CAM SC zeichnet bei Bewegungserkennung Videoclips auf die interne MicroSD-Karte auf. Die Bridge ermöglicht den Zugriff darauf über REST-API und **ONVIF Profile G**.

### 1. Event-Liste abrufen (`get_event_list`)
```json
{
  "from": "Android",
  "cmd": "get_event_list",
  "msgid": "...",
  "info": {
    "page": 0,
    "limit": 50,
    "type": "all",
    "start_time": 1723500000,
    "end_time": 1723586400
  }
}
```
Die Kamera antwortet asynchron mit der Liste der Aufnahme-Einträge (`time_tag`, `record_time`, `filesize`, `name`).

### 2. Video-Download via DataChannel (`get_event_video`)
1. Client sendet `get_event_video` mit `action: "start"` und `timestamp`.
2. Kamera bestätigt den Start mit `action: "start"`, `name` (`event_<ts>.mp4`) und `filesize`.
3. Die Videodaten werden als **binäre Chunks** über den DataChannel `test` gestreamt.
4. Nach Übertragung aller Bytes sendet die Kamera ein JSON-Paket mit `action: "end"`.

### 3. Das Snapshot-Dilemma (Erkenntnisse aus Issue #23)
- **Die Falle:** Die Firmware `0.1.2.29` der L 625 CAM SC speichert bei Events ausschließlich MP4-Dateien (**2560 × 1440 2K HEVC / H.265**). Es werden **physisch keinerlei JPEG-Thumbnails** auf der SD-Karte abgelegt!
- Wird der Befehl `get_snapshot` an die Kamera gesendet, sucht die Firmware vergeblich nach einem Bild, antwortet nicht und blockiert den DataChannel für bis zu 25 Sekunden.
- **Die Lösung:**
  - Der native Thumbnail-Endpunkt bricht bei Nabto-Modellen sofort mit `ErrFeatureDisabled` (`HTTP 501 Not Implemented`) ab, um Blockaden zu vermeiden.
  - Stattdessen nutzt die Bridge einen lokalen **FrameExtractor** (`FFmpegExtractor` im Google Distroless Image), der beim Herunterladen des MP4-Videos nach 5 Sekunden Videomaterial automatisch ein hochauflösendes Keyframe als JPEG extrahiert und im lokalen Cache (`/data/recordings`) ablegt.

### 4. Single-Flight Concurrency Schutz (Concurrency = 1)
Die embedded CPU der Leuchte stürzt ab, wenn parallele Leseanfragen an die SD-Karte gestellt werden.
- Sowohl `GetEventList` als auch `StreamVideo` sind durch strikte **Single-Flight Mutexes** (`eventListQueryMu` und `transferMu.TryLock()`) geschützt.
- Parallele Anfragen erhalten sofort `ErrSDCardBusy` (`HTTP 429 / 503`), wodurch die Kamera-Hardware zuverlässig geschützt wird.

---

## 8. Hardware-Vergleich: L 625 CAM SC vs. L 620 CAM / XLED CAM 1

| Eigenschaft | Steinel L 625 CAM SC / Spot CAM | Steinel L 620 CAM / XLED CAM 1 |
|:---|:---|:---|
| **Plattform & SoC** | Embedded Linux SoC mit Nabto Edge Daemon | Xiongmai Tech (XM) SoC (HI3518EV200 / XM530) |
| **Primär-Protokoll** | Nabto Edge P2P Direct Tunnel (UDP 5592) | Xiongmai Sofia Binärprotokoll (TCP 34567) |
| **Discovery** | mDNS Unicast/Multicast (UDP 5353) | Xiongmai Broadcast Probe (UDP 34569) |
| **Live Video-Stream** | WebRTC DTLS/SRTP (1080p H.264 Passthrough) | Nativer RTSP-Server (Port 554) oder Sofia NetIP (1410) |
| **Mikrofon-Audio** | PCMU (G.711 $\mu$-law, 8 kHz Mono) | G.711 A-law (PCMA, 8 kHz) |
| **Audio-Transcoding** | Go-Pipeline $\rightarrow$ AAC-LC (16 kHz) für HKSV | Transkodierung PCMA $\leftrightarrow$ PCMU für Talk-Backchannel |
| **Gegensprechen** | 20ms SRTP PCMU Chunks an WebRTC Audio-Track | Xiongmai Talk-Protokoll (`MsgTalkClaimReq` 1434) |
| **MCU-Verbindung** | DataChannel `test` (Base64 UART Hex-Frames) | Sofia OPTrans RS232 (`MsgTransSendData` 1572) |
| **MCU Frame-Format** | **18-Byte Binär-Hex** (`5A 0F 0F...`) | **12-Byte ASCII-Format** (`B...U`) |
| **PIR-Empfindlichkeit** | Stufenlos $0\ \%$ bis $100\ \%$ | 3 diskrete Stufen (`k` = Min, `p` = Med, `u` = Max) |
| **Alarmsirene** | Manuell per Befehl schaltbar (`alarm_voice_ctl`) | Bewegungsalarm-Summer (`Detect.MotionDetect.[0]`) |
| **SD-Karten Format** | Nur MP4-Videoclips (2K HEVC / H.265) | MP4-Dateien mit Xiongmai Header |

---

## 9. Resilienz, Watchdogs & Reconnect-Supervisor

Die L 625 CAM SC ist als Außenleuchte transienten WLAN-Schwankungen, winterlichen Temperaturschwankungen und gelegentlichen automatischen Reboots ausgesetzt. Die Bridge implementiert ein mehrstufiges Resilienz- und Überwachungssystem:

### 1. Adaptiver exponentieller Reconnect-Backoff
Schlägt der Verbindungsaufbau fehl (z. B. wenn die Leuchte nach einem Stromausfall bootet), greift der in `pkg/driver/l625.go` implementierte Backoff-Algorithmus:
$$\text{Delay} = \min\left(15\text{ s} \times 2^{\text{failures} - 1},\; 240\text{ s}\right)$$
- Progression: **15s $\rightarrow$ 30s $\rightarrow$ 60s $\rightarrow$ 120s $\rightarrow$ 240s (max. 4 Minuten)**.
- Dies verhindert Verbindungsstürme auf den frisch bootenden Netzwerk-Daemon der Kamera.

### 2. Cooldown-Phasen
- **Hardware-Reboot Cooldown (60 Sekunden)**: Nach einem unvorhergesehenen Verbindungsabbruch oder Watchdog-Reset wartet die Bridge 60 Sekunden, um der Leuchte ausreichend Zeit für den vollständigen Linux-Kernel- und Daemon-Start zu geben.
- **Soft-Recovery Cooldown (3 Sekunden)**: Wurde der Reset lediglich durch einen Kontrollkanal-Wechsel oder eine manuelle Anforderung ausgelöst (`ResetReasonControlPlaneRecovery`), verbindet sich die Bridge nach nur 3 Sekunden wieder.

### 3. CGo Fast-Fail (`ConnectTimeout: 25s`)
Das offizielle Nabto C-SDK besitzt einen internen Timeout von bis zu 120 Sekunden. Um zu verhindern, dass blockierende C-Aufrufe die Go-Goroutinen lahmlegen:
- Setzt die Bridge `nabto_client_connection_set_options(conn, "{\"ConnectTimeout\":25000}")`.
- Reagiert die Leuchte nicht, bricht der Aufruf nach **25 Sekunden** ab, sodass der Supervisor die Bereinigung sofort einleiten kann.

### 4. CGo Concurrency- & Mutex-Disziplin
- **Keine Locks während blockierender C-Calls**: Mutexes (`sync.Mutex`) dürfen niemals während des Wartens auf Nabto-Futures (`future_wait`) gehalten werden.
- **Sofortiger Abbruch**: Bei Verbindungsabbrüchen oder Timeouts ruft `Close()` sofort `C.nabto_client_stop(ctx)` auf. Dies bricht alle pendenden Netzwerk-Futures auf C-Ebene unmittelbar mit `NABTO_CLIENT_EC_STOPPED` ab, ohne auf Timeouts zu warten.
- **Goroutine-Tracking**: Laufende Hintergrund-Worker werden über `sync.WaitGroup` nachverfolgt, um Goroutine-Leaks auszuschließen.

### 5. Video-Silence Watchdog
- Die WebRTC-Engine überwacht kontinuierlich den Zeitstempel des letzten empfangenen H.264 RTP-Videopakets (`lastVideoPacket`).
- **Initial Grace Period**: 15 Sekunden nach Session-Start zur Durchführung von ICE-Gathering und SDP-Aushandlung.
- **Silence Threshold**: Bleibt der Videostrom während des Betriebs länger als **15 Sekunden** vollständig aus, schließt die Bridge die Session und initiiert einen kontrollierten Reconnect.

### 6. CGo-freier Bootstrap Launcher (`cmd/launcher`)
- Aus Lizenz- und Sicherheitsgründen enthält das Google Distroless Docker-Image (`gcr.io/distroless/cc-debian12`) keine vorabgebündelten proprietären Binärdateien.
- Beim Containerstart prüft der statisch kompilierte Launcher (`cmd/launcher`, `CGO_ENABLED=0`) das Cache-Verzeichnis `/data/lib/`, lädt bei Bedarf die offizielle `libnabto_client.so` direkt von Nabto GitHub Releases herunter und startet die eigentliche Bridge nahtlos über `syscall.Exec`.
- Dadurch bleibt `steinel-bridge` PID 1 im Container, Signalbehandlung (`SIGTERM`/`SIGINT`) funktioniert verzögerungsfrei, und das Image bleibt 100 % Open-Source-konform.
