package mqtt

import (
	"testing"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCameraInfoState(t *testing.T) {
	mockCl := &mockPahoClient{published: make(map[string]string)}
	c := NewClient(Config{TopicPrefix: "steinel", DiscoveryPrefix: "homeassistant", DeviceID: "de-test"}, Callbacks{}, nil)
	c.client = mockCl
	state := func(sub string) string { return mockCl.published["steinel/de-test/"+sub] }
	dev := &CameraDevice{baseTopic: "steinel/de-test"}

	// Nothing known yet: nothing is published.
	c.publishDeviceStatus(dev, events.DeviceStatus{})
	for _, sub := range []string{"wifi_signal/state", "sd_free/state", "sd_used/state", "sd_total/state", "sd_status/state", "firmware/state"} {
		_, ok := mockCl.published["steinel/de-test/"+sub]
		assert.False(t, ok, sub)
	}

	c.publishDeviceStatus(dev, events.DeviceStatus{
		WifiSignal: 57, SDFreeMB: 120480, SDTotalMB: 122000, SDStatus: "normal", FirmwareVer: "0.1.2.31",
	})
	assert.Equal(t, "57", state("wifi_signal/state"))
	// 122000 MiB is a 128 GB card: 126.3 GB free, 1.59 GB used.
	assert.Equal(t, "126.3", state("sd_free/state"))
	assert.Equal(t, "1.59", state("sd_used/state"))
	assert.Equal(t, "127.9", state("sd_total/state"))
	assert.Equal(t, "normal", state("sd_status/state"))
	assert.Equal(t, "0.1.2.31", state("firmware/state"))
}

func TestCameraInfoDiscovery(t *testing.T) {
	has := func(cb Callbacks, objectID string) bool {
		mockCl := &mockPahoClient{published: make(map[string]string)}
		c := NewClient(Config{TopicPrefix: "steinel", DiscoveryPrefix: "homeassistant", DeviceID: "de-test"}, Callbacks{}, nil)
		c.client = mockCl
		c.RegisterCamera(CameraDevice{Path: "steinel", DeviceID: "de-test", Model: "L 625 CAM SC", Callbacks: cb})
		c.camerasMu.RLock()
		dev := c.cameras["steinel"]
		c.camerasMu.RUnlock()
		require.NotNil(t, dev)
		c.publishDeviceDiscovery(mockCl, dev)
		_, ok := mockCl.published["homeassistant/sensor/steinel_de_test/"+objectID+"/config"]
		return ok
	}

	// Cameras without device info (e.g. L 620): no diagnostic sensors.
	assert.False(t, has(Callbacks{}, "wifi_signal"))

	for _, id := range []string{"wifi_signal", "sd_free", "sd_used", "sd_total", "sd_status", "firmware"} {
		assert.True(t, has(Callbacks{CameraInfo: true}, id), id)
	}
}

func TestSDGigabytes(t *testing.T) {
	assert.InDelta(t, 127.93, sdGigabytes(122000), 0.01)
	assert.Equal(t, 0.0, sdGigabytes(0))
	assert.Equal(t, 0.0, sdGigabytes(-5))
}
