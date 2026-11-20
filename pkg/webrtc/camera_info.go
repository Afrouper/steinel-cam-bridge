package webrtc

import (
	"context"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

const cameraInfoInterval = 60 * time.Second

// runCameraInfoLoop asks the camera SoC for its device info right after the
// session is up and then once a minute. The reply carries the Wi-Fi signal,
// the SD card usage and the firmware version.
func (b *Bridge) runCameraInfoLoop(ctx context.Context) {
	ticker := time.NewTicker(cameraInfoInterval)
	defer ticker.Stop()

	b.requestCameraInfo()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.requestCameraInfo()
		}
	}
}

func (b *Bridge) requestCameraInfo() {
	if err := b.sendJSONCmd("get_device_info", nil); err != nil {
		logger.Debug("DataChannel", "get_device_info could not be sent: %v", err)
	}
}

// onDeviceInfo stores the content of a get_device_info reply in the device status.
// The reply also names the Wi-Fi network; it must never be logged.
func (b *Bridge) onDeviceInfo(info map[string]interface{}) {
	status := b.eventBus.GetStatus()
	applyDeviceInfo(&status, info)
	b.eventBus.UpdateStatus(status)
}

// applyDeviceInfo copies the known fields of a get_device_info reply into st.
// Missing fields leave the previous values untouched.
func applyDeviceInfo(st *events.DeviceStatus, info map[string]interface{}) {
	if fw, ok := info["FW_version"].(string); ok && fw != "" {
		st.FirmwareVer = fw
	}
	if wifi, ok := info["wifi"].(map[string]interface{}); ok {
		if rssi, ok := jsonInt(wifi["RSSI"]); ok {
			st.WifiSignal = rssi
		}
	}
	if sd, ok := info["SD"].(map[string]interface{}); ok {
		if free, ok := jsonInt(sd["volume_free"]); ok {
			st.SDFreeMB = free
		}
		if total, ok := jsonInt(sd["volume_total"]); ok {
			st.SDTotalMB = total
		}
		if s, ok := sd["status"].(string); ok {
			st.SDStatus = s
		}
	}
}

// jsonInt reads a JSON number (decoded as float64) as int.
func jsonInt(v interface{}) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}
