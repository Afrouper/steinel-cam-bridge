package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/pion/rtp"

	"github.com/Afrouper/steinel-cam-bridge/pkg/mcu"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/Afrouper/steinel-cam-bridge/pkg/webrtc"
	"github.com/Afrouper/steinel-cam-bridge/pkg/xiongmai"
)

// BridgeManager is a thread-safe multiplexer delegating camera commands
// to either the active L 625 WebRTC Bridge or the active L 620 Xiongmai Driver.
type BridgeManager struct {
	currentBridge   *webrtc.Bridge
	currentXMDriver *xiongmai.Driver
	mu              sync.RWMutex
}

// NewBridgeManager initializes a new empty BridgeManager.
func NewBridgeManager() *BridgeManager {
	return &BridgeManager{}
}

// SetBridge sets the currently active WebRTC bridge for L 625.
func (m *BridgeManager) SetBridge(b *webrtc.Bridge) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentBridge = b
	m.currentXMDriver = nil
}

// SetXMDriver sets the currently active Xiongmai driver for L 620.
func (m *BridgeManager) SetXMDriver(d *xiongmai.Driver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentXMDriver = d
	m.currentBridge = nil
}

// SetResolution updates the video stream resolution.
func (m *BridgeManager) SetResolution(res string) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		return b.SetResolution(res)
	}
	if d != nil {
		return d.SetResolution(res)
	}
	return nil
}

// SetLampState turns the main lamp ON or OFF.
func (m *BridgeManager) SetLampState(mode string) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		return b.SetLampState(mode)
	}
	if d != nil {
		modeLower := strings.ToLower(mode)
		on := modeLower == "on" || modeLower == "1" || modeLower == "dauerlicht"
		return d.SetLamp(on)
	}
	return fmt.Errorf("camera bridge offline")
}

// SetHighlight sets the primary light brightness percentage (10-100%).
func (m *BridgeManager) SetHighlight(percent int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetHighlight(percent)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetDim(percent)
	}
	return fmt.Errorf("camera bridge offline")
}

// SetHighlightTime sets the primary light on-duration in seconds.
func (m *BridgeManager) SetHighlightTime(seconds int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetHighlightTime(seconds)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetDuration(seconds)
	}
	return fmt.Errorf("camera bridge offline")
}

// SetLowlight sets the nightlight brightness percentage (0-50%).
func (m *BridgeManager) SetLowlight(percent int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetLowlight(percent)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetNightlight(percent)
	}
	return fmt.Errorf("camera bridge offline")
}

// SetLowlightTime sets the nightlight duration.
func (m *BridgeManager) SetLowlightTime(timeVal int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetLowlightTime(timeVal)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetNightlightDuration(fmt.Sprintf("%dh", timeVal/60))
	}
	return fmt.Errorf("camera bridge offline")
}

// SetPIRSensitivity sets the motion sensor detection sensitivity (percentage).
func (m *BridgeManager) SetPIRSensitivity(percent int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetPIRSensitivity(percent)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		dist := percent / 10
		if dist <= 0 {
			dist = 1
		}
		return d.SetDistance(dist)
	}
	return fmt.Errorf("camera bridge offline")
}

// SetLuxThreshold sets the twilight switching threshold in lux (2-1000 lx).
func (m *BridgeManager) SetLuxThreshold(lux int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetLuxThreshold(lux)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetTwilight(lux)
	}
	return fmt.Errorf("camera bridge offline")
}

// SetSiren triggers or mutes the camera alarm siren.
func (m *BridgeManager) SetSiren(on bool) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return nil
	}
	cmd := map[string]interface{}{
		"play": on,
	}
	return b.SendCommand("alarm_voice_ctl", cmd)
}

// RequestKeyframe requests an immediate IDR video keyframe from the camera.
func (m *BridgeManager) RequestKeyframe() {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b != nil {
		b.RequestKeyframe()
	}
}

// GetRecordingProvider returns the SDCardManager of the currently active driver.
func (m *BridgeManager) GetRecordingProvider() storage.RecordingProvider {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		return b.GetSDCardManager()
	}
	if d != nil {
		return d.GetSDCardManager()
	}
	return nil
}

// WriteAudioBackchannel routes incoming two-way audio RTP packets to the camera speaker.
func (m *BridgeManager) WriteAudioBackchannel(pkt *rtp.Packet) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		return b.WriteAudioBackchannel(pkt)
	}
	if d != nil {
		d.OnAudioBackchannelPacket(pkt)
		return nil
	}
	return fmt.Errorf("camera bridge offline")
}
