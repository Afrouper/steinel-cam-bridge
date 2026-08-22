module steinel-cam-bridge

go 1.26.0

replace github.com/bluenviron/gortsplib/v4 => ./third_party/gortsplib

require (
	github.com/bluenviron/gortsplib/v4 v4.11.2
	github.com/bluenviron/mediacommon v1.13.1
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/gen2brain/aac-go v0.0.0-20230119102159-ef1e76509d21
	github.com/google/uuid v1.6.0
	github.com/pion/rtcp v1.2.17
	github.com/pion/rtp v1.10.5
	github.com/pion/webrtc/v4 v4.2.18
	github.com/stretchr/testify v1.12.1
	github.com/zaf/g711 v1.4.0
)

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/pion/datachannel v1.6.2 // indirect
	github.com/pion/dtls/v3 v3.1.5 // indirect
	github.com/pion/ice/v4 v4.4.0 // indirect
	github.com/pion/interceptor v0.1.47 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/sctp v1.11.1 // indirect
	github.com/pion/sdp/v3 v3.0.19 // indirect
	github.com/pion/srtp/v3 v3.0.12 // indirect
	github.com/pion/stun/v3 v3.1.6 // indirect
	github.com/pion/transport/v4 v4.0.2 // indirect
	github.com/pion/turn/v5 v5.0.12 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/time v0.14.0 // indirect
)
