package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"steinel-cam-bridge/pkg/events"

	paho "github.com/eclipse/paho.mqtt.golang"
)

type Config struct {
	Broker          string
	Username        string
	Password        string
	ClientID        string
	TopicPrefix     string // Base topic, default: "steinel"
	DiscoveryPrefix string // Default: "homeassistant"
	DeviceID        string // e.g. "de-xxxxxxx"
	ProductID       string // e.g. "pr-xxxxx"
	Model           string // e.g. "L 625 CAM SC"
	BridgeHTTPURL   string
}

type Callbacks struct {
	SetLampMode       func(mode string) error
	SetHighlight      func(percent int) error
	SetHighlightTime  func(seconds int) error
	SetLowlight       func(percent int) error
	SetLowlightTime   func(timeVal int) error
	SetPIRSensitivity func(percent int) error
	SetLuxThreshold   func(lux int) error
	SetSiren          func(on bool) error
	SetResolution     func(res string) error
}

type Client struct {
	cfg       Config
	cb        Callbacks
	client    paho.Client
	nodeID    string
	baseTopic string
	mu        sync.RWMutex
}

func NewClient(cfg Config, cb Callbacks) *Client {
	if cfg.DiscoveryPrefix == "" {
		cfg.DiscoveryPrefix = "homeassistant"
	}
	if cfg.Model == "" {
		cfg.Model = "L 625 CAM SC"
	}
	cleanDID := strings.ReplaceAll(cfg.DeviceID, "-", "_")
	if cleanDID == "" {
		cleanDID = "camera"
	}
	nodeID := fmt.Sprintf("steinel_%s", cleanDID)

	basePrefix := strings.TrimSuffix(cfg.TopicPrefix, "/")
	if basePrefix == "" {
		basePrefix = "steinel"
	}

	// Always nest DeviceID under base prefix: <basePrefix>/<deviceID>
	// This guarantees no conflicts when multiple Steinel cameras run on the same MQTT broker.
	fullBaseTopic := fmt.Sprintf("%s/%s", basePrefix, cfg.DeviceID)

	if cfg.ClientID == "" {
		cfg.ClientID = fmt.Sprintf("steinel_bridge_%s", cleanDID)
	}

	c := &Client{
		cfg:       cfg,
		cb:        cb,
		nodeID:    nodeID,
		baseTopic: fullBaseTopic,
	}

	return c
}

func (c *Client) Start(ctx context.Context) error {
	opts := paho.NewClientOptions()
	opts.AddBroker(c.cfg.Broker)
	opts.SetClientID(c.cfg.ClientID)
	if c.cfg.Username != "" {
		opts.SetUsername(c.cfg.Username)
	}
	if c.cfg.Password != "" {
		opts.SetPassword(c.cfg.Password)
	}

	availTopic := fmt.Sprintf("%s/availability", c.baseTopic)
	opts.SetWill(availTopic, "offline", 1, true)
	opts.SetAutoReconnect(true)
	opts.SetKeepAlive(30 * time.Second)

	opts.OnConnect = func(client paho.Client) {
		log.Printf("[MQTT] 🔌 Connected to MQTT broker: %s (Topic: %s)", c.cfg.Broker, c.baseTopic)
		// 1. Publish Online Status
		client.Publish(availTopic, 1, true, "online")

		// 2. Publish Home Assistant Discovery Topics
		c.publishDiscovery()

		// 3. Subscribe to Command Topics
		cmdFilter := fmt.Sprintf("%s/+/set", c.baseTopic)
		client.Subscribe(cmdFilter, 1, c.handleCommand)
		brightnessCmd := fmt.Sprintf("%s/light/brightness/set", c.baseTopic)
		client.Subscribe(brightnessCmd, 1, c.handleCommand)

		// 4. Publish Initial Device State
		c.publishStatus(events.GlobalBus.GetStatus())
	}

	opts.OnConnectionLost = func(client paho.Client, err error) {
		log.Printf("[MQTT] ⚠️ Connection lost: %v", err)
	}

	client := paho.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("MQTT connection error: %w", token.Error())
	}

	c.client = client

	// Hook into Global Event Bus
	events.GlobalBus.Subscribe(func(evt events.EventType, data interface{}) {
		if c.client != nil && c.client.IsConnected() {
			switch evt {
			case events.EventMotion:
				if m, ok := data.(events.MotionEvent); ok {
					c.publishMotion(m.IsMotion)
				}
			case events.EventDevice:
				if st, ok := data.(events.DeviceStatus); ok {
					c.publishStatus(st)
				}
			}
		}
	})

	return nil
}

func (c *Client) Close() {
	if c.client != nil && c.client.IsConnected() {
		availTopic := fmt.Sprintf("%s/availability", c.baseTopic)
		c.client.Publish(availTopic, 1, true, "offline").Wait()
		c.client.Disconnect(250)
	}
}

// --- Home Assistant Auto-Discovery ---

func (c *Client) publishDiscovery() {
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
		c.client.Publish(discTopic, 1, true, payload)
	}

	// 1. Light (Hauptlicht mit Dimmung)
	publishEntity("light", "main_light", map[string]interface{}{
		"name":                     "Hauptlicht",
		"state_topic":              fmt.Sprintf("%s/light/state", c.baseTopic),
		"command_topic":            fmt.Sprintf("%s/light/set", c.baseTopic),
		"brightness_state_topic":   fmt.Sprintf("%s/light/brightness/state", c.baseTopic),
		"brightness_command_topic": fmt.Sprintf("%s/light/brightness/set", c.baseTopic),
		"brightness_scale":         100,
		"icon":                     "mdi:outdoor-lamp",
	})

	// 2. Select (Betriebsmodus: Sensor, Dauerlicht, Aus)
	publishEntity("select", "mode", map[string]interface{}{
		"name":          "Betriebsmodus",
		"options":       []string{"Sensor", "Dauerlicht", "Aus"},
		"state_topic":   fmt.Sprintf("%s/mode/state", c.baseTopic),
		"command_topic": fmt.Sprintf("%s/mode/set", c.baseTopic),
		"icon":          "mdi:theme-light-dark",
	})

	// 3. Binary Sensor (Motion)
	publishEntity("binary_sensor", "motion", map[string]interface{}{
		"name":         "Bewegung",
		"device_class": "motion",
		"state_topic":  fmt.Sprintf("%s/motion/state", c.baseTopic),
	})

	// 4. Binary Sensor (PIR Status)
	publishEntity("binary_sensor", "pir_status", map[string]interface{}{
		"name":         "PIR Sensor aktiv",
		"device_class": "running",
		"state_topic":  fmt.Sprintf("%s/pir/state", c.baseTopic),
		"icon":         "mdi:motion-sensor",
	})

	// 5. Sensor (Lux)
	publishEntity("sensor", "lux", map[string]interface{}{
		"name":                "Umgebungshelligkeit",
		"device_class":        "illuminance",
		"state_class":         "measurement",
		"unit_of_measurement": "lx",
		"state_topic":         fmt.Sprintf("%s/lux/state", c.baseTopic),
	})

	// 6. Number (PIR Sensitivity: 0 - 100%)
	publishEntity("number", "pir_sensitivity", map[string]interface{}{
		"name":                "PIR Empfindlichkeit",
		"min":                 0,
		"max":                 100,
		"step":                1,
		"unit_of_measurement": "%",
		"icon":                "mdi:tune",
		"state_topic":         fmt.Sprintf("%s/pir_sensitivity/state", c.baseTopic),
		"command_topic":       fmt.Sprintf("%s/pir_sensitivity/set", c.baseTopic),
	})

	// 7. Number (Lux Threshold: 2 - 1000 lx)
	publishEntity("number", "lux_threshold", map[string]interface{}{
		"name":                "Dämmerungsschwelle",
		"min":                 2,
		"max":                 1000,
		"step":                5,
		"unit_of_measurement": "lx",
		"icon":                "mdi:weather-sunset",
		"state_topic":         fmt.Sprintf("%s/lux_threshold/state", c.baseTopic),
		"command_topic":       fmt.Sprintf("%s/lux_threshold/set", c.baseTopic),
	})

	// 8. Number (Duration: 5 - 900s)
	publishEntity("number", "duration", map[string]interface{}{
		"name":                "Nachlaufzeit",
		"min":                 5,
		"max":                 900,
		"step":                5,
		"unit_of_measurement": "s",
		"icon":                "mdi:timer-outline",
		"state_topic":         fmt.Sprintf("%s/duration/state", c.baseTopic),
		"command_topic":       fmt.Sprintf("%s/duration/set", c.baseTopic),
	})

	// 9. Number (Grundlicht: 0 - 50%)
	publishEntity("number", "lowlight", map[string]interface{}{
		"name":                "Grundlicht Helligkeit",
		"min":                 0,
		"max":                 50,
		"step":                5,
		"unit_of_measurement": "%",
		"icon":                "mdi:lightbulb-night",
		"state_topic":         fmt.Sprintf("%s/lowlight/state", c.baseTopic),
		"command_topic":       fmt.Sprintf("%s/lowlight/set", c.baseTopic),
	})

	// 10. Siren (Warnton / Alarm)
	publishEntity("siren", "siren", map[string]interface{}{
		"name":          "Sirene",
		"icon":          "mdi:bullhorn",
		"state_topic":   fmt.Sprintf("%s/siren/state", c.baseTopic),
		"command_topic": fmt.Sprintf("%s/siren/set", c.baseTopic),
	})

	// 11. Select (Video Auflösung)
	publishEntity("select", "resolution", map[string]interface{}{
		"name":          "Video Auflösung",
		"options":       []string{"1080p", "720p", "360p"},
		"icon":          "mdi:video-vintage",
		"state_topic":   fmt.Sprintf("%s/resolution/state", c.baseTopic),
		"command_topic": fmt.Sprintf("%s/resolution/set", c.baseTopic),
	})

	log.Printf("[MQTT] 📢 Published Home Assistant Auto-Discovery entities for %s under %s", c.nodeID, c.cfg.DiscoveryPrefix)
}

// --- State Publication ---

func (c *Client) publishMotion(isMotion bool) {
	state := "OFF"
	if isMotion {
		state = "ON"
	}
	c.client.Publish(fmt.Sprintf("%s/motion/state", c.baseTopic), 1, false, state)
}

func (c *Client) publishStatus(st events.DeviceStatus) {
	pub := func(subTopic string, val string) {
		c.client.Publish(fmt.Sprintf("%s/%s", c.baseTopic, subTopic), 1, true, val)
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
		pub("light/brightness/state", strconv.Itoa(st.Highlight))
	}
	pub("duration/state", strconv.Itoa(st.HighlightTime))
	pub("lowlight/state", strconv.Itoa(st.Lowlight))

	// 3. Sensor values
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

// --- Command Dispatching ---

func (c *Client) handleCommand(client paho.Client, msg paho.Message) {
	topic := msg.Topic()
	payload := strings.TrimSpace(string(msg.Payload()))
	log.Printf("[MQTT] 📩 Command received on %s: %s", topic, payload)

	switch {
	case strings.HasSuffix(topic, "/light/set"):
		if strings.EqualFold(payload, "ON") {
			if c.cb.SetLampMode != nil {
				_ = c.cb.SetLampMode("on")
			}
		} else {
			if c.cb.SetLampMode != nil {
				_ = c.cb.SetLampMode("off")
			}
		}

	case strings.HasSuffix(topic, "/light/brightness/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if c.cb.SetHighlight != nil {
				_ = c.cb.SetHighlight(val)
			}
		}

	case strings.HasSuffix(topic, "/mode/set"):
		switch strings.ToLower(payload) {
		case "sensor", "auto", "2":
			if c.cb.SetLampMode != nil {
				_ = c.cb.SetLampMode("auto")
			}
		case "dauerlicht", "on", "1":
			if c.cb.SetLampMode != nil {
				_ = c.cb.SetLampMode("on")
			}
		case "aus", "off", "0":
			if c.cb.SetLampMode != nil {
				_ = c.cb.SetLampMode("off")
			}
		}

	case strings.HasSuffix(topic, "/pir_sensitivity/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if c.cb.SetPIRSensitivity != nil {
				_ = c.cb.SetPIRSensitivity(val)
			}
		}

	case strings.HasSuffix(topic, "/lux_threshold/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if c.cb.SetLuxThreshold != nil {
				_ = c.cb.SetLuxThreshold(val)
			}
		}

	case strings.HasSuffix(topic, "/duration/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if c.cb.SetHighlightTime != nil {
				_ = c.cb.SetHighlightTime(val)
			}
		}

	case strings.HasSuffix(topic, "/lowlight/set"):
		if val, err := strconv.Atoi(payload); err == nil {
			if c.cb.SetLowlight != nil {
				_ = c.cb.SetLowlight(val)
			}
		}

	case strings.HasSuffix(topic, "/siren/set"):
		on := strings.EqualFold(payload, "ON") || payload == "1" || strings.EqualFold(payload, "true")
		if c.cb.SetSiren != nil {
			_ = c.cb.SetSiren(on)
		}

	case strings.HasSuffix(topic, "/resolution/set"):
		if c.cb.SetResolution != nil {
			_ = c.cb.SetResolution(payload)
		}
	}
}
