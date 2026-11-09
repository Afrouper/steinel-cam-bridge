# Steinel L 620 CAM / XLED CAM 1 — Xiongmai Sofia Protokoll- & Treiber-Leitfaden

Dieses Dokument beschreibt die hardware- und protokollspezifische Architektur der **Steinel L 620 CAM** (sowie baugleicher Modelle wie **XLED CAM 1**), basierend auf dem Xiongmai Sofia / NetIP Protokoll und den Erkenntnissen aus der Dekompilierung der offiziellen Steinel CAM Android App (`de.steinel.cam`) sowie Netzwerk- und Protokollmitschnitten.

---

## 1. High-Level Protokoll-Stack

Im Gegensatz zur **Steinel L 625 CAM SC** (Nabto Edge P2P + WebRTC DataChannel) basiert die **L 620 CAM** auf einer Xiongmai (XM / JFTech) IPC-Plattform mit SoC-Firmware (z. B. `V4.03.R12`):

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Steinel L 620 CAM Hardware                            │
│  ├─ Video Primär: Natives H.264 Video-Streaming (Sofia OPMonitor Port 34567)│
│  ├─ Video Fallback: Lokaler nativer RTSP-Stream (TCP Port 554, 1080p H.264) │
│  ├─ Talk-Backchannel: Xiongmai Talk Protokoll (MsgID 1432/1434, PCMA 8kHz)  │
│  ├─ Steuerung & MCU: Xiongmai Sofia / NetIP Protokoll (TCP Port 34567)      │
│  └─ Speicher: MicroSD-Karten Abfrage via OPFileQuery (MsgID 1440/1441)      │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ Port 34567 (NetIP)
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                  Sofia NetIP Verbindungs-Lifecycle                          │
│  1. Probe / Claim: MsgMonitorClaimReq (1413) -> RSA-1024 Public Key        │
│  2. Login: MsgLoginReq (1000) mit RSA PKCS#1 v1.5 + AES-128-CBC Session-Key│
│  3. Serieller Tunnel: MsgTransStartReq (1578) -> OPTrans RS232 aktiv        │
│  4. Steuerbefehle: MsgTransSendData (1572) via AES-128-CBC auf Kanal 1      │
│  5. Telemetrie & Push: MsgTransSerialData (1582) & MsgAlarmPush (1500)      │
│  6. On-Demand Video: OPMonitor (1410/1412) bei aktiven RTSP-Zuschauern      │
│  7. Talk Backchannel: OPTalk (1434/1432) mit PCMU <-> PCMA Transkodierung   │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Authentifizierung & Symmetrische Session-Verschlüsselung

Moderne L 620 Firmware-Versionen erzwingen für Konfiguration und Steuerbefehle verschlüsselte Kommunikation:

1. **RSA-Claim Handshake (MsgID 1413 / 1414)**:
   - Client sendet `OPMonitor Claim` (1413).
   - Kamera meldet Public Key (RSA 1024 Bit) und `DataEncryptionType: {"AES": true, "AESV2": true}` sowie `NotEncryptMsgID: [1000, 1001, 1008, 1009, 1412, 1413, 1414, 1432, 1434, 1435, 1580, 1582]`.
2. **Login mit CommunicateKey & 8-Character Hash (MsgID 1000 / 1001)**:
   - Client generiert einen zufälligen 16-stelligen alphanumerischen Session-Key (`CommunicateKey`).
   - Das Passwort wird über den proprietären **Xiongmai 8-Character Base62 MD5-Hash** (z. B. `"admin"` $\rightarrow$ `"tlJwpbo6"`) oder adaptiven Login-Kandidaten (z. B. `DVRIP-FutureHome`, `DVRIP-Xm030`) verschlüsselt.
   - `UserName`, `PassWord` und `CommunicateKey` werden mit RSA PKCS#1 v1.5 verschlüsselt.
   - Äußere Payload wird mit dem NetIP-Standardschlüssel (AES-128-CBC, Zero-IV, PKCS#7 Padding) verschlüsselt und mit `0x00` terminiert.
3. **Kanal-1 Payload-Verschlüsselung**:
   - Alle nachfolgenden Steuerbefehle auf Kanal 1 (wie `1578` OPTrans-Start, `1572` MCU-Kommandos, `1006` KeepAlive, `1440` OPFileQuery) werden symmetrisch mit dem ausgehandelten `CommunicateKey` verschlüsselt (AES-128-CBC, Zero-IV, Base64).
   - Eingehende Antworten auf Kanal 1 (wie `1579`, `1573`, `1007`, `1441`) werden symmetrisch mit demselben Schlüssel entschlüsselt.
   - Telemetrie (`1582`), Alarme (`1500`), Talk-Audio (`1432`) und Roh-Videochucks (`1412`) verbleiben gemäß `NotEncryptMsgID` unverschlüsselt.

---

## 3. Transparente serielle RS232-Schnittstelle (OPTrans)

Die Steinel App verwendet weder `FbExtraStateCtrl` (1040) noch `SerialPortsInfo` (1020) zum Schalten. Sie steuert die L 620 MCU ausschließlich über den transparenten NetIP RS232-Kanal:

- **Start des Tunnels**:
  - Request: `MsgTransStartReq` (1578) mit Payload `{"Name":"OPTrans","OPTrans":{"Channel":0,"TransType":"RS232"}}`.
  - Response: `MsgTransStartResp` (1579) `{"Name":"OPTrans","Ret":100}`.
- **Senden von MCU-Befehlen**:
  - Request: `MsgTransSendData` (1572) mit Payload `{"Name":"OPTrans","OPTrans":{"Data":"B...U"}}`.
  - Response: `MsgTransSendResp` (1573) `{"Name":"OPTrans","Ret":100}`.
- **Eingehende Telemetrie-Push-Nachrichten**:
  - Unaufgefordert alle 60 Sekunden (oder unmittelbar nach einem Steuerbefehl) sendet die Kamera unverschlüsselte `MsgTransSerialData` (1582) Pakete mit 12-Byte-ASCII-Frames.

### TCP-Paket-Fragmentierung (`tempData` Puffer)
Die serielle UART-Bridge der Kamera liefert die 12 Bytes gelegentlich in zwei getrennten TCP-Paketen (z. B. 8 Bytes `"Bpbkbzac"` gefolgt von 4 Bytes `"azOU"`).
Wie im App-Code (`XMLightSettingPresenter.java`, Zeilen 93–104) implementiert, muss der Client Fragmente puffern:
- Endet das empfangene Datenpaket auf `'U'`, wird es mit dem eventuell vorhandenen Puffer zusammengefügt, geparst und der Puffer geleert.
- Fehlt das abschließende `'U'`, wird der Chunk im Puffer zwischengespeichert.

---

## 4. Aufbau des 12-Byte MCU ASCII-Frames (`B...U`)

Jeder vollständige Telemetrie-Frame hat exakt 12 Zeichen (Indizes 0 bis 11):

$$\mathbf{B}\;[\text{dist}]\;[\text{hld}]\;[\text{lux}]\;[\text{mode}]\;[\text{indicator}]\;[\text{lowlight}]\;[\text{highlight}]\;[\text{lld}]\;[\text{level}]\;\mathbf{O}\;\mathbf{U}$$

| Index | Feld in `YTDevConfigInfo` | Bedeutung | Wertebereich / Kodierung |
|:---:|:---|:---|:---|
| **0** | Frame-Header | Start-Begrenzer | Immer `'B'` |
| **1** | `distance` | PIR-Erfassungsbereich / Empfindlichkeit | `'k'` (Min, 10), `'p'` (Med, 15), `'u'` (Max, 20) |
| **2** | `hld` | Stay-On Time (Nachlaufzeit) | `char - 'a'`: Multiplikator $\times 60$ Sekunden |
| **3** | `lux` | Dämmerungsschwelle (Twilight Lux) | Invertierter Index in 26-stufiger `luxs`-Tabelle |
| **4** | `mode` | Betriebsmodus der Leuchte | `'b'` (1) = Sensor Auto, `'d'` (3) = Dauerlicht, `'e'` (4) = Test |
| **5** | `indicator` | Status-LED / Indikator | `'z'` |
| **6** | `lowlight` | Grundlicht (Nachtlicht) Helligkeit | `'a'` (0) = Aus, sonst 10 % bis 40 % |
| **7** | `highlight` | Hauptlicht Helligkeit | `(char - 'a') \times 4$ % (Wertebereich 30 % bis 100 %) |
| **8** | `lld` | Grundlicht-Dauer (Lowlight Duration) | `'a'` (0) = Ganze Nacht (24h) |
| **9** | `level` | Sensor-Stufe | `'z'` |
| **10** | Frame-Trailer | Kennung | Immer `'O'` |
| **11** | Frame-Ende | Stop-Begrenzer | Immer `'U'` |

---

## 5. Exakte Konvertierungsformeln (nach App-Referenz)

### 1. Dämmerungsschwelle (Lux)
Die App nutzt eine feste 26-Elemente-Tabelle (`DetectSettingActivity.java`, Zeile 63):
```
luxs = [2, 3, 5, 7, 10, 18, 30, 38, 50, 58, 70, 85, 100, 125, 150, 175, 200, 250, 300, 400, 500, 750, 1000, 1300, 1600, 2000]
```
- **Dekodierung (Empfang)**:
  $$\text{progress} = 25 - (\text{char} - \text{'a'})$$
  $$\text{Lux} = \text{luxs}[\text{progress}]$$
  *Beispiele aus Traces:*
  - `'n'` ($13$) $\rightarrow 25 - 13 = 12 \rightarrow \mathbf{luxs[12] = 100\text{ Lux}}$
  - `'k'` ($10$) $\rightarrow 25 - 10 = 15 \rightarrow \mathbf{luxs[15] = 175\text{ Lux}}$
- **Kodierung (Senden)**:
  Finde Index $i \in [0, 25]$ mit geringstem Abstand zu gewünschtem Lux-Wert.
  $$\text{conv} = 25 - i \implies \text{Befehl: } \mathbf{"BX" + (\text{'a'} + \text{conv}) + "U"}$$

### 2. PIR-Empfindlichkeit (Distance)
Die L 620 MCU unterstützt genau 3 diskrete Stufen (`XMDetectSettingPresenter.java`, Zeilen 394–416):
- **Stufe 0 (Minimum)**: char `'k'` ($10$), entspricht ca. 3 m / 33 % in Home Assistant.
- **Stufe 1 (Medium)**: char `'p'` ($15$), entspricht ca. 6 m / 66 % in Home Assistant.
- **Stufe 2 (Maximum)**: char `'u'` ($20$), entspricht ca. 10 m / 100 % in Home Assistant.
- **Befehl zum Setzen**: $\mathbf{"BD" + \text{char} + "U"}$ (`"BDkU"`, `"BDpU"`, `"BDuU"`).

### 3. Nachlaufzeit (Stay-On Time / Highlight Delay)
- **Dekodierung**:
  $$\text{Sekunden} = (\text{char} - \text{'a'}) \times 60$$
  *Beispiel:* `'b'` ($1$) $\rightarrow 1 \times 60 = \mathbf{60\text{ s} = 1\text{ min}}$.
- **Kodierung (Senden)**:
  $$\text{min} = \max(1, \text{sekunden} / 60) \implies \text{Befehl: } \mathbf{"BT" + (\text{'a'} + \text{min}) + "U"}$$
  *(Hinweis: Befehls-Präfix ist `"BT"`, nicht `"BS"`!)*

### 4. Hauptlicht-Helligkeit (Highlight Dimming)
- **Dekodierung**:
  $$\text{Index } i = \text{char} - \text{'a'}$$
  $$\text{Helligkeit in } \% = \begin{cases} 30 \%, & \text{wenn } i \le 7 \\ i \times 4 \%, & \text{wenn } i > 7 \end{cases}$$
  *Beispiel aus Trace:* `'r'` ($17$) $\rightarrow 17 \times 4 = \mathbf{68\ \%}$.
- **Kodierung (Senden)**:
  $$i = \text{clamp}(\text{percent} / 4, 7, 25) \implies \text{Befehl: } \mathbf{"BH" + (\text{'a'} + i) + "U"}$$

### 5. Grundlicht (Lowlight / Nightlight)
- **Dekodierung**:
  - `'a'` ($0$): Grundlicht ist **AUS** ($0\ \%$).
  - $> \text{'a'}$: $\text{prog} = (\text{char} - \text{'a'}) - 6 \implies \text{Helligkeit} = \frac{\text{prog} \times 30}{19} + 10$ % ($10\ \%$ bis $40\ \%$).
- **Kodierung (Senden)**:
  - Bei $0\ \%$ (Aus): $\mathbf{"BLaU"}$.
  - Bei $> 0\ \%$: $\text{prog} = \frac{(\text{percent} - 10) \times 19}{30}$, $\text{char} = \text{'a'} + \text{prog} + 6 \implies \mathbf{"BL" + \text{char} + "U"}$.
- **Dauer**: Befehl $\mathbf{"BS" + \text{char} + "U"}$ (`"BSaU"` = 24h / Ganze Nacht).

### 6. Betriebsmodus & Licht-Schalten
- **Dauerlicht AN**: Befehl $\mathbf{"BMdU"}$ (Modus 3, `'d'`).
  $\rightarrow$ In Home Assistant: `light/state = "ON"`, `mode/state = "Dauerlicht"` (`LampMode = 1`).
- **Sensor-Automatik (Licht AUS)**: Befehl $\mathbf{"BMbU"}$ (Modus 1, `'b'`).
  $\rightarrow$ In Home Assistant: `light/state = "OFF"`, `mode/state = "Sensor"` (`LampMode = 2`).
- **PIR Sensor aktiv**:
  `PIRActive = (mcuCfg.Distance > 0 && mcuCfg.Mode != 0)`.
- **Statusabfrage**: Befehl $\mathbf{"BFbU"}$.

---

## 6. Hardware-Unterschiede L 620 CAM vs. L 625 CAM SC

| Eigenschaft | Steinel L 620 CAM / XLED CAM 1 | Steinel L 625 CAM SC / Spot CAM |
|:---|:---|:---|
| **Kamera-SoC & Treiber** | Xiongmai / JFTech Sofia (Port 34567) | Nabto Edge P2P Direct Tunnel (Port 5592) |
| **Video-Stream** | **Natives Sofia NetIP (MsgID 1410/1412)** mit RTSP-Fallback | WebRTC Media DTLS/SRTP (1080p Full HD) |
| **Video-Aktivierung** | **Ressourcenschonendes On-Demand** (15s Standby-Cooldown)| Dauerhaft aktive P2P WebRTC-Session |
| **Mikrofon-Audio** | G.711 A-law (PCMA, 8000 Hz Mono) | G.711 $\mu$-law (PCMU, 8000 Hz Mono) |
| **Gegensprechen** | Xiongmai OPTalk (1434/1432) mit PCMU $\leftrightarrow$ PCMA | 20ms SRTP PCMU Chunks via WebRTC Audio |
| **MCU-Kommunikation** | Sofia NetIP OPTrans RS232 (`MsgTransSendData` 1572) | WebRTC DataChannel `test` (18-Byte Hex Frames) |
| **MCU Frame-Format** | **12-Byte ASCII-Format** (`B...U`) | **18-Byte Binär-Hex** (`5A 0F 0F...`) |
| **PIR-Stufen** | 3 diskrete Stufen (Min / Med / Max) | Stufenlos (0–100 % bzw. 1–10 m) |
| **Alarmsirene / Warnton** | **Bewegungsalarm** (`Detect.MotionDetect.[0]` `VoiceEnable`) | **Manuelle Sirene** (Befehl `alarm_voice_ctl`) |
| **SD-Karten Format** | H.264 MP4-Videos via `OPFileQuery` (MsgID 1440) | H.265 MP4-Videos via DataChannel `get_event_video` |
| **ONVIF Model** | Dynamisch als `"L 620 CAM"` gemeldet | Dynamisch als `"L 625 CAM SC"` gemeldet |

---

## 7. Video-Streaming Pipeline (Natives Sofia 34567 & RTSP Fallback 554)

### 1. Primärpfad: Natives Sofia Video-Streaming (`OPMonitor` MsgID 1410 / 1412)
Die L 620 Firmware besitzt auf ihrem internen RTSP-Server (Port 554) bekannte Speicherbegrenzungen und Pufferüberläufe. Die Bridge nutzt daher das native Xiongmai Sofia/NetIP Medienprotokoll als **bevorzugten Primärpfad**:
1. **Media Socket Connect**: Baut eine sekundäre TCP-Verbindung zu Port `34567` auf.
2. **Claim Handshake (`MsgMonitorClaimReq` 1413)**: Sendet `OPMonitor Claim` mit `StreamType: "Main"` (1080p) oder `"Extra1"` (360p).
3. **Monitor Start (`MsgMonitorReq` 1410)**: Startet den H.264 Datenstrom auf dem Socket.
4. **Frame-Demuxing (`MsgMonitorData` 1412)**:
   - Die Kamera überträgt rohe Video-Chunks mit Sofia-Headern.
   - I-Frames beginnen mit Magic `00 00 01 FC`, P-Frames mit `00 00 01 FD`.
   - Die Bridge extrahiert die NAL-Units und paketiert sie konform nach **RFC 6184** mittels `rtph264.Encoder` direkt in den RTSP-Server.

### 2. Ressourcenschonendes On-Demand Streaming
Der Xiongmai SoC erhitzt sich bei dauerhaftem 24/7-Videostreaming stark, was zu WLAN-Abbrüchen führen kann.
- **On-Demand Steuerung**: Der Sofia Video-Socket wird erst dann geöffnet, wenn mindestens ein Client (z. B. Home Assistant, Scrypted oder VLC) den RTSP-Stream abspielt (`ActiveClients() > 0`).
- **15s Standby-Cooldown**: Trennen alle Clients die Verbindung, bleibt der Stream für 15 Sekunden gepuffert (`defaultIdleGracePeriod`). Verbindet sich in dieser Zeit kein neuer Client, wird der Medien-Socket geschlossen und die Kamera in den Ruhemodus versetzt.

### 3. Fallback-Pfad: RTSP-Ingest (Port 554)
Schlägt die Verbindung zu Sofia Port 34567 fehl, schaltet die Bridge nahtlos auf den internen RTSP-Server der Kamera um:
- **Kanonischer Pfad**: `rtsp://<ip>:554/user=admin_password=***_channel=1_stream=0.sdp`
- **Zero-Packet Watchdog (5s)**: Schaltet sofort auf alternative URL-Pfade um, wenn nach `PLAY` keine RTP-Pakete eintreffen.
- **Silence Watchdog (15s)**: Erkennt Verbindungsabbrüche und initialisiert den Stream neu.

### 4. Sofia Video-Encoding & H.264 Erzwingung (`Simplify.Encode`)
Beim Start prüft die Bridge über `MsgConfigGetReq` (1040) die Konfiguration `Simplify.Encode`:
- Antwortet die Kamera mit `Compression: "H.265"` (HEVC), rekonfiguriert die Bridge die Kamera automatisch via `MsgConfigSetReq` (1042) auf `Compression: "H.264"`, um die volle Kompatibilität mit RTSP, go2rtc, Home Assistant und ONVIF Profile S sicherzustellen.

---

## 8. Zwei-Wege-Audio & Talk-Protokoll (OPTalk)

Das Gegensprechen über den Kameralautsprecher erfolgt über das Xiongmai Talk-Protokoll:
1. **Talk Claim (`MsgTalkClaimV2Req` 1434)**:
   - Client sendet Claim mit `AudioFormat: "G711_ALAW"`, `SampleRate: 8000`, `Channels: 1`, `Bits: 16`.
2. **Audio-Transkodierung (PCMU $\leftrightarrow$ PCMA)**:
   - Apple HomeKit / Scrypted liefern standardmäßig G.711 $\mu$-law (**PCMU**).
   - Der DAC-Verstärker der L 620 CAM erwartet zwingend G.711 A-law (**PCMA**).
   - Die Bridge transkodiert Audio-RTP-Pakete in Echtzeit via `audio.TranscodePCMUToPCMA` und versendet sie als unverschlüsselte `MsgTalkAudioData` (1432) Chunks.
3. **Auto-Aktivierung**:
   - Der Talk-Kanal wird beim ersten eingehenden Backchannel-Audio-Paket automatisch geöffnet.

---

## 9. Alarm- & Bewegungserkennung (MsgID 1500 & Summer)

Die L 620 CAM unterstützt zwei komplementäre Mechanismen zur Bewegungsüberwachung:

### 1. Live Alarm-Push (NetIP MsgID 1500)
- Erkennt der interne Bildprozessor oder PIR der Leuchte eine Bewegung, sendet die Kamera unaufgefordert eine `MsgAlarmPush` (1500) Benachrichtigung:
  ```json
  {"Event": "MotionDetect", "Status": "Start"}
  ```
- Die Bridge empfängt diesen Event über `handleAlarmEvent`, meldet unmittelbar Bewegung an den `eventBus` (und damit an ONVIF PullPoint & MQTT) und setzt einen 10-Sekunden Auto-Reset Timer.
- Sendet die Kamera `Status: "Stop"`, wird der Bewegungsalarm sofort zurückgesetzt.

### 2. Bewegungsalarm-Summer (Siren Buzzer)
- Über die Konfiguration `Detect.MotionDetect.[0]` (`EventHandler.VoiceEnable: true` bzw. `BeepEnable: true`) kann der physische Warnton der Leuchte aktiviert werden.
- Bei Bewegung ertönt ein ca. 60-sekündiger Signalton direkt an der Leuchte.

---

## 10. MicroSD-Karten Edge Storage (OPFileQuery MsgID 1440/1441)

Die L 620 CAM speichert Videoaufnahmen als H.264/MP4 Dateien auf der internen MicroSD-Karte:

1. **Dateisuche (`OPFileQuery` 1440)**:
   ```json
   {
     "Name": "OPFileQuery",
     "OPFileQuery": {
       "BeginTime": "2026-10-19 18:00:00",
       "EndTime": "2026-10-20 18:00:00",
       "Channel": 0,
       "Type": "h264",
       "Event": "*"
     }
   }
   ```
2. **SoC-Schutz bei Initial-Query**:
   - Um bei vollen SD-Karten einen CPU-Lockout der Kamera zu verhindern, verwendet die Bridge beim Start ein pragmatisches **4-Stunden-Zeitfenster**.
3. **Download & Playback**:
   - Aufnahmen werden über den Sofia Dateitransfer oder HTTP heruntergeladen und lokal in `/data/recordings` gecacht.
   - Der REST-Endpunkt liefert Videos mit vollständiger HTTP 206 Partial-Content (Range Requests) Unterstützung aus.

---

## 11. Resilienz, Reconnect-Supervisor & Sicherheit

### 1. Autonomer Sofia Reconnect-Supervisor (`superviseSofiaLoop`)
- Startet non-blocking als autonomer Hintergrund-Worker in `pkg/xiongmai/driver.go`.
- Versucht sofort bzw. bei Verbindungsverlust im adaptiven Backoff-Intervall (5s $\rightarrow$ 10s $\rightarrow$ 20s $\rightarrow$ 40s $\rightarrow$ 60s max) die Wiederverbindung zu Sofia Port 34567.
- Führt bei bestehender Verbindung alle 15–20 Sekunden einen Keepalive-Heartbeat (`1006`) durch. Schlägt dieser fehl, wird der Socket ordnungsgemäß geschlossen und die Reconnect-Sequenz eingeleitet.
- Nach erfolgreichem Reconnect werden automatisch `EnsureH264Encoding()`, `syncInitialState()` und der On-Demand Media-Check ausgeführt.

### 2. Port 554 Dial Failure Short-Circuiting & Exponentieller Backoff
- Erkennt TCP Dial-Fehler (`net.OpError` / `connection refused`) sofort beim ersten Kandidaten.
- Bricht die sequentielle Prüfung aller 12 Kandidaten-URLs unmittelbar ab (`short-circuit`), anstatt für jeden Kandidaten Timeouts abzuwarten.
- Nutzt für Neuversuche einen adaptiven exponentiellen Backoff (5s $\rightarrow$ 10s $\rightarrow$ 20s $\rightarrow$ 40s $\rightarrow$ 60s max).

### 3. Passwort-Maskierung (CWE-312 Konformität)
- Passwörter werden in Log-Ausgaben zu 100 % maskiert (`strings.Repeat("*", len(pwd))`). Es werden keinerlei Klartext-Zeichen preisgegeben.

### 4. MQTT Initial-State Guarding
- Beim Verbindungsaufbau zu MQTT (`OnConnect`) wird der Gerätestatus nur dann publiziert, wenn er bereits durch echte Kamera-Telemetrie befüllt wurde (`LampMode > 0 || Lux >= 2 || Highlight > 0`). Dies verhindert falsche Dummy-Meldungen während des Bootvorgangs.
