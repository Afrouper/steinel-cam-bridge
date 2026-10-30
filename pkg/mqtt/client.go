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

// CameraDevice represents an individual camera's metadata, control hooks, and event bus for MQTT.
type CameraDevice struct {
	Path      string // Unique camera path identifier, e.g. "steinel-hinterhof"
	Name      string // Friendly name, e.g. "Einfahrt" or "Garten"
	DeviceID  string // Immutable technical ID, e.g. "de-m4yfowbr"
	ProductID string // e.g. "pr-qtatbtbi"
	Model     string // e.g. "L 625 CAM SC"
	Callbacks Callbacks
	EventBus  *events.Bus
	nodeID    string
	baseTopic string
}

// Key returns the internal map lookup key for the camera.
func (d *CameraDevice) Key() string {
	if d.Path != "" {
		return d.Path
	}
	if d.DeviceID != "" {
		return d.DeviceID
	}
	return "primary"
}

// Client manages the MQTT connection, Home Assistant discovery, state publishing, and command reception.
type Client struct {
	cfg       Config
	cb        Callbacks
	client    paho.Client
	eventBus  *events.Bus
	nodeID    string
	baseTopic string
	mu        sync.RWMutex

	camerasMu sync.RWMutex
	cameras   map[string]*CameraDevice
}

// NewClient initializes a new MQTT client instance.
func NewClient(cfg Config, cb Callbacks, eventBus *events.Bus) *Client {
	if cfg.DiscoveryPrefix == "" {
		cfg.DiscoveryPrefix = "homeassistant"
	}
	if cfg.Model == "" {
		cfg.Model = "L 625 CAM SC"
	}
	if eventBus == nil {
		eventBus = events.GlobalBus
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

	c := &Client{
		cfg:       cfg,
		cb:        cb,
		eventBus:  eventBus,
		nodeID:    nodeID,
		baseTopic: fullBaseTopic,
		cameras:   make(map[string]*CameraDevice),
	}

	key := cfg.DeviceID
	if key == "" {
		key = "primary"
	}
	primaryDev := &CameraDevice{
		Path:      key,
		Name:      "",
		DeviceID:  cfg.DeviceID,
		ProductID: cfg.ProductID,
		Model:     cfg.Model,
		Callbacks: cb,
		EventBus:  eventBus,
		nodeID:    nodeID,
		baseTopic: fullBaseTopic,
	}
	c.cameras[key] = primaryDev

	return c
}

func isPlaceholderDevice(dev *CameraDevice) bool {
	if dev == nil {
		return false
	}
	return (dev.Path == "primary" || dev.Path == "") &&
		dev.Name == "" &&
		dev.Callbacks.SetLampMode == nil &&
		dev.Callbacks.SetHighlight == nil &&
		dev.Callbacks.SetSiren == nil &&
		dev.Callbacks.SetResolution == nil
}

// RegisterCamera registers an additional camera device on the MQTT client.
func (c *Client) RegisterCamera(dev CameraDevice) {
	if dev.Model == "" {
		dev.Model = "L 625 CAM SC"
	}

	basePrefix := strings.TrimSuffix(c.cfg.TopicPrefix, "/")
	if basePrefix == "" {
		basePrefix = "steinel"
	}

	if dev.DeviceID != "" {
		cleanDID := strings.ReplaceAll(dev.DeviceID, "-", "_")
		dev.nodeID = fmt.Sprintf("steinel_%s", cleanDID)
		dev.baseTopic = fmt.Sprintf("%s/%s", basePrefix, dev.DeviceID)
	} else if dev.Path != "" {
		cleanPath := strings.ReplaceAll(dev.Path, "-", "_")
		dev.nodeID = fmt.Sprintf("steinel_%s", cleanPath)
		dev.baseTopic = fmt.Sprintf("%s/%s", basePrefix, dev.Path)
	} else {
		dev.nodeID = "steinel_camera"
		dev.baseTopic = fmt.Sprintf("%s/camera", basePrefix)
	}

	camCopy := dev
	camKey := dev.Key()

	c.camerasMu.Lock()
	// If the cameras map only contains the unconfigured placeholder from NewClient,
	// clean it up so we don't have a zombie device.
	if len(c.cameras) == 1 {
		for k, existing := range c.cameras {
			if isPlaceholderDevice(existing) {
				delete(c.cameras, k)
			}
		}
	}
	c.cameras[camKey] = &camCopy
	c.camerasMu.Unlock()

	c.mu.RLock()
	cl := c.client
	c.mu.RUnlock()

	if cl != nil && cl.IsConnected() {
		availTopic := fmt.Sprintf("%s/availability", dev.baseTopic)
		cl.Publish(availTopic, 1, true, "online")

		c.publishDeviceDiscovery(cl, &camCopy)

		cmdFilter := fmt.Sprintf("%s/+/set", dev.baseTopic)
		cl.Subscribe(cmdFilter, 1, c.handleCommand)
		brightnessCmd := fmt.Sprintf("%s/light/brightness/set", dev.baseTopic)
		cl.Subscribe(brightnessCmd, 1, c.handleCommand)

		if dev.EventBus != nil {
			st := dev.EventBus.GetStatus()
			if st.LampMode > 0 || st.Lux >= 2 || st.Highlight > 0 {
				c.publishDeviceStatus(&camCopy, st)
			}
		}
	}

	if dev.EventBus != nil {
		dev.EventBus.Subscribe(func(evt events.EventType, data interface{}) {
			c.mu.RLock()
			client := c.client
			c.mu.RUnlock()

			if client != nil && client.IsConnected() {
				switch evt {
				case events.EventMotion:
					if m, ok := data.(events.MotionEvent); ok {
						c.publishDeviceMotion(&camCopy, m.IsMotion)
					}
				case events.EventDevice:
					if st, ok := data.(events.DeviceStatus); ok {
						c.publishDeviceStatus(&camCopy, st)
					}
				}
			}
		})
	}
}

// UpdateCameraDeviceInfo updates the device identifiers for a specific camera (identified by cameraPath or deviceID)
// and re-publishes discovery & availability for that camera only.
func (c *Client) UpdateCameraDeviceInfo(cameraPath, deviceID, productID string) {
	if deviceID == "" {
		return
	}

	c.camerasMu.Lock()
	defer c.camerasMu.Unlock()

	var targetDev *CameraDevice
	var targetKey string

	// 1. Try finding by cameraPath
	if cameraPath != "" {
		if dev, ok := c.cameras[cameraPath]; ok {
			targetDev = dev
			targetKey = cameraPath
		}
	}

	// 2. Fallback search by Path, DeviceID, or Name
	if targetDev == nil {
		for k, dev := range c.cameras {
			if (cameraPath != "" && (dev.Path == cameraPath || dev.DeviceID == cameraPath || dev.Name == cameraPath)) ||
				(cameraPath == "" && len(c.cameras) == 1) {
				targetDev = dev
				targetKey = k
				break
			}
		}
	}

	// 3. Fallback to primary camera if still not found and cameraPath matches primary or is empty
	if targetDev == nil && (cameraPath == "" || cameraPath == "primary") {
		c.mu.RLock()
		primaryDID := c.cfg.DeviceID
		c.mu.RUnlock()
		if dev, ok := c.cameras[primaryDID]; ok {
			targetDev = dev
			targetKey = primaryDID
		}
	}

	if targetDev == nil {
		logger.Warn("MQTT", "⚠️ Cannot update DeviceID '%s': camera '%s' not found", deviceID, cameraPath)
		return
	}

	// If already up to date, nothing to do
	if targetDev.DeviceID == deviceID && (productID == "" || targetDev.ProductID == productID) {
		return
	}

	oldDID := targetDev.DeviceID
	oldBaseTopic := targetDev.baseTopic
	camName := targetDev.Name
	if camName == "" {
		camName = targetDev.Path
	}
	logger.Info("MQTT", "🔄 Updating DeviceID for '%s' (%s): '%s' -> '%s'", camName, targetDev.Path, oldDID, deviceID)

	targetDev.DeviceID = deviceID
	if productID != "" {
		targetDev.ProductID = productID
	}
	cleanDID := strings.ReplaceAll(deviceID, "-", "_")
	targetDev.nodeID = fmt.Sprintf("steinel_%s", cleanDID)

	basePrefix := strings.TrimSuffix(c.cfg.TopicPrefix, "/")
	if basePrefix == "" {
		basePrefix = "steinel"
	}
	targetDev.baseTopic = fmt.Sprintf("%s/%s", basePrefix, deviceID)

	// Update client-level identifiers if this is the primary camera
	c.mu.Lock()
	if c.cfg.DeviceID == "" || c.cfg.DeviceID == oldDID || c.cfg.DeviceID == targetKey {
		c.cfg.DeviceID = deviceID
		if productID != "" {
			c.cfg.ProductID = productID
		}
		c.nodeID = targetDev.nodeID
		c.baseTopic = targetDev.baseTopic
	}
	cl := c.client
	c.mu.Unlock()

	// If connected, update MQTT subscriptions and publish discovery
	if cl != nil && cl.IsConnected() {
		if oldBaseTopic != "" && oldBaseTopic != targetDev.baseTopic {
			cl.Unsubscribe(fmt.Sprintf("%s/+/set", oldBaseTopic), fmt.Sprintf("%s/light/brightness/set", oldBaseTopic))
			cl.Publish(fmt.Sprintf("%s/availability", oldBaseTopic), 1, true, "offline")
		}

		availTopic := fmt.Sprintf("%s/availability", targetDev.baseTopic)
		cl.Publish(availTopic, 1, true, "online")

		c.publishDeviceDiscovery(cl, targetDev)

		cmdFilter := fmt.Sprintf("%s/+/set", targetDev.baseTopic)
		cl.Subscribe(cmdFilter, 1, c.handleCommand)
		brightnessCmd := fmt.Sprintf("%s/light/brightness/set", targetDev.baseTopic)
		cl.Subscribe(brightnessCmd, 1, c.handleCommand)

		if targetDev.EventBus != nil {
			st := targetDev.EventBus.GetStatus()
			if st.LampMode > 0 || st.Lux >= 2 || st.Highlight > 0 {
				c.publishDeviceStatus(targetDev, st)
			}
		}
	}
}

// UpdateDeviceInfo updates the primary camera device identifiers and re-publishes discovery & availability.
// Maintained for backward compatibility.
func (c *Client) UpdateDeviceInfo(deviceID, productID string) {
	c.UpdateCameraDeviceInfo("", deviceID, productID)
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

		logger.Info("MQTT", "🔌 Connected to MQTT broker: %s", c.cfg.Broker)

		c.camerasMu.RLock()
		devs := make([]*CameraDevice, 0, len(c.cameras))
		for _, d := range c.cameras {
			devs = append(devs, d)
		}
		c.camerasMu.RUnlock()

		for _, dev := range devs {
			devAvailTopic := fmt.Sprintf("%s/availability", dev.baseTopic)
			client.Publish(devAvailTopic, 1, true, "online")

			c.publishDeviceDiscovery(client, dev)

			cmdFilter := fmt.Sprintf("%s/+/set", dev.baseTopic)
			client.Subscribe(cmdFilter, 1, c.handleCommand)
			brightnessCmd := fmt.Sprintf("%s/light/brightness/set", dev.baseTopic)
			client.Subscribe(brightnessCmd, 1, c.handleCommand)

			if dev.EventBus != nil {
				st := dev.EventBus.GetStatus()
				if st.LampMode > 0 || st.Lux >= 2 || st.Highlight > 0 {
					c.publishDeviceStatus(dev, st)
				}
			}
		}
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

	// Hook into Event Bus
	c.eventBus.Subscribe(func(evt events.EventType, data interface{}) {
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
		c.camerasMu.RLock()
		for _, dev := range c.cameras {
			devAvailTopic := fmt.Sprintf("%s/availability", dev.baseTopic)
			cl.Publish(devAvailTopic, 1, true, "offline").Wait()
		}
		c.camerasMu.RUnlock()
		cl.Disconnect(250)
	}
}
