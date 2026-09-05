package supervisor

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/nabto"
	"github.com/Afrouper/steinel-cam-bridge/pkg/nabtopure"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/Afrouper/steinel-cam-bridge/pkg/webrtc"
	"github.com/Afrouper/steinel-cam-bridge/pkg/xiongmai"
)

// BridgeController allows the supervisor to bind active camera bridge/driver instances.
type BridgeController interface {
	SetBridge(b *webrtc.Bridge)
	SetXMDriver(d *xiongmai.Driver)
}

// DeviceInfoUpdater updates MQTT discovery information when camera hardware IDs are discovered.
type DeviceInfoUpdater interface {
	UpdateDeviceInfo(deviceID, productID string)
}

// Supervisor orchestrates the resilient connection lifecycle and automatic reconnection to Steinel cameras.
type Supervisor struct {
	cfg           *config.Config
	isL620        bool
	modelName     string
	rtspServer    *rtsp.Server
	bridgeCtrl    BridgeController
	deviceUpdater DeviceInfoUpdater
}

// New creates a new Supervisor instance.
func New(
	cfg *config.Config,
	isL620 bool,
	modelName string,
	rtspServer *rtsp.Server,
	bridgeCtrl BridgeController,
	deviceUpdater DeviceInfoUpdater,
) *Supervisor {
	return &Supervisor{
		cfg:           cfg,
		isL620:        isL620,
		modelName:     modelName,
		rtspServer:    rtspServer,
		bridgeCtrl:    bridgeCtrl,
		deviceUpdater: deviceUpdater,
	}
}

// Run executes the camera supervisor loop until the context is canceled.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.isL620 {
		return s.runXiongmai(ctx)
	}
	return s.runNabto(ctx)
}

// runXiongmai manages the connection lifecycle for Steinel L 620 CAM (Sofia DVRIP protocol).
func (s *Supervisor) runXiongmai(ctx context.Context) error {
	logger.Info("Bridge", "🚀 [ONLINE] Steinel L 620 CAM stream ready at rtsp://0.0.0.0:%d/%s", s.cfg.RTSPPort, s.cfg.RTSPPath)
	logger.Info("Bridge", "🛰️ [ONVIF] Endpoints active at http://0.0.0.0:%d/onvif/device_service", s.cfg.ONVIFPort)

	xmDriver := xiongmai.NewDriver(
		s.cfg.NabtoConfig.CameraIP,
		s.cfg.CameraUser,
		s.cfg.CameraPassword,
		s.cfg.Resolution,
		s.rtspServer,
		events.GlobalBus,
	)
	s.bridgeCtrl.SetXMDriver(xmDriver)

	if err := xmDriver.Start(ctx); err != nil {
		logger.Warn("Xiongmai", "⚠️ Driver initialization warning: %v", err)
	}
	defer func() {
		_ = xmDriver.Close()
		s.bridgeCtrl.SetXMDriver(nil)
	}()

	<-ctx.Done()
	return nil
}

// runNabto manages the supervisor reconnect loop for Steinel L 625 CAM SC (Nabto Edge + WebRTC).
func (s *Supervisor) runNabto(ctx context.Context) error {
	const reconnectCooldown = 30 * time.Second
	cfg := s.cfg.NabtoConfig

supervisorLoop:
	for ctx.Err() == nil {
		var client nabto.Driver
		var err error
		usePure := s.cfg.NabtoDriver == "pure" || os.Getenv("USE_CGO_NABTO") == "false" || os.Getenv("USE_CGO_NABTO") == "0"
		if usePure {
			logger.Info("Driver", "🚀 Using native Pure-Go Nabto driver (experimental)")
			client, err = nabtopure.NewClient(cfg)
		} else {
			logger.Info("Driver", "🔧 Using C-SDK wrapper driver (libnabto_client.so, default)")
			client, err = nabto.NewClient(cfg)
		}
		if err != nil {
			logger.Error("Nabto", "❌ Nabto client init error: %v", err)
			select {
			case <-ctx.Done():
				break supervisorLoop
			case <-time.After(5 * time.Second):
			}
			continue
		}

		// Connect with driver-appropriate timeout protection
		connectTimeout := 35 * time.Second

		connectDone := make(chan error, 1)
		go func() {
			connectDone <- client.Connect()
		}()

		var connectErr error
		select {
		case <-ctx.Done():
			client.Close()
			select {
			case <-connectDone:
			case <-time.After(2 * time.Second):
			}
			break supervisorLoop
		case <-time.After(connectTimeout):
			connectErr = fmt.Errorf("connection timeout (%v) reached", connectTimeout)
			client.Close()
			select {
			case <-connectDone:
			case <-time.After(3 * time.Second):
				logger.Warn("Supervisor", "⚠️ Warning: connect goroutine did not exit within 3s after Close")
			}
		case err := <-connectDone:
			connectErr = err
		}

		if connectErr != nil {
			logger.Error("Supervisor", "❌ Connect failed (%v)", connectErr)
			logger.Info("Supervisor", "🧹 Cleaning up camera connection state...")
			client.Close()
			if ctx.Err() != nil {
				break supervisorLoop
			}
			if usePure {
				logger.Warn("Supervisor", "🚨 Native Pure-Go Nabto driver failed to connect to camera.")
				logger.Info("Supervisor", "💡 Recommendation: Set 'nabto_driver: cgo' in Home Assistant Add-on config for official Nabto C-SDK support.")
			}
			logger.Info("Supervisor", "⏳ Waiting 15s before retry to allow camera cooldown...")
			select {
			case <-ctx.Done():
				break supervisorLoop
			case <-time.After(15 * time.Second):
			}
			continue
		}

		// If DeviceID was discovered during connection, update MQTT discovery & availability
		if s.deviceUpdater != nil && cfg.DeviceID != "" {
			s.deviceUpdater.UpdateDeviceInfo(cfg.DeviceID, cfg.ProductID)
		}

		logger.Info("Supervisor", "🛰️ Querying WebRTC signaling port from camera...")
		type portResult struct {
			port uint32
			err  error
		}
		portCh := make(chan portResult, 1)
		go func() {
			p, e := client.GetSignalingPort()
			portCh <- portResult{port: p, err: e}
		}()

		var port uint32
		var portErr error
		select {
		case <-ctx.Done():
			client.Close()
			select {
			case <-portCh:
			case <-time.After(2 * time.Second):
			}
			break supervisorLoop
		case <-time.After(15 * time.Second):
			portErr = fmt.Errorf("timeout (15s) while querying signaling port")
			client.Close()
			select {
			case <-portCh:
			case <-time.After(3 * time.Second):
				logger.Warn("Supervisor", "⚠️ Warning: port query goroutine did not exit within 3s after Close")
			}
		case res := <-portCh:
			port = res.port
			portErr = res.err
		}

		if portErr != nil {
			logger.Error("Supervisor", "❌ GetSignalingPort failed (%v)", portErr)
			logger.Info("Supervisor", "🧹 Cleaning up camera connection state...")
			client.Close()
			if ctx.Err() != nil {
				break supervisorLoop
			}
			if usePure {
				logger.Warn("Supervisor", "🚨 Native Pure-Go Nabto driver failed to query signaling port from camera.")
				logger.Info("Supervisor", "💡 Recommendation: Set 'nabto_driver: cgo' in Home Assistant Add-on config for official Nabto C-SDK support.")
			}
			logger.Info("Supervisor", "⏳ Waiting 15s before retry...")
			select {
			case <-ctx.Done():
				break supervisorLoop
			case <-time.After(15 * time.Second):
			}
			continue
		}

		logger.Info("Supervisor", "🔄 Opening Nabto signaling stream on port %d...", port)
		type streamResult struct {
			stream nabto.StreamDriver
			err    error
		}
		streamCh := make(chan streamResult, 1)
		go func() {
			st, e := client.OpenSignalingStream(port)
			streamCh <- streamResult{stream: st, err: e}
		}()

		var stream nabto.StreamDriver
		var streamErr error
		select {
		case <-ctx.Done():
			client.Close()
			select {
			case <-streamCh:
			case <-time.After(2 * time.Second):
			}
			break supervisorLoop
		case <-time.After(15 * time.Second):
			streamErr = fmt.Errorf("timeout (15s) while opening signaling stream on port %d", port)
			client.Close()
			select {
			case <-streamCh:
			case <-time.After(3 * time.Second):
				logger.Warn("Supervisor", "⚠️ Warning: stream open goroutine did not exit within 3s after Close")
			}
		case res := <-streamCh:
			stream = res.stream
			streamErr = res.err
		}

		if streamErr != nil {
			logger.Error("Supervisor", "❌ OpenSignalingStream failed (%v)", streamErr)
			logger.Info("Supervisor", "🧹 Cleaning up camera connection state...")
			client.Close()
			if ctx.Err() != nil {
				break supervisorLoop
			}
			if usePure {
				logger.Warn("Supervisor", "🚨 Native Pure-Go Nabto driver failed to open signaling stream with camera.")
				logger.Info("Supervisor", "💡 Recommendation: Set 'nabto_driver: cgo' in Home Assistant Add-on config for official Nabto C-SDK support.")
			}
			logger.Info("Supervisor", "⏳ Waiting 15s before retry...")
			select {
			case <-ctx.Done():
				break supervisorLoop
			case <-time.After(15 * time.Second):
			}
			continue
		}
		logger.Info("Supervisor", "✅ Nabto signaling stream connected on port %d", port)

		logger.Info("Bridge", "🚀 [ONLINE] Stream ready at rtsp://0.0.0.0:%d/%s", s.cfg.RTSPPort, s.cfg.RTSPPath)
		logger.Info("Bridge", "🛰️ [ONVIF] Endpoints active at http://0.0.0.0:%d/onvif/device_service", s.cfg.ONVIFPort)

		bridge := webrtc.NewBridge(client, stream, s.rtspServer, s.cfg.Resolution, 1*time.Second)
		s.bridgeCtrl.SetBridge(bridge)

		_ = bridge.Run(ctx)

		s.bridgeCtrl.SetBridge(nil)
		stream.Close()
		logger.Info("Supervisor", "🧹 Closing camera session and releasing connection...")
		client.Close()

		if ctx.Err() == nil {
			logger.Info("Supervisor", "⏳ Stream session disconnected / Watchdog reset. Waiting 30s cooldown before reconnecting to allow camera reboot...")
			select {
			case <-ctx.Done():
				break supervisorLoop
			case <-time.After(reconnectCooldown):
			}
		}
	}
	return nil
}
