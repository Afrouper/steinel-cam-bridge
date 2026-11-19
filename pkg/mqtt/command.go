package mqtt

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// publishOptimistic publishes an immediate optimistic state update to Home Assistant
// to prevent UI slider jumping and toggle rubberbanding during network latency.
func (c *Client) publishOptimistic(baseTopic, subTopic, val string) {
	c.mu.RLock()
	cl := c.client
	c.mu.RUnlock()
	if cl != nil && cl.IsConnected() {
		cl.Publish(fmt.Sprintf("%s/%s", baseTopic, subTopic), 1, true, val)
	}
}

// handleCommand processes incoming command messages from Home Assistant and dispatches to Callbacks.
func (c *Client) handleCommand(_ paho.Client, msg paho.Message) {
	topic := msg.Topic()
	payload := strings.TrimSpace(string(msg.Payload()))
	logger.Info("MQTT", "📩 Command received on %s: %s", topic, payload)

	cb := c.cb
	baseTopic := c.baseTopic

	c.camerasMu.RLock()
	for _, dev := range c.cameras {
		if strings.HasPrefix(topic, dev.baseTopic+"/") {
			cb = dev.Callbacks
			baseTopic = dev.baseTopic
			break
		}
	}
	c.camerasMu.RUnlock()

	if c.handleCameraSettingsCommand(topic, payload, baseTopic, cb) {
		return
	}
	if c.handleCameraControlsCommand(topic, payload, baseTopic, cb) {
		return
	}

	switch {
	case strings.HasSuffix(topic, "/highlight/set"), strings.HasSuffix(topic, "/light/brightness/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if cb.SetHighlight != nil {
				_ = cb.SetHighlight(val)
			}
			c.publishOptimistic(baseTopic, "highlight/state", strconv.Itoa(val))
			c.publishOptimistic(baseTopic, "light/brightness/state", strconv.Itoa(val))
		}

	case strings.HasSuffix(topic, "/light/set"):
		if strings.EqualFold(payload, "ON") {
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("on")
			}
			c.publishOptimistic(baseTopic, "light/state", "ON")
			c.publishOptimistic(baseTopic, "mode/state", "Dauerlicht")
		} else {
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("off")
			}
			c.publishOptimistic(baseTopic, "light/state", "OFF")
			c.publishOptimistic(baseTopic, "mode/state", "Sensor")
		}

	case strings.HasSuffix(topic, "/mode/set"):
		switch strings.ToLower(payload) {
		case "sensor", "auto", "2":
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("auto")
			}
			c.publishOptimistic(baseTopic, "mode/state", "Sensor")
			c.publishOptimistic(baseTopic, "light/state", "OFF")
		case "dauerlicht", "on", "1":
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("on")
			}
			c.publishOptimistic(baseTopic, "mode/state", "Dauerlicht")
			c.publishOptimistic(baseTopic, "light/state", "ON")
		case "aus", "off", "0":
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("aus")
			}
			c.publishOptimistic(baseTopic, "mode/state", "Aus")
			c.publishOptimistic(baseTopic, "light/state", "OFF")
		}

	case strings.HasSuffix(topic, "/pir_sensitivity/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if cb.SetPIRSensitivity != nil {
				_ = cb.SetPIRSensitivity(val)
			}
			c.publishOptimistic(baseTopic, "pir_sensitivity/state", strconv.Itoa(val))
		}

	case strings.HasSuffix(topic, "/lux_threshold/set"), strings.HasSuffix(topic, "/lux/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if cb.SetLuxThreshold != nil {
				_ = cb.SetLuxThreshold(val)
			}
			c.publishOptimistic(baseTopic, "lux_threshold/state", strconv.Itoa(val))
			c.publishOptimistic(baseTopic, "lux/state", strconv.Itoa(val))
		}

	case strings.HasSuffix(topic, "/duration/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if val < 5 {
				val = 5
			} else if val > 900 {
				val = 900
			}
			if cb.SetHighlightTime != nil {
				_ = cb.SetHighlightTime(val)
			}
			c.publishOptimistic(baseTopic, "duration/state", strconv.Itoa(val))
		}

	case strings.HasSuffix(topic, "/lowlight/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if val < 0 {
				val = 0
			} else if val > 50 {
				val = 50
			}
			if cb.SetLowlight != nil {
				_ = cb.SetLowlight(val)
			}
			c.rememberLowlight(baseTopic, val)
			c.publishOptimistic(baseTopic, "lowlight/state", strconv.Itoa(val))
			c.publishOptimistic(baseTopic, "lowlight_switch/state", lowlightSwitchState(val))
		}

	case strings.HasSuffix(topic, "/lowlight_switch/set"):
		// Base light on/off like the toggle in the STEINEL CAM app:
		// off = 0 %, on = the brightness that was set last.
		val := 0
		if strings.EqualFold(payload, "ON") || payload == "1" || strings.EqualFold(payload, "true") {
			val = c.lastLowlight(baseTopic)
		}
		if cb.SetLowlight != nil {
			_ = cb.SetLowlight(val)
		}
		c.publishOptimistic(baseTopic, "lowlight/state", strconv.Itoa(val))
		c.publishOptimistic(baseTopic, "lowlight_switch/state", lowlightSwitchState(val))

	case strings.HasSuffix(topic, "/lowlight_duration/set"), strings.HasSuffix(topic, "/lowlight_time/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if val < 0 {
				val = 0
			} else if val > 12 {
				val = 12
			}
			if cb.SetLowlightTime != nil {
				_ = cb.SetLowlightTime(val)
			}
			c.publishOptimistic(baseTopic, "lowlight_duration/state", strconv.Itoa(val))
			c.publishOptimistic(baseTopic, "lowlight_time/state", strconv.Itoa(val))
		}

	case strings.HasSuffix(topic, "/siren/set"):
		var on bool
		if strings.HasPrefix(payload, "{") {
			var sirenCmd struct {
				State string `json:"state"`
			}
			if err := json.Unmarshal([]byte(payload), &sirenCmd); err == nil {
				on = strings.EqualFold(sirenCmd.State, "ON") || sirenCmd.State == "1" || strings.EqualFold(sirenCmd.State, "true")
			}
		} else {
			on = strings.EqualFold(payload, "ON") || payload == "1" || strings.EqualFold(payload, "true")
		}
		if cb.SetSiren != nil {
			_ = cb.SetSiren(on)
		}
		stateStr := "OFF"
		if on {
			stateStr = "ON"
		}
		c.publishOptimistic(baseTopic, "siren/state", stateStr)

	case strings.HasSuffix(topic, "/resolution/set"):
		if cb.SetResolution != nil {
			if err := cb.SetResolution(payload); err == nil {
				c.publishOptimistic(baseTopic, "resolution/state", payload)
			}
		}

	case strings.HasSuffix(topic, "/reconnect/set"), strings.HasSuffix(topic, "/reconnect"):
		if cb.ReconnectSession != nil {
			if err := cb.ReconnectSession(); err != nil {
				logger.Warn("MQTT", "⚠️ Failed to reconnect camera session: %v", err)
			} else {
				logger.Info("MQTT", "✅ Camera session reconnect triggered successfully")
			}
		}
	}
}
