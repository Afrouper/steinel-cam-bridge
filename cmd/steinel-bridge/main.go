package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/Afrouper/steinel-cam-bridge/pkg/app"
	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

// AppVersion is injected at build time via -ldflags="-X main.AppVersion=${APP_VERSION}".
var AppVersion = "dev"

func registerFlags(fs *flag.FlagSet) {
	fs.String("qr", "", "Steinel camera QR code string (did=...,pid=...,sct=...,pairPwd=...)")
	fs.String("ip", "", "Steinel camera local IP address")
	fs.String("type", "", "Camera model type ('auto', 'l625', 'l620')")
	fs.String("user", "", "Camera authentication username (for L 620 CAM)")
	fs.String("pass", "", "Camera authentication password (for L 620 CAM)")
	fs.String("bridge-user", "", "Downstream bridge authentication username for RTSP, ONVIF and REST")
	fs.String("bridge-pass", "", "Downstream bridge authentication password for RTSP, ONVIF and REST")
	fs.String("key", "", "Path to client private key file")
	fs.String("res", "", "Video resolution (1080p, 720p, 360p)")
	fs.Int("port", 0, "RTSP server port")
	fs.String("path", "", "RTSP stream path (e.g. steinel -> rtsp://host:port/steinel)")
	fs.Int("onvif", 0, "ONVIF HTTP server port")
	fs.Bool("reset-pairing", false, "Reset local client key and force re-pairing with camera")
	fs.String("mqtt-broker", "", "MQTT broker URL (e.g. tcp://192.168.1.100:1883)")
	fs.String("mqtt-user", "", "MQTT username")
	fs.String("mqtt-pass", "", "MQTT password")
	fs.String("mqtt-topic", "", "MQTT base topic prefix (default: steinel)")
	fs.String("mqtt-disc", "", "MQTT Home Assistant Discovery Prefix")
	fs.String("audio-codec", "", "Audio codec for RTSP/ONVIF stream: 'aac' (transcoded, default) or 'pcmu' (raw passthrough)")
	fs.Int("sync-interval", 120, "Interval in seconds to poll SD card for new recordings (default: 120s)")
	fs.Int("sdcard-sync-interval", 120, "Interval in seconds to poll SD card for new recordings (alias)")
	fs.String("nabto-driver", "cgo", "Nabto Edge driver engine ('cgo' for C-SDK, default; 'pure' for native Go)")
	fs.Bool("use-cgo", true, "Use C-SDK libnabto_client wrapper (default: true)")
	fs.String("log-level", "info", "Log level (trace, debug, info, warn, error)")
	fs.Bool("beta", false, "Identify as beta instance for IAM registration")
}

func main() {
	registerFlags(flag.CommandLine)
	flag.Parse()

	// 1. Resolve configuration following 12-Factor App hierarchy
	cfg := config.Resolve("/data/options.json", flag.CommandLine)
	if AppVersion != "dev" && cfg.AppVersion == "dev" {
		cfg.AppVersion = AppVersion
	}

	// 2. Initialize centralized logging framework
	logger.Init(cfg.LogLevel, cfg.LogFormat)

	// 3. Graceful shutdown root context
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 4. Create and run the application
	application, err := app.New(cfg, AppVersion)
	if err != nil {
		logger.Error("Main", "❌ Failed to initialize application: %v", err)
		os.Exit(1)
	}

	if err := application.Run(ctx); err != nil {
		logger.Error("Main", "❌ Application encountered fatal error: %v", err)
		os.Exit(1)
	}
}
