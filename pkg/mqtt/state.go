package mqtt

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
)

// PublishRecordingEvent publishes a new recording event to Home Assistant MQTT.
func (c *Client) PublishRecordingEvent(item storage.RecordingItem) {
	if item.ID == "" || item.StartTime.IsZero() {
		logger.Debug("MQTT", "Skipping publication of uninitialized recording event")
		return
	}

	c.mu.RLock()
	cl := c.client
	c.mu.RUnlock()

	if cl == nil || !cl.IsConnected() {
		return
	}

	targetBaseTopic := c.baseTopic
	if item.Camera != "" {
		c.camerasMu.RLock()
		for _, dev := range c.cameras {
			if strings.EqualFold(dev.DeviceID, item.Camera) ||
				strings.EqualFold(dev.Name, item.Camera) ||
				strings.EqualFold(dev.nodeID, item.Camera) {
				targetBaseTopic = dev.baseTopic
				break
			}
		}
		c.camerasMu.RUnlock()
	}

	eventType := strings.ToLower(item.EventType)
	if eventType == "" || (eventType != "manual" && eventType != "alarm" && eventType != "record" && eventType != "plan") {
		eventType = "motion"
	}

	payload := map[string]interface{}{
		"event_type":      eventType,
		"id":              item.ID,
		"timestamp":       item.StartTime.Format(time.RFC3339),
		"duration_sec":    item.DurationSeconds,
		"file_size_bytes": item.FileSizeBytes,
		"video_url":       fmt.Sprintf("%s%s", c.cfg.BridgeHTTPURL, item.VideoURL),
	}
	if item.ThumbnailURL != "" {
		payload["thumbnail_url"] = fmt.Sprintf("%s%s", c.cfg.BridgeHTTPURL, item.ThumbnailURL)
	}

	data, err := json.Marshal(payload)
	if err == nil {
		logger.Debug("MQTT", "📢 Publishing recording event to %s/event/recording: %s", targetBaseTopic, string(data))
		// 1. Momentary event for automations (non-retained)
		tokenEvt := cl.Publish(fmt.Sprintf("%s/event/recording", targetBaseTopic), 1, false, data)
		_ = tokenEvt.WaitTimeout(2 * time.Second)

		// 2. Persistent state for dashboards and sensors (retained)
		logger.Debug("MQTT", "📢 Publishing latest recording state to %s/recording/latest: %s", targetBaseTopic, string(data))
		tokenLatest := cl.Publish(fmt.Sprintf("%s/recording/latest", targetBaseTopic), 1, true, data)
		_ = tokenLatest.WaitTimeout(2 * time.Second)
	}
}

// publishMotion publishes the binary sensor motion state (ON/OFF) for the primary device.
func (c *Client) publishMotion(isMotion bool) {
	c.camerasMu.RLock()
	dev := c.cameras[c.cfg.DeviceID]
	c.camerasMu.RUnlock()

	if dev != nil {
		c.publishDeviceMotion(dev, isMotion)
	} else {
		c.publishDeviceMotion(&CameraDevice{baseTopic: c.baseTopic}, isMotion)
	}
}

// publishDeviceMotion publishes the binary sensor motion state (ON/OFF) for a specific CameraDevice.
func (c *Client) publishDeviceMotion(dev *CameraDevice, isMotion bool) {
	c.mu.RLock()
	cl := c.client
	c.mu.RUnlock()

	if cl == nil || !cl.IsConnected() {
		return
	}

	state := "OFF"
	if isMotion {
		state = "ON"
	}
	cl.Publish(fmt.Sprintf("%s/motion/state", dev.baseTopic), 1, false, state)
}

// publishStatus publishes telemetry and device status to Home Assistant for the primary device.
func (c *Client) publishStatus(st events.DeviceStatus) {
	c.camerasMu.RLock()
	dev := c.cameras[c.cfg.DeviceID]
	c.camerasMu.RUnlock()

	if dev != nil {
		c.publishDeviceStatus(dev, st)
	} else {
		c.publishDeviceStatus(&CameraDevice{baseTopic: c.baseTopic}, st)
	}
}

// publishDeviceStatus publishes telemetry and device status for a specific CameraDevice.
func (c *Client) publishDeviceStatus(dev *CameraDevice, st events.DeviceStatus) {
	c.mu.RLock()
	cl := c.client
	c.mu.RUnlock()

	if cl == nil || !cl.IsConnected() {
		return
	}

	logger.Trace("MQTT", "Publishing device status for %s: mode=%d res=%s pir=%v lux=%d", dev.baseTopic, st.LampMode, st.Resolution, st.PIRActive, st.Lux)

	pub := func(subTopic string, val string) {
		cl.Publish(fmt.Sprintf("%s/%s", dev.baseTopic, subTopic), 1, true, val)
	}

	// 1. Lamp State & Mode
	switch st.LampMode {
	case 1:
		pub("light/state", "ON")
		pub("mode/state", "Dauerlicht")
	case 2:
		pub("light/state", "ON")
		pub("mode/state", "Sensor")
	default:
		pub("light/state", "OFF")
		pub("mode/state", "Aus")
	}

	// 2. Brightness & Dimm values
	if st.Highlight > 0 {
		pub("highlight/state", strconv.Itoa(st.Highlight))
		pub("light/brightness/state", strconv.Itoa(st.Highlight))
	}
	pub("duration/state", strconv.Itoa(st.HighlightTime))
	pub("lowlight/state", strconv.Itoa(st.Lowlight))

	// 3. Sensor values & thresholds
	if st.Lux >= 2 {
		pub("lux_threshold/state", strconv.Itoa(st.Lux))
		pub("lux/state", strconv.Itoa(st.Lux))
	}
	pub("pir_sensitivity/state", strconv.Itoa(st.PIRSensitivity))

	pirState := "OFF"
	if st.PIRActive {
		pirState = "ON"
	}
	pub("pir/state", pirState)

	// 4. Resolution
	if st.Resolution != "" {
		pub("resolution/state", st.Resolution)
	}
}
