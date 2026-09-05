package mqtt

import (
	"encoding/json"
	"fmt"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// publishDiscovery registers all Home Assistant MQTT Auto-Discovery entities.
func (c *Client) publishDiscovery(client paho.Client) {
	availTopic := fmt.Sprintf("%s/availability", c.baseTopic)
	devMap := map[string]interface{}{
		"identifiers":  []string{c.nodeID},
		"name":         fmt.Sprintf("Steinel %s (%s)", c.cfg.Model, c.cfg.DeviceID),
		"manufacturer": "STEINEL",
		"model":        c.cfg.Model,
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
			config["unique_id"] = fmt.Sprintf("%s_%s", c.nodeID, objectID)
		}

		discTopic := fmt.Sprintf("%s/%s/%s/%s/config", c.cfg.DiscoveryPrefix, component, c.nodeID, objectID)
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
		"state_topic":         fmt.Sprintf("%s/highlight/state", c.baseTopic),
		"command_topic":       fmt.Sprintf("%s/highlight/set", c.baseTopic),
	})

	// 2. Select (Betriebsmodus: Sensor, Dauerlicht, Aus)
	publishEntity("select", "mode", map[string]interface{}{
		"name":          "Betriebsmodus",
		"options":       []string{"Sensor", "Dauerlicht", "Aus"},
		"state_topic":   fmt.Sprintf("%s/mode/state", c.baseTopic),
		"command_topic": fmt.Sprintf("%s/mode/set", c.baseTopic),
		"icon":          "mdi:theme-light-dark",
	})

	// 3. Binary Sensor (PIR Status)
	publishEntity("binary_sensor", "pir_status", map[string]interface{}{
		"name":            "PIR Sensor aktiv",
		"device_class":    "running",
		"entity_category": "diagnostic",
		"state_topic":     fmt.Sprintf("%s/pir/state", c.baseTopic),
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
		"state_topic":         fmt.Sprintf("%s/pir_sensitivity/state", c.baseTopic),
		"command_topic":       fmt.Sprintf("%s/pir_sensitivity/set", c.baseTopic),
	})

	// 5. Number (Lux Threshold: 2 - 1000 lx)
	publishEntity("number", "lux_threshold", map[string]interface{}{
		"name":                "Dämmerungsschwelle",
		"min":                 2,
		"max":                 1000,
		"step":                5,
		"unit_of_measurement": "lx",
		"icon":                "mdi:weather-sunset",
		"entity_category":     "config",
		"state_topic":         fmt.Sprintf("%s/lux_threshold/state", c.baseTopic),
		"command_topic":       fmt.Sprintf("%s/lux_threshold/set", c.baseTopic),
	})

	// 6. Number (Duration: 5 - 900s)
	publishEntity("number", "duration", map[string]interface{}{
		"name":                "Nachlaufzeit",
		"min":                 5,
		"max":                 900,
		"step":                5,
		"unit_of_measurement": "s",
		"icon":                "mdi:timer-outline",
		"entity_category":     "config",
		"state_topic":         fmt.Sprintf("%s/duration/state", c.baseTopic),
		"command_topic":       fmt.Sprintf("%s/duration/set", c.baseTopic),
	})

	// 7. Number (Grundlicht: 0 - 50%)
	publishEntity("number", "lowlight", map[string]interface{}{
		"name":                "Grundlicht Helligkeit",
		"min":                 0,
		"max":                 50,
		"step":                5,
		"unit_of_measurement": "%",
		"icon":                "mdi:lightbulb-night",
		"entity_category":     "config",
		"state_topic":         fmt.Sprintf("%s/lowlight/state", c.baseTopic),
		"command_topic":       fmt.Sprintf("%s/lowlight/set", c.baseTopic),
	})

	// 8. Siren (Warnton / Alarm)
	publishEntity("siren", "siren", map[string]interface{}{
		"name":          "Sirene",
		"icon":          "mdi:bullhorn",
		"state_topic":   fmt.Sprintf("%s/siren/state", c.baseTopic),
		"command_topic": fmt.Sprintf("%s/siren/set", c.baseTopic),
	})

	// 9. Select (Video Auflösung)
	publishEntity("select", "resolution", map[string]interface{}{
		"name":            "Video Auflösung",
		"options":         []string{"1080p", "720p", "360p"},
		"icon":            "mdi:video-vintage",
		"entity_category": "config",
		"state_topic":     fmt.Sprintf("%s/resolution/state", c.baseTopic),
		"command_topic":   fmt.Sprintf("%s/resolution/set", c.baseTopic),
	})

	// 10. Event (Letzte SD-Aufnahme)
	publishEntity("event", "recording", map[string]interface{}{
		"name":        "Letzte SD-Aufnahme",
		"icon":        "mdi:video-box",
		"state_topic": fmt.Sprintf("%s/event/recording", c.baseTopic),
		"event_types": []string{"motion", "manual", "alarm", "record", "plan", "all"},
	})

	logger.Info("MQTT", "📢 Published Home Assistant Auto-Discovery entities for %s under %s", c.nodeID, c.cfg.DiscoveryPrefix)
}
