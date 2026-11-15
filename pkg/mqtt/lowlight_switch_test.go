package mqtt

import (
	"testing"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaseLightSwitch(t *testing.T) {
	mockCl := &mockPahoClient{published: make(map[string]string)}
	var lowlight []int
	c := NewClient(Config{TopicPrefix: "steinel", DeviceID: "de-test"}, Callbacks{
		SetLowlight: func(p int) error { lowlight = append(lowlight, p); return nil },
	}, nil)
	c.client = mockCl
	send := func(sub, payload string) {
		c.handleCommand(nil, &mockMessage{topic: "steinel/de-test/" + sub, payload: []byte(payload)})
	}
	state := func(sub string) string { return mockCl.published["steinel/de-test/"+sub] }

	// Nothing known yet: the switch turns the base light on with the lamp's minimum.
	send("lowlight_switch/set", "ON")
	assert.Equal(t, []int{defaultLowlightPercent}, lowlight)
	assert.Equal(t, "ON", state("lowlight_switch/state"))
	assert.Equal(t, "10", state("lowlight/state"))

	// The lamp reports 30 %: remembered for the next switch-on.
	c.publishDeviceStatus(&CameraDevice{baseTopic: "steinel/de-test"}, events.DeviceStatus{Lowlight: 30})
	assert.Equal(t, "ON", state("lowlight_switch/state"))

	send("lowlight_switch/set", "OFF")
	assert.Equal(t, "OFF", state("lowlight_switch/state"))
	assert.Equal(t, "0", state("lowlight/state"))

	// Off as reported by the lamp does not erase the remembered brightness.
	c.publishDeviceStatus(&CameraDevice{baseTopic: "steinel/de-test"}, events.DeviceStatus{Lowlight: 0})
	assert.Equal(t, "OFF", state("lowlight_switch/state"))

	send("lowlight_switch/set", "ON")
	assert.Equal(t, []int{10, 0, 30}, lowlight)
	assert.Equal(t, "30", state("lowlight/state"))

	// Setting the brightness number moves the switch along and is remembered.
	send("lowlight/set", "45")
	assert.Equal(t, "ON", state("lowlight_switch/state"))
	send("lowlight/set", "0")
	assert.Equal(t, "OFF", state("lowlight_switch/state"))
	send("lowlight_switch/set", "ON")
	assert.Equal(t, 45, lowlight[len(lowlight)-1])
}

func TestBaseLightSwitchDiscovery(t *testing.T) {
	mockCl := &mockPahoClient{published: make(map[string]string)}
	c := NewClient(Config{TopicPrefix: "steinel", DiscoveryPrefix: "homeassistant", DeviceID: "de-test"}, Callbacks{}, nil)
	c.RegisterCamera(CameraDevice{Path: "steinel", DeviceID: "de-test", Model: "L 625 CAM SC"})
	c.camerasMu.RLock()
	dev := c.cameras["steinel"]
	c.camerasMu.RUnlock()
	require.NotNil(t, dev)

	c.publishDeviceDiscovery(mockCl, dev)

	topic := "homeassistant/switch/steinel_de_test/lowlight_switch/config"
	_, ok := mockCl.published[topic]
	assert.True(t, ok, "base light switch is announced to Home Assistant")
	assert.True(t, mockCl.retained[topic])
}
