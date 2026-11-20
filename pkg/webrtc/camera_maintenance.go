package webrtc

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

// Firmware update and restart of the L 625. The camera looks for updates by
// itself and reports the result in get_device_info; the STEINEL CAM app
// only offers the update button when the camera's update flag is true and the remote
// version differs. Starting the update is a plain DataChannel command:
//
//	camera firmware: {"cmd":"reboot","info":{"do_update_check":true}}
//	lamp MCU:        {"cmd":"set_ota","info":{"type":"mcu","url":"null"}}
//
// The camera downloads the file itself. Without an offer it answers "success"
// to the reboot command but does not restart (seen on firmware 0.1.2.31).

// Update targets.
const (
	UpdateTargetCamera = "camera"
	UpdateTargetMCU    = "mcu"
)

// ErrNoUpdateOffered is returned when an update is requested although the
// camera does not report one.
var ErrNoUpdateOffered = errors.New("the camera does not offer an update")

// JSON keys of the update flags in get_device_info, spelled as the firmware
// spells them.
const (
	keyUpdateFlag    = "update_avaiable"     //nolint:misspell // firmware spelling
	keyMCUUpdateFlag = "mcu_update_avaiable" //nolint:misspell // firmware spelling
)

// applyUpdateInfo copies the update fields of a get_device_info reply.
func applyUpdateInfo(st *events.DeviceStatus, info map[string]interface{}) {
	known := false
	if v, ok := info["FW_remote_version"].(string); ok {
		st.FirmwareRemote = v
		known = true
	}
	if v, ok := info[keyUpdateFlag].(bool); ok {
		st.FirmwareUpdate = v
		known = true
	}
	if v, ok := info["MCU_version"].(string); ok && v != "" {
		st.MCUVersion = v
	}
	if v, ok := info["MCU_remote_version"].(string); ok {
		st.MCURemote = v
		known = true
	}
	if v, ok := info[keyMCUUpdateFlag].(bool); ok {
		st.MCUUpdate = v
		known = true
	}
	if known {
		st.UpdateInfoKnown = true
	}
}

// CameraUpdateOffered reports whether the camera offers new camera firmware
// (same rule as the app).
func CameraUpdateOffered(st events.DeviceStatus) bool {
	return st.FirmwareUpdate && st.FirmwareRemote != "" && st.FirmwareRemote != st.FirmwareVer
}

// MCUUpdateOffered reports whether the camera offers new lamp MCU firmware.
func MCUUpdateOffered(st events.DeviceStatus) bool {
	return st.MCUUpdate && st.MCURemote != "" && st.MCURemote != st.MCUVersion
}

// InstallUpdate starts the offered update for target ("camera" or "mcu").
// It refuses when the camera does not offer one, so a stray command can never
// restart the camera for nothing.
func (b *Bridge) InstallUpdate(target string) error {
	st := b.eventBus.GetStatus()
	switch strings.ToLower(strings.TrimSpace(target)) {
	case UpdateTargetCamera:
		if !CameraUpdateOffered(st) {
			return ErrNoUpdateOffered
		}
		logger.Info("DataChannel", "⬆️ Starting camera firmware update %s → %s (camera restarts)", st.FirmwareVer, st.FirmwareRemote)
		return b.sendJSONCmd("reboot", map[string]interface{}{"do_update_check": true})
	case UpdateTargetMCU:
		if !MCUUpdateOffered(st) {
			return ErrNoUpdateOffered
		}
		logger.Info("DataChannel", "⬆️ Starting lamp MCU update %s → %s", st.MCUVersion, st.MCURemote)
		return b.sendJSONCmd("set_ota", map[string]interface{}{"type": "mcu", "url": "null"})
	default:
		return fmt.Errorf("unknown update target %q", target)
	}
}
