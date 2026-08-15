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

func main() {
	qrFlag := flag.String("qr", "", "Steinel camera QR code string (did=...,pid=...,sct=...,pairPwd=...)")
	ipFlag := flag.String("ip", "", "Steinel camera local IP address (default: 192.168.1.100)")
	keyPath := flag.String("key", "data/client.key", "Path to client private key file")
	resolution := flag.String("res", "1080p", "Video resolution (1080p, 720p, 360p)")
	rtspPort := flag.Int("port", 8554, "RTSP server port")
	rtspPath := flag.String("path", "steinel", "RTSP stream path (e.g. steinel -> rtsp://host:port/steinel)")
	onvifPort := flag.Int("onvif", 8000, "ONVIF HTTP server port")
	flag.Parse()

	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println(" Steinel L 625 CAM SC — Standalone ONVIF & 2-Way Audio Bridge")
	fmt.Println(" 100% Native Single Binary (Nabto + WebRTC + RTSP + ONVIF)")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	// Default fallback config
	cfg := &nabto.Config{
		CameraIP:  "192.168.1.100",
		DeviceID:  "de-xxxxxxx",
		ProductID: "pr-xxxxx",
		SCT:       "xxxx",
		PairPwd:   "xxxx",
		KeyPath:   *keyPath,
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

	// Ensure key directory exists
	if dir := filepath.Dir(cfg.KeyPath); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}

	log.Printf("[Config] Camera: %s (ID: %s, Res: %s)", cfg.CameraIP, cfg.DeviceID, *resolution)
	log.Printf("[Config] Key:    %s", cfg.KeyPath)
	log.Printf("[Config] Ports:  RTSP=%d, ONVIF=%d, WS-Discovery=3702/udp", *rtspPort, *onvifPort)

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

	const reconnectCooldown = 30 * time.Second

	// 3. Supervisor loop for Nabto + WebRTC
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

		bridge := webrtc.NewBridge(client, stream, rtspServer, *resolution, 3*time.Second)
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
