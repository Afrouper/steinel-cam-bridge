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
	defaultIdleGracePeriod               = 15 * time.Second
	defaultDescribeWarmupTimeout         = 2500 * time.Millisecond
	defaultFallbackControlOutageGrace    = 15 * time.Second
	defaultFallbackSofiaProbeInterval    = 2 * time.Minute
	maxSofiaStreamFailuresBeforeFallback = 3
)

// Driver implements the Steinel Camera Driver for Generation 1 devices (Steinel L 620 CAM / XLED CAM 1).
type Driver struct {
	cameraIP              string
	user                  string
	password              string
	resolution            string
	rtspServer            rtsp.MediaStream
	eventBus              *events.Bus
	client                *Client
	sofiaStream           *SofiaStream
	ingest                *RTSPIngest
	talk                  *TalkClient
	sdcard                *SDCardManager
	ctx                   context.Context
	motionResetTimer      *time.Timer
	motionMu              sync.Mutex
	mu                    sync.Mutex
	streamMu              sync.Mutex
	ingestMu              sync.Mutex
	lastClientSeen        time.Time
	idleSinceLastClient   bool
	describeWarmupTimeout time.Duration
	onDemandTickInterval  time.Duration
	initialStreamBackoff  time.Duration
	controlOutageGrace    time.Duration
	sofiaProbeInterval    time.Duration
	sofiaWakeCh           chan struct{}
	streamWakeCh          chan struct{}
	forceRTSPIngest       bool
	running               bool
	closeChan             chan struct{}
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

	d := &Driver{
		cameraIP:            cameraIP,
		user:                user,
		password:            password,
		resolution:          resNormalized,
		rtspServer:          rtspServer,
		eventBus:            eventBus,
		client:              client,
		sofiaStream:         sofiaStream,
		talk:                talk,
		ingest:              nil, // Only started if Sofia connection fails
		sdcard:              sdcard,
		idleSinceLastClient: true,
		sofiaWakeCh:         make(chan struct{}, 1),
		streamWakeCh:        make(chan struct{}, 1),
		closeChan:           make(chan struct{}),
	}
	if dh, ok := rtspServer.(interface {
		SetOnDescribeHandler(rtsp.OnDescribeHandler)
	}); ok {
		dh.SetOnDescribeHandler(d.PrepareDescribe)
	}
	return d
}

func (d *Driver) GetSDCardManager() *SDCardManager {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sdcard
}

// SetSDCardRateLimit configures the download rate limit on the SDCardManager in KB/s.
func (d *Driver) SetSDCardRateLimit(kbps int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sdcard != nil {
		d.sdcard.SetRateLimit(kbps)
	}
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
		d.syncInitialStateLocked()

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
		d.ingestMu.Lock()
		if d.ingest != nil {
			_ = d.ingest.Close()
		}
		d.ingest = NewRTSPIngest(d.cameraIP, RTSPPort, d.user, effectivePwd, streamSubtype, d.rtspServer, d.password)
		ingestRef := d.ingest
		d.ingestMu.Unlock()
		if err := ingestRef.Start(ctx); err != nil {
			logger.Warn("Xiongmai Driver", "⚠️ Failed to start RTSP Ingest: %v", err)
		}
	} else {
		d.closeIngest()
	}

	d.running = true
	logger.Info("Xiongmai Driver", "✅ Steinel L 620 CAM driver running")
	return nil
}

func distanceToPIRSensitivity(dist int) int {
	if dist >= 8 {
		return 100
	}
	if dist >= 5 {
		return 66
	}
	if dist > 0 {
		return 33
	}
	return 0
}

func (d *Driver) applyMCUConfig(mcuCfg *MCUConfig) {
	if d.eventBus == nil || mcuCfg == nil {
		return
	}
	d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
		st.LampMode = MCUModeToLampMode(mcuCfg.Mode)
		st.PIRActive = (mcuCfg.Distance > 0 && mcuCfg.Mode != MCUModeOff)

		if mcuCfg.Highlight > 0 {
			st.Highlight = mcuCfg.Highlight
		}
		if mcuCfg.HighlightDelaySec > 0 {
			st.HighlightTime = mcuCfg.HighlightDelaySec
		}
		if mcuCfg.TwilightLux > 0 {
			st.Lux = mcuCfg.TwilightLux
		}
		if sens := distanceToPIRSensitivity(mcuCfg.Distance); sens > 0 {
			st.PIRSensitivity = sens
		}
		st.Lowlight = mcuCfg.Lowlight
		if mcuCfg.LowlightDuration == "all_night" || strings.HasPrefix(mcuCfg.LowlightDuration, "all") {
			st.LowlightTime = 24 * 60
		} else if dur, err := strconv.Atoi(strings.TrimSuffix(mcuCfg.LowlightDuration, "h")); err == nil {
			st.LowlightTime = dur * 60
		}
	})
}

func (d *Driver) closeIngest() {
	d.ingestMu.Lock()
	ing := d.ingest
	d.ingest = nil
	d.ingestMu.Unlock()
	if ing != nil {
		_ = ing.Close()
	}
}

func (d *Driver) syncInitialStateLocked() {
	if d.eventBus == nil {
		return
	}

	logger.Trace("Xiongmai Driver", "Synchronizing initial camera state via Sofia port %d...", DefaultPort)

	// Propagate detected FPS and Resolution from client Simplify.Encode
	if d.client != nil {
		targetStream := "Main"
		if strings.EqualFold(d.resolution, "360p") || strings.EqualFold(d.resolution, "sub") {
			targetStream = "Extra1"
		}
		if fps := d.client.GetVideoFPS(targetStream); fps > 0 {
			if d.sofiaStream != nil {
				d.sofiaStream.SetFPS(fps)
			}
			logger.Info("Xiongmai Driver", "📹 Detected camera video framerate: %d FPS (%s)", fps, targetStream)
		}
		if res := d.client.GetVideoResolution(targetStream); res != "" {
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

	activeRes := d.resolution
	d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
		st.Resolution = activeRes
		if st.FirmwareVer == "" {
			st.FirmwareVer = "Xiongmai-Sofia"
		}
	})

	sofiaSt, statusErr := d.client.QueryDeviceStatus()
	if statusErr != nil {
		logger.Debug("Xiongmai Driver", "Sofia device status query incomplete: %v", statusErr)
	}
	if sofiaSt != nil {
		d.applySofiaDeviceStatus(sofiaSt)
	}

	// Query MCU Config via OPTrans (which also queries light on/off state via mode)
	if mcuCfg, err := d.client.QueryMCUConfig(); err == nil && mcuCfg != nil {
		d.client.runSerializedTelemetry(func(_ *MCUConfig, alreadyApplied bool) {
			if !alreadyApplied {
				d.applyMCUConfig(mcuCfg)
			}
		})
		logger.Debug("Xiongmai Driver", "💡 Synced MCU Config: LightOn=%v, Bright=%d%%, Lux=%d, Dist=%dm, Delay=%ds, Lowlight=%d%% (%s)",
			mcuCfg.IsLightOn(), mcuCfg.Highlight, mcuCfg.TwilightLux, mcuCfg.Distance, mcuCfg.HighlightDelaySec, mcuCfg.Lowlight, mcuCfg.LowlightDuration)
	}
}

func (d *Driver) applySofiaDeviceStatus(sofiaSt *SofiaDeviceStatus) {
	if d.eventBus == nil || sofiaSt == nil {
		return
	}
	d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
		if sofiaSt.FirmwareVer != "" {
			st.FirmwareVer = sofiaSt.FirmwareVer
		}
		if sofiaSt.SDTotalMB > 0 {
			st.SDTotalMB = sofiaSt.SDTotalMB
			st.SDFreeMB = sofiaSt.SDFreeMB
		}
		if sofiaSt.SDStatus != "" {
			st.SDStatus = sofiaSt.SDStatus
		}
		if sofiaSt.SpeakerVolume >= 0 {
			st.SpeakerVolume = sofiaSt.SpeakerVolume
		}
		if sofiaSt.OSDPosition >= 0 {
			st.OSDPosition = sofiaSt.OSDPosition
		}
		if sofiaSt.DetectMode != "" {
			st.DetectMode = sofiaSt.DetectMode
		}
		if sofiaSt.RecordMode > 0 {
			st.RecordMode = sofiaSt.RecordMode
		}
		if sofiaSt.SirenKnown {
			if sofiaSt.SirenEnabled {
				st.SirenEnabled = 1
			} else {
				st.SirenEnabled = 0
			}
		}
	})
}

func (d *Driver) wakeSofiaLoop() {
	if d.sofiaWakeCh == nil {
		return
	}
	select {
	case d.sofiaWakeCh <- struct{}{}:
	default:
	}
}

func (d *Driver) superviseSofiaLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	reconnectBackoff := 5 * time.Second
	firstAttempt := true

	for {
		if !d.client.IsLoggedIn() {
			if d.talk != nil {
				d.talk.Reset()
			}
			if !firstAttempt {
				timer := time.NewTimer(reconnectBackoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-d.closeChan:
					timer.Stop()
					return
				case <-d.sofiaWakeCh:
					timer.Stop()
				case <-timer.C:
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
				if d.talk != nil {
					d.talk.Reset()
				}

				_ = d.client.EnsureH264Encoding()
				d.mu.Lock()
				d.syncInitialStateLocked()
				forceRTSP := d.forceRTSPIngest
				d.mu.Unlock()

				if !forceRTSP {
					d.ingestMu.Lock()
					hasIngest := d.ingest != nil
					d.ingestMu.Unlock()
					if hasIngest {
						logger.Info("Xiongmai Driver", "⏹️ Closing fallback RTSP ingest now that Sofia control is restored")
						d.closeIngest()
					}
				}

				if !forceRTSP && d.sofiaStream != nil {
					if d.sofiaStream.IsRunning() {
						_ = d.sofiaStream.Stop()
					}
					if d.rtspServer != nil && d.rtspServer.ActiveClients() > 0 {
						_ = d.startSofiaStream(ctx)
					}
				}
			} else {
				logger.Debug("Xiongmai Driver", "ℹ️ Sofia reconnect attempt failed: %v (retrying in %v)", err, reconnectBackoff)
				if reconnectBackoff < 60*time.Second {
					reconnectBackoff = min(reconnectBackoff*2, 60*time.Second)
				}
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-d.closeChan:
			return
		case <-d.sofiaWakeCh:
			if !d.client.IsLoggedIn() {
				reconnectBackoff = 5 * time.Second
				firstAttempt = true
			}
		case <-ticker.C:
			if d.client.IsLoggedIn() {
				if err := d.client.SendKeepAlive(); err != nil {
					logger.Warn("Xiongmai Driver", "⚠️ Sofia heartbeat failed: %v (connection lost, reconnecting in background...)", err)
					if d.talk != nil {
						d.talk.Reset()
					}
					_ = d.client.Close()
					if d.sofiaStream != nil {
						_ = d.sofiaStream.Stop()
					}
					reconnectBackoff = 5 * time.Second
				}
			} else {
				if d.talk != nil {
					d.talk.Reset()
				}
				if d.sofiaStream != nil && d.sofiaStream.IsRunning() {
					_ = d.sofiaStream.Stop()
				}
				reconnectBackoff = 5 * time.Second
				firstAttempt = true
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
			return d.SetLampMode(mode)
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
			return d.SetLowlightTime(timeVal)
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
		SetSiren:             d.SetSiren,
		SetResolution:        d.SetResolution,
		ReconnectSession:     d.ReconnectSession,
		CameraInfo:           true,
		SetTimestampPosition: d.SetTimestampPosition,
		SetSpeakerVolume:     d.SetSpeakerVolume,
		SetDetectMode:        d.SetDetectMode,
		SetRecordMode:        d.SetRecordMode,
		RebootCamera:         d.Reboot,
	}
}

// ReconnectSession triggers an immediate reconnect of the Xiongmai client and streams.
func (d *Driver) ReconnectSession() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		logger.Info("Xiongmai Driver", "🔄 Manual session reconnect requested via MQTT / API")
		if d.sofiaStream != nil {
			_ = d.sofiaStream.Stop()
		}
		if d.talk != nil {
			d.talk.Reset()
		}
		_ = d.client.Close()
		d.wakeSofiaLoop()
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

// SetLampMode switches the lamp operating mode ("on"/"dauerlicht", "off"/"auto"/"sensor", or "aus").
func (d *Driver) SetLampMode(mode string) error {
	logger.Trace("Xiongmai Driver", "SetLampMode: mode=%q", mode)
	if d.client == nil {
		return errors.New("camera driver not connected")
	}
	_, expectedMCUMode, err := BuildSetLampModeCommand(mode)
	if err != nil {
		return err
	}
	err = d.client.SetLightMode(mode)
	if err == nil && d.eventBus != nil {
		d.client.runSerializedTelemetry(func(cfg *MCUConfig, alreadyApplied bool) {
			if cfg != nil {
				if !alreadyApplied {
					d.applyMCUConfig(cfg)
				}
				return
			}
			d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
				st.LampMode = MCUModeToLampMode(expectedMCUMode)
				if expectedMCUMode == MCUModeOff {
					st.PIRActive = false
				}
			})
		})
	}
	return err
}

// SetLamp turns the main light on or off (sensor auto).
func (d *Driver) SetLamp(on bool) error {
	logger.Trace("Xiongmai Driver", "SetLamp: on=%v", on)
	if on {
		return d.SetLampMode("on")
	}
	return d.SetLampMode("off")
}

// SetDim sets the main light dimming level (10-100%).
func (d *Driver) SetDim(val int) error {
	logger.Trace("Xiongmai Driver", "SetDim: val=%d%%", val)
	err := d.client.SetHighlight(val)
	if err == nil && d.eventBus != nil {
		d.client.runSerializedTelemetry(func(_ *MCUConfig, _ bool) {
			d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
				st.Highlight = val
			})
		})
	}
	return err
}

// SetTwilight sets the twilight sensor threshold (2-1000 Lux).
func (d *Driver) SetTwilight(val int) error {
	logger.Trace("Xiongmai Driver", "SetTwilight: lux=%d", val)
	err := d.client.SetLux(val)
	if err == nil && d.eventBus != nil {
		d.client.runSerializedTelemetry(func(_ *MCUConfig, _ bool) {
			d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
				st.Lux = val
			})
		})
	}
	return err
}

// SetDistance sets the PIR detection distance (1-10 meters).
func (d *Driver) SetDistance(val int) error {
	logger.Trace("Xiongmai Driver", "SetDistance: distance=%dm", val)
	err := d.client.SetDistance(val)
	if err == nil && d.eventBus != nil {
		sens := distanceToPIRSensitivity(val)
		if sens == 0 {
			sens = 33
		}
		d.client.runSerializedTelemetry(func(_ *MCUConfig, _ bool) {
			d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
				st.PIRSensitivity = sens
			})
		})
	}
	return err
}

// SetDuration sets the main light on-time in seconds.
func (d *Driver) SetDuration(val int) error {
	logger.Trace("Xiongmai Driver", "SetDuration: seconds=%ds", val)
	err := d.client.SetHighlightDelay(val)
	if err == nil && d.eventBus != nil {
		d.client.runSerializedTelemetry(func(_ *MCUConfig, _ bool) {
			d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
				st.HighlightTime = val
			})
		})
	}
	return err
}

// SetNightlight sets the nightlight brightness (0-50%).
func (d *Driver) SetNightlight(val int) error {
	logger.Trace("Xiongmai Driver", "SetNightlight: val=%d%%", val)
	err := d.client.SetLowlight(val)
	if err == nil && d.eventBus != nil {
		d.client.runSerializedTelemetry(func(_ *MCUConfig, _ bool) {
			d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
				st.Lowlight = val
			})
		})
	}
	return err
}

// SetNightlightDuration sets the nightlight duration mode ("all_night", "4h", "off").
func (d *Driver) SetNightlightDuration(val string) error {
	logger.Trace("Xiongmai Driver", "SetNightlightDuration: mode=%q", val)
	var durInt int
	valLower := strings.ToLower(strings.TrimSpace(val))
	switch {
	case valLower == "all_night" || valLower == "ganze nacht" || valLower == "24h" || valLower == "all" || valLower == "12h":
		durInt = 20
	case valLower == "off" || valLower == "aus" || valLower == "0" || valLower == "0h":
		durInt = 0
	case strings.HasSuffix(valLower, "h"):
		hStr := strings.TrimSuffix(valLower, "h")
		if hours, err := strconv.Atoi(hStr); err == nil {
			durInt = hours * 2
		}
	}

	err := d.client.SetLowlightDuration(durInt)
	if err == nil && d.eventBus != nil {
		d.client.runSerializedTelemetry(func(_ *MCUConfig, _ bool) {
			d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
				if valLower == "off" || valLower == "aus" || valLower == "0" || valLower == "0h" {
					st.LowlightTime = 0
				} else if valLower == "all_night" || valLower == "ganze nacht" || valLower == "24h" || valLower == "all" || valLower == "12h" {
					st.LowlightTime = 12 * 60
				} else if dur, err := strconv.Atoi(strings.TrimSuffix(valLower, "h")); err == nil {
					st.LowlightTime = dur * 60
				}
			})
		})
	}
	return err
}

// SetLowlightTime sets the nightlight duration from hours (0-12), minutes (>12), or seconds (>720).
func (d *Driver) SetLowlightTime(timeVal int) error {
	hours := timeVal
	if timeVal > 720 {
		hours = (timeVal + 1800) / 3600
	} else if timeVal > 12 {
		hours = (timeVal + 30) / 60
	}
	if hours <= 0 {
		return d.SetNightlightDuration("off")
	}
	if hours >= 12 {
		return d.SetNightlightDuration("all_night")
	}
	return d.SetNightlightDuration(fmt.Sprintf("%dh", hours))
}

// SetSpeakerVolume sets the camera speaker output volume (0-100%).
func (d *Driver) SetSpeakerVolume(percent int) error {
	if d.client == nil {
		return errors.New("camera driver not connected")
	}
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	err := d.client.SetSpeakerVolume(percent)
	if err == nil && d.eventBus != nil {
		d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
			st.SpeakerVolume = percent
		})
	}
	return err
}

// SetTimestampPosition switches the timestamp overlay off (0) or moves it to a corner (1-4).
func (d *Driver) SetTimestampPosition(position int) error {
	if d.client == nil {
		return errors.New("camera driver not connected")
	}
	err := d.client.SetTimestampPosition(position)
	if err == nil && d.eventBus != nil {
		d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
			st.OSDPosition = position
		})
	}
	return err
}

// SetDetectMode enables ("ai") or disables ("off") camera-side motion detection.
func (d *Driver) SetDetectMode(mode string) error {
	if d.client == nil {
		return errors.New("camera driver not connected")
	}
	err := d.client.SetDetectMode(mode)
	if err == nil && d.eventBus != nil {
		detectMode := "off"
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case "ai", "on", "true", "1":
			detectMode = "ai"
		}
		d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
			st.DetectMode = detectMode
		})
	}
	return err
}

// SetRecordMode configures the SD card recording mode (4 = off, 2 or 3 = motion).
func (d *Driver) SetRecordMode(mode int) error {
	if d.client == nil {
		return errors.New("camera driver not connected")
	}
	err := d.client.SetRecordMode(mode)
	if err == nil && d.eventBus != nil {
		d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
			st.RecordMode = mode
		})
	}
	return err
}

// SetSiren enables or disables the camera's motion alarm voice/buzzer output.
func (d *Driver) SetSiren(on bool) error {
	if d.client == nil {
		return errors.New("camera driver not connected")
	}
	err := d.client.SetSiren(on)
	if err == nil && d.eventBus != nil {
		sirenVal := 0
		if on {
			sirenVal = 1
		}
		d.eventBus.UpdateStatusFunc(func(st *events.DeviceStatus) {
			st.SirenEnabled = sirenVal
		})
	}
	return err
}

// Reboot sends a restart command to the camera.
func (d *Driver) Reboot() error {
	if d.client == nil {
		return errors.New("camera driver not connected")
	}
	return d.client.Reboot()
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

	prevRes := d.resolution
	prevStreamType := sofiaStreamType
	if d.sofiaStream != nil {
		prevStreamType = d.sofiaStream.StreamType()
	}
	if prevRes == resNormalized && prevStreamType == sofiaStreamType {
		return nil
	}

	d.resolution = resNormalized
	if d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.Resolution = resNormalized
		d.eventBus.UpdateStatus(st)
	}

	// 1. If SofiaStream is configured:
	if d.sofiaStream != nil {
		prevStreamType := d.sofiaStream.StreamType()
		d.sofiaStream.SetStreamType(sofiaStreamType)
		if prevStreamType != sofiaStreamType && d.rtspServer != nil {
			if pr, ok := d.rtspServer.(interface{ ResetVideoParameters() }); ok {
				pr.ResetVideoParameters()
			}
		}
		if d.client != nil {
			if fps := d.client.GetVideoFPS(sofiaStreamType); fps > 0 {
				d.sofiaStream.SetFPS(fps)
			}
		}
	}

	streamCtx := d.ctx
	if streamCtx == nil {
		streamCtx = context.Background()
	}

	// 2. Forced RTSP Ingest mode
	if d.forceRTSPIngest {
		d.ingestMu.Lock()
		hasIngest := d.ingest != nil
		d.ingestMu.Unlock()
		if hasIngest {
			effectivePwd := d.client.GetEffectivePassword()
			if effectivePwd == "" && d.password != "" {
				effectivePwd = d.password
			}
			d.closeIngest()
			newIngest := NewRTSPIngest(d.cameraIP, RTSPPort, d.user, effectivePwd, streamSubtype, d.rtspServer, d.password)
			d.ingestMu.Lock()
			d.ingest = newIngest
			d.ingestMu.Unlock()
			return newIngest.Start(streamCtx)
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
			if err := d.sofiaStream.Start(streamCtx); err != nil {
				logger.Warn("Xiongmai Driver", "⚠️ Failed to restart Sofia stream with %s: %v", resNormalized, err)
				return err
			}
			logger.Info("Xiongmai Driver", "🎥 Native Sofia video stream active with resolution %s (%s)", resNormalized, sofiaStreamType)
		}

		// Ensure fallback Ingest is closed and nil to prevent dual streaming
		d.closeIngest()
		return nil
	}

	// 2. Fallback mode: RTSP Ingest on port 554
	d.ingestMu.Lock()
	hasIngest := d.ingest != nil
	d.ingestMu.Unlock()
	if hasIngest {
		effectivePwd := d.client.GetEffectivePassword()
		if effectivePwd == "" && d.password != "" {
			effectivePwd = d.password
		}
		d.closeIngest()
		newIngest := NewRTSPIngest(d.cameraIP, RTSPPort, d.user, effectivePwd, streamSubtype, d.rtspServer, d.password)
		d.ingestMu.Lock()
		d.ingest = newIngest
		d.ingestMu.Unlock()
		return newIngest.Start(streamCtx)
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
		_ = d.sofiaStream.Close()
	}
	if d.talk != nil {
		_ = d.talk.StopTalk()
		d.talk.Reset()
	}
	d.closeIngest()
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
	d.closeIngest()
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

func (d *Driver) ensureFallbackIngest(ctx context.Context) {
	d.ingestMu.Lock()
	if d.ingest != nil {
		d.ingestMu.Unlock()
		return
	}
	d.ingestMu.Unlock()

	d.mu.Lock()
	if !d.running || d.forceRTSPIngest {
		d.mu.Unlock()
		return
	}
	res := d.resolution
	user := d.user
	pwd := d.password
	camIP := d.cameraIP
	rtspSrv := d.rtspServer
	d.mu.Unlock()

	var effectivePwd string
	if d.client != nil {
		effectivePwd = d.client.GetEffectivePassword()
	}
	if effectivePwd == "" && pwd != "" {
		effectivePwd = pwd
	}
	streamSubtype := 0
	if res == "360p" {
		streamSubtype = 1
	}

	d.ingestMu.Lock()
	if d.ingest != nil {
		d.ingestMu.Unlock()
		return
	}
	logger.Warn("Xiongmai Driver", "⚠️ Native Sofia stream repeatedly failed; activating fallback RTSP ingest on port %d (User=%s, PwdMasked=%s)",
		RTSPPort, user, MaskPassword(effectivePwd))
	d.ingest = NewRTSPIngest(camIP, RTSPPort, user, effectivePwd, streamSubtype, rtspSrv, pwd)
	ingestRef := d.ingest
	d.ingestMu.Unlock()

	if err := ingestRef.Start(ctx); err != nil {
		logger.Warn("Xiongmai Driver", "⚠️ Failed to start fallback RTSP Ingest: %v", err)
	}
}

func (d *Driver) hasFallbackIngest() bool {
	d.ingestMu.Lock()
	has := d.ingest != nil
	d.ingestMu.Unlock()
	return has
}

func (d *Driver) wakeOnDemandLoop() {
	if d.streamWakeCh == nil {
		return
	}
	select {
	case d.streamWakeCh <- struct{}{}:
	default:
	}
}

// superviseOnDemandLoop monitors active RTSP clients and starts/stops the Sofia video stream on demand.
func (d *Driver) superviseOnDemandLoop(ctx context.Context) {
	d.mu.Lock()
	tickInterval := d.onDemandTickInterval
	initialRestartBackoff := d.initialStreamBackoff
	outageGrace := d.controlOutageGrace
	probeInterval := d.sofiaProbeInterval
	d.mu.Unlock()
	if tickInterval <= 0 {
		tickInterval = 1 * time.Second
	}
	if initialRestartBackoff <= 0 {
		initialRestartBackoff = 2 * time.Second
	}
	if outageGrace <= 0 {
		outageGrace = defaultFallbackControlOutageGrace
	}
	if probeInterval <= 0 {
		probeInterval = defaultFallbackSofiaProbeInterval
	}

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	const maxRestartBackoff = 15 * time.Second
	var (
		nextRestartAt       time.Time
		nextSofiaProbeAt    time.Time
		controlDownSince    time.Time
		restartBackoff      time.Duration
		consecutiveFailures int
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.closeChan:
			return
		case <-d.streamWakeCh:
		case <-ticker.C:
		}

		d.mu.Lock()
		forceRTSP := d.forceRTSPIngest
		d.mu.Unlock()
		if forceRTSP || d.rtspServer == nil {
			continue
		}

		now := time.Now()
		clients := d.rtspServer.ActiveClients()
		if clients > 0 {
			d.mu.Lock()
			d.lastClientSeen = now
			d.idleSinceLastClient = false
			d.mu.Unlock()

			sRunning := d.sofiaStream != nil && d.sofiaStream.IsRunning()
			loggedIn := d.client != nil && d.client.IsLoggedIn()
			hasFallback := d.hasFallbackIngest()

			switch {
			case sRunning:
				consecutiveFailures = 0
				restartBackoff = 0
				nextRestartAt = time.Time{}
				nextSofiaProbeAt = time.Time{}
				controlDownSince = time.Time{}
				if hasFallback {
					d.closeIngest()
				}

			case d.sofiaStream != nil && loggedIn:
				controlDownSince = time.Time{}
				// While fallback RTSP ingest is actively serving clients, avoid hammering OPMonitor
				// on the control socket every few seconds (a control timeout would tear down MCU/alarm
				// supervision); only probe native Sofia recovery after probeInterval.
				if hasFallback && !nextSofiaProbeAt.IsZero() && now.Before(nextSofiaProbeAt) {
					continue
				}
				if !nextRestartAt.IsZero() && now.Before(nextRestartAt) {
					continue
				}
				if err := d.startSofiaStream(ctx); err != nil {
					consecutiveFailures++
					if consecutiveFailures >= maxSofiaStreamFailuresBeforeFallback {
						d.ensureFallbackIngest(ctx)
						nextSofiaProbeAt = now.Add(probeInterval)
					}
					if restartBackoff == 0 {
						restartBackoff = initialRestartBackoff
					} else {
						restartBackoff = min(restartBackoff*2, maxRestartBackoff)
					}
					nextRestartAt = now.Add(restartBackoff)
				} else {
					consecutiveFailures = 0
					restartBackoff = 0
					nextRestartAt = time.Time{}
					nextSofiaProbeAt = time.Time{}
				}

			default:
				// Sofia control port 34567 is down while viewers are waiting: activate port 554
				// RTSP fallback once the control outage exceeds outageGrace.
				restartBackoff = 0
				nextRestartAt = time.Time{}
				if controlDownSince.IsZero() {
					controlDownSince = now
				} else if !hasFallback && now.Sub(controlDownSince) >= outageGrace {
					d.ensureFallbackIngest(ctx)
				}
			}
		} else {
			consecutiveFailures = 0
			restartBackoff = 0
			nextRestartAt = time.Time{}
			nextSofiaProbeAt = time.Time{}
			controlDownSince = time.Time{}

			d.mu.Lock()
			d.idleSinceLastClient = true
			lastSeen := d.lastClientSeen
			d.mu.Unlock()

			if !lastSeen.IsZero() && now.Sub(lastSeen) >= defaultIdleGracePeriod {
				if d.sofiaStream != nil && d.sofiaStream.IsRunning() {
					d.stopSofiaStream()
				}
				d.closeIngest()
			}
		}
	}
}

// RequestKeyframe triggers immediate startup of the on-demand stream when an RTSP client connects.
// Unlike WebRTC PLI on the L 625, Xiongmai Sofia has no out-of-band command to force an extra
// hardware I-frame; dropping P-frames on an active stream would freeze video for existing viewers.
func (d *Driver) RequestKeyframe() {
	d.mu.Lock()
	wasIdle := d.idleSinceLastClient
	d.idleSinceLastClient = false
	d.lastClientSeen = time.Now()
	forceRTSP := d.forceRTSPIngest
	d.mu.Unlock()

	if forceRTSP || d.sofiaStream == nil {
		return
	}
	if d.sofiaStream.IsRunning() {
		// Only gate P-frames when the stream was sitting in its 15s idle window with zero viewers
		// and the last keyframe is stale (not right after an automatic H.264->H.265 codec switch).
		if wasIdle && (d.rtspServer == nil || d.rtspServer.ActiveClients() <= 1) {
			d.sofiaStream.AwaitNextKeyframeIfStale(2 * time.Second)
		}
		return
	}
	d.wakeOnDemandLoop()
}

// PrepareDescribe warms the on-demand Sofia stream before RTSP DESCRIBE returns its SDP.
// On cold boot or right after a resolution change (when out-of-band video SDP parameters
// are not yet populated), it synchronously starts SofiaStream and waits briefly for the
// first I-frame so any H.264->H.265 codec switch completes before the client receives the SDP.
// This ensures desktop players that do not auto-reconnect after EOF (such as VLC) start
// playing on the very first attempt.
func (d *Driver) PrepareDescribe() {
	d.mu.Lock()
	running := d.running
	forceRTSP := d.forceRTSPIngest
	streamCtx := d.ctx
	warmupTimeout := d.describeWarmupTimeout
	if running && !forceRTSP {
		d.lastClientSeen = time.Now()
	}
	d.mu.Unlock()

	if !running || forceRTSP || d.sofiaStream == nil {
		return
	}
	if warmupTimeout <= 0 {
		warmupTimeout = defaultDescribeWarmupTimeout
	}
	if streamCtx == nil {
		streamCtx = context.Background()
	}

	hasParams := false
	if hp, ok := d.rtspServer.(interface{ HasVideoParameters() bool }); ok {
		hasParams = hp.HasVideoParameters()
	}

	if hasParams {
		if !d.sofiaStream.IsRunning() {
			d.wakeOnDemandLoop()
		}
		return
	}

	if d.client == nil || !d.client.IsLoggedIn() {
		return
	}

	if !d.sofiaStream.IsRunning() {
		if err := d.startSofiaStream(streamCtx); err != nil {
			return
		}
	}
	if d.sofiaStream.WaitFirstKeyframe(streamCtx, warmupTimeout) {
		d.mu.Lock()
		d.idleSinceLastClient = false
		d.lastClientSeen = time.Now()
		d.mu.Unlock()
	}
}
