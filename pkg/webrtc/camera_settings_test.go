package webrtc

import (
	"encoding/json"
	"testing"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyDeviceSettings(t *testing.T) {
	// Settings part of a get_device_info reply of the L 625 (firmware 0.1.2.31).
	raw := `{"FW_version":"0.1.2.31","time":{"OSD":3},"volume":80,"detect":{"mode":"ai"},"FW_remote_version":"0.1.2.31","MCU_version":"1.5","MCU_remote_version":"1.5"}`
	var info map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(raw), &info))

	st := events.DeviceStatus{OSDPosition: -1, SpeakerVolume: -1}
	applyDeviceInfo(&st, info)
	assert.Equal(t, "0.1.2.31", st.FirmwareVer)
	assert.Equal(t, OSDTopRight, st.OSDPosition)
	assert.Equal(t, 80, st.SpeakerVolume)
	assert.Equal(t, "ai", st.DetectMode)
	assert.True(t, st.UpdateInfoKnown)
	assert.Equal(t, "0.1.2.31", st.FirmwareRemote)
	assert.Equal(t, "1.5", st.MCUVersion)
	assert.False(t, CameraUpdateOffered(st))

	// A reply without the settings leaves them untouched.
	applyDeviceInfo(&st, map[string]interface{}{"wifi": map[string]interface{}{"RSSI": 40.0}})
	assert.Equal(t, 40, st.WifiSignal)
	assert.Equal(t, OSDTopRight, st.OSDPosition)
	assert.Equal(t, 80, st.SpeakerVolume)
	assert.Equal(t, "ai", st.DetectMode)

	// Timestamp switched off.
	applyDeviceInfo(&st, map[string]interface{}{"time": map[string]interface{}{"OSD": 0.0}})
	assert.Equal(t, OSDOff, st.OSDPosition)
}

func TestIsKnownDetectMode(t *testing.T) {
	assert.True(t, IsKnownDetectMode("ai"))
	assert.True(t, IsKnownDetectMode("off"))
	assert.False(t, IsKnownDetectMode("AI"))
	assert.False(t, IsKnownDetectMode("motion"))
}
