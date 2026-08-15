package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"steinel-cam-bridge/pkg/mcu"
	"steinel-cam-bridge/pkg/mqtt"
	"steinel-cam-bridge/pkg/nabto"
	"steinel-cam-bridge/pkg/onvif"
	"steinel-cam-bridge/pkg/rtsp"
	"steinel-cam-bridge/pkg/webrtc"
)

type BridgeManager struct {
	currentBridge *webrtc.Bridge
	mu            sync.RWMutex
}

func (m *BridgeManager) SetBridge(b *webrtc.Bridge) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentBridge = b
}

func (m *BridgeManager) SetResolution(res string) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("camera bridge offline")
	}
	return b.SetResolution(res)
}

func (m *BridgeManager) SetLampState(mode string) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("camera bridge offline")
	}
	return b.SetLampState(mode)
}

func (m *BridgeManager) SetHighlight(percent int) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("camera bridge offline")
	}
	b64, err := mcu.BuildSetHighlight(percent)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

func (m *BridgeManager) SetHighlightTime(seconds int) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("camera bridge offline")
	}
	b64, err := mcu.BuildSetHighlightTime(seconds)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

func (m *BridgeManager) SetLowlight(percent int) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("camera bridge offline")
	}
	b64, err := mcu.BuildSetLowlight(percent)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

func (m *BridgeManager) SetLowlightTime(timeVal int) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("camera bridge offline")
	}
	b64, err := mcu.BuildSetLowlightTime(timeVal)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

func (m *BridgeManager) SetPIRSensitivity(percent int) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("camera bridge offline")
	}
	b64, err := mcu.BuildSetPIRSensitivity(percent)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

func (m *BridgeManager) SetLuxThreshold(lux int) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("camera bridge offline")
	}
	b64, err := mcu.BuildSetLuxThreshold(lux)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

func (m *BridgeManager) SetSiren(on bool) error {
	m.mu.RLock()
	b := m.currentBridge
	m.mu.RUnlock()

	if b == nil {
		return fmt.Errorf("camera bridge offline")
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

func main() {
	qrFlag := flag.String("qr", "", "Steinel camera QR code string (did=...,pid=...,sct=...,pairPwd=...)")
	ipFlag := flag.String("ip", "", "Steinel camera local IP address")
	keyPath := flag.String("key", "data/client.key", "Path to client private key file")
	resolution := flag.String("res", "1080p", "Video resolution (1080p, 720p, 360p)")
	rtspPort := flag.Int("port", 8554, "RTSP server port")
	rtspPath := flag.String("path", "steinel", "RTSP stream path (e.g. steinel -> rtsp://host:port/steinel)")
	onvifPort := flag.Int("onvif", 8000, "ONVIF HTTP server port")
	mqttBroker := flag.String("mqtt-broker", "", "MQTT broker URL (e.g. tcp://192.168.1.100:1883)")
	mqttUser := flag.String("mqtt-user", "", "MQTT username")
	mqttPass := flag.String("mqtt-pass", "", "MQTT password")
	mqttTopic := flag.String("mqtt-topic", "steinel", "MQTT base topic prefix (default: steinel)")
	mqttDisc := flag.String("mqtt-disc", "homeassistant", "MQTT Home Assistant Discovery Prefix")
	flag.Parse()

	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Println(" Steinel L 625 CAM SC — Standalone ONVIF & Home Assistant Bridge")
	fmt.Println(" 100% Native Single Binary (Nabto + WebRTC + RTSP + ONVIF + MQTT)")
	fmt.Println("═══════════════════════════════════════════════════════════════════")

	// Default empty config
	cfg := &nabto.Config{
		CameraIP: "192.168.1.100",
		KeyPath:  *keyPath,
	}

	// Override from CLI flags
	if *ipFlag != "" {
		cfg.CameraIP = *ipFlag
	}
	if *qrFlag != "" {
		nabto.ParseQRCode(*qrFlag, cfg)
	}

	// Override from Environment Variables
	if envQR := os.Getenv("QR_CODE"); envQR != "" {
		nabto.ParseQRCode(envQR, cfg)
	}
	if ip := os.Getenv("CAMERA_IP"); ip != "" {
		cfg.CameraIP = ip
	}
	if key := os.Getenv("KEY_PATH"); key != "" {
		cfg.KeyPath = key
	}
	if res := os.Getenv("RESOLUTION"); res != "" {
		*resolution = res
	}
	if portStr := os.Getenv("ONVIF_PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			*onvifPort = p
		}
	}
	if mb := os.Getenv("MQTT_BROKER"); mb != "" {
		*mqttBroker = mb
	}
	if mu := os.Getenv("MQTT_USER"); mu != "" {
		*mqttUser = mu
	}
	if mp := os.Getenv("MQTT_PASSWORD"); mp != "" {
		*mqttPass = mp
	}
	if mt := os.Getenv("MQTT_TOPIC_PREFIX"); mt != "" {
		*mqttTopic = mt
	}
	if md := os.Getenv("MQTT_DISCOVERY_PREFIX"); md != "" {
		*mqttDisc = md
	}
	if sct := os.Getenv("SCT"); sct != "" {
		cfg.SCT = sct
	}
	if pwd := os.Getenv("PAIR_PWD"); pwd != "" {
		cfg.PairPwd = pwd
	}
	if did := os.Getenv("DEVICE_ID"); did != "" {
		cfg.DeviceID = did
	}
	if pid := os.Getenv("PRODUCT_ID"); pid != "" {
		cfg.ProductID = pid
	}

	if cfg.DeviceID == "" && cfg.SCT == "" {
		log.Printf("[!] Note: No QR code or credentials provided. Please supply -qr / QR_CODE or -ip.")
	}

	// Ensure key directory exists
	if dir := filepath.Dir(cfg.KeyPath); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}

	log.Printf("[Config] Camera: %s (ID: %s, Res: %s)", cfg.CameraIP, cfg.DeviceID, *resolution)
	log.Printf("[Config] Key:    %s", cfg.KeyPath)
	log.Printf("[Config] Ports:  RTSP=%d, ONVIF=%d, WS-Discovery=3702/udp", *rtspPort, *onvifPort)
	if *mqttBroker != "" {
		log.Printf("[Config] MQTT:   Broker=%s, BaseTopic=%s, Discovery=%s", *mqttBroker, *mqttTopic, *mqttDisc)
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
	rtspServer, err := rtsp.NewServer(*rtspPort, *rtspPath)
	if err != nil {
		log.Fatalf("[!] Failed to initialize RTSP server: %v", err)
	}
	defer rtspServer.Close()

	rtspServer.SetOnPlayHandler(bridgeMgr.RequestKeyframe)

	if err := rtspServer.Start(); err != nil {
		log.Fatalf("[!] Failed to start RTSP server: %v", err)
	}

	// 2. Start embedded ONVIF Profile S/T Server (WS-Discovery + Media + Events + DeviceIO)
	onvifServer := onvif.NewServer(
		*onvifPort,
		*rtspPort,
		*rtspPath,
		cfg.DeviceID,
		cfg.ProductID,
		bridgeMgr.SetResolution,
		func() error {
			log.Printf("[ONVIF] Reboot requested")
			return nil
		},
		bridgeMgr.SetLampState,
		bridgeMgr.SetSiren,
		rtspServer.GetSnapshot,
	)
	defer onvifServer.Close()

	if err := onvifServer.Start(ctx); err != nil {
		log.Printf("[!] Warning: Could not start ONVIF server: %v", err)
	}

	// 3. Start MQTT Client (Optional)
	if *mqttBroker != "" {
		mqttClient := mqtt.NewClient(mqtt.Config{
			Broker:          *mqttBroker,
			Username:        *mqttUser,
			Password:        *mqttPass,
			TopicPrefix:     *mqttTopic,
			DiscoveryPrefix: *mqttDisc,
			DeviceID:        cfg.DeviceID,
			ProductID:       cfg.ProductID,
			Model:           "L 625 CAM SC",
			BridgeHTTPURL:   fmt.Sprintf("http://%s:%d", cfg.CameraIP, *onvifPort),
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

	const reconnectCooldown = 30 * time.Second

	// 4. Supervisor loop for Nabto + WebRTC
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

		log.Printf("[Bridge] 🚀 [ONLINE] Stream ready at rtsp://0.0.0.0:%d/%s", *rtspPort, *rtspPath)
		log.Printf("[Bridge] 🛰️ [ONVIF] Endpoints active at http://0.0.0.0:%d/onvif/device_service", *onvifPort)

		bridge := webrtc.NewBridge(client, stream, rtspServer, *resolution, 2*time.Second)
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

	rtspServer.Close()
	onvifServer.Close()
	log.Printf("[*] Standalone Go Bridge stopped cleanly.")
}
