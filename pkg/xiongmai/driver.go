package xiongmai

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mqtt"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/pion/rtp"
)

const (
	defaultIdleGracePeriod = 15 * time.Second
)

// Driver implements the Steinel Camera Driver for Generation 1 devices (Steinel L 620 CAM / XLED CAM 1).
type Driver struct {
	cameraIP         string
	user             string
	password         string
	resolution       string
	rtspServer       rtsp.MediaStream
	eventBus         *events.Bus
	client           *Client
	sofiaStream      *SofiaStream
	ingest           *RTSPIngest
	talk             *TalkClient
	sdcard           *SDCardManager
	ctx              context.Context
	motionResetTimer *time.Timer
	motionMu         sync.Mutex
	mu               sync.Mutex
	streamMu         sync.Mutex
	lastClientSeen   time.Time
	forceRTSPIngest  bool
	running          bool
	closeChan        chan struct{}
}

// SetForceRTSPIngest enables or disables forcing RTSP Port 554 video ingest instead of Sofia port 34567.
func (d *Driver) SetForceRTSPIngest(force bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.forceRTSPIngest = force
}

// ForceRTSPIngest reports whether forced RTSP ingest is enabled.
func (d *Driver) ForceRTSPIngest() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.forceRTSPIngest
}

// NewDriver creates a new driver instance for a Steinel L 620 CAM.
func NewDriver(cameraIP string, user, password string, resolution string, rtspServer rtsp.MediaStream, eventBus *events.Bus) *Driver {
	if user == "" {
		user = "admin"
	}
	if eventBus == nil {
		eventBus = events.GlobalBus
	}

	resNormalized := "1080p"
	sofiaStreamType := "Main"
	if strings.EqualFold(resolution, "360p") {
		resNormalized = "360p"
		sofiaStreamType = "Extra1"
	} else if strings.EqualFold(resolution, "720p") {
		resNormalized = "720p"
	}

	client := NewClient(cameraIP, DefaultPort, user, password)
	sofiaStream := NewSofiaStream(cameraIP, DefaultPort, client, rtspServer, sofiaStreamType)
	talk := NewTalkClient(client)
	sdcard := NewSDCardManager(client, cameraIP, user, password)

	return &Driver{
		cameraIP:    cameraIP,
		user:        user,
		password:    password,
		resolution:  resNormalized,
		rtspServer:  rtspServer,
		eventBus:    eventBus,
		client:      client,
		sofiaStream: sofiaStream,
		talk:        talk,
		ingest:      nil, // Only started if Sofia connection fails
		sdcard:      sdcard,
		closeChan:   make(chan struct{}),
	}
}

func (d *Driver) GetSDCardManager() *SDCardManager {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sdcard
}

// Start connects to the camera, activates RTSP, queries state, and starts streaming and keepalive loops.
func (d *Driver) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return nil
	}
	d.ctx = ctx

	logger.Info("Xiongmai Driver", "🚀 Connecting to Steinel L 620 CAM at %s (TCP Port %d)...", d.cameraIP, DefaultPort)

	// Register live telemetry push handler (MsgID 1582)
	d.client.SetOnTelemetry(func(mcuCfg *MCUConfig) {
		d.applyMCUConfig(mcuCfg)
	})

	// Register live alarm/motion push handler (MsgID 1500)
	d.client.SetOnAlarm(d.handleAlarmEvent)

	// Step 1: Connect & Login on Sofia port 34567
	var sofiaAvailable bool
	if err := d.client.Connect(ctx); err != nil {
		logger.Warn("Xiongmai Driver", "⚠️ Sofia port %d connection error (%v) — starting in resilient RTSP mode with background reconnect", DefaultPort, err)
	} else if d.client.IsLoggedIn() {
		sofiaAvailable = true
		// Step 2: Query video encode config & ensure H.264
		if err := d.client.EnsureH264Encoding(); err != nil {
			logger.Warn("Xiongmai Driver", "⚠️ Video encode check result: %v", err)
		}

		// Step 3: Query initial light and MCU states
		d.syncInitialState()

		// Step 4: On-demand streaming check
		if d.forceRTSPIngest {
			logger.Info("Xiongmai Driver", "📡 Forced RTSP Port %d mode enabled: Sofia video stream disabled, starting native RTSP ingest", RTSPPort)
			sofiaAvailable = false
		} else if d.rtspServer != nil && d.rtspServer.ActiveClients() > 0 {
			d.lastClientSeen = time.Now()
			if err := d.startSofiaStreamLocked(ctx); err != nil {
				logger.Warn("Xiongmai Driver", "⚠️ Failed to start initial Sofia stream (%v), falling back to RTSP ingest", err)
				sofiaAvailable = false
			} else {
				logger.Info("Xiongmai Driver", "🎥 Initial Sofia video stream active on port %d (clients connected)", DefaultPort)
			}
		} else {
			logger.Info("Xiongmai Driver", "💤 On-demand streaming armed: camera media socket idle until RTSP client connects")
		}
	}

	// Step 5: Always launch autonomous background supervisor for Sofia control connection
	go d.superviseSofiaLoop(ctx)

	// Step 6: Launch on-demand media supervisor (handles start/stop based on RTSP client count)
	go d.superviseOnDemandLoop(ctx)

	// Step 7: Fallback or forced RTSP Ingest
	if !sofiaAvailable || d.forceRTSPIngest {
		effectivePwd := d.client.GetEffectivePassword()
		if effectivePwd == "" && d.password != "" {
			effectivePwd = d.password
		}
		streamSubtype := 0
		if d.resolution == "360p" {
			streamSubtype = 1
		}
		logger.Info("Xiongmai Driver", "Starting RTSP Ingest: IP=%s, Port=%d, User=%s, PwdMasked=%s",
			d.cameraIP, RTSPPort, d.user, MaskPassword(effectivePwd))
		d.ingest = NewRTSPIngest(d.cameraIP, RTSPPort, d.user, effectivePwd, streamSubtype, d.rtspServer, d.password)
		if err := d.ingest.Start(ctx); err != nil {
			logger.Warn("Xiongmai Driver", "⚠️ Failed to start RTSP Ingest: %v", err)
		}
	} else {
		if d.ingest != nil {
			_ = d.ingest.Close()
			d.ingest = nil
		}
	}

	d.running = true
	logger.Info("Xiongmai Driver", "✅ Steinel L 620 CAM driver running")
	return nil
}

func (d *Driver) applyMCUConfig(mcuCfg *MCUConfig) {
	if d.eventBus == nil || mcuCfg == nil {
		return
	}
	st := d.eventBus.GetStatus()
	switch mcuCfg.Mode {
	case 3: // 'd' -> Continuous Light (Dauerlicht)
		st.LampMode = 1
	case 1: // 'b' -> Sensor Automatik
		st.LampMode = 2
	default:
		st.LampMode = 0
	}
	st.PIRActive = (mcuCfg.Distance > 0 && mcuCfg.Mode != 0)

	if mcuCfg.Highlight > 0 {
		st.Highlight = mcuCfg.Highlight
	}
	if mcuCfg.HighlightDelaySec > 0 {
		st.HighlightTime = mcuCfg.HighlightDelaySec
	}
	if mcuCfg.TwilightLux > 0 {
		st.Lux = mcuCfg.TwilightLux
	}
	if mcuCfg.Distance >= 10 {
		st.PIRSensitivity = 100
	} else if mcuCfg.Distance >= 6 {
		st.PIRSensitivity = 66
	} else if mcuCfg.Distance > 0 {
		st.PIRSensitivity = 33
	}
	st.Lowlight = mcuCfg.Lowlight
	if mcuCfg.LowlightDuration == "all_night" || strings.HasPrefix(mcuCfg.LowlightDuration, "all") {
		st.LowlightTime = 24 * 60
	} else if dur, err := strconv.Atoi(strings.TrimSuffix(mcuCfg.LowlightDuration, "h")); err == nil {
		st.LowlightTime = dur * 60
	}
	d.eventBus.UpdateStatus(st)
}

func (d *Driver) syncInitialState() {
	if d.eventBus == nil {
		return
	}

	logger.Trace("Xiongmai Driver", "Synchronizing initial camera state via Sofia port %d...", DefaultPort)

	// Propagate detected FPS and Resolution from client Simplify.Encode
	if d.client != nil {
		if fps := d.client.GetVideoFPS(); fps > 0 {
			if d.sofiaStream != nil {
				d.sofiaStream.SetFPS(fps)
			}
			logger.Info("Xiongmai Driver", "📹 Detected camera video framerate: %d FPS", fps)
		}
		if res := d.client.GetVideoResolution(); res != "" {
			if strings.EqualFold(res, "720p") || strings.EqualFold(res, "720") {
				if d.resolution != "360p" {
					d.resolution = "720p"
				}
			} else if strings.EqualFold(res, "1080p") || strings.EqualFold(res, "1080") {
				if d.resolution != "360p" {
					d.resolution = "1080p"
				}
			}
			logger.Info("Xiongmai Driver", "📹 Detected camera video resolution: %s (active: %s)", res, d.resolution)
		}
	}

	st := d.eventBus.GetStatus()
	st.Resolution = d.resolution
	st.FirmwareVer = "Xiongmai-Sofia"
	d.eventBus.UpdateStatus(st)

	// Query MCU Config via OPTrans (which also queries light on/off state via mode)
	if mcuCfg, err := d.client.QueryMCUConfig(); err == nil && mcuCfg != nil {
		d.applyMCUConfig(mcuCfg)
		logger.Debug("Xiongmai Driver", "💡 Synced MCU Config: LightOn=%v, Bright=%d%%, Lux=%d, Dist=%dm, Delay=%ds, Lowlight=%d%% (%s)",
			mcuCfg.IsLightOn(), mcuCfg.Highlight, mcuCfg.TwilightLux, mcuCfg.Distance, mcuCfg.HighlightDelaySec, mcuCfg.Lowlight, mcuCfg.LowlightDuration)
	}
}

func (d *Driver) superviseSofiaLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	reconnectBackoff := 5 * time.Second
	firstAttempt := true

	for {
		if !d.client.IsLoggedIn() {
			if !firstAttempt {
				select {
				case <-ctx.Done():
					return
				case <-d.closeChan:
					return
				case <-time.After(reconnectBackoff):
				}
			}
			firstAttempt = false

			logger.Debug("Xiongmai Driver", "🔄 Attempting background reconnect to Sofia port %d...", DefaultPort)
			connCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := d.client.Connect(connCtx)
			cancel()

			if err == nil && d.client.IsLoggedIn() {
				logger.Info("Xiongmai Driver", "✅ Sofia control connection established (SessionID: 0x%08X)", d.client.GetSessionID())
				reconnectBackoff = 5 * time.Second
				firstAttempt = false

				_ = d.client.EnsureH264Encoding()
				d.syncInitialState()

				if !d.forceRTSPIngest && d.ingest != nil {
					logger.Info("Xiongmai Driver", "⏹️ Closing fallback RTSP ingest now that Sofia control is restored")
					_ = d.ingest.Close()
					d.ingest = nil
				}

				if !d.forceRTSPIngest && d.sofiaStream != nil && !d.sofiaStream.IsRunning() && d.rtspServer != nil && d.rtspServer.ActiveClients() > 0 {
					go func() {
						_ = d.startSofiaStream(ctx)
					}()
				}
			} else {
				logger.Debug("Xiongmai Driver", "ℹ️ Sofia reconnect attempt failed: %v (retrying in %v)", err, reconnectBackoff)
				if reconnectBackoff < 60*time.Second {
					reconnectBackoff *= 2
					if reconnectBackoff > 60*time.Second {
						reconnectBackoff = 60 * time.Second
					}
				}
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-d.closeChan:
			return
		case <-ticker.C:
			if d.client.IsLoggedIn() {
				if err := d.client.SendKeepAlive(); err != nil {
					logger.Warn("Xiongmai Driver", "⚠️ Sofia heartbeat failed: %v (connection lost, reconnecting in background...)", err)
					_ = d.client.Close()
					reconnectBackoff = 5 * time.Second
				}
			}
		}
	}
}

// OnAudioBackchannelPacket forwards incoming audio from the RTSP server to the camera speaker.
func (d *Driver) OnAudioBackchannelPacket(pkt *rtp.Packet) {
	if d.talk != nil {
		_ = d.talk.SendAudioPacket(pkt)
	}
}

// GetMQTTCallbacks returns the Callbacks struct configured for this driver.
func (d *Driver) GetMQTTCallbacks() mqtt.Callbacks {
	return mqtt.Callbacks{
		SetLampMode: func(mode string) error {
			modeLower := strings.ToLower(mode)
			on := modeLower == "on" || modeLower == "1" || modeLower == "dauerlicht"
			return d.SetLamp(on)
		},
		SetHighlight: func(percent int) error {
			return d.SetDim(percent)
		},
		SetHighlightTime: func(seconds int) error {
			return d.SetDuration(seconds)
		},
		SetLowlight: func(percent int) error {
			return d.SetNightlight(percent)
		},
		SetLowlightTime: func(timeVal int) error {
			return d.SetNightlightDuration(fmt.Sprintf("%dh", timeVal/60))
		},
		SetPIRSensitivity: func(percent int) error {
			var dist int
			if percent >= 67 {
				dist = 10 // Maximum ('u')
			} else if percent >= 34 {
				dist = 6 // Medium ('p')
			} else {
				dist = 3 // Minimum ('k')
			}
			return d.SetDistance(dist)
		},
		SetLuxThreshold: func(lux int) error {
			return d.SetTwilight(lux)
		},
		ReconnectSession: d.ReconnectSession,
	}
}

// ReconnectSession triggers an immediate reconnect of the Xiongmai client and streams.
func (d *Driver) ReconnectSession() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		logger.Info("Xiongmai Driver", "🔄 Manual session reconnect requested via MQTT / API")
		_ = d.client.Close()
		return nil
	}
	return errors.New("no active camera connection")
}

// SetTime synchronizes the system clock on the L 620 camera.
func (d *Driver) SetTime(t time.Time) error {
	if d.client != nil {
		return d.client.SetTime(t)
	}
	return errors.New("camera driver not connected")
}

// SetLamp turns the main light on or off.
func (d *Driver) SetLamp(on bool) error {
	logger.Trace("Xiongmai Driver", "SetLamp: on=%v", on)
	err := d.client.SetLightState(on)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		if on {
			st.LampMode = 1 // Dauerlicht (Mode 3, 'd')
		} else {
			st.LampMode = 2 // Sensor Automatik (Mode 1, 'b')
		}
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetDim sets the main light dimming level (10-100%).
func (d *Driver) SetDim(val int) error {
	logger.Trace("Xiongmai Driver", "SetDim: val=%d%%", val)
	err := d.client.SetHighlight(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.Highlight = val
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetTwilight sets the twilight sensor threshold (2-1000 Lux).
func (d *Driver) SetTwilight(val int) error {
	logger.Trace("Xiongmai Driver", "SetTwilight: lux=%d", val)
	err := d.client.SetLux(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.Lux = val
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetDistance sets the PIR detection distance (1-10 meters).
func (d *Driver) SetDistance(val int) error {
	logger.Trace("Xiongmai Driver", "SetDistance: distance=%dm", val)
	err := d.client.SetDistance(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		if val >= 8 {
			st.PIRSensitivity = 100
		} else if val >= 5 {
			st.PIRSensitivity = 66
		} else {
			st.PIRSensitivity = 33
		}
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetDuration sets the main light on-time in seconds.
func (d *Driver) SetDuration(val int) error {
	logger.Trace("Xiongmai Driver", "SetDuration: seconds=%ds", val)
	err := d.client.SetHighlightDelay(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.HighlightTime = val
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetNightlight sets the nightlight brightness (0-50%).
func (d *Driver) SetNightlight(val int) error {
	logger.Trace("Xiongmai Driver", "SetNightlight: val=%d%%", val)
	err := d.client.SetLowlight(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.Lowlight = val
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetNightlightDuration sets the nightlight duration mode ("all_night", "4h", "off").
func (d *Driver) SetNightlightDuration(val string) error {
	logger.Trace("Xiongmai Driver", "SetNightlightDuration: mode=%q", val)
	var durInt int
	valLower := strings.ToLower(strings.TrimSpace(val))
	switch {
	case valLower == "all_night" || valLower == "ganze nacht":
		durInt = 20
	case valLower == "off" || valLower == "aus":
		durInt = 0
	case strings.HasSuffix(valLower, "h"):
		hStr := strings.TrimSuffix(valLower, "h")
		if hours, err := strconv.Atoi(hStr); err == nil {
			durInt = hours * 2
		}
	}

	err := d.client.SetLowlightDuration(durInt)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		if dur, err := strconv.Atoi(strings.TrimSuffix(valLower, "h")); err == nil {
			st.LowlightTime = dur * 60
		}
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetResolution switches between 1080p/720p (stream 0 / "Main") and 360p (stream 1 / "Extra1") for the Steinel L 620 CAM.
func (d *Driver) SetResolution(res string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	streamSubtype := 0
	resNormalized := "1080p"
	sofiaStreamType := "Main"
	if strings.EqualFold(res, "360p") {
		streamSubtype = 1
		resNormalized = "360p"
		sofiaStreamType = "Extra1"
	} else if strings.EqualFold(res, "720p") {
		streamSubtype = 0
		resNormalized = "720p"
	}

	d.resolution = resNormalized
	if d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.Resolution = resNormalized
		d.eventBus.UpdateStatus(st)
	}

	// 1. If SofiaStream is configured:
	if d.sofiaStream != nil {
		d.sofiaStream.SetStreamType(sofiaStreamType)
	}

	// 2. Forced RTSP Ingest mode
	if d.forceRTSPIngest {
		if d.ingest != nil {
			effectivePwd := d.client.GetEffectivePassword()
			if effectivePwd == "" && d.password != "" {
				effectivePwd = d.password
			}
			_ = d.ingest.Close()
			d.ingest = NewRTSPIngest(d.cameraIP, RTSPPort, d.user, effectivePwd, streamSubtype, d.rtspServer, d.password)
			return d.ingest.Start(context.Background())
		}
		return nil
	}

	// 3. If Sofia client is connected and active:
	if d.client != nil && d.client.IsLoggedIn() && d.sofiaStream != nil {
		d.streamMu.Lock()
		defer d.streamMu.Unlock()

		if d.sofiaStream.IsRunning() {
			logger.Info("Xiongmai Driver", "🔄 Switching native Sofia video stream resolution to %s (%s)...", resNormalized, sofiaStreamType)
			_ = d.sofiaStream.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := d.sofiaStream.Start(ctx); err != nil {
				logger.Warn("Xiongmai Driver", "⚠️ Failed to restart Sofia stream with %s: %v", resNormalized, err)
				return err
			}
			logger.Info("Xiongmai Driver", "🎥 Native Sofia video stream active with resolution %s (%s)", resNormalized, sofiaStreamType)
		}

		// Ensure fallback Ingest is closed and nil to prevent dual streaming
		if d.ingest != nil {
			_ = d.ingest.Close()
			d.ingest = nil
		}
		return nil
	}

	// 2. Fallback mode: RTSP Ingest on port 554
	if d.ingest != nil {
		effectivePwd := d.client.GetEffectivePassword()
		if effectivePwd == "" && d.password != "" {
			effectivePwd = d.password
		}
		_ = d.ingest.Close()
		d.ingest = NewRTSPIngest(d.cameraIP, RTSPPort, d.user, effectivePwd, streamSubtype, d.rtspServer, d.password)
		return d.ingest.Start(context.Background())
	}
	return nil
}

func (d *Driver) handleAlarmEvent(event string, isStart bool) {
	logger.Info("Xiongmai Driver", "🚨 Alarm/Motion event received from camera: %s (isStart: %v)", event, isStart)
	if isStart {
		if d.eventBus != nil {
			d.eventBus.SetMotion(true)
		}
		d.motionMu.Lock()
		if d.motionResetTimer != nil {
			d.motionResetTimer.Stop()
		}
		d.motionResetTimer = time.AfterFunc(10*time.Second, func() {
			logger.Info("Xiongmai Driver", "⚪ Motion cleared (10s timeout)")
			if d.eventBus != nil {
				d.eventBus.SetMotion(false)
			}
		})
		d.motionMu.Unlock()
	} else {
		d.motionMu.Lock()
		if d.motionResetTimer != nil {
			d.motionResetTimer.Stop()
			d.motionResetTimer = nil
		}
		d.motionMu.Unlock()
		if d.eventBus != nil {
			d.eventBus.SetMotion(false)
		}
	}
}

// Close shuts down all driver connections and streams.
func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.running {
		return nil
	}
	d.running = false
	close(d.closeChan)

	d.motionMu.Lock()
	if d.motionResetTimer != nil {
		d.motionResetTimer.Stop()
		d.motionResetTimer = nil
	}
	d.motionMu.Unlock()

	if d.sofiaStream != nil {
		_ = d.sofiaStream.Stop()
	}
	if d.talk != nil {
		_ = d.talk.StopTalk()
	}
	if d.ingest != nil {
		_ = d.ingest.Close()
	}
	if d.client != nil {
		_ = d.client.Close()
	}
	return nil
}

// startSofiaStream safely initiates the Sofia video stream if not already active.
func (d *Driver) startSofiaStream(ctx context.Context) error {
	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	return d.startSofiaStreamLocked(ctx)
}

func (d *Driver) startSofiaStreamLocked(ctx context.Context) error {
	if d.sofiaStream == nil || d.sofiaStream.IsRunning() {
		return nil
	}
	if d.client == nil || !d.client.IsLoggedIn() {
		return errors.New("client not logged in")
	}

	logger.Info("Xiongmai Driver", "🎥 Activating on-demand Sofia video stream (Port %d)...", DefaultPort)
	if err := d.sofiaStream.Start(ctx); err != nil {
		logger.Warn("Xiongmai Driver", "⚠️ Failed to start native Sofia stream (%v)", err)
		return err
	}
	logger.Info("Xiongmai Driver", "🎥 Native Sofia video stream active on port %d", DefaultPort)
	if d.ingest != nil {
		_ = d.ingest.Close()
		d.ingest = nil
	}
	return nil
}

// stopSofiaStream stops the Sofia video stream and puts the camera media socket to sleep.
func (d *Driver) stopSofiaStream() {
	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	d.stopSofiaStreamLocked()
}

func (d *Driver) stopSofiaStreamLocked() {
	if d.sofiaStream == nil || !d.sofiaStream.IsRunning() {
		return
	}
	logger.Info("Xiongmai Driver", "⏹️ No active RTSP clients: stopping on-demand Sofia video stream (idle grace period %v reached)", defaultIdleGracePeriod)
	_ = d.sofiaStream.Stop()
}

// superviseOnDemandLoop monitors active RTSP clients and starts/stops the Sofia video stream on demand.
func (d *Driver) superviseOnDemandLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.closeChan:
			return
		case <-ticker.C:
			d.mu.Lock()
			forceRTSP := d.forceRTSPIngest
			d.mu.Unlock()
			if forceRTSP || d.rtspServer == nil {
				continue
			}

			clients := d.rtspServer.ActiveClients()
			if clients > 0 {
				d.mu.Lock()
				d.lastClientSeen = time.Now()
				d.mu.Unlock()

				if d.sofiaStream != nil && !d.sofiaStream.IsRunning() && d.client != nil && d.client.IsLoggedIn() {
					_ = d.startSofiaStream(ctx)
				}
			} else {
				d.mu.Lock()
				lastSeen := d.lastClientSeen
				d.mu.Unlock()

				if d.sofiaStream != nil && d.sofiaStream.IsRunning() {
					if !lastSeen.IsZero() && time.Since(lastSeen) >= defaultIdleGracePeriod {
						d.stopSofiaStream()
					}
				}
			}
		}
	}
}

// RequestKeyframe triggers immediate startup of the on-demand stream when an RTSP client connects.
func (d *Driver) RequestKeyframe() {
	d.mu.Lock()
	d.lastClientSeen = time.Now()
	forceRTSP := d.forceRTSPIngest
	ctx := d.ctx
	d.mu.Unlock()

	if !forceRTSP && d.sofiaStream != nil && !d.sofiaStream.IsRunning() && d.client != nil && d.client.IsLoggedIn() && ctx != nil {
		go func() {
			_ = d.startSofiaStream(ctx)
		}()
	}
}
