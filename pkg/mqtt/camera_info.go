package mqtt

import (
	"fmt"
	"strconv"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
)

// Diagnostic sensors for values the camera reports about itself (L 625,
// get_device_info): Wi-Fi signal, SD card usage and firmware version. They
// are only announced for cameras whose driver sets Callbacks.CameraInfo.

// publishCameraInfoDiscovery announces the diagnostic sensors.
func (c *Client) publishCameraInfoDiscovery(dev *CameraDevice, publishEntity func(component, objectID string, config map[string]interface{})) {
	if !dev.Callbacks.CameraInfo {
		return
	}
	topic := func(sub string) string { return fmt.Sprintf("%s/%s", dev.baseTopic, sub) }

	publishEntity("sensor", "wifi_signal", map[string]interface{}{
		"name":                "WLAN-Signal",
		"unit_of_measurement": "%",
		"state_class":         "measurement",
		"icon":                "mdi:wifi",
		"entity_category":     "diagnostic",
		"state_topic":         topic("wifi_signal/state"),
	})
	publishEntity("sensor", "sd_free", map[string]interface{}{
		"name":                        "SD-Karte frei",
		"unit_of_measurement":         "GB",
		"device_class":                "data_size",
		"state_class":                 "measurement",
		"suggested_display_precision": 1,
		"icon":                        "mdi:micro-sd",
		"entity_category":             "diagnostic",
		"state_topic":                 topic("sd_free/state"),
	})
	publishEntity("sensor", "sd_used", map[string]interface{}{
		"name":                        "SD-Karte belegt",
		"unit_of_measurement":         "GB",
		"device_class":                "data_size",
		"state_class":                 "measurement",
		"suggested_display_precision": 2,
		"icon":                        "mdi:micro-sd",
		"entity_category":             "diagnostic",
		"state_topic":                 topic("sd_used/state"),
	})
	publishEntity("sensor", "sd_total", map[string]interface{}{
		"name":                        "SD-Karte Größe",
		"unit_of_measurement":         "GB",
		"device_class":                "data_size",
		"suggested_display_precision": 0,
		"icon":                        "mdi:micro-sd",
		"entity_category":             "diagnostic",
		"state_topic":                 topic("sd_total/state"),
	})
	publishEntity("sensor", "sd_status", map[string]interface{}{
		"name":            "SD-Karte Status",
		"icon":            "mdi:micro-sd",
		"entity_category": "diagnostic",
		"state_topic":     topic("sd_status/state"),
	})
	publishEntity("sensor", "firmware", map[string]interface{}{
		"name":            "Firmware",
		"icon":            "mdi:chip",
		"entity_category": "diagnostic",
		"state_topic":     topic("firmware/state"),
	})
}

// sdGigabytes converts the camera's SD card figures to gigabytes (10^9 bytes).
// The camera names no unit; its total of 122000 for a 128 GB card (128 * 10^9
// bytes = 122070 MiB) shows that it counts in mebibytes.
func sdGigabytes(mebibytes int) float64 {
	if mebibytes < 0 {
		mebibytes = 0
	}
	return float64(mebibytes) * 1048576 / 1e9
}

// publishCameraInfoState publishes the diagnostic values that are known.
func (c *Client) publishCameraInfoState(st events.DeviceStatus, pub func(subTopic, val string)) {
	if st.WifiSignal > 0 {
		pub("wifi_signal/state", strconv.Itoa(st.WifiSignal))
	}
	if st.SDTotalMB > 0 {
		pub("sd_free/state", strconv.FormatFloat(sdGigabytes(st.SDFreeMB), 'f', 1, 64))
		pub("sd_used/state", strconv.FormatFloat(sdGigabytes(st.SDTotalMB-st.SDFreeMB), 'f', 2, 64))
		pub("sd_total/state", strconv.FormatFloat(sdGigabytes(st.SDTotalMB), 'f', 1, 64))
	}
	if st.SDStatus != "" {
		pub("sd_status/state", st.SDStatus)
	}
	if st.FirmwareVer != "" {
		pub("firmware/state", st.FirmwareVer)
	}
}
