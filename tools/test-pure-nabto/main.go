package main

import (
	"context"
	"flag"
	"os"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/nabtopure"
	"github.com/Afrouper/steinel-cam-bridge/pkg/webrtc"
)

func main() {
	ip := flag.String("ip", "192.168.1.2", "Camera IP address")
	port := flag.Int("port", 5592, "Camera UDP port")
	keyPath := flag.String("key", "data/local_client.key", "Path to EC private key")
	logLevel := flag.String("log-level", "debug", "Log level (trace, debug, info, warn, error)")
	flag.Parse()

	logger.Init(*logLevel, "console")

	logger.Info("Test", "🔍 Initializing Pure-Go Nabto Client...")
	logger.Info("Test", "Target: %s:%d | Key: %s", *ip, *port, *keyPath)

	client, err := nabtopure.NewClient(&nabtopure.Config{
		CameraIP:   *ip,
		CameraPort: *port,
		KeyPath:    *keyPath,
	})
	if err != nil {
		logger.Error("Test", "❌ NewClient failed: %v", err)
		os.Exit(1)
	}
	defer client.Close()

	logger.Info("Test", "⏳ Starting DTLS 1.2 Handshake with camera...")
	start := time.Now()
	if err := client.Connect(); err != nil {
		logger.Error("Test", "❌ Connect failed after %v: %v", time.Since(start), err)
		os.Exit(1)
	}

	logger.Info("Test", "🎉 SUCCESS! Pure-Go DTLS 1.2 connection established in %v!", time.Since(start))

	// Step 2: Test CoAP /p2p/webrtc-info
	logger.Info("Test", "🛰️ Sending CoAP GET /p2p/webrtc-info...")
	sigPort, err := client.GetSignalingPort()
	if err != nil {
		logger.Error("Test", "❌ GetSignalingPort failed: %v", err)
		os.Exit(1)
	}
	logger.Info("Test", "✅ Received SignalingStreamPort: %d", sigPort)

	// Step 2a: Test CoAP GET /iam/pairing
	logger.Info("Test", "🔐 Sending CoAP GET /iam/pairing...")
	req := nabtopure.NewRequest(nabtopure.CodeGET, "/iam/pairing", 0, nil)
	resp, err := client.CoAPClient().Execute(req, 5*time.Second)
	if err != nil {
		logger.Warn("Test", "⚠️ CoAP /iam/pairing error: %v", err)
	} else {
		logger.Info("Test", "📋 CoAP /iam/pairing response status %s, payload: %x (hex)", resp.StatusString(), resp.Payload)
	}

	logger.Info("Test", "🔌 Opening WebRTC signaling stream on port %d...", sigPort)
	stream, err := client.OpenSignalingStream(sigPort)
	if err != nil {
		logger.Error("Test", "❌ OpenSignalingStream failed: %v", err)
		os.Exit(1)
	}

	bridge := webrtc.NewBridge(client, stream, nil, "1080p", 3*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger.Info("Test", "🚀 Starting WebRTC Bridge session for 30s...")
	if err := bridge.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("Test", "❌ Bridge.Run failed: %v", err)
		os.Exit(1)
	}

	logger.Info("Test", "🏆 FULL WEBRTC SESSION COMPLETED SUCCESSFULLY IN PURE GO!")
}
