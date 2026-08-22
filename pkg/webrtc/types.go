package webrtc

import "encoding/json"

type SignalMessageType int

const (
	TypeOffer        SignalMessageType = 0
	TypeAnswer       SignalMessageType = 1
	TypeICECandidate SignalMessageType = 2
	TypeTurnRequest  SignalMessageType = 3
	TypeTurnResponse SignalMessageType = 4
)

type SignalMessage struct {
	Type     SignalMessageType      `json:"type"`
	Data     string                 `json:"data,omitempty"`
	Metadata *SignalMessageMetadata `json:"metadata,omitempty"`
}

type MetadataTrack struct {
	Mid     string `json:"mid"`
	TrackID string `json:"trackId"`
	Error   string `json:"error,omitempty"`
}

type SignalMessageMetadata struct {
	NoTrickle bool            `json:"no_trickle,omitempty"`
	Status    string          `json:"status,omitempty"`
	Tracks    []MetadataTrack `json:"tracks,omitempty"`
}

type TurnServer struct {
	Hostname string `json:"hostname"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type ICEServerConfig struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type TurnResponsePayload struct {
	Servers     []TurnServer      `json:"servers,omitempty"`
	ICEServers  []ICEServerConfig `json:"iceServers,omitempty"`
	TurnServers []TurnServer      `json:"turn_servers,omitempty"`
}

type SDPWrapper struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type ICECandidateWrapper struct {
	Candidate string `json:"candidate"`
	SDPMid    string `json:"sdpMid"`
}

func MarshalSignalMessage(msg *SignalMessage) ([]byte, error) {
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return append(b, 0x00), nil
}

func UnmarshalSignalMessage(raw []byte) (*SignalMessage, error) {
	if len(raw) > 0 && raw[len(raw)-1] == 0x00 {
		raw = raw[:len(raw)-1]
	}
	var msg SignalMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
