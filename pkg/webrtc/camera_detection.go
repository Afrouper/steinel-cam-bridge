package webrtc

import (
	"fmt"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mcu"
)

// Detection and recording settings of the L 625. The commands were checked
// on firmware 0.1.2.31 by writing and reading back.

// Person detection sensitivity levels as used by the app's three-step slider.
const (
	AIPersonLow    = 2
	AIPersonMedium = 5
	AIPersonHigh   = 7
)

// SD recording modes (set_record_setting "mode").
const (
	RecordModeNone        = 4 // no recording ("Keine Aufnahme")
	RecordModeCamera      = 2 // motion detection by the camera
	RecordModeCameraOrPIR = 3 // camera or the lamp's motion sensor

	// recordTimeSeconds is the clip length; the app always sends 30.
	recordTimeSeconds = 30
)

// AIPersonLevel maps a raw ai_person value to the app's slider step
// (low ≤ 3, medium ≤ 6, high above), as the app does when it shows the value.
func AIPersonLevel(raw int) int {
	switch {
	case raw <= 3:
		return AIPersonLow
	case raw <= 6:
		return AIPersonMedium
	default:
		return AIPersonHigh
	}
}

// IsKnownRecordMode reports whether mode is one of the modes the app offers.
func IsKnownRecordMode(mode int) bool {
	return mode == RecordModeNone || mode == RecordModeCamera || mode == RecordModeCameraOrPIR
}

func boolToState(on bool) int {
	if on {
		return 1
	}
	return 0
}

// requestDetectionSettings reads the detection and recording settings.
func (b *Bridge) requestDetectionSettings() {
	if err := b.sendJSONCmd("get_detect_data", nil); err != nil {
		logger.Debug("DataChannel", "get_detect_data could not be sent: %v", err)
	}
	if err := b.sendJSONCmd("get_record_setting", nil); err != nil {
		logger.Debug("DataChannel", "get_record_setting could not be sent: %v", err)
	}
}

// onDetectData stores the content of a get_detect_data reply.
func (b *Bridge) onDetectData(info map[string]interface{}) {
	b.eventBus.UpdateStatusFunc(func(status *events.DeviceStatus) {
		applyDetectData(status, info)
	})
}

// onRecordSetting stores the content of a get_record_setting reply.
func (b *Bridge) onRecordSetting(info map[string]interface{}) {
	b.eventBus.UpdateStatusFunc(func(status *events.DeviceStatus) {
		applyRecordSetting(status, info)
	})
}

func applyDetectData(st *events.DeviceStatus, info map[string]interface{}) {
	if v, ok := jsonInt(info["draw_frame"]); ok {
		st.DrawFrame = boolToState(v != 0)
	}
	if v, ok := jsonInt(info["ai_person"]); ok {
		st.AIPerson = v
	}
}

func applyRecordSetting(st *events.DeviceStatus, info map[string]interface{}) {
	if v, ok := jsonInt(info["mode"]); ok {
		st.RecordMode = v
	}
}

// SetPixelChangeDetection switches the lamp's "Pixel Change Detection" (light
// reacts to changes in the camera picture) via the MCU.
func (b *Bridge) SetPixelChangeDetection(on bool) error {
	b64, err := mcu.BuildSetPixelChangeDetection(on)
	if err != nil {
		return err
	}
	if err := b.SendCommand("tran_ctl", map[string]interface{}{"data": b64}); err != nil {
		return err
	}
	b.eventBus.UpdateStatusFunc(func(status *events.DeviceStatus) {
		status.PixelChangeDetection = boolToState(on)
	})
	return nil
}

// SetDrawFrame switches the detection boxes burnt into the picture on or off.
// Detection itself keeps running.
func (b *Bridge) SetDrawFrame(on bool) error {
	val := boolToState(on)
	if err := b.sendJSONCmd("set_detect_data", map[string]interface{}{"action": "draw_frame", "draw_frame": val}); err != nil {
		return err
	}
	b.eventBus.UpdateStatusFunc(func(status *events.DeviceStatus) {
		status.DrawFrame = val
	})
	b.refreshDetectionSoon()
	return nil
}

// SetAIPersonSensitivity sets the person detection sensitivity to one of the
// app's three steps (AIPersonLow / Medium / High).
func (b *Bridge) SetAIPersonSensitivity(level int) error {
	if level != AIPersonLow && level != AIPersonMedium && level != AIPersonHigh {
		return fmt.Errorf("invalid person sensitivity %d (allowed: 2, 5, 7)", level)
	}
	if err := b.sendJSONCmd("set_detect_data", map[string]interface{}{"action": "ai_person", "ai_person": level}); err != nil {
		return err
	}
	status := b.eventBus.GetStatus()
	status.AIPerson = level
	b.eventBus.UpdateStatus(status)
	b.refreshDetectionSoon()
	return nil
}

// SetRecordMode chooses what makes the camera record to its SD card.
func (b *Bridge) SetRecordMode(mode int) error {
	if !IsKnownRecordMode(mode) {
		return fmt.Errorf("invalid record mode %d (allowed: 2, 3, 4)", mode)
	}
	info := map[string]interface{}{"action": "all", "mode": mode, "record_time": recordTimeSeconds}
	if err := b.sendJSONCmd("set_record_setting", info); err != nil {
		return err
	}
	status := b.eventBus.GetStatus()
	status.RecordMode = mode
	b.eventBus.UpdateStatus(status)
	b.refreshDetectionSoon()
	return nil
}

// refreshDetectionSoon re-reads the settings shortly after a change, so the
// reported state reflects what the camera really stored.
func (b *Bridge) refreshDetectionSoon() {
	afterSettingDelay(b.requestDetectionSettings)
}
