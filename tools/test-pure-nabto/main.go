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
}
