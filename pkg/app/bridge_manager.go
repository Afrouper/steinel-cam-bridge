package app

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

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

// ReconnectSession triggers an immediate reconnection of the camera driver session.
func (m *BridgeManager) ReconnectSession() error {
	d := m.getDriver()
	if d != nil {
		return d.ReconnectSession()
	}
	return driver.ErrCameraOffline
}

// SetSystemDateAndTime synchronizes the date, time and timezone on the camera hardware.
func (m *BridgeManager) SetSystemDateAndTime(t time.Time, tz string) error {
	d := m.getDriver()
	if d != nil {
		if timeDriver, ok := d.(interface {
			SetSystemDateAndTime(t time.Time, tz string) error
		}); ok {
			return timeDriver.SetSystemDateAndTime(t, tz)
		}
	}
	return nil
}

// Reboot triggers a controlled camera reboot if supported by the active driver.
func (m *BridgeManager) Reboot() error {
	d := m.getDriver()
	if d != nil {
		if rebootDriver, ok := d.(interface {
			Reboot() error
		}); ok {
			return rebootDriver.Reboot()
		}
	}
	return nil
}

// cameraSettings is implemented by drivers whose camera exposes its own
// settings (timestamp overlay, speaker volume, detection, recording, update).
type cameraSettings interface {
	SetTimestampPosition(position int) error
	SetSpeakerVolume(percent int) error
	SetDetectMode(mode string) error
	SetPixelChangeDetection(on bool) error
	SetDrawFrame(on bool) error
	SetAIPersonSensitivity(level int) error
	SetRecordMode(mode int) error
	InstallUpdate(target string) error
}

var errNoCameraSettings = errors.New("camera settings are not supported by this camera model")

func (m *BridgeManager) cameraSettings() (cameraSettings, error) {
	d := m.getDriver()
	if d == nil {
		return nil, driver.ErrCameraOffline
	}
	cs, ok := d.(cameraSettings)
	if !ok {
		return nil, errNoCameraSettings
	}
	return cs, nil
}

// SetTimestampPosition switches the timestamp overlay off (0) or moves it to a corner (1-4).
func (m *BridgeManager) SetTimestampPosition(position int) error {
	cs, err := m.cameraSettings()
	if err != nil {
		return err
	}
	return cs.SetTimestampPosition(position)
}

// SetSpeakerVolume sets the volume of the camera's speaker (0-100).
func (m *BridgeManager) SetSpeakerVolume(percent int) error {
	cs, err := m.cameraSettings()
	if err != nil {
		return err
	}
	return cs.SetSpeakerVolume(percent)
}

// SetDetectMode sets the camera-side detection mode.
func (m *BridgeManager) SetDetectMode(mode string) error {
	cs, err := m.cameraSettings()
	if err != nil {
		return err
	}
	return cs.SetDetectMode(mode)
}

// SetPixelChangeDetection switches the lamp's "Pixel Change Detection" (light reacts to picture changes).
func (m *BridgeManager) SetPixelChangeDetection(on bool) error {
	cs, err := m.cameraSettings()
	if err != nil {
		return err
	}
	return cs.SetPixelChangeDetection(on)
}

// SetDrawFrame switches the detection boxes burnt into the picture on or off.
func (m *BridgeManager) SetDrawFrame(on bool) error {
	cs, err := m.cameraSettings()
	if err != nil {
		return err
	}
	return cs.SetDrawFrame(on)
}

// SetAIPersonSensitivity sets the person detection sensitivity (2, 5 or 7).
func (m *BridgeManager) SetAIPersonSensitivity(level int) error {
	cs, err := m.cameraSettings()
	if err != nil {
		return err
	}
	return cs.SetAIPersonSensitivity(level)
}

// SetRecordMode chooses what makes the camera record to its SD card (2, 3 or 4).
func (m *BridgeManager) SetRecordMode(mode int) error {
	cs, err := m.cameraSettings()
	if err != nil {
		return err
	}
	return cs.SetRecordMode(mode)
}

// InstallUpdate starts an update the camera offers ("camera" or "mcu").
func (m *BridgeManager) InstallUpdate(target string) error {
	cs, err := m.cameraSettings()
	if err != nil {
		return err
	}
	return cs.InstallUpdate(target)
}

// debugCommander is implemented by drivers that can pass raw commands to the camera.
type debugCommander interface {
	DebugCommand(cmd, action string, info map[string]interface{}, timeout time.Duration) ([]json.RawMessage, error)
}

// DebugCommand forwards a raw diagnostic command to the camera driver.
func (m *BridgeManager) DebugCommand(cmd, action string, info map[string]interface{}, timeout time.Duration) ([]json.RawMessage, error) {
	d := m.getDriver()
	if d == nil {
		return nil, driver.ErrCameraOffline
	}
	dc, ok := d.(debugCommander)
	if !ok {
		return nil, errors.New("diagnostic commands are not supported by this camera model")
	}
	return dc.DebugCommand(cmd, action, info, timeout)
}
