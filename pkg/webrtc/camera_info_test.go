package webrtc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyDeviceInfo(t *testing.T) {
	// Shape of a get_device_info reply of the L 625 (firmware 0.1.2.31), reduced
	// to the fields the bridge reads. Numbers arrive as JSON numbers.
	raw := `{"FW_version":"0.1.2.31","wifi":{"RSSI":57,"SSID":"<WLAN>"},"SD":{"status":"normal","volume_free":120480,"volume_total":122000}}`
	var info map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(raw), &info))

	st := events.DeviceStatus{}
	applyDeviceInfo(&st, info)
	assert.Equal(t, "0.1.2.31", st.FirmwareVer)
	assert.Equal(t, 57, st.WifiSignal)
	assert.Equal(t, 120480, st.SDFreeMB)
	assert.Equal(t, 122000, st.SDTotalMB)
	assert.Equal(t, "normal", st.SDStatus)

	// A reply without these fields leaves the previous values untouched.
	applyDeviceInfo(&st, map[string]interface{}{"FW_version": ""})
	assert.Equal(t, "0.1.2.31", st.FirmwareVer)
	assert.Equal(t, 57, st.WifiSignal)
	assert.Equal(t, 122000, st.SDTotalMB)

	// Unexpected types are ignored.
	applyDeviceInfo(&st, map[string]interface{}{"wifi": "x", "SD": map[string]interface{}{"volume_free": "many"}})
	assert.Equal(t, 57, st.WifiSignal)
	assert.Equal(t, 120480, st.SDFreeMB)
}

// The device info is asked once per session unless an interval is set.
func TestDeviceInfoInterval(t *testing.T) {
	b := &Bridge{}
	if got := b.deviceInfoInterval(); got != 0 {
		t.Fatalf("default interval = %v, want 0 (once per session)", got)
	}
	b.SetDeviceInfoInterval(6 * time.Hour)
	if got := b.deviceInfoInterval(); got != 6*time.Hour {
		t.Fatalf("interval = %v, want 6h", got)
	}
	b.SetDeviceInfoInterval(-time.Minute)
	if got := b.deviceInfoInterval(); got != 0 {
		t.Fatalf("negative interval = %v, want 0", got)
	}
}
