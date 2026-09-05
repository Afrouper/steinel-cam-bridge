package driver

import (
	"context"
	"errors"

	"github.com/pion/rtp"

	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
)

// ErrCameraOffline is returned when an operation is requested but no camera connection is active.
var ErrCameraOffline = errors.New("camera driver offline")

// CameraDriver defines the unified domain interface for all Steinel camera models.
// It abstracts model-specific protocols (Nabto Edge/WebRTC vs. Xiongmai Sofia/DVRIP)
// into a standardized contract for lifecycle management, streaming, sensor telemetry, and controls.
type CameraDriver interface {
	// Lifecycle
	Run(ctx context.Context) error
	Close() error

	// Video & Stream
	SetResolution(res string) error
	RequestKeyframe()

	// Lighting, Sensor & Alarm Controls
	SetLampState(mode string) error
	SetHighlight(percent int) error
	SetHighlightTime(seconds int) error
	SetLowlight(percent int) error
	SetLowlightTime(timeVal int) error
	SetPIRSensitivity(percent int) error
	SetLuxThreshold(lux int) error
	SetSiren(on bool) error

	// Storage & Two-Way Audio
	GetRecordingProvider() storage.RecordingProvider
	WriteAudioBackchannel(pkt *rtp.Packet) error
}
