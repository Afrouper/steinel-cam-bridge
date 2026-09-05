package mqtt

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// Config holds the configuration options for connecting to an MQTT broker and defining Home Assistant entities.
type Config struct {
	Broker          string
	Username        string
	Password        string
	ClientID        string
	TopicPrefix     string // Base topic, default: "steinel"
	DiscoveryPrefix string // Default: "homeassistant"
	DeviceID        string // e.g. "de-m4yfowbr"
	ProductID       string // e.g. "pr-qtatbtbi"
	Model           string // e.g. "L 625 CAM SC"
	BridgeHTTPURL   string
}

// Callbacks holds function hooks for executing device control actions triggered via MQTT command topics.
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

// Client manages the MQTT connection, Home Assistant discovery, state publishing, and command reception.
type Client struct {
	cfg       Config
	cb        Callbacks
	client    paho.Client
	nodeID    string
	baseTopic string
	mu        sync.RWMutex
}

// NewClient initializes a new MQTT client instance.
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

	var fullBaseTopic string
	if cfg.DeviceID != "" {
		fullBaseTopic = fmt.Sprintf("%s/%s", basePrefix, cfg.DeviceID)
	} else {
		fullBaseTopic = fmt.Sprintf("%s/%s", basePrefix, cleanDID)
	}

	if cfg.ClientID == "" {
		cfg.ClientID = fmt.Sprintf("steinel_bridge_%s", cleanDID)
	}

	return &Client{
		cfg:       cfg,
		cb:        cb,
		nodeID:    nodeID,
		baseTopic: fullBaseTopic,
	}
}

// UpdateDeviceInfo updates the device identifiers and re-publishes discovery & availability.
func (c *Client) UpdateDeviceInfo(deviceID, productID string) {
	c.mu.Lock()
	if deviceID == "" || c.cfg.DeviceID == deviceID {
		c.mu.Unlock()
		return
	}
	logger.Info("MQTT", "🔄 Updating DeviceID: '%s' -> '%s'", c.cfg.DeviceID, deviceID)
	c.cfg.DeviceID = deviceID
	if productID != "" {
		c.cfg.ProductID = productID
	}
	cleanDID := strings.ReplaceAll(deviceID, "-", "_")
	c.nodeID = fmt.Sprintf("steinel_%s", cleanDID)
	basePrefix := strings.TrimSuffix(c.cfg.TopicPrefix, "/")
	if basePrefix == "" {
		basePrefix = "steinel"
	}
	c.baseTopic = fmt.Sprintf("%s/%s", basePrefix, deviceID)
	cl := c.client
	c.mu.Unlock()

	if cl != nil && cl.IsConnected() {
		availTopic := fmt.Sprintf("%s/availability", c.baseTopic)
		cl.Publish(availTopic, 1, true, "online")
		c.publishDiscovery(cl)
		c.publishStatus(events.GlobalBus.GetStatus())
	}
}

// Start connects to the MQTT broker, configures LWT, subscribes to commands, and publishes initial discovery & state.
func (c *Client) Start(_ context.Context) error {
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
		c.mu.Lock()
		c.client = client
		c.mu.Unlock()

		logger.Info("MQTT", "🔌 Connected to MQTT broker: %s (Topic: %s)", c.cfg.Broker, c.baseTopic)
		// 1. Publish Online Status
		client.Publish(availTopic, 1, true, "online")

		// 2. Publish Home Assistant Discovery Topics
		c.publishDiscovery(client)

		// 3. Subscribe to Command Topics
		cmdFilter := fmt.Sprintf("%s/+/set", c.baseTopic)
		client.Subscribe(cmdFilter, 1, c.handleCommand)
		brightnessCmd := fmt.Sprintf("%s/light/brightness/set", c.baseTopic)
		client.Subscribe(brightnessCmd, 1, c.handleCommand)

		// 4. Publish Initial Device State
		c.publishStatus(events.GlobalBus.GetStatus())
	}

	opts.OnConnectionLost = func(_ paho.Client, err error) {
		logger.Warn("MQTT", "⚠️ Connection lost: %v", err)
	}

	client := paho.NewClient(opts)
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()

	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("MQTT connection error: %w", token.Error())
	}

	// Hook into Global Event Bus
	events.GlobalBus.Subscribe(func(evt events.EventType, data interface{}) {
		c.mu.RLock()
		cl := c.client
		c.mu.RUnlock()

		if cl != nil && cl.IsConnected() {
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

// Close gracefully disconnects from the MQTT broker after publishing offline availability.
func (c *Client) Close() {
	c.mu.RLock()
	cl := c.client
	c.mu.RUnlock()

	if cl != nil && cl.IsConnected() {
		availTopic := fmt.Sprintf("%s/availability", c.baseTopic)
		cl.Publish(availTopic, 1, true, "offline").Wait()
		cl.Disconnect(250)
	}
}
