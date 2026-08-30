package nabto

// Config holds parameters for Nabto Edge connections.
type Config struct {
	CameraIP   string
	CameraPort int
	ProductID  string
	DeviceID   string
	SCT        string
	PairPwd    string
	KeyPath    string
	IsBeta     bool
	ClientName string
}

// Driver defines the common interface for communicating with a Nabto Edge camera.
// Implemented by CGoClient (using libnabto_client) and PureClient (pure Go driver).
type Driver interface {
	Connect() error
	Close()
	GetSignalingPort() (uint32, error)
	RequestTracks() (uint16, error)
	OpenSignalingStream(port uint32) (StreamDriver, error)
}

// StreamDriver defines the message-based streaming interface for WebRTC signaling.
type StreamDriver interface {
	ReadMsg() ([]byte, error)
	WriteMsg(payload []byte) error
	Abort()
	Close()
}
