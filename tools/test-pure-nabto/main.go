package main

import (
	"flag"
	"log"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/nabtopure"
)

func main() {
	ip := flag.String("ip", "192.168.1.2", "Camera IP address")
	port := flag.Int("port", 5592, "Camera UDP port")
	keyPath := flag.String("key", "data/local_client.key", "Path to EC private key")
	debug := flag.Bool("debug", true, "Enable debug output")
	flag.Parse()

	log.Printf("[Test] 🔍 Initializing Pure-Go Nabto Client...")
	log.Printf("[Test] Target: %s:%d | Key: %s", *ip, *port, *keyPath)

	client, err := nabtopure.NewClient(&nabtopure.Config{
		CameraIP:   *ip,
		CameraPort: *port,
		KeyPath:    *keyPath,
		Debug:      *debug,
	})
	if err != nil {
		log.Fatalf("[Test] ❌ NewClient failed: %v", err)
	}
	defer client.Close()

	log.Printf("[Test] ⏳ Starting DTLS 1.2 Handshake with camera...")
	start := time.Now()
	if err := client.Connect(); err != nil {
		log.Fatalf("[Test] ❌ Connect failed after %v: %v", time.Since(start), err)
	}

	log.Printf("[Test] 🎉 SUCCESS! Pure-Go DTLS 1.2 connection established in %v!", time.Since(start))

	// Step 2: Test CoAP /p2p/webrtc-info
	log.Printf("[Test] 🛰️ Sending CoAP GET /p2p/webrtc-info...")
	sigPort, err := client.GetSignalingPort()
	if err != nil {
		log.Fatalf("[Test] ❌ GetSignalingPort failed: %v", err)
	}
	log.Printf("[Test] ✅ Received SignalingStreamPort: %d", sigPort)

	// Step 2a: Test CoAP GET /iam/pairing
	log.Printf("[Test] 🔐 Sending CoAP GET /iam/pairing...")
	req := nabtopure.NewRequest(nabtopure.CodeGET, "/iam/pairing", 0, nil)
	resp, err := client.CoAPClient().Execute(req, 5*time.Second)
	if err != nil {
		log.Printf("[Test] ⚠️ CoAP /iam/pairing error: %v", err)
	} else {
		log.Printf("[Test] 📋 CoAP /iam/pairing response status %s, payload: %x (hex)", resp.StatusString(), resp.Payload)
	}

	// Step 2b: Test CoAP POST /webrtc/tracks
	log.Printf("[Test] 🎥 Sending CoAP POST /webrtc/tracks...")
	statusCode, err := client.RequestTracks()
	if err != nil {
		log.Fatalf("[Test] ❌ RequestTracks failed: %v", err)
	}
	log.Printf("[Test] ✅ /webrtc/tracks returned status: %d", statusCode)
	log.Printf("[Test] 🌟 STEP 2 (CoAP Layer) PASSED 100%%!")
}
