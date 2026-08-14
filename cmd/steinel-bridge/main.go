package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"steinel-cam-bridge/pkg/nabto"
	"steinel-cam-bridge/pkg/rtsp"
	"steinel-cam-bridge/pkg/webrtc"
)

func main() {
	qrFlag := flag.String("qr", "", "Steinel camera QR code string (did=...,pid=...,sct=...,pairPwd=...)")
	ipFlag := flag.String("ip", "", "Steinel camera local IP address (default: 192.168.88.89)")
	keyPath := flag.String("key", "data/client.key", "Path to client private key file")
	resolution := flag.String("res", "1080p", "Video resolution (1080p, 720p, 360p)")
	rtspPort := flag.Int("port", 8554, "RTSP server port")
	rtspPath := flag.String("path", "steinel", "RTSP stream path (e.g. steinel -> rtsp://host:port/steinel)")
	flag.Parse()

	fmt.Println("══════════════════════════════════════════════════")
	fmt.Println(" Steinel L 625 CAM SC — Standalone Go Bridge")
	fmt.Println(" 100% Native Single Binary (Nabto + WebRTC + RTSP)")
	fmt.Println("══════════════════════════════════════════════════")

	// Default fallback config
	cfg := &nabto.Config{
		CameraIP:  "192.168.88.89",
		DeviceID:  "de-m4yfowbr",
		ProductID: "pr-qtatbtbi",
		SCT:       "tjLljaZXZcc2",
		PairPwd:   "x9iJdqf9uwws",
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

	// 1. Start embedded RTSP Server
	rtspServer, err := rtsp.NewServer(*rtspPort, *rtspPath)
	if err != nil {
		log.Fatalf("[!] Failed to initialize RTSP server: %v", err)
	}
	defer rtspServer.Close()

	if err := rtspServer.Start(); err != nil {
		log.Fatalf("[!] Failed to start RTSP server: %v", err)
	}

	const reconnectCooldown = 30 * time.Second

	// 2. Supervisor loop for Nabto + WebRTC
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

		bridge := webrtc.NewBridge(client, stream, rtspServer, *resolution, 3*time.Second)
		_ = bridge.Run(ctx)

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
	log.Printf("[*] Standalone Go Bridge stopped cleanly.")
}
