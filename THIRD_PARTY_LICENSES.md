# Third-Party Licenses and Notices

This project uses and integrates with several third-party libraries and SDKs.

---

## Nabto Edge Client SDK
- **Copyright:** © Nabto ApS (https://www.nabto.com)
- **Repository:** https://github.com/nabto/nabto-client-sdk-releases
- **Usage:** Precompiled dynamic client library (`libnabto_client`) downloaded during build/setup to establish P2P signaling with Nabto-enabled devices.
- **Third-Party components bundled in Nabto Client SDK:**
  - **MbedTLS:** Apache License 2.0 (https://github.com/ARMmbed/mbedtls)
  - **Boost Software License:** (https://www.boost.org)
  - **TinyCBOR:** MIT License (https://github.com/intel/tinycbor)
  - **JSON for Modern C++:** MIT License (https://github.com/nlohmann/json)
  - **fmtlib:** BSD 2-Clause License (https://github.com/fmtlib/fmt)

All rights, trademarks, and copyrights regarding Nabto remain with Nabto ApS.

---

## Go Open-Source Dependencies

This project directly depends on the following Go open-source libraries:

### 1. WebRTC & RTP / RTCP Stack
- **Pion WebRTC, RTP, RTCP, DTLS, ICE** (`github.com/pion/webrtc/v4`, `github.com/pion/rtp`, `github.com/pion/rtcp`, `github.com/pion/dtls/v3`, `github.com/pion/ice/v4`)
  - **License:** MIT License (Copyright © Pion authors)
  - **Repository:** https://github.com/pion/webrtc

### 2. RTSP Server & Media Utilities
- **gortsplib** (`github.com/bluenviron/gortsplib/v4`)
  - **License:** MIT License (Copyright © Alessandro Pezzato)
  - **Repository:** https://github.com/bluenviron/gortsplib
- **mediacommon** (`github.com/bluenviron/mediacommon`)
  - **License:** MIT License (Copyright © Alessandro Pezzato)
  - **Repository:** https://github.com/bluenviron/mediacommon

### 3. Audio Transcoding & Codecs
- **VisualOn AAC Encoder (aac-go)** (`github.com/gen2brain/aac-go`)
  - **License:** Apache License 2.0 (Copyright © VisualOn, Inc. & gen2brain)
  - **Repository:** https://github.com/gen2brain/aac-go
- **G.711 Audio Codec** (`github.com/zaf/g711`)
  - **License:** BSD 3-Clause License (Copyright © 2019 Zaf)
  - **Repository:** https://github.com/zaf/g711

### 4. MQTT & IoT Connectivity
- **Eclipse Paho MQTT** (`github.com/eclipse/paho.mqtt.golang`)
  - **License:** Eclipse Public License 2.0 / Eclipse Distribution License 1.0 (Copyright © Eclipse Foundation)
  - **Repository:** https://github.com/eclipse/paho.mqtt.golang

### 5. Utility Libraries
- **Google UUID** (`github.com/google/uuid`)
  - **License:** BSD 3-Clause License (Copyright © Google LLC)
  - **Repository:** https://github.com/google/uuid
