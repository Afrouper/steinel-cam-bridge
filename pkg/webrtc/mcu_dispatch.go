package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mcu"

	"github.com/google/uuid"
)

// SetResolution sends a DataChannel command to change video quality dynamically.
func (b *Bridge) SetResolution(resolution string) error {
	b.mu.Lock()
	b.resolution = resolution
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

// SetLampState turns the lamp on, off or auto.
func (b *Bridge) SetLampState(mode string) error {
	switch strings.ToLower(mode) {
	case "on", "1":
		return b.SendMCUCommand(mcu.CmdLightOn)
	case "off", "0":
		return b.SendMCUCommand(mcu.CmdLightOff)
	case "auto", "2":
		return b.SendMCUCommand(mcu.CmdLightAuto)
	default:
		return fmt.Errorf("unknown lamp mode: %s (use on, off, auto)", mode)
	}
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

			// Check for get_device_info
			if strVal, ok := root["resp"].(string); ok && strVal == "get_device_info" {
				if infoMap, ok := root["info"].(map[string]interface{}); ok {
					fw, _ := infoMap["FW_version"].(string)
					status := b.eventBus.GetStatus()
					status.FirmwareVer = fw
					b.eventBus.UpdateStatus(status)
				}
				return
			}

			// Check and log for any Motion, PIR, Alarm or Event notifications from camera
			str := string(data)
			lowerStr := strings.ToLower(str)
			if strings.Contains(lowerStr, "alarm") ||
				strings.Contains(lowerStr, "motion") ||
				strings.Contains(lowerStr, "pir") ||
				strings.Contains(lowerStr, "event") ||
				strings.Contains(lowerStr, "doorbell") {
				logger.Info("DataChannel", "🚨 Motion / Event notification received from camera: %s", str)
				b.eventBus.SetMotion(true)
			} else {
				logger.Debug("DataChannel", "📩 Received JSON message: %s", str)
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

func (b *Bridge) onMCUStatus(cfg *mcu.ConfigInfo) {
	status := b.eventBus.GetStatus()
	status.LampMode = cfg.Mode
	status.Lux = cfg.Lux
	status.PIRActive = cfg.PIRActive
	status.PIRSensitivity = cfg.PIRSensitivity
	status.Highlight = cfg.Highlight
	status.HighlightTime = cfg.HighlightTime
	status.Lowlight = cfg.Lowlight
	status.LowlightTime = cfg.LowlightTime
	status.ColorTemp = cfg.ColorTemp
	status.Resolution = b.resolution
	b.eventBus.UpdateStatus(status)

	// Motion Detection Handling (Hardware PIR + Optical Camera Detection)
	if cfg.MotionDetected || cfg.PhotosensitiveDetection {
		motionType := "PIR Sensor"
		if cfg.MotionDetected && cfg.PhotosensitiveDetection {
			motionType = "PIR + Kamera-Bilderkennung"
		} else if cfg.PhotosensitiveDetection {
			motionType = "Kamera-Bilderkennung"
		}
		logger.Info("MCU", "🚨 Bewegung erkannt (%s)! (Lux: %d, Mode: %d)", motionType, cfg.Lux, cfg.Mode)
		b.eventBus.SetMotion(true)

		b.mu.Lock()
		if b.motionResetTimer != nil {
			b.motionResetTimer.Stop()
		}
		b.motionResetTimer = time.AfterFunc(10*time.Second, func() {
			logger.Info("MCU", "⚪ Motion cleared (10s timeout)")
			b.eventBus.SetMotion(false)
		})
		b.mu.Unlock()
	}
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
