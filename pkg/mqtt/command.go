package mqtt

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"

	paho "github.com/eclipse/paho.mqtt.golang"
)

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

	switch {
	case strings.HasSuffix(topic, "/highlight/set"), strings.HasSuffix(topic, "/light/brightness/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if cb.SetHighlight != nil {
				_ = cb.SetHighlight(val)
			}
		}

	case strings.HasSuffix(topic, "/light/set"):
		if strings.EqualFold(payload, "ON") {
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("on")
			}
		} else {
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("off")
			}
		}

	case strings.HasSuffix(topic, "/mode/set"):
		switch strings.ToLower(payload) {
		case "sensor", "auto", "2":
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("auto")
			}
		case "dauerlicht", "on", "1":
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("on")
			}
		case "aus", "off", "0":
			if cb.SetLampMode != nil {
				_ = cb.SetLampMode("off")
			}
		}

	case strings.HasSuffix(topic, "/pir_sensitivity/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if cb.SetPIRSensitivity != nil {
				_ = cb.SetPIRSensitivity(val)
			}
		}

	case strings.HasSuffix(topic, "/lux_threshold/set"), strings.HasSuffix(topic, "/lux/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if cb.SetLuxThreshold != nil {
				_ = cb.SetLuxThreshold(val)
			}
			c.mu.RLock()
			cl := c.client
			c.mu.RUnlock()
			if cl != nil && cl.IsConnected() {
				cl.Publish(fmt.Sprintf("%s/lux_threshold/state", baseTopic), 1, true, strconv.Itoa(val))
				cl.Publish(fmt.Sprintf("%s/lux/state", baseTopic), 1, true, strconv.Itoa(val))
			}
		}

	case strings.HasSuffix(topic, "/duration/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if cb.SetHighlightTime != nil {
				_ = cb.SetHighlightTime(val)
			}
		}

	case strings.HasSuffix(topic, "/lowlight/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if cb.SetLowlight != nil {
				_ = cb.SetLowlight(val)
			}
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
		c.mu.RLock()
		cl := c.client
		c.mu.RUnlock()
		if cl != nil && cl.IsConnected() {
			cl.Publish(fmt.Sprintf("%s/siren/state", baseTopic), 1, true, stateStr)
		}

	case strings.HasSuffix(topic, "/resolution/set"):
		if cb.SetResolution != nil {
			_ = cb.SetResolution(payload)
		}
	}
}
