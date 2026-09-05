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
	c.mu.RLock()
	cl := c.client
	c.mu.RUnlock()

	if cl == nil || !cl.IsConnected() {
		return
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
		logger.Debug("MQTT", "📢 Publishing recording event to %s/event/recording: %s", c.baseTopic, string(data))
		token := cl.Publish(fmt.Sprintf("%s/event/recording", c.baseTopic), 1, false, data)
		_ = token.WaitTimeout(2 * time.Second)
	}
}

// publishMotion publishes the binary sensor motion state (ON/OFF).
func (c *Client) publishMotion(isMotion bool) {
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
	cl.Publish(fmt.Sprintf("%s/motion/state", c.baseTopic), 1, false, state)
}

// publishStatus publishes telemetry and device status to Home Assistant.
func (c *Client) publishStatus(st events.DeviceStatus) {
	c.mu.RLock()
	cl := c.client
	c.mu.RUnlock()

	if cl == nil || !cl.IsConnected() {
		return
	}

	logger.Trace("MQTT", "Publishing device status: mode=%d res=%s pir=%v lux=%d", st.LampMode, st.Resolution, st.PIRActive, st.Lux)

	pub := func(subTopic string, val string) {
		cl.Publish(fmt.Sprintf("%s/%s", c.baseTopic, subTopic), 1, true, val)
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
	pub("lux_threshold/state", strconv.Itoa(st.Lux))
	pub("lux/state", strconv.Itoa(st.Lux))
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
