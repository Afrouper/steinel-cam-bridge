# Steinel L 620 CAM / XLED CAM 1 — Xiongmai Sofia Protokoll- & Treiber-Leitfaden

Dieses Dokument beschreibt die hardware- und protokollspezifische Architektur der **Steinel L 620 CAM** (sowie baugleicher Modelle wie **XLED CAM 1**), basierend auf dem Xiongmai Sofia / NetIP Protokoll und den Erkenntnissen aus der Dekompilierung der offiziellen Steinel CAM Android App (`de.steinel.cam`).

---

## 1. High-Level Protokoll-Stack

Im Gegensatz zur **Steinel L 625 CAM SC** (Nabto Edge P2P + WebRTC DataChannel) basiert die **L 620 CAM** auf einer Xiongmai (XM / JFTech) IPC-Plattform mit SoC-Firmware (z. B. `V4.03.R12`):

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Steinel L 620 CAM Hardware                            │
│  ├─ Video/Audio: Lokaler nativer RTSP-Stream (TCP Port 554, 1080p Full HD H.264/AAC)│
│  ├─ Talk-Backchannel: Xiongmai Talk Protokoll (MsgID 1432/1434, G.711u)     │
│  └─ Steuerung & MCU: Xiongmai Sofia / NetIP Protokoll (TCP Port 34567)      │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ Port 34567 (NetIP)
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                  Sofia NetIP Verbindungs-Lifecycle                          │
│  1. Probe / Claim: MsgMonitorClaimReq (1413) -> RSA-1024 Public Key        │
│  2. Login: MsgLoginReq (1000) mit RSA PKCS#1 v1.5 + AES-128-CBC Session-Key│
│  3. Serieller Tunnel: MsgTransStartReq (1578) -> OPTrans RS232 aktiv        │
│  4. Steuerbefehle: MsgTransSendData (1572) via AES-128-CBC auf Kanal 1      │
│  5. Telemetrie: MsgTransSerialData (1582) unverschlüsselte ASCII-Frames     │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Authentifizierung & Symmetrische Session-Verschlüsselung

Moderne L 620 Firmware-Versionen erzwingen für Konfiguration und Steuerbefehle verschlüsselte Kommunikation:

1. **RSA-Claim Handshake (MsgID 1413 / 1414)**:
   - Client sendet `OPMonitor Claim` (1413).
   - Kamera meldet Public Key (RSA 1024 Bit) und `DataEncryptionType: {"AES": true, "AESV2": true}` sowie `NotEncryptMsgID: [1000, 1001, 1008, 1009, 1413, 1414, 1432, 1434, 1435, 1580, 1582]`.
2. **Login mit CommunicateKey (MsgID 1000 / 1001)**:
   - Client generiert einen zufälligen 16-stelligen alphanumerischen Session-Key (`CommunicateKey`).
   - `UserName`, `PassWord` und `CommunicateKey` werden mit RSA PKCS#1 v1.5 verschlüsselt.
   - Äußere Payload wird mit dem NetIP-Standardschlüssel (AES-128-CBC, Zero-IV, PKCS#7 Padding) verschlüsselt und mit `0x00` terminiert.
3. **Kanal-1 Payload-Verschlüsselung**:
   - Alle nachfolgenden Steuerbefehle auf Kanal 1 (wie `1578` OPTrans-Start, `1572` MCU-Kommandos, `1006` KeepAlive) werden symmetrisch mit dem ausgehandelten `CommunicateKey` verschlüsselt (AES-128-CBC, Zero-IV, Base64).
   - Eingehende Antworten auf Kanal 1 (wie `1579`, `1573`, `1007`) werden symmetrisch mit demselben Schlüssel entschlüsselt.
   - Telemetrie (`1582`) und Talk-Audio (`1432`) verbleiben gemäß `NotEncryptMsgID` unverschlüsselt.

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

| Eigenschaft | Steinel L 620 CAM / XLED CAM 1 | Steinel L 625 CAM SC |
|:---|:---|:---|
| **Kamera-SoC & Treiber** | Xiongmai / JFTech Sofia (Port 34567) | Nabto Edge P2P Direct Tunnel (Port 5592) |
| **Kamera-Stream** | Nativer lokaler RTSP-Server (Port 554, 1080p Full HD) | WebRTC Media DTLS/SRTP (1080p Full HD) |
| **MCU-Kommunikation** | Sofia NetIP OPTrans RS232 (`MsgTransSendData` 1572) | WebRTC DataChannel `test` (18-Byte Hex Frames) |
| **Alarmsirene / Warnton** | **Bewegungsalarm** (`Detect.MotionDetect.[0]` `VoiceEnable: true` / "Push and Alarm") | **Manuelle Sirene** (18-Byte Frame `5A...06` schaltet Sirene) |
| **Licht-Schalten** | Modus-Umschaltung (`"BMdU"` vs. `"BMbU"`) | UART Byte-Flag (0x01 / 0x00) |
| **PIR-Stufen** | 3 diskrete Stufen (Min / Med / Max) | Stufenlos (0–100 % bzw. 1–10 m) |
| **Telemetrie-Übertragung** | Plaintext ASCII-Push via MsgID 1582 (gepuffert) | Binäre UART-Frames auf DataChannel `test` |
| **ONVIF Model** | Dynamisch als `"L 620 CAM"` gemeldet | Dynamisch als `"L 625 CAM SC"` gemeldet |
