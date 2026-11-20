package webrtc

import (
	"encoding/json"
	"testing"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jsonInfo(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var info map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(raw), &info))
	return info
}

func TestAIPersonLevel(t *testing.T) {
	for raw, want := range map[int]int{
		0: AIPersonLow, 2: AIPersonLow, 3: AIPersonLow,
		4: AIPersonMedium, 5: AIPersonMedium, 6: AIPersonMedium,
		7: AIPersonHigh, 10: AIPersonHigh,
	} {
		assert.Equal(t, want, AIPersonLevel(raw), "raw %d", raw)
	}
}

func TestIsKnownRecordMode(t *testing.T) {
	for _, mode := range []int{RecordModeNone, RecordModeCamera, RecordModeCameraOrPIR} {
		assert.True(t, IsKnownRecordMode(mode), "mode %d", mode)
	}
	for _, mode := range []int{-1, 0, 1, 5} {
		assert.False(t, IsKnownRecordMode(mode), "mode %d", mode)
	}
}

func TestApplyDetectData(t *testing.T) {
	st := events.DeviceStatus{DrawFrame: -1, AIPerson: -1}

	applyDetectData(&st, jsonInfo(t, `{"draw_frame":1,"ai_person":5}`))
	assert.Equal(t, 1, st.DrawFrame)
	assert.Equal(t, 5, st.AIPerson)

	// Only one of the fields: the other one keeps its value.
	applyDetectData(&st, jsonInfo(t, `{"draw_frame":0}`))
	assert.Equal(t, 0, st.DrawFrame)
	assert.Equal(t, 5, st.AIPerson)

	// Any value other than 0 means "on"; unexpected types are ignored.
	applyDetectData(&st, jsonInfo(t, `{"draw_frame":2,"ai_person":"high"}`))
	assert.Equal(t, 1, st.DrawFrame)
	assert.Equal(t, 5, st.AIPerson)
}

func TestApplyRecordSetting(t *testing.T) {
	st := events.DeviceStatus{RecordMode: -1}

	applyRecordSetting(&st, jsonInfo(t, `{"mode":3,"record_time":30}`))
	assert.Equal(t, RecordModeCameraOrPIR, st.RecordMode)

	applyRecordSetting(&st, jsonInfo(t, `{"record_time":30}`))
	assert.Equal(t, RecordModeCameraOrPIR, st.RecordMode)
}

// The replies of the camera reach the device status through the DataChannel
// handler and are not mistaken for motion.
func TestDetectionRepliesUpdateStatus(t *testing.T) {
	b := &Bridge{eventBus: events.NewBus()}

	b.handleDataChannelMessage([]byte(`{"resp":"get_detect_data","result":"success","info":{"draw_frame":0,"ai_person":7}}`))
	b.handleDataChannelMessage([]byte(`{"resp":"get_record_setting","result":"success","info":{"mode":4,"record_time":30}}`))

	st := b.eventBus.GetStatus()
	assert.Equal(t, 0, st.DrawFrame)
	assert.Equal(t, AIPersonHigh, st.AIPerson)
	assert.Equal(t, RecordModeNone, st.RecordMode)
	assert.False(t, st.IsMotion)
}

// Values the camera does not know are refused before anything is sent. Valid
// values get as far as the (missing) DataChannel and leave the status alone.
func TestDetectionSettersValidate(t *testing.T) {
	b := &Bridge{eventBus: events.NewBus()}
	const noChannel = "data channel not available"

	assert.ErrorContains(t, b.SetAIPersonSensitivity(4), "invalid person sensitivity")
	assert.ErrorContains(t, b.SetAIPersonSensitivity(AIPersonMedium), noChannel)

	assert.ErrorContains(t, b.SetRecordMode(1), "invalid record mode")
	assert.ErrorContains(t, b.SetRecordMode(RecordModeCamera), noChannel)

	assert.ErrorContains(t, b.SetDrawFrame(false), noChannel)
	assert.ErrorContains(t, b.SetPixelChangeDetection(true), noChannel)

	assert.ErrorContains(t, b.SetTimestampPosition(5), "invalid timestamp position")
	assert.ErrorContains(t, b.SetTimestampPosition(OSDBottomRight), noChannel)
	assert.ErrorContains(t, b.SetDetectMode("motion"), "unknown detection mode")
	assert.ErrorContains(t, b.SetDetectMode(" AI "), noChannel)

	st := b.eventBus.GetStatus()
	assert.Equal(t, -1, st.AIPerson)
	assert.Equal(t, -1, st.RecordMode)
	assert.Equal(t, -1, st.DrawFrame)
	assert.Equal(t, -1, st.PixelChangeDetection)
	assert.Equal(t, -1, st.OSDPosition)
	assert.Equal(t, "", st.DetectMode)
}
