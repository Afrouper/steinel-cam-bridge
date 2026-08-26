# Steinel L 620 CAM / XLED CAM 1 — Protocol Specification & Reverse Engineering Guide

This document specifies the network architecture, discovery mechanism, authentication protocol, MCU hardware serial control, 2-way audio streaming, and configuration endpoints reverse-engineered from the **Steinel L 620 CAM** (Generation 1 Xiongmai Sofia architecture) and the **Steinel CAM iOS / macOS / Android Applications**.

---

## 1. Device Architecture & Network Ports

The Steinel L 620 CAM differs fundamentally from the Generation 2 devices (L 625 CAM SC / Nabto Edge):

| Feature | Generation 1 (L 620 CAM / XLED CAM 1) | Generation 2 (L 625 CAM SC / Spot CAM) |
| :--- | :--- | :--- |
| **Primary Chipset** | Xiongmai Tech (XM) SoC (HI3518EV200 / XM530) | Nabto Edge Embedded Microcontroller |
| **Control Daemon** | Xiongmai Sofia Daemon (`TCP :34567`) | Nabto P2P RPC (`UDP :5592` / SCT) |
| **Discovery Protocol**| Xiongmai Broadcast Probe (`UDP :34569`) | Nabto mDNS / Local CoAP / Bluetooth QR |
| **Video Streaming** | Native RTSP Server (`TCP :554`) | WebRTC SRTP over Nabto Tunnel |
| **Audio Format** | G.711 A-law (8000 Hz, 8-bit mono) | AAC-ELD / Opus (16 kHz / 48 kHz) |
| **MCU Interconnect** | Transparent Serial Bridge (`OPTrans` / RS232) | Nabto CoAP Custom Frame (`5A0F0F...`) |

---

## 2. UDP Discovery Protocol (`UDP :34569`)

The Steinel iOS / Android app searches for cameras in the local subnet by broadcasting UDP discovery probes.

### 2.1 Request Header (`MsgSearchDeviceReq` / MsgID 1530)
- **Port:** UDP `34569`
- **Destination:** `255.255.255.255:34569` and subnet broadcast
- **Length:** 20 bytes (Sofia Protocol Header)
```
00000000  ff 00 00 00 00 00 00 00  00 00 00 00 00 00 fa 05  00 00 00 00
```
- `Magic`: `0xFF`
- `MsgID`: `1530` (`0x05FA` Little-Endian)
- `DataLength`: `0`

### 2.2 Response Format (`MsgSearchDeviceResp` / MsgID 1531)
The native ARM64 Mach-O iOS binary (`CDeviceV2::SearchDevices` at `0x10057d478` / `0x1005bc3c0`) enforces strict JSON field checks:
1. It searches for `NetWork.NetCommon.SN` (not `SerialNo` or `DeviceID`).
2. It verifies that `strlen(SN) == 16` (`0x10`).
3. It extracts `HostIP`, `TCPPort` (34567), `HostName`, `MAC`, `DeviceType`, `BuildDate`, `OtherFunction`, and `SystemInfo`.

```json
{
  "Ret": 100,
  "NetWork.NetCommon": {
    "SN": "0011223344556677",
    "HostIP": "192.168.88.86",
    "TCPPort": 34567,
    "SSLPort": 8443,
    "UDPPort": 34568,
    "HttpPort": 80,
    "GateWay": "192.168.88.1",
    "Submask": "255.255.255.0",
    "HostName": "Steinel-L620-CAM",
    "MAC": "00:11:22:33:44:55",
    "Version": "V4.02.R12.D4806531.10002.142100.00000",
    "BuildDate": "2023-01-01 12:00:00",
    "DeviceType": 0,
    "OtherFunction": "0x00000001",
    "RandomAcc": 0,
    "Pid": "Steinel_L620"
  },
  "SystemInfo": {
    "DeviceModel": "Steinel-L620-CAM",
    "SerialNo": "0011223344556677",
    "SoftWareVersion": "V4.02.R12.D4806531.10002.142100.00000",
    "BuildTime": "2023-01-01 12:00:00"
  }
}
```

---

## 3. Authentication & Password Hashing (`TCP :34567`)

When connecting to the Sofia daemon on TCP port 34567, the client logs in via `MsgLoginReq (1000)`.

### 3.1 Login Request
```json
{
  "EncryptType": "MD5",
  "LoginType": "DVRIP-Mobile",
  "UserName": "admin",
  "PassWord": "tlJwpbo6"
}
```
*(Note: `LoginType` must be `DVRIP-Mobile` or `DVRIP-Web`)*

### 3.2 Xiongmai 8-Character Password Hash Algorithm
Passwords are MD5 hashed and transformed into an 8-character Base62 string using byte-pair additions:
```go
func HashPassword(secret string) string {
    if secret == "" {
        return ""
    }
    digest := md5.Sum([]byte(secret))
    var result strings.Builder
    result.Grow(8)
    for i := 0; i < 8; i++ {
        pairSum := int(digest[2*i]) + int(digest[2*i+1])
        pairMod := pairSum % 0x3E
        var charCode byte
        if pairMod <= 9 {
            charCode = byte(pairMod + 0x30) // '0'-'9'
        } else if pairMod <= 35 {
            charCode = byte(pairMod + 0x37) // 'A'-'Z'
        } else {
            charCode = byte(pairMod + 0x3D) // 'a'-'z'
        }
        result.WriteByte(charCode)
    }
    return result.String()
}
```
**Verified Test Vectors:**
- Plaintext: `"admin"` $\rightarrow$ Hash: `"tlJwpbo6"`
- Plaintext: `"12345678A"` $\rightarrow$ Hash: `"EAyIB8vx"`

---

## 4. Steinel MCU Serial Protocol over `OPTrans` RS232

Hardware controls (lights, dimmers, PIR sensor, twilight threshold) are communicated via a transparent serial channel over Xiongmai `OPTrans` (`MsgTransStartReq` 1578 / `MsgTransSendData` 1572).

### 4.1 Channel Initiation
```json
{
  "Name": "OPTrans",
  "OPTrans": {
    "CommName": "RS232",
    "Action": "Start"
  },
  "SessionID": "0x0001869f"
}
```

### 4.2 MCU Command Set (Captured Live)
Commands follow the ASCII prefix-suffix format `B<CMD><PARAM>U`:

| Command String | Target Function | Description & Parameters |
| :--- | :--- | :--- |
| **`BFbU`** | Firmware Version Query | Queries MCU firmware version (Response e.g. `v16`) |
| **`BDmU...`** | PIR Detection Distance | Sets sensor distance range (1 – 10 meters) |
| **`BXpU...`** | Photosensitivity / Mode | Sensor Mode (Day / Night / Sensor preset) |
| **`BHpU...`** | Highlight Brightness | Main light dimming level (10% – 100%) |
| **`BLaU...`** | Lowlight Brightness | Basic light / Nightlight dimming level (0% – 50%) |
| **`BTdU...`** | Highlight Duration | Light stay-on duration (5s – 900s) |
| **`BLqU...`** | Twilight Lux Threshold | Twilight sensitivity threshold (2 – 1000 Lux) |

### 4.3 MCU Status Telemetry Frame
The MCU periodically broadcasts a 12-character telemetry string:
```
B <dist> <hld> <lux> <mode> <indicator> <lowlight> <highlight> <lld> <level> U
Example: "BubzbzfzazOU"
```
- Index 1: `u` (10m Distance)
- Index 2: `b` (60s Duration)
- Index 3: `z` (1000 Lux Twilight)
- Index 6: `z` (Lowlight / Nightlight level)
- Index 7: `f` (Highlight / Main light dimming)

---

## 5. Two-Way Audio (Gegensprechen) Protocol

### 5.1 Channel Setup (`MsgTalkClaimReq` 1434 / 1410)
```json
{
  "Name": "OPTalk",
  "OPTalk": {
    "Action": "Claim"
  },
  "SessionID": "0x0001869f"
}
```
**Response Format:**
```json
{
  "Ret": 100,
  "Name": "OPTalk",
  "AudioFormat": {
    "BitRate": 64,
    "SampleBit": 16,
    "SampleRate": 8000,
    "EncodeType": "G711_ALAW"
  }
}
```

### 5.2 Upload Control (`MsgTalkControlReq` 1430)
- `Action: "Start"` $\rightarrow$ Begins microphone upload
- `Action: "PauseUpload"` / `Action: "ResumeUpload"` $\rightarrow$ Pauses / resumes stream
- `Action: "Stop"` $\rightarrow$ Closes talk session

### 5.3 Audio Streaming (`MsgTalkAudioData` 1432)
Microphone audio is packetized into 328-byte G.711 A-law chunks wrapped in a 20-byte Sofia Header (`MsgID: 1432`, `DataLength: 328`).

---

## 6. Configuration Endpoints & Schemas

| Config Name | MsgID | Structure / Value |
| :--- | :--- | :--- |
| `fVideo.Volume.[0]` | 1042 / 1040 | `{"AudioMode": "Single", "LeftVolume": 80, "RightVolume": 80}` |
| `fVideo.InVolume.[0]` | 1042 / 1040 | `{"AudioMode": "Single", "LeftVolume": 80, "RightVolume": 80}` |
| `fVideo.AudioSupportType` | 1042 | `{"AudioIn": true, "AudioOut": true, "AudioInMode": "Single", "AudioOutMode": "Single"}` |
| `Simplify.Encode` | 1042 / 1040 | `[{"MainFormat": {"AudioEnable": true, "Resolution": "720P", "FPS": 25}}]` |
| `System.TimeZone` | 1042 / 1040 | `{"TimeZone": 60, "timeMin": 60}` |
| `General.Location` | 1042 / 1040 | `{"Language": "German", "VideoFormat": "PAL", "DateFormat": "YYYY-MM-DD"}` |
| `OPTimeSetting` | 1450 | `{"Name": "OPTimeSetting", "OPTimeSetting": "2026-08-25 20:20:29"}` |
| `Camera.Param.[0]` | 1042 | `{"ElecLevel": 50, "Gain": 50, "WhiteBlance": "Auto"}` |
| `Detect.MotionDetect.[0]` | 1042 | `{"Enable": true, "Level": 3, "Grid": ["0xFFFFFFFF"]}` |

---

## 7. Home Assistant & RTSP / ONVIF Integration

The standalone Go bridge (`cmd/steinel-bridge`):
1. Bridges the camera's native RTSP stream (`TCP :554`) to Home Assistant (`rtsp://<bridge-ip>:8554/live`).
2. Provides an embedded ONVIF Profile S/T server on port `8000`.
3. Bridges microphone audio from the RTSP Backchannel into `OPTalk` G.711 A-law packets.
4. Exposes MQTT Auto-Discovery entities for Home Assistant (`light`, `number.highlight`, `number.lowlight`, `number.lux_threshold`, `number.duration`, `number.pir_sensitivity`, `binary_sensor.motion`).
