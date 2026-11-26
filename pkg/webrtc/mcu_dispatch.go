package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mcu"

	"github.com/google/uuid"
)

// SetResolution sends a DataChannel command to change video quality dynamically.
func (b *Bridge) SetResolution(resolution string) error {
	b.mu.Lock()
	b.resolution = resolution
	b.resGuard.Requested(resolution, time.Now())
	b.mu.Unlock()

	return b.sendResolutionRequest(resolution)
}

// sendResolutionRequest sends the quality request without touching the requested state.
func (b *Bridge) sendResolutionRequest(resolution string) error {
	b.mu.Lock()
	dc := b.dc
	b.mu.Unlock()

	if dc == nil {
		return fmt.Errorf("data channel not available")
	}

	cmd := map[string]interface{}{
		"from":   "Android",
		"cmd":    "set_video_setting",
		"msgid":  uuid.New().String(),
		"action": "all",
		"info": map[string]interface{}{
			"quality": map[string]interface{}{
				"sub1": map[string]interface{}{
					"resolution": resolution,
				},
			},
		},
	}
	data, _ := json.Marshal(cmd)
	logger.Debug("DataChannel", "🎦 Requesting camera resolution: %s", resolution)
	logger.Trace("DataChannel", "SetResolution payload: %s", string(data))
	return dc.Send(data)
}

// SendCommand sends an arbitrary JSON command over the DataChannel.
func (b *Bridge) SendCommand(cmdName string, info map[string]interface{}) error {
	b.mu.Lock()
	dc := b.dc
	b.mu.Unlock()

	if dc == nil {
		return fmt.Errorf("data channel not available")
	}

	cmd := map[string]interface{}{
		"from":  "Android",
		"cmd":   cmdName,
		"msgid": uuid.New().String(),
		"info":  info,
	}
	data, _ := json.Marshal(cmd)
	logger.Debug("DataChannel", "📤 Sending command '%s'", cmdName)
	logger.Trace("DataChannel", "Command '%s' payload: %s", cmdName, string(data))
	return dc.Send(data)
}

// SendMCUCommand sends a raw Hex command to the MCU via tran_ctl.
func (b *Bridge) SendMCUCommand(cmdHex string) error {
	b64, err := mcu.BuildCommand(cmdHex)
	if err != nil {
		return err
	}

	b.mu.Lock()
	dc := b.dc
	b.mu.Unlock()

	if dc == nil {
		return fmt.Errorf("data channel not available")
	}

	cmd := map[string]interface{}{
		"from":  "Android",
		"cmd":   "tran_ctl",
		"msgid": uuid.New().String(),
		"info": map[string]interface{}{
			"data": b64,
		},
	}
	data, _ := json.Marshal(cmd)
	return dc.SendText(string(data))
}

// SetLampState turns the lamp on, off (sensor auto) or aus (completely disabled).
func (b *Bridge) SetLampState(mode string) error {
	switch strings.ToLower(mode) {
	case "on", "1", "dauerlicht":
		return b.SendMCUCommand(mcu.CmdLightOn)
	case "off", "auto", "sensor", "2":
		// Switching light "off" returns to Sensor Auto mode so the motion sensor remains active
		return b.SendMCUCommand(mcu.CmdLightAuto)
	case "aus", "0":
		// Explicit "Aus" completely disables lamp and sensor
		return b.SendMCUCommand(mcu.CmdLightOff)
	default:
		return fmt.Errorf("unknown lamp mode: %s (use on, off, auto, aus)", mode)
	}
}

// SetSystemDateAndTime synchronizes the timezone and clock on the L 625 camera via WebRTC DataChannel.
func (b *Bridge) SetSystemDateAndTime(t time.Time, tz string) error {
	if tz == "" {
		tz = time.Now().Location().String()
		if tz == "Local" || tz == "" {
			tz = "Europe/Berlin"
		}
	}
	logger.Info("DataChannel", "⏰ Syncing time setting to camera (TZ: %s, Time: %s)", tz, t.Format(time.RFC3339))
	return b.sendJSONCmd("set_time_setting", map[string]interface{}{
		"action":   "timezone",
		"timezone": tz,
	})
}

// Reboot triggers a controlled reboot of the L 625 camera via WebRTC DataChannel.
func (b *Bridge) Reboot() error {
	logger.Info("DataChannel", "🔄 Requesting controlled camera reboot via DataChannel")
	return b.sendJSONCmd("reboot", map[string]interface{}{
		"do_update_check": false,
	})
}

func (b *Bridge) sendJSONCmd(cmdName string, info map[string]interface{}) error {
	b.mu.Lock()
	dc := b.dc
	b.mu.Unlock()

	if dc == nil {
		return fmt.Errorf("data channel not available")
	}

	cmd := map[string]interface{}{
		"from":  "Android",
		"cmd":   cmdName,
		"msgid": uuid.New().String(),
	}
	if info != nil {
		infoCopy := make(map[string]interface{})
		for k, v := range info {
			if k == "action" || k == "_action" {
				if actStr, ok := v.(string); ok {
					cmd["action"] = actStr
				}
			} else {
				infoCopy[k] = v
			}
		}
		cmd["info"] = infoCopy
	}
	data, _ := json.Marshal(cmd)
	logger.Debug("DataChannel", "📤 Sending JSON command '%s'", cmdName)
	logger.Trace("DataChannel", "📤 Sending JSON command '%s': %s", cmdName, string(data))
	return dc.Send(data)
}

func (b *Bridge) handleDataChannelMessage(data []byte) {
	if len(data) == 0 {
		return
	}

	// 1. Check if payload is a JSON control message
	if data[0] == '{' {
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err == nil {
			b.feedDebugTap(data)

			// Check for SD Card JSON responses (get_event_list, get_snapshot, get_event_video)
			b.mu.Lock()
			sdm := b.sdcardManager
			b.mu.Unlock()
			if sdm != nil && sdm.HandleJSONMessage(root) {
				return
			}

			// Check for MCU base64 status payloads (tran_report, tran_ctl, etc.)
			if infoMap, ok := root["info"].(map[string]interface{}); ok {
				if b64Data, ok := infoMap["data"].(string); ok && b64Data != "" {
					cfg, err := mcu.ParseBase64Data(b64Data)
					if err != nil {
						logger.Warn("MCU", "⚠️ Failed to parse MCU Base64 '%s': %v", b64Data, err)
					} else if cfg != nil {
						logger.Trace("MCU", "Received MCU Base64 '%s': %+v", b64Data, cfg)
						b.onMCUStatus(cfg)
					}
					return
				}
			}

			// Replies that carry camera settings
			if strVal, ok := root["resp"].(string); ok {
				infoMap, hasInfo := root["info"].(map[string]interface{})
				switch strVal {
				case "get_device_info":
					if hasInfo {
						b.onDeviceInfo(infoMap)
					}
					return
				case "get_detect_data":
					if hasInfo {
						b.onDetectData(infoMap)
					}
					return
				case "get_record_setting":
					if hasInfo {
						b.onRecordSetting(infoMap)
					}
					return
				}
			}

			// Replies to our own commands (e.g. "alarm_voice_ctl" after switching the
			// siren) are acknowledgements, not camera events.
			if respName, ok := root["resp"].(string); ok && respName != "" {
				logger.Debug("DataChannel", "📩 Command '%s' acknowledged by camera: %s", respName, string(data))
				return
			}

			// Check and log for any Motion, PIR, Alarm or Event notifications from camera.
			// Inspect parsed string fields/keys rather than raw JSON substrings to avoid false positives
			// (e.g. "pir" inside "expired" or "event" inside "prevent").
			if isMotionPushMessage(root) {
				logger.Info("DataChannel", "🚨 Motion / Event notification received from camera: %s", string(data))
				b.signalMotion()
			} else {
				logger.Debug("DataChannel", "📩 Received JSON message: %s", string(data))
			}

			return
		}
	}

	// 2. Binary chunks (e.g. video / snapshot streaming)
	b.mu.Lock()
	sdm := b.sdcardManager
	b.mu.Unlock()
	if sdm != nil {
		sdm.HandleBinaryChunk(data)
	}
}

func isMotionPushMessage(v interface{}) bool {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, child := range val {
			lk := strings.ToLower(k)
			if lk == "alarm" || lk == "motion" || lk == "pir" || lk == "doorbell" {
				return true
			}
			if isMotionPushMessage(child) {
				return true
			}
		}
	case []interface{}:
		for _, child := range val {
			if isMotionPushMessage(child) {
				return true
			}
		}
	case string:
		for _, tok := range strings.FieldsFunc(strings.ToLower(val), func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9')
		}) {
			switch tok {
			case "alarm", "motion", "pir", "event", "doorbell":
				return true
			}
		}
	}
	return false
}

func (b *Bridge) onMCUStatus(cfg *mcu.ConfigInfo) {
	res := b.reportedResolution()
	b.eventBus.UpdateStatusFunc(func(status *events.DeviceStatus) {
		status.LampMode = cfg.Mode
		status.Lux = cfg.Lux
		status.PIRActive = cfg.PIRActive
		status.PIRSensitivity = cfg.PIRSensitivity
		status.Highlight = cfg.Highlight
		status.HighlightTime = cfg.HighlightTime
		status.Lowlight = cfg.Lowlight
		status.LowlightTime = cfg.LowlightTime
		status.ColorTemp = cfg.ColorTemp
		status.PixelChangeDetection = boolToState(cfg.PixelChangeDetection)
		status.Resolution = res
	})

	// Bits 0/1 of the "tuxing" nibble are settings ("Pixel Change Detection",
	// "Image Photosensitive Detection"), not motion. Reading them as motion made
	// the bridge report endless motion as soon as the setting was switched on.
	// Motion is reported by the camera's push messages, see signalMotion.
}

// motionResetDelay is how long a motion push keeps the motion state on.
// A variable so tests can shorten it.
var motionResetDelay = 10 * time.Second

func (b *Bridge) stopMotionTimer() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.motionResetTimer != nil {
		b.motionResetTimer.Stop()
		b.motionResetTimer = nil
	}
}

// signalMotion reports motion and clears it again after motionResetDelay,
// unless another push arrives in the meantime. The camera only tells us that
// something happened, never that it is over, and nothing else resets the state.
func (b *Bridge) signalMotion() {
	b.eventBus.SetMotion(true)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.motionResetTimer != nil {
		b.motionResetTimer.Stop()
	}
	b.motionResetTimer = time.AfterFunc(motionResetDelay, func() {
		logger.Info("DataChannel", "⚪ Motion cleared (%s timeout)", motionResetDelay)
		b.eventBus.SetMotion(false)
	})
}

func (b *Bridge) runMCUPollingLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Immediate first query
	_ = b.SendMCUCommand(mcu.CmdGetLightInfo)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = b.SendMCUCommand(mcu.CmdGetLightInfo)
		}
	}
}
