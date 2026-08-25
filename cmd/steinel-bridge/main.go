package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pion/rtp"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mcu"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mqtt"
	"github.com/Afrouper/steinel-cam-bridge/pkg/nabto"
	"github.com/Afrouper/steinel-cam-bridge/pkg/onvif"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/Afrouper/steinel-cam-bridge/pkg/webrtc"
	"github.com/Afrouper/steinel-cam-bridge/pkg/xiongmai"
)

type BridgeManager struct {
	currentBridge   *webrtc.Bridge
	currentXMDriver *xiongmai.Driver
	mu              sync.RWMutex
}

func (m *BridgeManager) SetBridge(b *webrtc.Bridge) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentBridge = b
	m.currentXMDriver = nil
}

func (m *BridgeManager) SetXMDriver(d *xiongmai.Driver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentXMDriver = d
	m.currentBridge = nil
}

func (m *BridgeManager) SetResolution(res string) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return nil
	}
	return b.SetResolution(res)
}

func (m *BridgeManager) SetLampState(mode string) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		return b.SetLampState(mode)
	}
	if d != nil {
		modeLower := strings.ToLower(mode)
		on := modeLower == "on" || modeLower == "1" || modeLower == "dauerlicht"
		return d.SetLamp(on)
	}
	return fmt.Errorf("camera bridge offline")
}

func (m *BridgeManager) SetHighlight(percent int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetHighlight(percent)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetDim(percent)
	}
	return fmt.Errorf("camera bridge offline")
}

func (m *BridgeManager) SetHighlightTime(seconds int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetHighlightTime(seconds)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetDuration(seconds)
	}
	return fmt.Errorf("camera bridge offline")
}

func (m *BridgeManager) SetLowlight(percent int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetLowlight(percent)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetNightlight(percent)
	}
	return fmt.Errorf("camera bridge offline")
}

func (m *BridgeManager) SetLowlightTime(timeVal int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetLowlightTime(timeVal)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetNightlightDuration(fmt.Sprintf("%dh", timeVal/60))
	}
	return fmt.Errorf("camera bridge offline")
}

func (m *BridgeManager) SetPIRSensitivity(percent int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetPIRSensitivity(percent)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		dist := percent / 10
		if dist <= 0 {
			dist = 1
		}
		return d.SetDistance(dist)
	}
	return fmt.Errorf("camera bridge offline")
}

func (m *BridgeManager) SetLuxThreshold(lux int) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		b64, err := mcu.BuildSetLuxThreshold(lux)
		if err != nil {
			return err
		}
		return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	}
	if d != nil {
		return d.SetTwilight(lux)
	}
	return fmt.Errorf("camera bridge offline")
}

func (m *BridgeManager) SetSiren(on bool) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return nil
	}
	cmd := map[string]interface{}{
		"play": on,
	}
	return b.SendCommand("alarm_voice_ctl", cmd)
}

func (m *BridgeManager) RequestKeyframe() {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b != nil {
		b.RequestKeyframe()
	}
}

func (m *BridgeManager) GetSDCardManager() *webrtc.SDCardManager {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return nil
	}
	return b.GetSDCardManager()
}

func (m *BridgeManager) WriteAudioBackchannel(pkt *rtp.Packet) error {
	m.mu.RLock()
	b := m.currentBridge
	d := m.currentXMDriver
	m.mu.RUnlock()

	if b != nil {
		return b.WriteAudioBackchannel(pkt)
	}
	if d != nil {
		d.OnAudioBackchannelPacket(pkt)
		return nil
	}
	return fmt.Errorf("camera bridge offline")
}

var AppVersion = "dev"

// AppConfig encapsulates the complete resolved runtime configuration
type AppConfig struct {
	NabtoConfig    *nabto.Config
	CameraType     string
	CameraUser     string
	CameraPassword string
	Resolution     string
	AudioCodec     string
	RTSPPort       int
	RTSPPath       string
	ONVIFPort      int
	ResetPairing   bool
	MQTTBroker     string
	MQTTUser       string
	MQTTPassword   string
	MQTTTopic      string
	MQTTDiscovery  string
	Debug          bool
}

func loadHomeAssistantOptionsFromPath(path string, cfg *AppConfig) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[HA Addon] ⚠️ Warning: Failed to read %s: %v", path, err)
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
		Debug               bool   `json:"debug"`
	}

	if err := json.Unmarshal(data, &opts); err != nil {
		log.Printf("[HA Addon] ⚠️ Warning: Failed to parse %s: %v", path, err)
		return
	}

	log.Printf("[HA Addon] 🏠 Loaded configuration from %s", path)

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
	if opts.Debug {
		cfg.Debug = true
	}
}

type supervisorMQTTResponse struct {
	Result string `json:"result"`
	Data   struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		SSL      bool   `json:"ssl"`
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"data"`
}

// fetchSupervisorMQTTOptions queries Home Assistant's Supervisor API for the official MQTT broker addon credentials
func fetchSupervisorMQTTOptions() (broker, user, pass string, err error) {
	token := os.Getenv("SUPERVISOR_TOKEN")
	if token == "" {
		token = os.Getenv("HASSIO_TOKEN")
	}
	if token == "" {
		return "", "", "", fmt.Errorf("no supervisor token available")
	}

	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	urls := []string{
		"http://supervisor/services/mqtt",
		"http://hassio/services/mqtt",
	}

	var lastErr error
	for _, u := range urls {
		req, reqErr := http.NewRequest("GET", u, nil)
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("supervisor API returned HTTP %d", resp.StatusCode)
			continue
		}

		var sResp supervisorMQTTResponse
		decErr := json.NewDecoder(resp.Body).Decode(&sResp)
		_ = resp.Body.Close()
		if decErr != nil {
			lastErr = decErr
			continue
		}

		if sResp.Result == "ok" && sResp.Data.Host != "" {
			port := sResp.Data.Port
			if port <= 0 {
				port = 1883
			}
			scheme := "tcp"
			if sResp.Data.SSL {
				scheme = "ssl"
			}
			broker = fmt.Sprintf("%s://%s:%d", scheme, sResp.Data.Host, port)
			user = sResp.Data.Username
			pass = sResp.Data.Password
			return broker, user, pass, nil
		}
	}

	return "", "", "", lastErr
}

// resolveConfig resolves configuration following the POSIX / 12-Factor App hierarchy:
// 1. Code Defaults (Layer 1 - lowest)
// 2. Config File e.g. /data/options.json & Supervisor Auto-Discovery (Layer 2)
// 3. Explicit Environment Variables (Layer 3)
// 4. Explicit CLI Flags (Layer 4 - highest)
func resolveConfig(optionsPath string, fs *flag.FlagSet) *AppConfig {
	// 1. Layer 1: Code Defaults
	cfg := &AppConfig{
		NabtoConfig: &nabto.Config{
			CameraIP:   "",
			CameraPort: 5592,
			KeyPath:    "data/client.key",
		},
		Resolution:    "1080p",
		AudioCodec:    "aac",
		RTSPPort:      8554,
		RTSPPath:      "steinel",
		ONVIFPort:     8000,
		ResetPairing:  false,
		MQTTBroker:    "",
		MQTTUser:      "",
		MQTTPassword:  "",
		MQTTTopic:     "steinel",
		MQTTDiscovery: "homeassistant",
	}

	// 2. Layer 2: Configuration File
	if optionsPath != "" {
		loadHomeAssistantOptionsFromPath(optionsPath, cfg)
	}

	// 2.1 Auto-discover Home Assistant MQTT service via Supervisor API if no broker was manually configured
	if cfg.MQTTBroker == "" {
		if broker, user, pass, err := fetchSupervisorMQTTOptions(); err == nil && broker != "" {
			cfg.MQTTBroker = broker
			cfg.MQTTUser = user
			cfg.MQTTPassword = pass
			log.Printf("[HA Addon] 📡 Auto-discovered Home Assistant MQTT service: %s (User: %s)", broker, user)
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
	if envDebug := os.Getenv("DEBUG"); envDebug == "true" || envDebug == "1" {
		cfg.Debug = true
	}

	// 4. Layer 4: Explicit CLI Flags (POSIX)
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
			case "debug":
				if b, err := strconv.ParseBool(f.Value.String()); err == nil {
					cfg.Debug = b
				}
			}
		})
	}

	return cfg
}

// probePort checks if a specific TCP port is open and accepting connections within a given timeout.
func probePort(ip string, port int, timeout time.Duration) bool {
	target := net.JoinHostPort(ip, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err == nil {
		_ = conn.Close()
		return true
	}
	return false
}

func main() {
	flag.String("qr", "", "Steinel camera QR code string (did=...,pid=...,sct=...,pairPwd=...)")
	flag.String("ip", "", "Steinel camera local IP address")
	flag.String("type", "", "Camera model type ('auto', 'l625', 'l620')")
	flag.String("user", "", "Camera authentication username (for L 620 CAM)")
	flag.String("pass", "", "Camera authentication password (for L 620 CAM)")
	flag.String("key", "", "Path to client private key file")
	flag.String("res", "", "Video resolution (1080p, 720p, 360p)")
	flag.Int("port", 0, "RTSP server port")
	flag.String("path", "", "RTSP stream path (e.g. steinel -> rtsp://host:port/steinel)")
	flag.Int("onvif", 0, "ONVIF HTTP server port")
	flag.Bool("reset-pairing", false, "Reset local client key and force re-pairing with camera")
	flag.String("mqtt-broker", "", "MQTT broker URL (e.g. tcp://192.168.1.100:1883)")
	flag.String("mqtt-user", "", "MQTT username")
	flag.String("mqtt-pass", "", "MQTT password")
	flag.String("mqtt-topic", "", "MQTT base topic prefix (default: steinel)")
	flag.String("mqtt-disc", "", "MQTT Home Assistant Discovery Prefix")
	flag.String("audio-codec", "", "Audio codec for RTSP/ONVIF stream: 'aac' (transcoded, default) or 'pcmu' (raw passthrough)")
	flag.Bool("debug", false, "Enable verbose debug logging")
	betaFlag := flag.Bool("beta", false, "Identify as beta instance for IAM registration")
	flag.Parse()

	if envVer := os.Getenv("APP_VERSION"); envVer != "" {
		AppVersion = envVer
	} else if envVer := os.Getenv("VERSION"); envVer != "" {
		AppVersion = envVer
	}

	// Resolve configuration according to POSIX & 12-Factor App hierarchy
	appCfg := resolveConfig("/data/options.json", flag.CommandLine)
	cfg := appCfg.NabtoConfig
	cfg.IsBeta = *betaFlag ||
		strings.Contains(strings.ToLower(AppVersion), "beta") ||
		os.Getenv("IS_BETA") == "true" ||
		os.Getenv("IS_BETA") == "1" ||
		os.Getenv("BETA") == "true"

	if cfg.CameraIP == "" {
		log.Fatalf("[Config] ❌ Error: Camera IP address is mandatory! Please configure 'camera_ip' in Home Assistant or supply -ip / CAMERA_IP.")
	}

	// Model Selection: Explicit configuration vs. Auto-Detection via Port 34567 Probe
	var isL620 bool
	switch strings.ToLower(strings.TrimSpace(appCfg.CameraType)) {
	case "l620":
		isL620 = true
		log.Printf("[Config] 📷 Camera model configured explicitly: Steinel L 620 CAM / XLED CAM 1 (Xiongmai Sofia)")
	case "l625":
		isL620 = false
		log.Printf("[Config] 📷 Camera model configured explicitly: Steinel L 625 CAM SC (Nabto Edge)")
	default: // "auto" or unspecified
		log.Printf("[Config] 🔍 Camera model set to 'auto': Probing %s on Port 34567...", cfg.CameraIP)
		if probePort(cfg.CameraIP, 34567, 1500*time.Millisecond) {
			isL620 = true
			log.Printf("[Config] 🎯 Auto-detected Steinel L 620 CAM / XLED CAM 1 (Port 34567 Xiongmai Sofia is open)")
		} else {
			isL620 = false
			log.Printf("[Config] 🎯 Auto-detected Steinel L 625 CAM SC (Port 34567 closed, selecting Nabto Edge driver)")
		}
	}

	modelName := "L 625 CAM SC"
	if isL620 {
		modelName = "L 620 CAM"
		if cfg.DeviceID == "" {
			cfg.DeviceID = "steinel-l620"
		}
		if cfg.ProductID == "" {
			cfg.ProductID = "pr-xiongmai"
		}
	}

	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf(" Steinel %s — Standalone Bridge (%s)\n", modelName, AppVersion)
	if isL620 {
		fmt.Println(" 100% Native Single Binary (Xiongmai Sofia + Local RTSP + ONVIF + MQTT)")
	} else {
		fmt.Println(" 100% Native Single Binary (Nabto + WebRTC + RTSP + ONVIF + MQTT)")
	}
	fmt.Println("═══════════════════════════════════════════════════════════════════")

	// Handle pairing reset
	if appCfg.ResetPairing && !isL620 {
		if cfg.PairPwd == "" || cfg.PairPwd == "xxxx" {
			log.Printf("[Reset] ⚠️ Warning: Pairing reset requested, but no valid QR code ('qr_code') is configured! Re-pairing requires a valid QR code.")
		} else {
			log.Printf("[Reset] 🔄 Pairing reset requested: Removing client key '%s' to force fresh EC key generation & re-pairing with configured QR code...", cfg.KeyPath)
		}
		if err := os.Remove(cfg.KeyPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[Reset] ⚠️ Warning: Could not delete '%s': %v", cfg.KeyPath, err)
		} else {
			log.Printf("[Reset] ✅ Existing key removed. Fresh pairing will be performed on connect.")
		}
	}

	if !isL620 && cfg.DeviceID == "" && cfg.SCT == "" {
		log.Printf("[!] Note: No QR code provided. Local direct connection mode will be used for %s.", cfg.CameraIP)
	}

	// Ensure key directory exists
	if dir := filepath.Dir(cfg.KeyPath); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}

	log.Printf("[Config] Camera: %s (Type: %s, Model: %s, Res: %s, Audio: %s, Debug: %v)", cfg.CameraIP, appCfg.CameraType, modelName, appCfg.Resolution, appCfg.AudioCodec, appCfg.Debug)
	if !isL620 {
		log.Printf("[Config] Key:    %s", cfg.KeyPath)
	}
	log.Printf("[Config] Ports:  RTSP=%d, ONVIF=%d, WS-Discovery=3702/udp", appCfg.RTSPPort, appCfg.ONVIFPort)
	if appCfg.MQTTBroker != "" {
		log.Printf("[Config] MQTT:   Broker=%s, BaseTopic=%s, Discovery=%s", appCfg.MQTTBroker, appCfg.MQTTTopic, appCfg.MQTTDiscovery)
	}

	// Context and signal trap for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Printf("\n[*] Received %v. Stopping bridge gracefully...", sig)
		cancel()
	}()

	bridgeMgr := &BridgeManager{}

	// 1. Start embedded RTSP Server (with Profile T 2-Way Audio Backchannel)
	rtspServer, err := rtsp.NewServer(appCfg.RTSPPort, appCfg.RTSPPath, appCfg.AudioCodec, appCfg.Debug)
	if err != nil {
		log.Fatalf("[!] Failed to initialize RTSP server: %v", err)
	}
	defer rtspServer.Close()

	rtspServer.SetOnPlayHandler(bridgeMgr.RequestKeyframe)
	rtspServer.SetAudioBackchannelHandler(bridgeMgr.WriteAudioBackchannel)

	if err := rtspServer.Start(); err != nil {
		log.Fatalf("[!] Failed to start RTSP server: %v", err)
	}

	// 2. Start embedded ONVIF Profile S/T Server (WS-Discovery + Media + Events + DeviceIO)
	onvifServer := onvif.NewServer(
		appCfg.ONVIFPort,
		appCfg.RTSPPort,
		appCfg.RTSPPath,
		appCfg.AudioCodec,
		cfg.DeviceID,
		cfg.ProductID,
		bridgeMgr.SetResolution,
		func() error {
			log.Printf("[ONVIF] Reboot requested")
			return nil
		},
		bridgeMgr.SetLampState,
		bridgeMgr.SetSiren,
		bridgeMgr.GetSDCardManager,
	)
	defer onvifServer.Close()

	if err := onvifServer.Start(ctx); err != nil {
		log.Printf("[!] Warning: Could not start ONVIF server: %v", err)
	}

	// 3. Start MQTT Client (Optional — paused for L 620 until control protocol is finalized)
	if appCfg.MQTTBroker != "" {
		if isL620 {
			log.Printf("[MQTT] ℹ️ MQTT Auto-Discovery entities for Steinel L 620 CAM are paused (video/audio provided directly via RTSP & ONVIF)")
		} else {
			mqttClient := mqtt.NewClient(mqtt.Config{
				Broker:          appCfg.MQTTBroker,
				Username:        appCfg.MQTTUser,
				Password:        appCfg.MQTTPassword,
				TopicPrefix:     appCfg.MQTTTopic,
				DiscoveryPrefix: appCfg.MQTTDiscovery,
				DeviceID:        cfg.DeviceID,
				ProductID:       cfg.ProductID,
				Model:           modelName,
				BridgeHTTPURL:   fmt.Sprintf("http://%s:%d", cfg.CameraIP, appCfg.ONVIFPort),
			}, mqtt.Callbacks{
				SetLampMode:       bridgeMgr.SetLampState,
				SetHighlight:      bridgeMgr.SetHighlight,
				SetHighlightTime:  bridgeMgr.SetHighlightTime,
				SetLowlight:       bridgeMgr.SetLowlight,
				SetLowlightTime:   bridgeMgr.SetLowlightTime,
				SetPIRSensitivity: bridgeMgr.SetPIRSensitivity,
				SetLuxThreshold:   bridgeMgr.SetLuxThreshold,
				SetSiren:          bridgeMgr.SetSiren,
				SetResolution:     bridgeMgr.SetResolution,
			})
			defer mqttClient.Close()

			go func() {
				if err := mqttClient.Start(ctx); err != nil {
					log.Printf("[MQTT] ⚠️ MQTT client error: %v", err)
				}
			}()
		}
	}

	// 4. Branch: Xiongmai Sofia Driver (L 620 CAM) vs. Nabto WebRTC Driver (L 625 CAM SC)
	if isL620 {
		log.Printf("[Bridge] 🚀 [ONLINE] Steinel L 620 CAM stream ready at rtsp://0.0.0.0:%d/%s", appCfg.RTSPPort, appCfg.RTSPPath)
		log.Printf("[Bridge] 🛰️ [ONVIF] Endpoints active at http://0.0.0.0:%d/onvif/device_service", appCfg.ONVIFPort)

		xmDriver := xiongmai.NewDriver(cfg.CameraIP, appCfg.CameraUser, appCfg.CameraPassword, rtspServer, events.GlobalBus, appCfg.Debug)
		bridgeMgr.SetXMDriver(xmDriver)

		if err := xmDriver.Start(ctx); err != nil {
			log.Printf("[Xiongmai] ⚠️ Driver initialization warning: %v", err)
		}
		defer func() { _ = xmDriver.Close() }()

		<-ctx.Done()
	} else {
		const reconnectCooldown = 30 * time.Second

		// Supervisor loop for Nabto + WebRTC
		for ctx.Err() == nil {
			client, err := nabto.NewClient(cfg)
			if err != nil {
				log.Printf("[!] Nabto client init error: %v", err)
				select {
				case <-ctx.Done():
					break
				case <-time.After(5 * time.Second):
				}
				continue
			}

			if err := client.Connect(); err != nil {
				client.Close()
				if ctx.Err() != nil {
					break
				}
				log.Printf("[Supervisor] ⏳ Connect failed (%v). Waiting 30s before retry to allow camera reboot...", err)
				select {
				case <-ctx.Done():
					break
				case <-time.After(reconnectCooldown):
				}
				continue
			}

			port, err := client.GetSignalingPort()
			if err != nil {
				client.Close()
				if ctx.Err() != nil {
					break
				}
				log.Printf("[Supervisor] ⏳ GetSignalingPort failed (%v). Waiting 30s before retry...", err)
				select {
				case <-ctx.Done():
					break
				case <-time.After(reconnectCooldown):
				}
				continue
			}

			stream, err := client.OpenSignalingStream(port)
			if err != nil {
				client.Close()
				if ctx.Err() != nil {
					break
				}
				log.Printf("[Supervisor] ⏳ OpenSignalingStream failed (%v). Waiting 30s before retry...", err)
				select {
				case <-ctx.Done():
					break
				case <-time.After(reconnectCooldown):
				}
				continue
			}

			log.Printf("[Bridge] 🚀 [ONLINE] Stream ready at rtsp://0.0.0.0:%d/%s", appCfg.RTSPPort, appCfg.RTSPPath)
			log.Printf("[Bridge] 🛰️ [ONVIF] Endpoints active at http://0.0.0.0:%d/onvif/device_service", appCfg.ONVIFPort)

			bridge := webrtc.NewBridge(client, stream, rtspServer, appCfg.Resolution, 1*time.Second, appCfg.Debug)
			bridgeMgr.SetBridge(bridge)

			_ = bridge.Run(ctx)

			bridgeMgr.SetBridge(nil)
			stream.Close()
			client.Close()

			if ctx.Err() == nil {
				log.Printf("[Supervisor] ⏳ Stream session disconnected / Watchdog reset. Waiting 30s cooldown before reconnecting to allow camera reboot...")
				select {
				case <-ctx.Done():
					break
				case <-time.After(reconnectCooldown):
				}
			}
		}
	}

	rtspServer.Close()
	onvifServer.Close()
	log.Printf("[*] Standalone Go Bridge stopped cleanly.")
}
