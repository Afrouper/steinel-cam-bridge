package driver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtp"

	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mcu"
	"github.com/Afrouper/steinel-cam-bridge/pkg/nabto"
	_ "github.com/Afrouper/steinel-cam-bridge/pkg/nabtopure"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/Afrouper/steinel-cam-bridge/pkg/webrtc"
)

var _ CameraDriver = (*L625Driver)(nil)

// L625Driver implements CameraDriver for Steinel L 625 CAM SC (Generation 2, Nabto Edge & WebRTC architecture).
type L625Driver struct {
	cfg                *config.Config
	rtspServer         *rtsp.Server
	eventBus           *events.Bus
	onDeviceDiscovered func(deviceID, productID string)
	activeBridge       *webrtc.Bridge
	mu                 sync.RWMutex
}

// NewL625Driver creates a new Driver instance for Steinel L 625 CAM SC.
func NewL625Driver(cfg *config.Config, rtspServer *rtsp.Server, eventBus *events.Bus, onDeviceDiscovered func(deviceID, productID string)) *L625Driver {
	return &L625Driver{
		cfg:                cfg,
		rtspServer:         rtspServer,
		eventBus:           eventBus,
		onDeviceDiscovered: onDeviceDiscovered,
	}
}

func (d *L625Driver) getBridge() *webrtc.Bridge {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.activeBridge
}

func (d *L625Driver) setBridge(b *webrtc.Bridge) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.activeBridge = b
}

// Run manages the Nabto Edge handshakes, WebRTC signaling and automatic reconnection loop.
func (d *L625Driver) Run(ctx context.Context) error {
	const reconnectCooldown = 30 * time.Second
	cfg := d.cfg.NabtoConfig

connectionLoop:
	for ctx.Err() == nil {
		client, err := nabto.New(d.cfg.NabtoDriver, cfg)
		if err != nil {
			logger.Error("Nabto", "❌ Nabto client init error: %v", err)
			select {
			case <-ctx.Done():
				break connectionLoop
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
			break connectionLoop
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
				break connectionLoop
			}
			if client.DriverName() == "pure" {
				logger.Warn("Supervisor", "🚨 Native Pure-Go Nabto driver failed to connect to camera.")
				logger.Info("Supervisor", "💡 Recommendation: Set 'nabto_driver: cgo' in Home Assistant Add-on config for official Nabto C-SDK support.")
			}
			logger.Info("Supervisor", "⏳ Waiting 15s before retry to allow camera cooldown...")
			select {
			case <-ctx.Done():
				break connectionLoop
			case <-time.After(15 * time.Second):
			}
			continue
		}

		// If DeviceID was discovered during connection, update MQTT discovery
		if d.onDeviceDiscovered != nil && cfg.DeviceID != "" {
			d.onDeviceDiscovered(cfg.DeviceID, cfg.ProductID)
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
			break connectionLoop
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
				break connectionLoop
			}
			if client.DriverName() == "pure" {
				logger.Warn("Supervisor", "🚨 Native Pure-Go Nabto driver failed to query signaling port from camera.")
				logger.Info("Supervisor", "💡 Recommendation: Set 'nabto_driver: cgo' in Home Assistant Add-on config for official Nabto C-SDK support.")
			}
			logger.Info("Supervisor", "⏳ Waiting 15s before retry...")
			select {
			case <-ctx.Done():
				break connectionLoop
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
			break connectionLoop
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
				break connectionLoop
			}
			if client.DriverName() == "pure" {
				logger.Warn("Supervisor", "🚨 Native Pure-Go Nabto driver failed to open signaling stream with camera.")
				logger.Info("Supervisor", "💡 Recommendation: Set 'nabto_driver: cgo' in Home Assistant Add-on config for official Nabto C-SDK support.")
			}
			logger.Info("Supervisor", "⏳ Waiting 15s before retry...")
			select {
			case <-ctx.Done():
				break connectionLoop
			case <-time.After(15 * time.Second):
			}
			continue
		}
		logger.Info("Supervisor", "✅ Nabto signaling stream connected on port %d", port)

		logger.Info("Bridge", "🚀 [ONLINE] Stream ready at rtsp://0.0.0.0:%d/%s", d.cfg.RTSPPort, d.cfg.RTSPPath)
		logger.Info("Bridge", "🛰️ [ONVIF] Endpoints active at http://0.0.0.0:%d/onvif/device_service", d.cfg.ONVIFPort)

		bridge := webrtc.NewBridge(client, stream, d.rtspServer, d.eventBus, d.cfg.Resolution, 1*time.Second)
		d.setBridge(bridge)

		_ = bridge.Run(ctx)

		d.setBridge(nil)
		stream.Close()
		logger.Info("Supervisor", "🧹 Closing camera session and releasing connection...")
		client.Close()

		if ctx.Err() == nil {
			logger.Info("Supervisor", "⏳ Stream session disconnected / Watchdog reset. Waiting 30s cooldown before reconnecting to allow camera reboot...")
			select {
			case <-ctx.Done():
				break connectionLoop
			case <-time.After(reconnectCooldown):
			}
		}
	}
	return nil
}

// Close disconnects the active bridge.
func (d *L625Driver) Close() error {
	d.setBridge(nil)
	return nil
}

// SetResolution updates the video stream resolution.
func (d *L625Driver) SetResolution(res string) error {
	b := d.getBridge()
	if b != nil {
		return b.SetResolution(res)
	}
	return nil
}

// RequestKeyframe requests an immediate video keyframe.
func (d *L625Driver) RequestKeyframe() {
	b := d.getBridge()
	if b != nil {
		b.RequestKeyframe()
	}
}

// SetLampState switches the main lamp on or off.
func (d *L625Driver) SetLampState(mode string) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.SetLampState(mode)
}

// SetHighlight sets the primary light brightness percentage.
func (d *L625Driver) SetHighlight(percent int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	b64, err := mcu.BuildSetHighlight(percent)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

// SetHighlightTime sets the primary light duration.
func (d *L625Driver) SetHighlightTime(seconds int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	b64, err := mcu.BuildSetHighlightTime(seconds)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

// SetLowlight sets the nightlight brightness percentage.
func (d *L625Driver) SetLowlight(percent int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	b64, err := mcu.BuildSetLowlight(percent)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

// SetLowlightTime sets the nightlight duration.
func (d *L625Driver) SetLowlightTime(timeVal int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	b64, err := mcu.BuildSetLowlightTime(timeVal)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

// SetPIRSensitivity sets the PIR detection sensitivity.
func (d *L625Driver) SetPIRSensitivity(percent int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	b64, err := mcu.BuildSetPIRSensitivity(percent)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

// SetLuxThreshold sets the twilight switching threshold.
func (d *L625Driver) SetLuxThreshold(lux int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	b64, err := mcu.BuildSetLuxThreshold(lux)
	if err != nil {
		return err
	}
	return b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
}

// SetSiren triggers or mutes the camera alarm siren.
func (d *L625Driver) SetSiren(on bool) error {
	b := d.getBridge()
	if b == nil {
		return nil
	}
	return b.SendCommand("alarm_voice_ctl", map[string]interface{}{"play": on})
}

// GetRecordingProvider returns the SD card storage provider.
func (d *L625Driver) GetRecordingProvider() storage.RecordingProvider {
	b := d.getBridge()
	if b != nil {
		return b.GetSDCardManager()
	}
	return nil
}

// WriteAudioBackchannel routes two-way audio RTP packets to the camera speaker.
func (d *L625Driver) WriteAudioBackchannel(pkt *rtp.Packet) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.WriteAudioBackchannel(pkt)
}
