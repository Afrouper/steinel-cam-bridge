package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

type cameraInstance struct {
	config          config.CameraConfig
	isL620          bool
	modelName       string
	eventBus        *events.Bus
	bridgeMgr       *BridgeManager
	stream          *rtsp.Stream
	getRecProvider  func() storage.RecordingProvider
	recordingSyncer *storage.RecordingSyncer
	cameraDriver    driver.CameraDriver
	supervisor      *supervisor.Supervisor
}

// App orchestrates all services of the Steinel CAM Bridge (RTSP, ONVIF, MQTT, Camera Drivers & Supervisors).
type App struct {
	cfg         *config.Config
	appVersion  string
	rtspServer  *rtsp.Server
	onvifServer *onvif.Server
	mqttClient  *mqtt.Client
	cameras     []*cameraInstance

	// Legacy single-camera compatibility accessors (for tests / backward compatibility):
	isL620          bool
	modelName       string
	eventBus        *events.Bus
	bridgeMgr       *BridgeManager
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

	numCameras := len(cfg.Cameras)
	if numCameras == 0 {
		return nil, fmt.Errorf("no cameras configured")
	}

	primaryCam := &cfg.Cameras[0]
	isL620Primary, primaryModelName := primaryCam.ProbeCameraModel()

	if numCameras == 1 {
		printBanner(primaryModelName, appVersion, isL620Primary)
	} else {
		printMultiBanner(numCameras, appVersion)
	}

	logger.Info("Config", "Configured Cameras (%d):", numCameras)
	for i, cam := range cfg.Cameras {
		logger.Info("Config", "  [%d] '%s' -> IP=%s, Path=/%s, Type=%s, Res=%s, Audio=%s, MAC=%s",
			i+1, cam.Name, cam.IP, cam.Path, cam.Type, cam.Resolution, cam.AudioCodec, cam.MAC)
	}
	logger.Info("Config", "Ports: RTSP=%d, ONVIF=%d, WS-Discovery=3702/udp", cfg.RTSPPort, cfg.ONVIFPort)
	if cfg.BridgeUser != "" {
		logger.Info("Config", "Auth:  Enabled (User: %s, RTSP/ONVIF/REST protected)", cfg.BridgeUser)
	} else {
		logger.Info("Config", "Auth:  Disabled (Public mode with ONVIF dummy credential compatibility)")
	}
	if cfg.MQTTBroker != "" {
		logger.Info("Config", "MQTT:  Broker=%s, BaseTopic=%s, Discovery=%s", cfg.MQTTBroker, cfg.MQTTTopic, cfg.MQTTDiscovery)
	}

	// 1. Embedded RTSP Server with primary stream
	rtspServer, err := rtsp.NewServer(cfg.RTSPPort, primaryCam.Path, primaryCam.AudioCodec, cfg.BridgeUser, cfg.BridgePass)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize RTSP server: %w", err)
	}

	// 2. Shared MQTT Client (optional)
	var mqttClient *mqtt.Client
	if cfg.MQTTBroker != "" {
		primaryDID := ""
		primaryPID := ""
		if primaryCam.Nabto != nil {
			primaryDID = primaryCam.Nabto.DeviceID
			primaryPID = primaryCam.Nabto.ProductID
		}
		mqttClient = mqtt.NewClient(mqtt.Config{
			Broker:          cfg.MQTTBroker,
			Username:        cfg.MQTTUser,
			Password:        cfg.MQTTPassword,
			TopicPrefix:     cfg.MQTTTopic,
			DiscoveryPrefix: cfg.MQTTDiscovery,
			DeviceID:        primaryDID,
			ProductID:       primaryPID,
			Model:           primaryModelName,
			AppVersion:      appVersion,
			BridgeHTTPURL:   fmt.Sprintf("http://%s:%d", getLocalBridgeIP(primaryCam.IP), cfg.ONVIFPort),
		}, mqtt.Callbacks{}, nil)
	} else {
		logger.Info("Config", "ℹ️ MQTT is disabled (no broker configured). To enable Home Assistant entities, configure 'mqtt_broker'.")
	}

	var extractor storage.FrameExtractor = storage.NewFFmpegExtractor("")
	camInstances := make([]*cameraInstance, 0, numCameras)
	onvifChannels := make([]onvif.CameraChannel, 0, numCameras)

	for i := range cfg.Cameras {
		cam := &cfg.Cameras[i]
		isL620, modelName := cam.ProbeCameraModel()

		// Key path and pairing reset
		keyPath := cam.KeyPath
		if keyPath == "" && cam.Nabto != nil {
			keyPath = cam.Nabto.KeyPath
		}
		if keyPath != "" {
			if err := os.MkdirAll(filepath.Dir(keyPath), 0755); err != nil {
				logger.Warn("Config", "⚠️ Warning: Could not create key directory for %s: %v", cam.Name, err)
			}
		}
		if cam.ResetPair && !isL620 && keyPath != "" {
			_ = os.Remove(keyPath)
			logger.Info("Reset", "🔄 Pairing reset for %s: removed key '%s'", cam.Name, keyPath)
		}

		camBus := events.NewBus()
		camBridgeMgr := NewBridgeManager()

		// RTSP Stream binding
		var camStream *rtsp.Stream
		if i == 0 {
			camStream = rtspServer.PrimaryStream()
		} else {
			camStream, err = rtspServer.RegisterStream(cam.Path, cam.AudioCodec)
			if err != nil {
				return nil, fmt.Errorf("failed to register RTSP stream '%s': %w", cam.Path, err)
			}
		}
		// Holding the last picture only makes sense for the always-on L 625
		// stream; the L 620 pauses its stream on purpose when nobody watches.
		holdLastFrame := time.Duration(0)
		if !isL620 {
			holdLastFrame = time.Duration(cfg.HoldLastFrameSeconds) * time.Second
		}
		camStream.SetVideoOptions(cfg.InstantStart, holdLastFrame)
		camStream.SetOnPlayHandler(camBridgeMgr.RequestKeyframe)
		camStream.SetAudioBackchannelHandler(camBridgeMgr.WriteAudioBackchannel)

		// Storage cache
		camCacheDir := filepath.Join(cfg.CacheDir, cam.Path)
		recordingCache := storage.NewRecordingCache(camCacheDir, cfg.CacheRecordings, extractor)
		cachedProvider := storage.NewCachedRecordingProvider(recordingCache, camBridgeMgr.GetRecordingProvider)
		rateLimit := cfg.SDCardDownloadRateLimitKBps
		if cam.SDCardDownloadRateLimitKBps != nil && *cam.SDCardDownloadRateLimitKBps >= 0 {
			rateLimit = *cam.SDCardDownloadRateLimitKBps
		}
		cachedProvider.SetRateLimitKBps(rateLimit)
		cachedProvider.SetStreamActiveFunc(func() bool {
			return camStream.ActiveClients() > 0
		})

		getRecProvider := func() storage.RecordingProvider {
			if cfg.CacheRecordings > 0 {
				return cachedProvider
			}
			return camBridgeMgr.GetRecordingProvider()
		}

		// Recording Syncer & Motion Trigger
		var motionResetTimer *time.Timer
		var motionTimerMu sync.Mutex

		onNewRec := func(item storage.RecordingItem, isInitial bool) {
			if mqttClient != nil {
				camID := cam.Path
				if cam.Nabto != nil && cam.Nabto.DeviceID != "" {
					camID = cam.Nabto.DeviceID
				}
				item.Camera = camID
				mqttClient.PublishRecordingEvent(item)
			}

			// Model-agnostic motion trigger: A newly recorded clip on the SD card (non-initial)
			// reliably indicates motion detection across all camera models (L 620, L 625, XLED).
			if !isInitial {
				logger.Debug("Recording Sync", "🚨 Motion event triggered by new SD card recording: %s (%s)", item.ID, item.FileName)
				camBus.SetMotionWithSource(true, "recording")

				motionTimerMu.Lock()
				if motionResetTimer != nil {
					motionResetTimer.Stop()
				}
				motionResetTimer = time.AfterFunc(10*time.Second, func() {
					logger.Debug("Recording Sync", "⚪ Motion cleared (10s timeout after recording)")
					camBus.SetMotionWithSource(false, "recording")
				})
				motionTimerMu.Unlock()
			}
		}

		syncer := storage.NewRecordingSyncer(
			getRecProvider,
			onNewRec,
			time.Duration(cfg.SDCardSyncInterval)*time.Second,
		)
		syncer.SetStreamActiveFunc(func() bool {
			return camStream.ActiveClients() > 0
		})
		camBus.Subscribe(func(evt events.EventType, data interface{}) {
			if evt == events.EventMotion {
				if m, ok := data.(events.MotionEvent); ok && m.IsMotion && m.Source != "recording" {
					syncer.TriggerSync()
				}
			}
		})

		// MQTT Registration
		if mqttClient != nil {
			camDID := ""
			camPID := ""
			if cam.Nabto != nil {
				camDID = cam.Nabto.DeviceID
				camPID = cam.Nabto.ProductID
			}
			callbacks := mqtt.Callbacks{
				SetLampMode:          camBridgeMgr.SetLampState,
				SetHighlight:         camBridgeMgr.SetHighlight,
				SetHighlightTime:     camBridgeMgr.SetHighlightTime,
				SetLowlight:          camBridgeMgr.SetLowlight,
				SetLowlightTime:      camBridgeMgr.SetLowlightTime,
				SetPIRSensitivity:    camBridgeMgr.SetPIRSensitivity,
				SetLuxThreshold:      camBridgeMgr.SetLuxThreshold,
				SetSiren:             camBridgeMgr.SetSiren,
				SetResolution:        camBridgeMgr.SetResolution,
				ReconnectSession:     camBridgeMgr.ReconnectSession,
				CameraInfo:           true,
				SetTimestampPosition: camBridgeMgr.SetTimestampPosition,
				SetSpeakerVolume:     camBridgeMgr.SetSpeakerVolume,
				SetDetectMode:        camBridgeMgr.SetDetectMode,
				SetRecordMode:        camBridgeMgr.SetRecordMode,
				RebootCamera:         camBridgeMgr.Reboot,
			}
			if !isL620 {
				callbacks.WifiSignal = true
				callbacks.SetPixelChangeDetection = camBridgeMgr.SetPixelChangeDetection
				callbacks.SetDrawFrame = camBridgeMgr.SetDrawFrame
				callbacks.SetAIPersonSensitivity = camBridgeMgr.SetAIPersonSensitivity
				callbacks.InstallUpdate = camBridgeMgr.InstallUpdate
			}
			mqttClient.RegisterCamera(mqtt.CameraDevice{
				Path:      cam.Path,
				Name:      cam.Name,
				DeviceID:  camDID,
				ProductID: camPID,
				Model:     modelName,
				Callbacks: callbacks,
				EventBus:  camBus,
			})
		}

		// Camera Driver
		camCfg := cfg.ForCamera(cam)
		camPath := cam.Path
		onDeviceDiscovered := func(deviceID, productID string) {
			if mqttClient != nil {
				mqttClient.UpdateCameraDeviceInfo(camPath, deviceID, productID)
			}
		}

		camDrv, err := driver.New(camCfg, isL620, camStream, camBus, onDeviceDiscovered)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize camera driver for '%s': %w", cam.Name, err)
		}
		camBridgeMgr.SetDriver(camDrv)

		camSup := supervisor.New(camDrv)

		inst := &cameraInstance{
			config:          *cam,
			isL620:          isL620,
			modelName:       modelName,
			eventBus:        camBus,
			bridgeMgr:       camBridgeMgr,
			stream:          camStream,
			getRecProvider:  getRecProvider,
			recordingSyncer: syncer,
			cameraDriver:    camDrv,
			supervisor:      camSup,
		}
		camInstances = append(camInstances, inst)

		var debugCommand onvif.DebugCommandFunc
		if cfg.DebugAPI {
			debugCommand = camBridgeMgr.DebugCommand
		}
		onvifChannels = append(onvifChannels, onvif.CameraChannel{
			Index:             i + 1,
			Name:              cam.Name,
			Path:              cam.Path,
			AudioCodec:        cam.AudioCodec,
			Resolution:        cam.Resolution,
			VideoCodecFunc:    camStream.ResolveVideoCodec,
			ChangeResFunc:     camBridgeMgr.SetResolution,
			SetLampFunc:       camBridgeMgr.SetLampState,
			SetSirenFunc:      camBridgeMgr.SetSiren,
			RecordingProvider: getRecProvider,
			EventBus:          camBus,
			DebugCommandFunc:  debugCommand,
		})
	}

	// 3. Embedded ONVIF Server
	primaryInst := camInstances[0]
	primaryDID := ""
	primaryPID := ""
	if primaryCam.Nabto != nil {
		primaryDID = primaryCam.Nabto.DeviceID
		primaryPID = primaryCam.Nabto.ProductID
	}

	onvifServer := onvif.NewServer(
		cfg.ONVIFPort,
		cfg.RTSPPort,
		primaryCam.Path,
		primaryCam.AudioCodec,
		primaryDID,
		primaryPID,
		primaryModelName,
		primaryCam.MAC,
		cfg.BridgeUser,
		cfg.BridgePass,
		primaryInst.bridgeMgr.SetResolution,
		func() error {
			logger.Info("ONVIF", "Reboot requested")
			return primaryInst.bridgeMgr.Reboot()
		},
		primaryInst.bridgeMgr.SetLampState,
		primaryInst.bridgeMgr.SetSiren,
		primaryInst.getRecProvider,
		primaryInst.eventBus,
	)
	onvifServer.SetChannels(onvifChannels)
	if cfg.DebugAPI {
		logger.Warn("Config", "🧪 Diagnostic API enabled: POST /api/debug/command passes raw commands to the camera. Switch 'debug_api' off after use.")
		if cfg.BridgeUser == "" || cfg.BridgePass == "" {
			logger.Warn("Config", "⚠️ Diagnostic API without access protection: bridge user and password are not set, so anyone in the network can send commands to the camera, including write commands.")
		}
		onvifServer.SetDebugCommandFunc(primaryInst.bridgeMgr.DebugCommand)
	}
	onvifServer.SetTimeFunc(func(t time.Time, tz string) error {
		var lastErr error
		for _, cam := range camInstances {
			if cam.bridgeMgr != nil {
				if err := cam.bridgeMgr.SetSystemDateAndTime(t, tz); err != nil {
					lastErr = err
				}
			}
		}
		return lastErr
	})

	app := &App{
		cfg:             cfg,
		appVersion:      appVersion,
		rtspServer:      rtspServer,
		onvifServer:     onvifServer,
		mqttClient:      mqttClient,
		cameras:         camInstances,
		isL620:          primaryInst.isL620,
		modelName:       primaryInst.modelName,
		eventBus:        primaryInst.eventBus,
		bridgeMgr:       primaryInst.bridgeMgr,
		recordingSyncer: primaryInst.recordingSyncer,
		cameraDriver:    primaryInst.cameraDriver,
		supervisor:      primaryInst.supervisor,
	}

	return app, nil
}

// Run starts all subsystems, runs the camera supervisor(s), and handles graceful shutdown upon context cancellation.
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

	// 3. Start MQTT Client and Syncers if enabled
	var wg sync.WaitGroup
	if a.mqttClient != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.mqttClient.Start(ctx); err != nil {
				logger.Warn("MQTT", "⚠️ MQTT client error: %v", err)
			}
		}()
	}

	// 4. Run Recording Syncers and Camera Supervisors in parallel
	for _, inst := range a.cameras {
		if inst.recordingSyncer != nil {
			syncer := inst.recordingSyncer
			wg.Add(1)
			go func() {
				defer wg.Done()
				syncer.Start(ctx)
			}()
		}
	}
	for _, inst := range a.cameras {
		camInst := inst
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := camInst.supervisor.Run(ctx); err != nil {
				logger.Error("Supervisor", "❌ Supervisor error (%s): %v", camInst.config.Name, err)
			}
		}()
	}

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("Main", "Stopping bridge gracefully...")

	// Wait for all supervisors to finish cleanup
	wg.Wait()

	return nil
}

// shutdown releases all server resources in deterministic order.
func (a *App) shutdown() {
	for _, inst := range a.cameras {
		if inst.cameraDriver != nil {
			_ = inst.cameraDriver.Close()
		}
	}
	if a.rtspServer != nil {
		a.rtspServer.Close()
	}
	if a.onvifServer != nil {
		a.onvifServer.Close()
	}
	if a.mqttClient != nil {
		a.mqttClient.Close()
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

func printMultiBanner(count int, appVersion string) {
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf(" Steinel CAM Bridge — Multi-Camera Mode (%d cameras, %s)\n", count, appVersion)
	fmt.Println(" Single Binary Multi-Camera Hub (Nabto / Xiongmai + RTSP + ONVIF + MQTT)")
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
