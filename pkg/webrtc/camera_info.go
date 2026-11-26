package webrtc

import (
	"context"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

const cameraSettingsInterval = 60 * time.Second

// runCameraInfoLoop asks the camera SoC for its device info and its detection
// and recording settings right after the session is up. The detection and
// recording settings are then read once a minute.
//
// get_device_info (Wi-Fi signal, SD card usage, firmware version and update
// offer) is NOT repeated by default: the L 625 freezes after about 328
// get_device_info calls since its last restart and is then gone for about
// four minutes. Measured on camera firmware 0.1.2.31 and 0.1.4.6: one call a
// minute freezes it after 5 h 28 min, one every 10 s after 54 min 40 s; the
// other queries and the MCU polling do not count. SetDeviceInfoInterval
// allows a slow repetition for setups that want fresher values.
func (b *Bridge) runCameraInfoLoop(ctx context.Context) {
	b.requestCameraInfo()
	b.requestDetectionSettings()

	settings := time.NewTicker(cameraSettingsInterval)
	defer settings.Stop()

	// A nil channel never fires: without an interval the device info stays
	// at the one query above.
	var deviceInfo <-chan time.Time
	if every := b.deviceInfoInterval(); every > 0 {
		t := time.NewTicker(every)
		defer t.Stop()
		deviceInfo = t.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-settings.C:
			b.requestDetectionSettings()
		case <-deviceInfo:
			b.requestCameraInfo()
		}
	}
}

// deviceInfoInterval returns how often get_device_info is repeated; 0 means
// once per session.
func (b *Bridge) deviceInfoInterval() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.deviceInfoEvery < 0 {
		return 0
	}
	return b.deviceInfoEvery
}

func (b *Bridge) requestCameraInfo() {
	if err := b.sendJSONCmd("get_device_info", nil); err != nil {
		logger.Debug("DataChannel", "get_device_info could not be sent: %v", err)
	}
}

// onDeviceInfo stores the content of a get_device_info reply in the device status.
// The reply also names the Wi-Fi network; it must never be logged.
func (b *Bridge) onDeviceInfo(info map[string]interface{}) {
	b.eventBus.UpdateStatusFunc(func(status *events.DeviceStatus) {
		applyDeviceInfo(status, info)
	})
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
	applyDeviceSettings(st, info)
}

// jsonInt reads a JSON number (decoded as float64) as int.
func jsonInt(v interface{}) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}
