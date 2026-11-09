# Third-Party Licenses and Notices

This project uses and integrates with several third-party libraries, SDKs, and container components.

---

## Nabto Edge Client SDK
- **Copyright:** © Nabto ApS (https://www.nabto.com)
- **Repository:** https://github.com/nabto/nabto-client-sdk-releases
- **Usage:** Precompiled dynamic client library (`libnabto_client`) downloaded dynamically during build/setup to establish P2P signaling with Nabto-enabled devices.
- **Third-Party components bundled in Nabto Client SDK:**
  - **MbedTLS:** Apache License 2.0 (https://github.com/ARMmbed/mbedtls)
  - **Boost Software License:** (https://www.boost.org)
  - **TinyCBOR:** MIT License (https://github.com/intel/tinycbor)
  - **JSON for Modern C++:** MIT License (https://github.com/nlohmann/json)
  - **fmtlib:** BSD 2-Clause License (https://github.com/fmtlib/fmt)

All rights, trademarks, and copyrights regarding Nabto remain with Nabto ApS.

---

## Container & Runtime Environment

### 1. Google Distroless Base Image
- **Component:** `gcr.io/distroless/cc-debian12`
- **Copyright:** © Google LLC
- **License:** Apache License 2.0
- **Repository:** https://github.com/GoogleContainerTools/distroless
- **Usage:** Minimal container runtime image providing C runtime (`glibc`) without shell or package managers.

### 2. FFmpeg Static Multi-Arch Binary
- **Component:** `mwader/static-ffmpeg:7.1` (installed as `/usr/local/bin/ffmpeg`)
- **Copyright:** © FFmpeg developers & Mattias Wadman
- **License:** GNU Lesser General Public License (LGPL) version 2.1 or later / GNU General Public License (GPL) version 3.0
- **Repository:** https://github.com/mwader/static-ffmpeg / https://ffmpeg.org
- **Usage:** Multi-arch static binary used by `pkg/storage/extractor.go` to extract 5-second JPEG keyframe thumbnails from MicroSD MP4/HEVC video recordings.

---

## Vendored Native C Libraries

### VisualOn AAC Encoder (`aacenc`)
- **Location:** `pkg/audio/internal/aac/external/aacenc`
- **Copyright:** © 2011-2012 VisualOn, Inc. & gen2brain
- **License:** Apache License 2.0
- **Source:** https://github.com/gen2brain/aac-go
- **Usage:** Embedded in-memory AAC-LC encoder for real-time audio transcoding (PCMU 8 kHz $\rightarrow$ AAC-LC 16 kHz) for Apple HomeKit Secure Video / Scrypted.

---

## Go Open-Source Dependencies

This project directly depends on the following Go open-source libraries:

### 1. WebRTC & RTP / RTCP / DTLS Stack
- **Pion WebRTC, RTP, RTCP, DTLS, ICE, SCTP, SDP, SRTP, STUN, TURN, Transport, Interceptor, DataChannel**
  - Packages:
    - `github.com/pion/webrtc/v4`
    - `github.com/pion/rtp`
    - `github.com/pion/rtcp`
    - `github.com/pion/dtls/v3`
    - `github.com/pion/ice/v4`
    - `github.com/pion/sctp`
    - `github.com/pion/sdp/v3`
    - `github.com/pion/srtp/v3`
    - `github.com/pion/stun/v4`
    - `github.com/pion/turn/v5`
    - `github.com/pion/transport/v5`
    - `github.com/pion/mdns/v2`
    - `github.com/pion/logging`
    - `github.com/pion/interceptor`
    - `github.com/pion/datachannel`
    - `github.com/pion/randutil`
  - **License:** MIT License (Copyright © Pion authors)
  - **Repository:** https://github.com/pion/webrtc

### 2. RTSP Server & Media Utilities
- **gortsplib** (`github.com/bluenviron/gortsplib/v4`)
  - **License:** MIT License (Copyright © Alessandro Pezzato)
  - **Repository:** https://github.com/bluenviron/gortsplib
- **mediacommon** (`github.com/bluenviron/mediacommon`)
  - **License:** MIT License (Copyright © Alessandro Pezzato)
  - **Repository:** https://github.com/bluenviron/mediacommon

### 3. Audio Codecs
- **G.711 Audio Codec** (`github.com/zaf/g711`)
  - **License:** BSD 3-Clause License (Copyright © 2019 Zaf)
  - **Repository:** https://github.com/zaf/g711

### 4. MQTT & IoT Connectivity
- **Eclipse Paho MQTT** (`github.com/eclipse/paho.mqtt.golang`)
  - **License:** Eclipse Public License 2.0 / Eclipse Distribution License 1.0 (Copyright © Eclipse Foundation)
  - **Repository:** https://github.com/eclipse/paho.mqtt.golang

### 5. Network & WebSocket Utilities
- **Gorilla WebSocket** (`github.com/gorilla/websocket`)
  - **License:** BSD 2-Clause License (Copyright © 2013 The Gorilla WebSocket Authors)
  - **Repository:** https://github.com/gorilla/websocket
- **wlynxg anet** (`github.com/wlynxg/anet`)
  - **License:** Apache License 2.0
  - **Repository:** https://github.com/wlynxg/anet

### 6. Core Utilities & Testing
- **Google UUID** (`github.com/google/uuid`)
  - **License:** BSD 3-Clause License (Copyright © Google LLC)
  - **Repository:** https://github.com/google/uuid
- **Stretchr Testify** (`github.com/stretchr/testify`)
  - **License:** MIT License (Copyright © 2012-2020 Mat Ryer, Tyler Bunnell and contributors)
  - **Repository:** https://github.com/stretchr/testify
- **YAML v3** (`go.yaml.in/yaml/v3`)
  - **License:** MIT License / Apache License 2.0
  - **Repository:** https://github.com/go-yaml/yaml

### 7. Go Sub-Repositories (`golang.org/x/*`)
- **Crypto, Net, Sync, Sys, Time**
  - `golang.org/x/crypto`
  - `golang.org/x/net`
  - `golang.org/x/sync`
  - `golang.org/x/sys`
  - `golang.org/x/time`
  - **License:** BSD 3-Clause License (Copyright © The Go Authors)
  - **Repository:** https://go.googlesource.com
