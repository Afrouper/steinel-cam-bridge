package app

import (
	"sync"

	"github.com/pion/rtp"

	"github.com/Afrouper/steinel-cam-bridge/pkg/driver"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
)

// BridgeManager is a thread-safe proxy that forwards control commands and audio packets
// to the actively registered CameraDriver. It completely decouples ONVIF, RTSP, and MQTT
// from concrete camera protocols and hardware generations.
type BridgeManager struct {
	driver driver.CameraDriver
	mu     sync.RWMutex
}

// NewBridgeManager initializes a new empty BridgeManager.
func NewBridgeManager() *BridgeManager {
	return &BridgeManager{}
}

// SetDriver binds the active CameraDriver instance.
func (m *BridgeManager) SetDriver(d driver.CameraDriver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.driver = d
}

func (m *BridgeManager) getDriver() driver.CameraDriver {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.driver
}

// SetResolution updates the video stream resolution.
func (m *BridgeManager) SetResolution(res string) error {
	d := m.getDriver()
	if d != nil {
		return d.SetResolution(res)
	}
	return nil
}

// SetLampState turns the main lamp ON or OFF.
func (m *BridgeManager) SetLampState(mode string) error {
	d := m.getDriver()
	if d != nil {
		return d.SetLampState(mode)
	}
	return driver.ErrCameraOffline
}

// SetHighlight sets the primary light brightness percentage (10-100%).
func (m *BridgeManager) SetHighlight(percent int) error {
	d := m.getDriver()
	if d != nil {
		return d.SetHighlight(percent)
	}
	return driver.ErrCameraOffline
}

// SetHighlightTime sets the primary light on-duration in seconds.
func (m *BridgeManager) SetHighlightTime(seconds int) error {
	d := m.getDriver()
	if d != nil {
		return d.SetHighlightTime(seconds)
	}
	return driver.ErrCameraOffline
}

// SetLowlight sets the nightlight brightness percentage (0-50%).
func (m *BridgeManager) SetLowlight(percent int) error {
	d := m.getDriver()
	if d != nil {
		return d.SetLowlight(percent)
	}
	return driver.ErrCameraOffline
}

// SetLowlightTime sets the nightlight duration.
func (m *BridgeManager) SetLowlightTime(timeVal int) error {
	d := m.getDriver()
	if d != nil {
		return d.SetLowlightTime(timeVal)
	}
	return driver.ErrCameraOffline
}

// SetPIRSensitivity sets the motion sensor detection sensitivity (percentage).
func (m *BridgeManager) SetPIRSensitivity(percent int) error {
	d := m.getDriver()
	if d != nil {
		return d.SetPIRSensitivity(percent)
	}
	return driver.ErrCameraOffline
}

// SetLuxThreshold sets the twilight switching threshold in lux (2-1000 lx).
func (m *BridgeManager) SetLuxThreshold(lux int) error {
	d := m.getDriver()
	if d != nil {
		return d.SetLuxThreshold(lux)
	}
	return driver.ErrCameraOffline
}

// SetSiren triggers or mutes the camera alarm siren.
func (m *BridgeManager) SetSiren(on bool) error {
	d := m.getDriver()
	if d != nil {
		return d.SetSiren(on)
	}
	return nil
}

// RequestKeyframe requests an immediate IDR video keyframe from the camera.
func (m *BridgeManager) RequestKeyframe() {
	d := m.getDriver()
	if d != nil {
		d.RequestKeyframe()
	}
}

// GetRecordingProvider returns the SDCardManager of the currently active driver.
func (m *BridgeManager) GetRecordingProvider() storage.RecordingProvider {
	d := m.getDriver()
	if d != nil {
		return d.GetRecordingProvider()
	}
	return nil
}

// WriteAudioBackchannel routes incoming two-way audio RTP packets to the camera speaker.
func (m *BridgeManager) WriteAudioBackchannel(pkt *rtp.Packet) error {
	d := m.getDriver()
	if d != nil {
		return d.WriteAudioBackchannel(pkt)
	}
	return driver.ErrCameraOffline
}
