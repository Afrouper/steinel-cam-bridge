package driver

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Afrouper/steinel-cam-bridge/pkg/netprobe"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/Afrouper/steinel-cam-bridge/pkg/webrtc"
)

var _ CameraDriver = (*L625Driver)(nil)

// L625Driver implements CameraDriver for Steinel L 625 CAM SC (Generation 2, Nabto Edge & WebRTC architecture).
type L625Driver struct {
	cfg                *config.Config
	rtspServer         rtsp.MediaStream
	eventBus           *events.Bus
	onDeviceDiscovered func(deviceID, productID string)
	activeBridge       *webrtc.Bridge
	mu                 sync.RWMutex
}

// NewL625Driver creates a new Driver instance for Steinel L 625 CAM SC.
func NewL625Driver(cfg *config.Config, rtspServer rtsp.MediaStream, eventBus *events.Bus, onDeviceDiscovered func(deviceID, productID string)) *L625Driver {
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

const (
	reconnectCooldown    = 60 * time.Second
	softRecoveryCooldown = 3 * time.Second
	baseReconnectDelay   = 15 * time.Second
	maxReconnectDelay    = 240 * time.Second
)

// CalculateBackoff returns the exponential backoff duration based on consecutive failure count.
// Progression: 15s -> 30s -> 60s -> 120s -> 240s (max 4 minutes).
func CalculateBackoff(consecutiveFailures int) time.Duration {
	if consecutiveFailures <= 1 {
		return baseReconnectDelay
	}
	if consecutiveFailures >= 5 {
		return maxReconnectDelay
	}
	delay := baseReconnectDelay * time.Duration(1<<(consecutiveFailures-1))
	if delay > maxReconnectDelay {
		return maxReconnectDelay
	}
	return delay
}

// safeCloseDriver ensures that closing a Nabto driver never blocks the supervisor indefinitely.
func safeCloseDriver(c nabto.Driver) {
	if c == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		c.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		logger.Warn("Supervisor", "⚠️ Warning: driver %s Close() did not finish within 6s", c.DriverName())
	}
}

// ProbeNetworkHealth checks if the camera IP responds on the network and logs clear diagnostics.
// It also serves to wake up the camera Wi-Fi radio from power-save sleep and refresh ARP tables.
func ProbeNetworkHealth(ctx context.Context, ip string) {
	if ip == "" {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	res, err := netprobe.Ping(probeCtx, ip, 1500*time.Millisecond)
	if res.Reachable {
		rtt := res.RTT.Round(time.Millisecond)
		if rtt == 0 {
			rtt = time.Millisecond
		}
		logger.Info("Supervisor", "🌐 Camera %s is reachable on network (Ping RTT: %v). Camera IP stack is alive.", ip, rtt)
	} else {
		logger.Warn("Supervisor", "🔌 Camera %s does NOT respond to network ping (Host unreachable)!", ip)
		logger.Warn("Supervisor", "👉 Action required:")
		logger.Warn("Supervisor", "   1. Check if camera power/wall switch is turned ON.")
		logger.Warn("Supervisor", "   2. Verify Wi-Fi signal strength at camera location.")
		logger.Warn("Supervisor", "   3. Check your router to confirm camera IP address is still %s.", ip)
		if err != nil {
			logger.Debug("Supervisor", "Ping probe error details: %v", err)
		}
	}
}

// Run manages the Nabto Edge handshakes, WebRTC signaling and automatic reconnection loop.
func (d *L625Driver) Run(ctx context.Context) error {
	cfg := d.cfg.NabtoConfig
	consecutiveFailures := 0

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

		// On retry, probe network health and wake up Wi-Fi radio before attempting Nabto connect
		if consecutiveFailures > 0 {
			ProbeNetworkHealth(ctx, cfg.CameraIP)
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
			safeCloseDriver(client)
			select {
			case err := <-connectDone:
				_ = err
			case <-time.After(2 * time.Second):
			}
			break connectionLoop
		case <-time.After(connectTimeout):
			connectErr = fmt.Errorf("connection timeout (%v) reached", connectTimeout)
			safeCloseDriver(client)
			select {
			case err := <-connectDone:
				_ = err
			case <-time.After(6 * time.Second):
				logger.Warn("Supervisor", "⚠️ Warning: connect goroutine did not exit within 6s after Close")
			}
		case err := <-connectDone:
			connectErr = err
		}

		if connectErr != nil {
			consecutiveFailures++
			backoffDelay := CalculateBackoff(consecutiveFailures)
			logger.Error("Supervisor", "❌ Connect failed (%v)", connectErr)
			logger.Info("Supervisor", "🧹 Cleaning up camera connection state...")
			safeCloseDriver(client)
			if ctx.Err() != nil {
				break connectionLoop
			}
			if client.DriverName() == "pure" {
				logger.Warn("Supervisor", "🚨 Native Pure-Go Nabto driver failed to connect to camera.")
				logger.Info("Supervisor", "💡 Recommendation: Set 'nabto_driver: cgo' in Home Assistant Add-on config for official Nabto C-SDK support.")
			}
			logger.Info("Supervisor", "⏳ Waiting %v before retry (consecutive failure #%d, backoff active) to allow camera cooldown...", backoffDelay, consecutiveFailures)
			select {
			case <-ctx.Done():
				break connectionLoop
			case <-time.After(backoffDelay):
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
			safeCloseDriver(client)
			select {
			case <-portCh:
			case <-time.After(2 * time.Second):
			}
			break connectionLoop
		case <-time.After(15 * time.Second):
			portErr = fmt.Errorf("timeout (15s) while querying signaling port")
			safeCloseDriver(client)
			select {
			case <-portCh:
			case <-time.After(6 * time.Second):
				logger.Warn("Supervisor", "⚠️ Warning: port query goroutine did not exit within 6s after Close")
			}
		case res := <-portCh:
			port = res.port
			portErr = res.err
		}

		if portErr != nil {
			consecutiveFailures++
			backoffDelay := CalculateBackoff(consecutiveFailures)
			logger.Error("Supervisor", "❌ GetSignalingPort failed (%v)", portErr)
			logger.Info("Supervisor", "🧹 Cleaning up camera connection state...")
			safeCloseDriver(client)
			if ctx.Err() != nil {
				break connectionLoop
			}
			if client.DriverName() == "pure" {
				logger.Warn("Supervisor", "🚨 Native Pure-Go Nabto driver failed to query signaling port from camera.")
				logger.Info("Supervisor", "💡 Recommendation: Set 'nabto_driver: cgo' in Home Assistant Add-on config for official Nabto C-SDK support.")
			}
			logger.Info("Supervisor", "⏳ Waiting %v before retry (consecutive failure #%d, backoff active)...", backoffDelay, consecutiveFailures)
			select {
			case <-ctx.Done():
				break connectionLoop
			case <-time.After(backoffDelay):
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
			safeCloseDriver(client)
			select {
			case <-streamCh:
			case <-time.After(2 * time.Second):
			}
			break connectionLoop
		case <-time.After(15 * time.Second):
			streamErr = fmt.Errorf("timeout (15s) while opening signaling stream on port %d", port)
			safeCloseDriver(client)
			select {
			case <-streamCh:
			case <-time.After(6 * time.Second):
				logger.Warn("Supervisor", "⚠️ Warning: stream open goroutine did not exit within 6s after Close")
			}
		case res := <-streamCh:
			stream = res.stream
			streamErr = res.err
		}

		if streamErr != nil {
			consecutiveFailures++
			backoffDelay := CalculateBackoff(consecutiveFailures)
			logger.Error("Supervisor", "❌ OpenSignalingStream failed (%v)", streamErr)
			logger.Info("Supervisor", "🧹 Cleaning up camera connection state...")
			safeCloseDriver(client)
			if ctx.Err() != nil {
				break connectionLoop
			}
			if client.DriverName() == "pure" {
				logger.Warn("Supervisor", "🚨 Native Pure-Go Nabto driver failed to open signaling stream with camera.")
				logger.Info("Supervisor", "💡 Recommendation: Set 'nabto_driver: cgo' in Home Assistant Add-on config for official Nabto C-SDK support.")
			}
			logger.Info("Supervisor", "⏳ Waiting %v before retry (consecutive failure #%d, backoff active)...", backoffDelay, consecutiveFailures)
			select {
			case <-ctx.Done():
				break connectionLoop
			case <-time.After(backoffDelay):
			}
			continue
		}
		logger.Info("Supervisor", "✅ Nabto signaling stream connected on port %d", port)
		logger.Info("Supervisor", "🚀 Establishing WebRTC bridge session for /%s...", d.cfg.RTSPPath)

		bridge := webrtc.NewBridge(client, stream, d.rtspServer, d.eventBus, d.cfg.Resolution, 1*time.Second)
		bridge.SetDeviceInfoInterval(time.Duration(d.cfg.DeviceInfoIntervalMinutes) * time.Minute)
		bridge.SetOnConnected(func() {
			logger.Info("Bridge", "🚀 [ONLINE] Stream ready at rtsp://0.0.0.0:%d/%s", d.cfg.RTSPPort, d.cfg.RTSPPath)
			logger.Info("Bridge", "🛰️ [ONVIF] Endpoints active at http://0.0.0.0:%d/onvif/device_service", d.cfg.ONVIFPort)
		})
		if d.cfg != nil {
			if d.cfg.SDCardRecoveryTimeoutMinutes > 0 {
				bridge.SetRecoveryTimeout(time.Duration(d.cfg.SDCardRecoveryTimeoutMinutes) * time.Minute)
			} else if d.cfg.SDCardRecoveryTimeoutMinutes == 0 {
				bridge.SetRecoveryTimeout(0)
			}
		}
		d.setBridge(bridge)

		sessionStart := time.Now()
		runErr := bridge.Run(ctx)
		if runErr != nil && ctx.Err() == nil {
			logger.Warn("Bridge", "⚠️ WebRTC bridge session ended with error: %v", runErr)
		}

		reason := bridge.ResetReason()
		d.setBridge(nil)
		stream.Close()
		logger.Info("Supervisor", "🧹 Closing camera session and releasing connection...")
		safeCloseDriver(client)

		if ctx.Err() == nil {
			if time.Since(sessionStart) > 60*time.Second {
				consecutiveFailures = 0
			} else {
				consecutiveFailures++
			}

			cooldown := reconnectCooldown
			if reason == webrtc.ResetReasonControlPlaneRecovery || reason == webrtc.ResetReasonManual {
				cooldown = softRecoveryCooldown
				logger.Info("Supervisor", "🔄 Soft-recovery session reset complete (%s). Reconnecting in %v (skipping full hardware reboot cooldown)...", reason, cooldown)
			} else {
				if consecutiveFailures > 1 {
					backoff := CalculateBackoff(consecutiveFailures)
					if backoff > cooldown {
						cooldown = backoff
					}
				}
				logger.Info("Supervisor", "⏳ Stream session disconnected / Watchdog reset. Waiting %v cooldown before reconnecting to allow camera reboot (failure #%d)...", cooldown, consecutiveFailures)
			}

			select {
			case <-ctx.Done():
				break connectionLoop
			case <-time.After(cooldown):
			}
		}
	}
	return nil
}

// ReconnectSession triggers an immediate soft reconnect of the camera session.
func (d *L625Driver) ReconnectSession() error {
	b := d.getBridge()
	if b != nil {
		logger.Info("Supervisor", "🔄 Manual session reconnect requested via MQTT / API")
		b.TriggerManualReset()
		return nil
	}
	return errors.New("no active camera bridge session")
}

// Reboot triggers a controlled hardware reboot of the L 625 camera.
func (d *L625Driver) Reboot() error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.Reboot()
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
	err = b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.HighlightTime = seconds
		d.eventBus.UpdateStatus(st)
	}
	return err
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
	err = b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.Lowlight = percent
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetLowlightTime sets the nightlight duration (hours 0-12 or seconds 0-43200).
func (d *L625Driver) SetLowlightTime(timeVal int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	seconds := timeVal
	if timeVal <= 12 {
		seconds = timeVal * 3600
	}
	b64, err := mcu.BuildSetLowlightTime(seconds)
	if err != nil {
		return err
	}
	err = b.SendCommand("tran_ctl", map[string]interface{}{"data": b64})
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.LowlightTime = seconds
		d.eventBus.UpdateStatus(st)
	}
	return err
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

// SetSystemDateAndTime synchronizes the timezone and clock on the L 625 camera.
func (d *L625Driver) SetSystemDateAndTime(t time.Time, tz string) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.SetSystemDateAndTime(t, tz)
}

// SetTimestampPosition switches the timestamp overlay off (0) or moves it to a corner (1-4).
func (d *L625Driver) SetTimestampPosition(position int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.SetTimestampPosition(position)
}

// SetSpeakerVolume sets the volume of the camera's speaker (0-100).
func (d *L625Driver) SetSpeakerVolume(percent int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.SetSpeakerVolume(percent)
}

// SetDetectMode sets the camera-side detection mode.
func (d *L625Driver) SetDetectMode(mode string) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.SetDetectMode(mode)
}

// SetPixelChangeDetection switches the lamp's "Pixel Change Detection" (light reacts to picture changes).
func (d *L625Driver) SetPixelChangeDetection(on bool) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.SetPixelChangeDetection(on)
}

// SetDrawFrame switches the detection boxes burnt into the picture on or off.
func (d *L625Driver) SetDrawFrame(on bool) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.SetDrawFrame(on)
}

// SetAIPersonSensitivity sets the person detection sensitivity (2, 5 or 7).
func (d *L625Driver) SetAIPersonSensitivity(level int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.SetAIPersonSensitivity(level)
}

// SetRecordMode chooses what makes the camera record to its SD card (2, 3 or 4).
func (d *L625Driver) SetRecordMode(mode int) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.SetRecordMode(mode)
}

// InstallUpdate starts an update the camera offers ("camera" or "mcu").
func (d *L625Driver) InstallUpdate(target string) error {
	b := d.getBridge()
	if b == nil {
		return ErrCameraOffline
	}
	return b.InstallUpdate(target)
}

// DebugCommand sends a raw JSON command to the camera and returns its replies
// (credentials removed). Used by the optional diagnostic API.
func (d *L625Driver) DebugCommand(cmd, action string, info map[string]interface{}, timeout time.Duration) ([]json.RawMessage, error) {
	b := d.getBridge()
	if b == nil {
		return nil, ErrCameraOffline
	}
	return b.DebugCommand(cmd, action, info, timeout)
}
