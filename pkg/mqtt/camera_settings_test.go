package mqtt

import (
	"testing"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCameraSettingsClient(cb Callbacks) (*Client, *mockPahoClient) {
	mockCl := &mockPahoClient{published: make(map[string]string)}
	c := NewClient(Config{TopicPrefix: "steinel", DiscoveryPrefix: "homeassistant", DeviceID: "de-test"}, cb, nil)
	c.client = mockCl
	return c, mockCl
}

func TestCameraSettingsState(t *testing.T) {
	c, mockCl := newCameraSettingsClient(Callbacks{})
	state := func(sub string) string { return mockCl.published["steinel/de-test/"+sub] }
	dev := &CameraDevice{baseTopic: "steinel/de-test"}

	// Nothing known yet (fresh bus defaults): nothing is published.
	c.publishDeviceStatus(dev, events.DeviceStatus{OSDPosition: -1, SpeakerVolume: -1})
	for _, sub := range []string{"osd_switch/state", "osd_position/state", "volume/state", "detect_mode/state"} {
		_, ok := mockCl.published["steinel/de-test/"+sub]
		assert.False(t, ok, sub)
	}

	c.publishDeviceStatus(dev, events.DeviceStatus{OSDPosition: 3, SpeakerVolume: 80, DetectMode: "ai"})
	assert.Equal(t, "ON", state("osd_switch/state"))
	assert.Equal(t, "Oben rechts", state("osd_position/state"))
	assert.Equal(t, "80", state("volume/state"))
	assert.Equal(t, "KI-Erkennung", state("detect_mode/state"))

	// Overlay off: the switch is off, the select keeps the last position.
	c.publishDeviceStatus(dev, events.DeviceStatus{OSDPosition: 0, SpeakerVolume: -1, DetectMode: "something-else"})
	assert.Equal(t, "OFF", state("osd_switch/state"))
	assert.Equal(t, "Oben rechts", state("osd_position/state"))
	assert.Equal(t, "KI-Erkennung", state("detect_mode/state"), "unknown modes are not published")
}

func TestCameraSettingsCommands(t *testing.T) {
	var positions, volumes []int
	var modes []string
	c, mockCl := newCameraSettingsClient(Callbacks{
		SetTimestampPosition: func(p int) error { positions = append(positions, p); return nil },
		SetSpeakerVolume:     func(v int) error { volumes = append(volumes, v); return nil },
		SetDetectMode:        func(m string) error { modes = append(modes, m); return nil },
	})
	send := func(sub, payload string) {
		c.handleCommand(nil, &mockMessage{topic: "steinel/de-test/" + sub, payload: []byte(payload)})
	}
	state := func(sub string) string { return mockCl.published["steinel/de-test/"+sub] }

	// Switch on without a known position: top left.
	send("osd_switch/set", "ON")
	send("osd_position/set", "Unten rechts")
	send("osd_switch/set", "OFF")
	send("osd_switch/set", "ON")
	send("osd_position/set", "Mitte") // not an option: ignored
	assert.Equal(t, []int{1, 4, 0, 4}, positions)
	assert.Equal(t, "ON", state("osd_switch/state"))
	assert.Equal(t, "Unten rechts", state("osd_position/state"))

	send("volume/set", "65.0")
	send("volume/set", "250")
	send("volume/set", "laut")
	assert.Equal(t, []int{65, 100}, volumes)
	assert.Equal(t, "100", state("volume/state"))

	send("detect_mode/set", "Aus")
	send("detect_mode/set", "KI-Erkennung")
	send("detect_mode/set", "quatsch")
	assert.Equal(t, []string{"off", "ai"}, modes)
	assert.Equal(t, "KI-Erkennung", state("detect_mode/state"))
}

func TestCameraSettingsDiscovery(t *testing.T) {
	has := func(cb Callbacks, component, objectID string) bool {
		c, mockCl := newCameraSettingsClient(Callbacks{})
		c.RegisterCamera(CameraDevice{Path: "steinel", DeviceID: "de-test", Model: "L 625 CAM SC", Callbacks: cb})
		c.camerasMu.RLock()
		dev := c.cameras["steinel"]
		c.camerasMu.RUnlock()
		require.NotNil(t, dev)
		c.publishDeviceDiscovery(mockCl, dev)
		_, ok := mockCl.published["homeassistant/"+component+"/steinel_de_test/"+objectID+"/config"]
		return ok
	}

	// Camera without camera-side settings (e.g. L 620): none of the entities.
	none := Callbacks{}
	assert.False(t, has(none, "switch", "osd_switch"))
	assert.False(t, has(none, "number", "volume"))

	full := Callbacks{
		SetTimestampPosition: func(int) error { return nil },
		SetSpeakerVolume:     func(int) error { return nil },
	}
	for _, e := range [][2]string{{"switch", "osd_switch"}, {"select", "osd_position"}, {"number", "volume"}} {
		assert.True(t, has(full, e[0], e[1]), "%s.%s", e[0], e[1])
	}
	assert.False(t, has(full, "select", "detect_mode"), "detection mode is only announced when its setter is wired")
	full.SetDetectMode = func(string) error { return nil }
	assert.True(t, has(full, "select", "detect_mode"))
}
