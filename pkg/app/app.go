package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/driver"
	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mqtt"
	"github.com/Afrouper/steinel-cam-bridge/pkg/onvif"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/Afrouper/steinel-cam-bridge/pkg/supervisor"
)

// App orchestrates all services of the Steinel CAM Bridge (RTSP, ONVIF, MQTT, Camera Driver & Supervisor).
type App struct {
	cfg             *config.Config
	isL620          bool
	modelName       string
	appVersion      string
	eventBus        *events.Bus
	bridgeMgr       *BridgeManager
	rtspServer      *rtsp.Server
	onvifServer     *onvif.Server
	mqttClient      *mqtt.Client
	recordingSyncer *storage.RecordingSyncer
	cameraDriver    driver.CameraDriver
	supervisor      *supervisor.Supervisor
}

// New creates and configures an App instance.
func New(cfg *config.Config, appVersion string) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if appVersion == "" {
		appVersion = cfg.AppVersion
	}

	isL620, modelName := cfg.ProbeCameraModel()

	printBanner(modelName, appVersion, isL620)

	// Handle pairing reset
	if cfg.ResetPairing && !isL620 {
		if cfg.NabtoConfig.PairPwd == "" || cfg.NabtoConfig.PairPwd == "xxxx" {
			logger.Warn("Reset", "⚠️ Warning: Pairing reset requested, but no valid QR code ('qr_code') is configured! Re-pairing requires a valid QR code.")
		} else {
			logger.Info("Reset", "🔄 Pairing reset requested: Removing client key '%s' to force fresh EC key generation & re-pairing with configured QR code...", cfg.NabtoConfig.KeyPath)
		}
		if err := os.Remove(cfg.NabtoConfig.KeyPath); err != nil && !os.IsNotExist(err) {
			logger.Warn("Reset", "⚠️ Warning: Could not delete '%s': %v", cfg.NabtoConfig.KeyPath, err)
		} else {
			logger.Info("Reset", "✅ Existing key removed. Fresh pairing will be performed on connect.")
		}
	}

	if !isL620 && cfg.NabtoConfig.DeviceID == "" && cfg.NabtoConfig.SCT == "" {
		logger.Info("Config", "ℹ️ Note: No QR code provided. Local direct connection mode will be used for %s.", cfg.NabtoConfig.CameraIP)
	}

	if err := cfg.SetupKeyPath(); err != nil {
		logger.Warn("Config", "⚠️ Warning: Could not create key directory: %v", err)
	}

	logger.Info("Config", "Camera: %s (Type: %s, Model: %s, Res: %s, Audio: %s, LogLevel: %s)",
		cfg.NabtoConfig.CameraIP, cfg.CameraType, modelName, cfg.Resolution, cfg.AudioCodec, cfg.LogLevel)
	if !isL620 {
		logger.Info("Config", "Key:    %s", cfg.NabtoConfig.KeyPath)
	}
	logger.Info("Config", "Ports:  RTSP=%d, ONVIF=%d, WS-Discovery=3702/udp", cfg.RTSPPort, cfg.ONVIFPort)
	if cfg.MQTTBroker != "" {
		logger.Info("Config", "MQTT:   Broker=%s, BaseTopic=%s, Discovery=%s", cfg.MQTTBroker, cfg.MQTTTopic, cfg.MQTTDiscovery)
	}

	bridgeMgr := NewBridgeManager()
	eventBus := events.NewBus()

	// 1. Embedded RTSP Server
	rtspServer, err := rtsp.NewServer(cfg.RTSPPort, cfg.RTSPPath, cfg.AudioCodec)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize RTSP server: %w", err)
	}
	rtspServer.SetOnPlayHandler(bridgeMgr.RequestKeyframe)
	rtspServer.SetAudioBackchannelHandler(bridgeMgr.WriteAudioBackchannel)

	// 2. Embedded ONVIF Server
	onvifServer := onvif.NewServer(
		cfg.ONVIFPort,
		cfg.RTSPPort,
		cfg.RTSPPath,
		cfg.AudioCodec,
		cfg.NabtoConfig.DeviceID,
		cfg.NabtoConfig.ProductID,
		bridgeMgr.SetResolution,
		func() error {
			logger.Info("ONVIF", "Reboot requested")
			return nil
		},
		bridgeMgr.SetLampState,
		bridgeMgr.SetSiren,
		bridgeMgr.GetRecordingProvider,
		eventBus,
	)

	// 3. Optional MQTT Client & SD-Card Recording Syncer
	var mqttClient *mqtt.Client
	var recordingSyncer *storage.RecordingSyncer

	if cfg.MQTTBroker != "" {
		mqttClient = mqtt.NewClient(mqtt.Config{
			Broker:          cfg.MQTTBroker,
			Username:        cfg.MQTTUser,
			Password:        cfg.MQTTPassword,
			TopicPrefix:     cfg.MQTTTopic,
			DiscoveryPrefix: cfg.MQTTDiscovery,
			DeviceID:        cfg.NabtoConfig.DeviceID,
			ProductID:       cfg.NabtoConfig.ProductID,
			Model:           modelName,
			BridgeHTTPURL:   fmt.Sprintf("http://%s:%d", getLocalBridgeIP(cfg.NabtoConfig.CameraIP), cfg.ONVIFPort),
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
		}, eventBus)

		recordingSyncer = storage.NewRecordingSyncer(
			bridgeMgr.GetRecordingProvider,
			mqttClient.PublishRecordingEvent,
			time.Duration(cfg.SDCardSyncInterval)*time.Second,
		)

		eventBus.SubscribeMotion(func(isMotion bool) {
			if isMotion {
				recordingSyncer.TriggerSync()
			}
		})
	} else {
		logger.Info("Config", "ℹ️ MQTT is disabled (no broker configured). To enable Home Assistant entities, configure 'mqtt_broker' in addon options or install an MQTT broker addon.")
	}

	// 4. Instantiate Polymorphic Camera Driver
	onDeviceDiscovered := func(deviceID, productID string) {
		if mqttClient != nil {
			mqttClient.UpdateDeviceInfo(deviceID, productID)
		}
	}

	camDriver, err := driver.New(cfg, isL620, rtspServer, eventBus, onDeviceDiscovered)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize camera driver: %w", err)
	}
	bridgeMgr.SetDriver(camDriver)

	// 5. Generic Camera Supervisor
	sup := supervisor.New(camDriver)

	return &App{
		cfg:             cfg,
		isL620:          isL620,
		modelName:       modelName,
		appVersion:      appVersion,
		eventBus:        eventBus,
		bridgeMgr:       bridgeMgr,
		rtspServer:      rtspServer,
		onvifServer:     onvifServer,
		mqttClient:      mqttClient,
		recordingSyncer: recordingSyncer,
		cameraDriver:    camDriver,
		supervisor:      sup,
	}, nil
}

// Run starts all subsystems, runs the camera supervisor, and handles graceful shutdown upon context cancellation.
func (a *App) Run(ctx context.Context) error {
	defer a.shutdown()

	// 1. Start RTSP Server
	if err := a.rtspServer.Start(); err != nil {
		return fmt.Errorf("failed to start RTSP server: %w", err)
	}

	// 2. Start ONVIF Server
	if err := a.onvifServer.Start(ctx); err != nil {
		logger.Warn("ONVIF", "⚠️ Could not start ONVIF server: %v", err)
	}

	// 3. Start MQTT Client and Syncer if enabled
	if a.mqttClient != nil {
		go func() {
			if err := a.mqttClient.Start(ctx); err != nil {
				logger.Warn("MQTT", "⚠️ MQTT client error: %v", err)
			}
		}()
	}
	if a.recordingSyncer != nil {
		go a.recordingSyncer.Start(ctx)
	}

	// 4. Run Camera Supervisor in separate goroutine
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := a.supervisor.Run(ctx); err != nil {
			logger.Error("Supervisor", "❌ Supervisor error: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("Main", "Stopping bridge gracefully...")

	// Wait for supervisor to finish cleanup
	wg.Wait()

	return nil
}

// shutdown releases all server resources in deterministic order.
func (a *App) shutdown() {
	if a.cameraDriver != nil {
		_ = a.cameraDriver.Close()
	}
	if a.rtspServer != nil {
		a.rtspServer.Close()
	}
	if a.onvifServer != nil {
		a.onvifServer.Close()
	}
	logger.Info("Main", "Standalone Go Bridge stopped cleanly.")
}

func printBanner(modelName, appVersion string, isL620 bool) {
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf(" Steinel %s — Standalone Bridge (%s)\n", modelName, appVersion)
	if isL620 {
		fmt.Println(" 100% Native Single Binary (Xiongmai Sofia + Local RTSP + ONVIF + MQTT)")
	} else {
		fmt.Println(" 100% Native Single Binary (Nabto + WebRTC + RTSP + ONVIF + MQTT)")
	}
	fmt.Println("═══════════════════════════════════════════════════════════════════")
}

func getLocalBridgeIP(target string) string {
	if target == "" {
		target = "8.8.8.8"
	}
	conn, err := net.Dial("udp", fmt.Sprintf("%s:80", target))
	if err != nil {
		return "127.0.0.1"
	}
	defer func() { _ = conn.Close() }()
	if localAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return localAddr.IP.String()
	}
	return "127.0.0.1"
}
