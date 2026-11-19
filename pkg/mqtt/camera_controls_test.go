package mqtt

import (
	"testing"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
)

func TestPublishCameraControlsState(t *testing.T) {
	st := events.NewBus().GetStatus()
	got := map[string]string{}
	pub := func(sub, val string) { got[sub] = val }

	// Unknown values publish nothing.
	(&Client{}).publishCameraControlsState(st, pub)
	if len(got) != 0 {
		t.Fatalf("expected no state for unknown values, got %v", got)
	}

	st.PixelChangeDetection = 0
	st.DrawFrame = 1
	st.AIPerson = 4
	st.RecordMode = 4
	st.UpdateInfoKnown = true
	st.FirmwareVer = "0.1.2.31"
	st.FirmwareRemote = ""
	st.FirmwareUpdate = false
	st.MCUVersion = "1.5"
	st.MCURemote = "1.6"
	st.MCUUpdate = true
	(&Client{}).publishCameraControlsState(st, pub)

	want := map[string]string{
		"pixel_change/state":    "OFF",
		"draw_frame/state":      "ON",
		"ai_person/state":       "Mittel",
		"record_mode/state":     "Keine Aufnahme",
		"firmware_update/state": `{"installed_version":"0.1.2.31","latest_version":"0.1.2.31"}`,
		"mcu_update/state":      `{"installed_version":"1.5","latest_version":"1.6"}`,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestHandleCameraControlsCommand(t *testing.T) {
	var pixel, frame *bool
	var person, record int
	var updateTarget string
	reboots := 0
	cb := Callbacks{
		SetPixelChangeDetection: func(on bool) error { pixel = &on; return nil },
		SetDrawFrame:            func(on bool) error { frame = &on; return nil },
		SetAIPersonSensitivity:  func(level int) error { person = level; return nil },
		SetRecordMode:           func(mode int) error { record = mode; return nil },
		InstallUpdate:           func(target string) error { updateTarget = target; return nil },
		RebootCamera:            func() error { reboots++; return nil },
	}
	c := &Client{}
	base := "steinel/cam"
	steps := []struct{ topic, payload string }{
		{base + "/pixel_change/set", "ON"},
		{base + "/draw_frame/set", "OFF"},
		{base + "/ai_person/set", "Hoch"},
		{base + "/record_mode/set", "Kamera oder Bewegungsmelder"},
		{base + "/mcu_update/set", "install"},
		{base + "/reboot/set", "PRESS"},
	}
	for _, s := range steps {
		if !c.handleCameraControlsCommand(s.topic, s.payload, base, cb) {
			t.Fatalf("topic %s not handled", s.topic)
		}
	}
	if pixel == nil || !*pixel || frame == nil || *frame || person != 7 || record != 3 || updateTarget != "mcu" || reboots != 1 {
		t.Errorf("unexpected callbacks: pixel=%v frame=%v person=%d record=%d update=%q reboots=%d", pixel, frame, person, record, updateTarget, reboots)
	}

	// Anything but "install" must not start an update.
	updateTarget = ""
	c.handleCameraControlsCommand(base+"/firmware_update/set", "PRESS", base, cb)
	if updateTarget != "" {
		t.Errorf("update started with wrong payload")
	}
	if c.handleCameraControlsCommand(base+"/highlight/set", "50", base, cb) {
		t.Errorf("foreign topic handled")
	}
}
