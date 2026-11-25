package mqtt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

// Further L 625 controls known from the STEINEL CAM app: pixel change
// detection, detection boxes, person sensitivity, SD recording mode, firmware
// update and camera restart. Each entity is only announced when the driver
// provides the matching callback.

// Person sensitivity steps of the app's slider (raw ai_person values).
var aiPersonNames = map[int]string{2: "Niedrig", 5: "Mittel", 7: "Hoch"}
var aiPersonOptions = []string{"Niedrig", "Mittel", "Hoch"}

// SD recording modes (raw set_record_setting "mode").
var recordModeNames = map[int]string{
	4: "Keine Aufnahme",
	2: "Bewegung durch Kamera",
	3: "Kamera oder Bewegungsmelder",
}
var recordModeOptions = []string{"Keine Aufnahme", "Bewegung durch Kamera", "Kamera oder Bewegungsmelder"}

func reverseLookup(names map[int]string, name string) (int, bool) {
	for v, n := range names {
		if strings.EqualFold(n, strings.TrimSpace(name)) {
			return v, true
		}
	}
	return 0, false
}

// aiPersonStep maps a raw ai_person value to the app's slider step.
func aiPersonStep(raw int) int {
	switch {
	case raw <= 3:
		return 2
	case raw <= 6:
		return 5
	default:
		return 7
	}
}

func isOn(payload string) bool {
	return strings.EqualFold(payload, "ON") || payload == "1" || strings.EqualFold(payload, "true")
}

func onOff(state int) string {
	if state == 1 {
		return "ON"
	}
	return "OFF"
}

// publishCameraControlsDiscovery announces the additional L 625 controls.
func (c *Client) publishCameraControlsDiscovery(dev *CameraDevice, publishEntity func(component, objectID string, config map[string]interface{})) {
	cb := dev.Callbacks
	topic := func(sub string) string { return fmt.Sprintf("%s/%s", dev.baseTopic, sub) }

	if cb.SetPixelChangeDetection != nil {
		publishEntity("switch", "pixel_change", map[string]interface{}{
			"name":            "Licht bei Bildänderung",
			"icon":            "mdi:image-filter-center-focus",
			"entity_category": "config",
			"payload_on":      "ON",
			"payload_off":     "OFF",
			"state_topic":     topic("pixel_change/state"),
			"command_topic":   topic("pixel_change/set"),
		})
	}
	if cb.SetDrawFrame != nil {
		publishEntity("switch", "draw_frame", map[string]interface{}{
			"name":            "Erkennungsrahmen im Bild",
			"icon":            "mdi:selection-ellipse",
			"entity_category": "config",
			"payload_on":      "ON",
			"payload_off":     "OFF",
			"state_topic":     topic("draw_frame/state"),
			"command_topic":   topic("draw_frame/set"),
		})
	}
	if cb.SetAIPersonSensitivity != nil {
		publishEntity("select", "ai_person", map[string]interface{}{
			"name":            "Personenerkennung Empfindlichkeit",
			"options":         aiPersonOptions,
			"icon":            "mdi:account-eye",
			"entity_category": "config",
			"state_topic":     topic("ai_person/state"),
			"command_topic":   topic("ai_person/set"),
		})
	}
	if cb.SetRecordMode != nil {
		publishEntity("select", "record_mode", map[string]interface{}{
			"name":            "SD-Aufnahme",
			"options":         recordModeOptions,
			"icon":            "mdi:record-rec",
			"entity_category": "config",
			"state_topic":     topic("record_mode/state"),
			"command_topic":   topic("record_mode/set"),
		})
	}
	if cb.InstallUpdate != nil {
		publishEntity("update", "firmware_update", map[string]interface{}{
			"name":            "Kamera-Firmware",
			"device_class":    "firmware",
			"entity_category": "config",
			"title":           "STEINEL Kameramodul",
			"state_topic":     topic("firmware_update/state"),
			"command_topic":   topic("firmware_update/set"),
			"payload_install": "install",
		})
		publishEntity("update", "mcu_update", map[string]interface{}{
			"name":            "Leuchten-Firmware",
			"device_class":    "firmware",
			"entity_category": "config",
			"title":           "STEINEL Leuchte (MCU)",
			"state_topic":     topic("mcu_update/state"),
			"command_topic":   topic("mcu_update/set"),
			"payload_install": "install",
		})
	}
	if cb.RebootCamera != nil {
		publishEntity("button", "reboot", map[string]interface{}{
			"name":            "Kamera neu starten",
			"device_class":    "restart",
			"entity_category": "config",
			"command_topic":   topic("reboot/set"),
		})
	}
}

// updateState builds the JSON state of an MQTT update entity. Without an
// offer the latest version equals the installed one.
func updateState(installed, remote string, offered bool) string {
	latest := installed
	if offered {
		latest = remote
	}
	data, _ := json.Marshal(map[string]string{"installed_version": installed, "latest_version": latest})
	return string(data)
}

// publishCameraControlsState publishes the values of the additional controls.
func (c *Client) publishCameraControlsState(st events.DeviceStatus, pub func(subTopic, val string)) {
	if st.PixelChangeDetection >= 0 {
		pub("pixel_change/state", onOff(st.PixelChangeDetection))
	}
	if st.DrawFrame >= 0 {
		pub("draw_frame/state", onOff(st.DrawFrame))
	}
	if st.AIPerson >= 0 {
		pub("ai_person/state", aiPersonNames[aiPersonStep(st.AIPerson)])
	}
	if name, ok := recordModeNames[st.RecordMode]; ok {
		pub("record_mode/state", name)
	}
	if st.UpdateInfoKnown && st.FirmwareVer != "" {
		offered := st.FirmwareUpdate && st.FirmwareRemote != "" && st.FirmwareRemote != st.FirmwareVer
		pub("firmware_update/state", updateState(st.FirmwareVer, st.FirmwareRemote, offered))
	}
	if st.UpdateInfoKnown && st.MCUVersion != "" {
		offered := st.MCUUpdate && st.MCURemote != "" && st.MCURemote != st.MCUVersion
		pub("mcu_update/state", updateState(st.MCUVersion, st.MCURemote, offered))
	}
}

// handleCameraControlsCommand processes the command topics of the additional
// controls. It returns false if the topic is not one of them.
func (c *Client) handleCameraControlsCommand(topic, payload, baseTopic string, cb Callbacks) bool {
	switch {
	case strings.HasSuffix(topic, "/pixel_change/set"):
		on := isOn(payload)
		if cb.SetPixelChangeDetection != nil && !runCommand(topic, payload, func() error { return cb.SetPixelChangeDetection(on) }) {
			return true
		}
		c.publishOptimistic(baseTopic, "pixel_change/state", onOff(boolState(on)))

	case strings.HasSuffix(topic, "/draw_frame/set"):
		on := isOn(payload)
		if cb.SetDrawFrame != nil && !runCommand(topic, payload, func() error { return cb.SetDrawFrame(on) }) {
			return true
		}
		c.publishOptimistic(baseTopic, "draw_frame/state", onOff(boolState(on)))

	case strings.HasSuffix(topic, "/ai_person/set"):
		level, ok := reverseLookup(aiPersonNames, payload)
		if !ok {
			return true
		}
		if cb.SetAIPersonSensitivity != nil && !runCommand(topic, payload, func() error { return cb.SetAIPersonSensitivity(level) }) {
			return true
		}
		c.publishOptimistic(baseTopic, "ai_person/state", aiPersonNames[level])

	case strings.HasSuffix(topic, "/record_mode/set"):
		mode, ok := reverseLookup(recordModeNames, payload)
		if !ok {
			return true
		}
		if cb.SetRecordMode != nil && !runCommand(topic, payload, func() error { return cb.SetRecordMode(mode) }) {
			return true
		}
		c.publishOptimistic(baseTopic, "record_mode/state", recordModeNames[mode])

	case strings.HasSuffix(topic, "/firmware_update/set"), strings.HasSuffix(topic, "/mcu_update/set"):
		if payload != "install" || cb.InstallUpdate == nil {
			return true
		}
		target := "camera"
		if strings.HasSuffix(topic, "/mcu_update/set") {
			target = "mcu"
		}
		if err := cb.InstallUpdate(target); err != nil {
			logger.Warn("MQTT", "⚠️ Update (%s) not started: %v", target, err)
		}

	case strings.HasSuffix(topic, "/reboot/set"):
		if cb.RebootCamera != nil {
			if err := cb.RebootCamera(); err != nil {
				logger.Warn("MQTT", "⚠️ Camera restart failed: %v", err)
			}
		}

	default:
		return false
	}
	return true
}

func boolState(on bool) int {
	if on {
		return 1
	}
	return 0
}
