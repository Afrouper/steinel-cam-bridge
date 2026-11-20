package webrtc

import (
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mcu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMotionPushClearsItself(t *testing.T) {
	old := motionResetDelay
	motionResetDelay = 50 * time.Millisecond
	defer func() { motionResetDelay = old }()

	b := &Bridge{eventBus: events.NewBus()}
	isMotion := func() bool { return b.eventBus.GetStatus().IsMotion }

	// A reply to one of our own commands is not motion.
	b.handleDataChannelMessage([]byte(`{"resp":"alarm_voice_ctl","result":"success"}`))
	assert.False(t, isMotion())

	// A push from the camera is, and nothing but the timer switches it off again.
	b.handleDataChannelMessage([]byte(`{"cmd":"push","info":{"type":"motion"}}`))
	assert.True(t, isMotion())
	require.Eventually(t, func() bool { return !isMotion() }, 3*time.Second, 10*time.Millisecond)
}

func TestMCUStatusBitsAreNotMotion(t *testing.T) {
	b := &Bridge{eventBus: events.NewBus(), resolution: "1080p"}

	// "Pixel Change Detection" and "Image Photosensitive Detection" are lamp
	// settings in the status frame. They must not switch the motion sensor on.
	b.onMCUStatus(&mcu.ConfigInfo{Mode: 2, PixelChangeDetection: true, ImagePhotosensitiveDetection: true})
	st := b.eventBus.GetStatus()
	assert.False(t, st.IsMotion)
	assert.Equal(t, 1, st.PixelChangeDetection)

	b.onMCUStatus(&mcu.ConfigInfo{Mode: 2})
	assert.Equal(t, 0, b.eventBus.GetStatus().PixelChangeDetection)
}
