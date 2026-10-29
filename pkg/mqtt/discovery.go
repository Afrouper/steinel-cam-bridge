package mqtt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// publishDiscovery registers all Home Assistant MQTT Auto-Discovery entities for the primary device.
func (c *Client) publishDiscovery(client paho.Client) {
	c.camerasMu.RLock()
	primaryDev := c.cameras[c.cfg.DeviceID]
	c.camerasMu.RUnlock()

	if primaryDev != nil {
		c.publishDeviceDiscovery(client, primaryDev)
	} else {
		dev := &CameraDevice{
			Name:      "",
			DeviceID:  c.cfg.DeviceID,
			Model:     c.cfg.Model,
			nodeID:    c.nodeID,
			baseTopic: c.baseTopic,
		}
		c.publishDeviceDiscovery(client, dev)
	}
}

// FormatDisplayName formats the user-facing device display name for Home Assistant.
func FormatDisplayName(model, deviceID, name string) string {
	if name != "" {
		if strings.HasPrefix(strings.ToLower(name), "steinel") {
			return name
		}
		return fmt.Sprintf("Steinel %s", name)
	}
	return fmt.Sprintf("Steinel %s (%s)", model, deviceID)
}

// publishDeviceDiscovery registers all Home Assistant MQTT Auto-Discovery entities for a specific CameraDevice.
func (c *Client) publishDeviceDiscovery(client paho.Client, dev *CameraDevice) {
	availTopic := fmt.Sprintf("%s/availability", dev.baseTopic)
	displayName := FormatDisplayName(dev.Model, dev.DeviceID, dev.Name)

	devMap := map[string]interface{}{
		"identifiers":  []string{dev.nodeID},
		"name":         displayName,
		"manufacturer": "STEINEL",
		"model":        dev.Model,
		"sw_version":   "2.0.0",
	}
	if c.cfg.BridgeHTTPURL != "" {
		devMap["configuration_url"] = c.cfg.BridgeHTTPURL
	}

	// Helper for publishing a single discovery entity
	publishEntity := func(component string, objectID string, config map[string]interface{}) {
		config["availability_topic"] = availTopic
		config["device"] = devMap
		if _, ok := config["unique_id"]; !ok {
			config["unique_id"] = fmt.Sprintf("%s_%s", dev.nodeID, objectID)
		}

		discTopic := fmt.Sprintf("%s/%s/%s/%s/config", c.cfg.DiscoveryPrefix, component, dev.nodeID, objectID)
		payload, _ := json.Marshal(config)
		client.Publish(discTopic, 1, true, payload)
	}

	// 1. Number (Hauptlicht Helligkeit: 10 - 100%)
	publishEntity("number", "highlight", map[string]interface{}{
		"name":                "Hauptlicht Helligkeit",
		"min":                 10,
		"max":                 100,
		"step":                5,
		"unit_of_measurement": "%",
		"icon":                "mdi:brightness-percent",
		"entity_category":     "config",
		"state_topic":         fmt.Sprintf("%s/highlight/state", dev.baseTopic),
		"command_topic":       fmt.Sprintf("%s/highlight/set", dev.baseTopic),
	})

	// 2. Select (Betriebsmodus: Sensor, Dauerlicht, Aus)
	publishEntity("select", "mode", map[string]interface{}{
		"name":          "Betriebsmodus",
		"options":       []string{"Sensor", "Dauerlicht", "Aus"},
		"state_topic":   fmt.Sprintf("%s/mode/state", dev.baseTopic),
		"command_topic": fmt.Sprintf("%s/mode/set", dev.baseTopic),
		"icon":          "mdi:theme-light-dark",
	})

	// 3. Binary Sensor (PIR Status)
	publishEntity("binary_sensor", "pir_status", map[string]interface{}{
		"name":            "PIR Sensor aktiv",
		"device_class":    "running",
		"entity_category": "diagnostic",
		"state_topic":     fmt.Sprintf("%s/pir/state", dev.baseTopic),
		"icon":            "mdi:motion-sensor",
	})

	// 4. Number (PIR Sensitivity: 0 - 100%)
	publishEntity("number", "pir_sensitivity", map[string]interface{}{
		"name":                "PIR Empfindlichkeit",
		"min":                 0,
		"max":                 100,
		"step":                1,
		"unit_of_measurement": "%",
		"icon":                "mdi:tune",
		"entity_category":     "config",
		"state_topic":         fmt.Sprintf("%s/pir_sensitivity/state", dev.baseTopic),
		"command_topic":       fmt.Sprintf("%s/pir_sensitivity/set", dev.baseTopic),
	})

	// 5. Light (Kamera Scheinwerfer An/Aus + Dimmwert)
	publishEntity("light", "light", map[string]interface{}{
		"name":                     "Flutlicht",
		"state_topic":              fmt.Sprintf("%s/light/state", dev.baseTopic),
		"command_topic":            fmt.Sprintf("%s/light/set", dev.baseTopic),
		"brightness_state_topic":   fmt.Sprintf("%s/light/brightness/state", dev.baseTopic),
		"brightness_command_topic": fmt.Sprintf("%s/light/brightness/set", dev.baseTopic),
		"brightness_scale":         100,
		"payload_on":               "ON",
		"payload_off":              "OFF",
		"icon":                     "mdi:flood-light",
	})

	// 6. Sensor (Umgebungshelligkeit in Lux)
	publishEntity("sensor", "lux", map[string]interface{}{
		"name":                "Umgebungshelligkeit",
		"device_class":        "illuminance",
		"state_class":         "measurement",
		"unit_of_measurement": "lx",
		"state_topic":         fmt.Sprintf("%s/lux/state", dev.baseTopic),
		"icon":                "mdi:weather-sunny",
	})

	// 7. Binary Sensor (Bewegungserkennung)
	publishEntity("binary_sensor", "motion", map[string]interface{}{
		"name":         "Bewegung",
		"device_class": "motion",
		"state_topic":  fmt.Sprintf("%s/motion/state", dev.baseTopic),
		"payload_on":   "ON",
		"payload_off":  "OFF",
		"icon":         "mdi:motion-sensor",
	})

	// 8. Number (Dämmerungsschwelle in Lux: 2 - 1000)
	publishEntity("number", "lux_threshold", map[string]interface{}{
		"name":                "Dämmerungsschwelle",
		"min":                 2,
		"max":                 1000,
		"step":                10,
		"unit_of_measurement": "lx",
		"icon":                "mdi:weather-night",
		"entity_category":     "config",
		"state_topic":         fmt.Sprintf("%s/lux_threshold/state", dev.baseTopic),
		"command_topic":       fmt.Sprintf("%s/lux_threshold/set", dev.baseTopic),
	})

	// 9. Siren (Sirene / Alarmfunktion)
	publishEntity("siren", "siren", map[string]interface{}{
		"name":          "Sirene",
		"state_topic":   fmt.Sprintf("%s/siren/state", dev.baseTopic),
		"command_topic": fmt.Sprintf("%s/siren/set", dev.baseTopic),
		"icon":          "mdi:bullhorn",
	})

	// 10. Select (Video Auflösung)
	publishEntity("select", "resolution", map[string]interface{}{
		"name":            "Video Auflösung",
		"options":         []string{"1080p", "720p", "360p"},
		"icon":            "mdi:video-vintage",
		"entity_category": "config",
		"state_topic":     fmt.Sprintf("%s/resolution/state", dev.baseTopic),
		"command_topic":   fmt.Sprintf("%s/resolution/set", dev.baseTopic),
	})

	// 11. Event (Letzte SD-Aufnahme Event Trigger)
	publishEntity("event", "recording", map[string]interface{}{
		"name":        "Letzte SD-Aufnahme Event",
		"icon":        "mdi:video-box",
		"state_topic": fmt.Sprintf("%s/event/recording", dev.baseTopic),
		"event_types": []string{"motion", "manual", "alarm", "record", "plan", "all"},
	})

	// 12. Sensor (Letzte SD-Aufnahme Status & Attribute)
	publishEntity("sensor", "recording_latest", map[string]interface{}{
		"name":                  "Letzte SD-Aufnahme",
		"icon":                  "mdi:filmstrip",
		"device_class":          "timestamp",
		"state_topic":           fmt.Sprintf("%s/recording/latest", dev.baseTopic),
		"value_template":        "{{ value_json.timestamp }}",
		"json_attributes_topic": fmt.Sprintf("%s/recording/latest", dev.baseTopic),
	})

	logger.Info("MQTT", "📢 Published Home Assistant Auto-Discovery entities for %s (%s) under %s", dev.nodeID, displayName, c.cfg.DiscoveryPrefix)
}
