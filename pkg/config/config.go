package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/nabto"
)

// Config encapsulates the complete resolved runtime configuration following the 12-Factor App methodology.
type Config struct {
	NabtoConfig        *nabto.Config
	CameraType         string
	CameraUser         string
	CameraPassword     string
	Resolution         string
	AudioCodec         string
	RTSPPort           int
	RTSPPath           string
	ONVIFPort          int
	ResetPairing       bool
	MQTTBroker         string
	MQTTUser           string
	MQTTPassword       string
	MQTTTopic          string
	MQTTDiscovery      string
	LogLevel           string
	LogFormat          string
	SDCardSyncInterval int
	NabtoDriver        string // "cgo" (default) or "pure"
	BridgeUser         string
	BridgePass         string
	IsBeta             bool
	AppVersion         string
}

// NewDefaultConfig returns a Config initialized with Layer 1 (code) defaults.
func NewDefaultConfig() *Config {
	return &Config{
		NabtoConfig: &nabto.Config{
			CameraIP:   "",
			CameraPort: 5592,
			KeyPath:    "data/client.key",
		},
		Resolution:         "1080p",
		AudioCodec:         "aac",
		RTSPPort:           8554,
		RTSPPath:           "steinel",
		ONVIFPort:          8000,
		ResetPairing:       false,
		MQTTBroker:         "",
		MQTTUser:           "",
		MQTTPassword:       "",
		MQTTTopic:          "steinel",
		MQTTDiscovery:      "homeassistant",
		LogLevel:           "info",
		LogFormat:          "",
		SDCardSyncInterval: 120,
		NabtoDriver:        "cgo",
		BridgeUser:         "",
		BridgePass:         "",
		IsBeta:             false,
		AppVersion:         "dev",
	}
}

// LoadHomeAssistantOptionsFromPath reads and unmarshals options from a JSON file (e.g. /data/options.json).
func LoadHomeAssistantOptionsFromPath(path string, cfg *Config) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("HA Addon", "⚠️ Warning: Failed to read %s: %v", path, err)
		}
		return
	}

	var opts struct {
		CameraIP            string `json:"camera_ip"`
		CameraType          string `json:"camera_type"`
		CameraUser          string `json:"camera_user"`
		CameraPassword      string `json:"camera_password"`
		QRCode              string `json:"qr_code"`
		Resolution          string `json:"resolution"`
		AudioCodec          string `json:"audio_codec"`
		RTSPPort            int    `json:"rtsp_port"`
		ONVIFPort           int    `json:"onvif_port"`
		ResetPairing        bool   `json:"reset_pairing"`
		MQTTBroker          string `json:"mqtt_broker"`
		MQTTHost            string `json:"mqtt_host"`
		MQTTPort            int    `json:"mqtt_port"`
		MQTTUser            string `json:"mqtt_user"`
		MQTTPassword        string `json:"mqtt_password"`
		MQTTTopicPrefix     string `json:"mqtt_topic_prefix"`
		MQTTDiscoveryPrefix string `json:"mqtt_discovery_prefix"`
		LogLevel            string `json:"log_level"`
		SDCardSyncInterval  int    `json:"sdcard_sync_interval"`
		NabtoDriver         string `json:"nabto_driver"`
		UseCGONabto         bool   `json:"use_cgo_nabto"`
		BridgeUser          string `json:"bridge_user"`
		BridgePass          string `json:"bridge_pass"`
		BridgePassword      string `json:"bridge_password"`
	}

	if err := json.Unmarshal(data, &opts); err != nil {
		logger.Warn("HA Addon", "⚠️ Warning: Failed to parse %s: %v", path, err)
		return
	}

	logger.Info("HA Addon", "🏠 Loaded configuration from %s", path)

	if opts.CameraIP != "" {
		cfg.NabtoConfig.CameraIP = opts.CameraIP
	}
	if opts.CameraType != "" {
		cfg.CameraType = opts.CameraType
	}
	if opts.CameraUser != "" {
		cfg.CameraUser = opts.CameraUser
	}
	if opts.CameraPassword != "" {
		cfg.CameraPassword = opts.CameraPassword
	}
	if opts.QRCode != "" {
		nabto.ParseQRCode(opts.QRCode, cfg.NabtoConfig)
	}
	if opts.Resolution != "" {
		cfg.Resolution = opts.Resolution
	}
	if opts.AudioCodec != "" {
		cfg.AudioCodec = opts.AudioCodec
	}
	if opts.RTSPPort > 0 {
		cfg.RTSPPort = opts.RTSPPort
	}
	if opts.ONVIFPort > 0 {
		cfg.ONVIFPort = opts.ONVIFPort
	}
	if opts.ResetPairing {
		cfg.ResetPairing = true
	}
	if opts.MQTTBroker != "" {
		cfg.MQTTBroker = opts.MQTTBroker
	} else if opts.MQTTHost != "" {
		port := 1883
		if opts.MQTTPort > 0 {
			port = opts.MQTTPort
		}
		cfg.MQTTBroker = fmt.Sprintf("tcp://%s:%d", opts.MQTTHost, port)
	}
	if opts.MQTTUser != "" {
		cfg.MQTTUser = opts.MQTTUser
	}
	if opts.MQTTPassword != "" {
		cfg.MQTTPassword = opts.MQTTPassword
	}
	if opts.MQTTTopicPrefix != "" {
		cfg.MQTTTopic = opts.MQTTTopicPrefix
	}
	if opts.MQTTDiscoveryPrefix != "" {
		cfg.MQTTDiscovery = opts.MQTTDiscoveryPrefix
	}
	if opts.LogLevel != "" {
		cfg.LogLevel = opts.LogLevel
	}
	if opts.SDCardSyncInterval > 0 {
		cfg.SDCardSyncInterval = opts.SDCardSyncInterval
	}
	if opts.NabtoDriver != "" {
		cfg.NabtoDriver = opts.NabtoDriver
	} else if opts.UseCGONabto {
		cfg.NabtoDriver = "cgo"
	}
	if opts.BridgeUser != "" {
		cfg.BridgeUser = opts.BridgeUser
	}
	if opts.BridgePass != "" {
		cfg.BridgePass = opts.BridgePass
	} else if opts.BridgePassword != "" {
		cfg.BridgePass = opts.BridgePassword
	}
}

// Resolve resolves configuration following the POSIX / 12-Factor App hierarchy:
// 1. Code Defaults (Layer 1 - lowest)
// 2. Config File e.g. /data/options.json & Supervisor Auto-Discovery (Layer 2)
// 3. Explicit Environment Variables (Layer 3)
// 4. Explicit CLI Flags (Layer 4 - highest)
func Resolve(optionsPath string, fs *flag.FlagSet) *Config {
	// 1. Layer 1: Code Defaults
	cfg := NewDefaultConfig()

	// 2. Layer 2: Configuration File
	if optionsPath != "" {
		LoadHomeAssistantOptionsFromPath(optionsPath, cfg)
	}

	// 2.1 Auto-discover Home Assistant MQTT service via Supervisor API if no broker was manually configured
	if cfg.MQTTBroker == "" {
		if broker, user, pass, err := FetchSupervisorMQTTOptions(); err == nil && broker != "" {
			cfg.MQTTBroker = broker
			cfg.MQTTUser = user
			cfg.MQTTPassword = pass
			logger.Info("HA Addon", "📡 Auto-discovered Home Assistant MQTT service: %s (User: %s)", broker, user)
		} else if err != nil && os.Getenv("SUPERVISOR_TOKEN") != "" {
			if strings.Contains(err.Error(), "not enabled") {
				logger.Info("HA Addon", "ℹ️ MQTT broker detected, but service binding is not yet active in Supervisor. Please restart the MQTT broker add-on once, or configure 'mqtt_broker' in add-on options.")
			} else {
				logger.Info("HA Addon", "ℹ️ MQTT auto-discovery check: %v", err)
			}
		}
	}

	// 3. Layer 3: Environment Variables (12-Factor App)
	if envQR := os.Getenv("QR_CODE"); envQR != "" {
		nabto.ParseQRCode(envQR, cfg.NabtoConfig)
	}
	if ip := os.Getenv("CAMERA_IP"); ip != "" {
		cfg.NabtoConfig.CameraIP = ip
	}
	if key := os.Getenv("KEY_PATH"); key != "" {
		cfg.NabtoConfig.KeyPath = key
	}
	if res := os.Getenv("RESOLUTION"); res != "" {
		cfg.Resolution = res
	}
	if ac := os.Getenv("AUDIO_CODEC"); ac != "" {
		cfg.AudioCodec = ac
	}
	if envReset := os.Getenv("RESET_PAIRING"); envReset == "true" || envReset == "1" {
		cfg.ResetPairing = true
	}
	if portStr := os.Getenv("RTSP_PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			cfg.RTSPPort = p
		}
	}
	if pathStr := os.Getenv("RTSP_PATH"); pathStr != "" {
		cfg.RTSPPath = pathStr
	}
	if portStr := os.Getenv("ONVIF_PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			cfg.ONVIFPort = p
		}
	}
	if ct := os.Getenv("CAMERA_TYPE"); ct != "" {
		cfg.CameraType = ct
	}
	if cu := os.Getenv("CAMERA_USER"); cu != "" {
		cfg.CameraUser = cu
	}
	if cp := os.Getenv("CAMERA_PASSWORD"); cp != "" {
		cfg.CameraPassword = cp
	} else if cp := os.Getenv("CAMERA_PASS"); cp != "" {
		cfg.CameraPassword = cp
	}
	if bu := os.Getenv("BRIDGE_USER"); bu != "" {
		cfg.BridgeUser = bu
	}
	if bp := os.Getenv("BRIDGE_PASS"); bp != "" {
		cfg.BridgePass = bp
	} else if bp := os.Getenv("BRIDGE_PASSWORD"); bp != "" {
		cfg.BridgePass = bp
	}
	if mb := os.Getenv("MQTT_BROKER"); mb != "" {
		cfg.MQTTBroker = mb
	}
	if mu := os.Getenv("MQTT_USER"); mu != "" {
		cfg.MQTTUser = mu
	}
	if mp := os.Getenv("MQTT_PASSWORD"); mp != "" {
		cfg.MQTTPassword = mp
	}
	if mt := os.Getenv("MQTT_TOPIC_PREFIX"); mt != "" {
		cfg.MQTTTopic = mt
	}
	if md := os.Getenv("MQTT_DISCOVERY_PREFIX"); md != "" {
		cfg.MQTTDiscovery = md
	}
	if syncStr := os.Getenv("SDCARD_SYNC_INTERVAL"); syncStr != "" {
		if s, err := strconv.Atoi(syncStr); err == nil && s > 0 {
			cfg.SDCardSyncInterval = s
		}
	} else if syncStr := os.Getenv("SYNC_INTERVAL"); syncStr != "" {
		if s, err := strconv.Atoi(syncStr); err == nil && s > 0 {
			cfg.SDCardSyncInterval = s
		}
	}
	if sct := os.Getenv("SCT"); sct != "" {
		cfg.NabtoConfig.SCT = sct
	}
	if pwd := os.Getenv("PAIR_PWD"); pwd != "" {
		cfg.NabtoConfig.PairPwd = pwd
	}
	if did := os.Getenv("DEVICE_ID"); did != "" {
		cfg.NabtoConfig.DeviceID = did
	}
	if pid := os.Getenv("PRODUCT_ID"); pid != "" {
		cfg.NabtoConfig.ProductID = pid
	}
	if nd := os.Getenv("NABTO_DRIVER"); nd != "" {
		cfg.NabtoDriver = nd
	}
	if os.Getenv("USE_CGO_NABTO") == "false" || os.Getenv("USE_CGO_NABTO") == "0" {
		cfg.NabtoDriver = "pure"
	} else if os.Getenv("USE_CGO_NABTO") == "true" || os.Getenv("USE_CGO_NABTO") == "1" {
		cfg.NabtoDriver = "cgo"
	}
	if envLogLevel := os.Getenv("LOG_LEVEL"); envLogLevel != "" {
		cfg.LogLevel = envLogLevel
	}
	if envLogFormat := os.Getenv("LOG_FORMAT"); envLogFormat != "" {
		cfg.LogFormat = envLogFormat
	}
	if envVer := os.Getenv("APP_VERSION"); envVer != "" {
		cfg.AppVersion = envVer
	} else if envVer := os.Getenv("VERSION"); envVer != "" {
		cfg.AppVersion = envVer
	}

	// 4. Layer 4: Explicit CLI Flags (POSIX)
	var cliBeta bool
	if fs != nil {
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "ip":
				cfg.NabtoConfig.CameraIP = f.Value.String()
			case "type":
				cfg.CameraType = f.Value.String()
			case "user":
				cfg.CameraUser = f.Value.String()
			case "pass", "password":
				cfg.CameraPassword = f.Value.String()
			case "bridge-user":
				cfg.BridgeUser = f.Value.String()
			case "bridge-pass", "bridge-password":
				cfg.BridgePass = f.Value.String()
			case "qr":
				nabto.ParseQRCode(f.Value.String(), cfg.NabtoConfig)
			case "key":
				cfg.NabtoConfig.KeyPath = f.Value.String()
			case "res":
				cfg.Resolution = f.Value.String()
			case "port":
				if p, err := strconv.Atoi(f.Value.String()); err == nil && p > 0 {
					cfg.RTSPPort = p
				}
			case "path":
				cfg.RTSPPath = f.Value.String()
			case "onvif":
				if p, err := strconv.Atoi(f.Value.String()); err == nil && p > 0 {
					cfg.ONVIFPort = p
				}
			case "reset-pairing":
				if b, err := strconv.ParseBool(f.Value.String()); err == nil {
					cfg.ResetPairing = b
				}
			case "mqtt-broker":
				cfg.MQTTBroker = f.Value.String()
			case "mqtt-user":
				cfg.MQTTUser = f.Value.String()
			case "mqtt-pass":
				cfg.MQTTPassword = f.Value.String()
			case "mqtt-topic":
				cfg.MQTTTopic = f.Value.String()
			case "mqtt-disc":
				cfg.MQTTDiscovery = f.Value.String()
			case "audio-codec":
				cfg.AudioCodec = f.Value.String()
			case "sync-interval", "sdcard-sync-interval":
				if s, err := strconv.Atoi(f.Value.String()); err == nil && s > 0 {
					cfg.SDCardSyncInterval = s
				}
			case "nabto-driver":
				cfg.NabtoDriver = f.Value.String()
			case "use-cgo":
				if b, err := strconv.ParseBool(f.Value.String()); err == nil && b {
					cfg.NabtoDriver = "cgo"
				}
			case "log-level", "loglevel":
				cfg.LogLevel = f.Value.String()
			case "beta":
				if b, err := strconv.ParseBool(f.Value.String()); err == nil {
					cliBeta = b
				}
			}
		})
	}

	cfg.IsBeta = cliBeta ||
		strings.Contains(strings.ToLower(cfg.AppVersion), "beta") ||
		os.Getenv("IS_BETA") == "true" ||
		os.Getenv("IS_BETA") == "1" ||
		os.Getenv("BETA") == "true"
	cfg.NabtoConfig.IsBeta = cfg.IsBeta

	return cfg
}

// Validate checks that all required configuration parameters are present and valid.
func (c *Config) Validate() error {
	if c.NabtoConfig.CameraIP == "" {
		return errors.New("camera IP address is mandatory! Please configure 'camera_ip' in Home Assistant or supply -ip / CAMERA_IP")
	}
	if c.RTSPPort <= 0 || c.RTSPPort > 65535 {
		return fmt.Errorf("invalid RTSP port: %d", c.RTSPPort)
	}
	if c.ONVIFPort <= 0 || c.ONVIFPort > 65535 {
		return fmt.Errorf("invalid ONVIF port: %d", c.ONVIFPort)
	}
	return nil
}

// ProbeCameraModel determines whether the camera is an L 620 CAM (Xiongmai Sofia) or L 625 CAM SC (Nabto Edge).
func (c *Config) ProbeCameraModel() (isL620 bool, modelName string) {
	switch strings.ToLower(strings.TrimSpace(c.CameraType)) {
	case "l620":
		isL620 = true
		logger.Info("Config", "📷 Camera model configured explicitly: Steinel L 620 CAM / XLED CAM 1 (Xiongmai Sofia)")
	case "l625":
		isL620 = false
		logger.Info("Config", "📷 Camera model configured explicitly: Steinel L 625 CAM SC (Nabto Edge)")
	default: // "auto" or unspecified
		logger.Info("Config", "🔍 Camera model set to 'auto': Probing %s on Port 34567...", c.NabtoConfig.CameraIP)
		if ProbePort(c.NabtoConfig.CameraIP, 34567, 1500*time.Millisecond) {
			isL620 = true
			logger.Info("Config", "🎯 Auto-detected Steinel L 620 CAM / XLED CAM 1 (Port 34567 Xiongmai Sofia is open)")
		} else {
			isL620 = false
			logger.Info("Config", "🎯 Auto-detected Steinel L 625 CAM SC (Port 34567 closed, selecting Nabto Edge driver)")
		}
	}

	modelName = "L 625 CAM SC"
	if isL620 {
		modelName = "L 620 CAM"
		if c.NabtoConfig.DeviceID == "" {
			c.NabtoConfig.DeviceID = "steinel-l620"
		}
		if c.NabtoConfig.ProductID == "" {
			c.NabtoConfig.ProductID = "pr-xiongmai"
		}
	}

	return isL620, modelName
}

// ProbePort checks if a specific TCP port is open and accepting connections within a given timeout.
func ProbePort(ip string, port int, timeout time.Duration) bool {
	target := net.JoinHostPort(ip, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err == nil {
		_ = conn.Close()
		return true
	}
	return false
}

// SetupKeyPath ensures parent directory for key file exists.
func (c *Config) SetupKeyPath() error {
	dir := filepath.Dir(c.NabtoConfig.KeyPath)
	if dir != "." && dir != "" {
		return os.MkdirAll(dir, 0755)
	}
	return nil
}
