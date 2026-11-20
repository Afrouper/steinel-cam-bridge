package webrtc

import (
	"testing"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/stretchr/testify/assert"
)

func TestApplyUpdateInfo(t *testing.T) {
	st := events.DeviceStatus{FirmwareVer: "0.1.2.31"}

	// A reply without any update field: nothing is known yet.
	applyUpdateInfo(&st, map[string]interface{}{"FW_version": "0.1.2.31"})
	assert.False(t, st.UpdateInfoKnown)

	// The installed MCU version alone does not say anything about an offer.
	applyUpdateInfo(&st, map[string]interface{}{"MCU_version": "1.5"})
	assert.Equal(t, "1.5", st.MCUVersion)
	assert.False(t, st.UpdateInfoKnown)

	applyUpdateInfo(&st, map[string]interface{}{
		"FW_remote_version":  "0.1.2.40",
		keyUpdateFlag:        true,
		"MCU_version":        "1.5",
		"MCU_remote_version": "1.5",
		keyMCUUpdateFlag:     false,
	})
	assert.True(t, st.UpdateInfoKnown)
	assert.Equal(t, "0.1.2.40", st.FirmwareRemote)
	assert.True(t, st.FirmwareUpdate)
	assert.Equal(t, "1.5", st.MCURemote)
	assert.False(t, st.MCUUpdate)
	assert.True(t, CameraUpdateOffered(st))
	assert.False(t, MCUUpdateOffered(st))

	// An empty MCU version does not overwrite the known one; wrong types are ignored.
	applyUpdateInfo(&st, map[string]interface{}{"MCU_version": "", keyUpdateFlag: "yes"})
	assert.Equal(t, "1.5", st.MCUVersion)
	assert.True(t, st.FirmwareUpdate)
}

func TestUpdateOffered(t *testing.T) {
	cases := []struct {
		name      string
		flag      bool
		installed string
		remote    string
		want      bool
	}{
		{"flag set and newer version", true, "0.1.2.31", "0.1.2.40", true},
		{"flag set but same version", true, "0.1.2.31", "0.1.2.31", false},
		{"flag set but no remote version", true, "0.1.2.31", "", false},
		{"other version without flag", false, "0.1.2.31", "0.1.2.40", false},
	}
	for _, c := range cases {
		cam := events.DeviceStatus{FirmwareUpdate: c.flag, FirmwareVer: c.installed, FirmwareRemote: c.remote}
		assert.Equal(t, c.want, CameraUpdateOffered(cam), "camera: %s", c.name)
		lamp := events.DeviceStatus{MCUUpdate: c.flag, MCUVersion: c.installed, MCURemote: c.remote}
		assert.Equal(t, c.want, MCUUpdateOffered(lamp), "mcu: %s", c.name)
	}
}

// InstallUpdate must not send anything unless the camera itself offers the
// update. With an offer it gets as far as the (missing) DataChannel.
func TestInstallUpdateNeedsOffer(t *testing.T) {
	b := &Bridge{eventBus: events.NewBus()}
	const noChannel = "data channel not available"

	assert.ErrorIs(t, b.InstallUpdate(UpdateTargetCamera), ErrNoUpdateOffered)
	assert.ErrorIs(t, b.InstallUpdate(UpdateTargetMCU), ErrNoUpdateOffered)
	assert.ErrorContains(t, b.InstallUpdate("lamp"), "unknown update target")

	st := b.eventBus.GetStatus()
	st.FirmwareVer, st.FirmwareRemote, st.FirmwareUpdate = "0.1.2.31", "0.1.2.40", true
	b.eventBus.UpdateStatus(st)

	err := b.InstallUpdate(" Camera ")
	assert.NotErrorIs(t, err, ErrNoUpdateOffered)
	assert.ErrorContains(t, err, noChannel)
	// The lamp firmware is still not on offer.
	assert.ErrorIs(t, b.InstallUpdate(UpdateTargetMCU), ErrNoUpdateOffered)

	st = b.eventBus.GetStatus()
	st.MCUVersion, st.MCURemote, st.MCUUpdate = "1.5", "1.6", true
	b.eventBus.UpdateStatus(st)
	err = b.InstallUpdate(UpdateTargetMCU)
	assert.NotErrorIs(t, err, ErrNoUpdateOffered)
	assert.ErrorContains(t, err, noChannel)
}
