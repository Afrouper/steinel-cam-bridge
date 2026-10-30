package mqtt

import (
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/stretchr/testify/assert"
)

type mockMessage struct {
	topic   string
	payload []byte
}

func (m *mockMessage) Duplicate() bool   { return false }
func (m *mockMessage) Qos() byte         { return 0 }
func (m *mockMessage) Retained() bool    { return false }
func (m *mockMessage) Topic() string     { return m.topic }
func (m *mockMessage) MessageID() uint16 { return 0 }
func (m *mockMessage) Payload() []byte   { return m.payload }
func (m *mockMessage) Ack()              {}

func TestNewClient(t *testing.T) {
	cfg := Config{
		Broker:   "tcp://127.0.0.1:1883",
		DeviceID: "de-xxxxxxx",
	}
	cb := Callbacks{}
	c := NewClient(cfg, cb, nil)

	if c.nodeID != "steinel_de_xxxxxxx" {
		t.Errorf("Expected nodeID steinel_de_xxxxxxx, got %s", c.nodeID)
	}
	if c.baseTopic != "steinel/de-xxxxxxx" {
		t.Errorf("Expected baseTopic steinel/de-xxxxxxx, got %s", c.baseTopic)
	}
}

func TestHandleHighlightCommand(t *testing.T) {
	var highlightVal int
	cb := Callbacks{
		SetHighlight: func(percent int) error {
			highlightVal = percent
			return nil
		},
	}
	c := NewClient(Config{DeviceID: "de-test"}, cb, nil)

	msg := &mockMessage{
		topic:   "steinel/de-test/highlight/set",
		payload: []byte("75"),
	}
	c.handleCommand(nil, msg)

	if highlightVal != 75 {
		t.Errorf("Expected highlight 75, got %d", highlightVal)
	}
}

func TestHandleSirenCommand(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected bool
	}{
		{"Plain ON", "ON", true},
		{"Plain OFF", "OFF", false},
		{"JSON ON", `{"state":"ON"}`, true},
		{"JSON OFF", `{"state":"OFF"}`, false},
		{"JSON with volume and duration", `{"state":"ON","volume_level":0.8,"duration":2}`, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sirenVal bool
			cb := Callbacks{
				SetSiren: func(on bool) error {
					sirenVal = on
					return nil
				},
			}
			c := NewClient(Config{DeviceID: "de-test"}, cb, nil)
			msg := &mockMessage{
				topic:   "steinel/de-test/siren/set",
				payload: []byte(tc.payload),
			}
			c.handleCommand(nil, msg)

			if sirenVal != tc.expected {
				t.Errorf("Payload %q: expected siren %v, got %v", tc.payload, tc.expected, sirenVal)
			}
		})
	}
}

func TestPublishRecordingEvent(t *testing.T) {
	c := NewClient(Config{
		DeviceID:      "de-test",
		BridgeHTTPURL: "http://192.168.1.100:8000",
	}, Callbacks{}, nil)

	// Calling PublishRecordingEvent when client is nil shouldn't panic
	c.PublishRecordingEvent(storage.RecordingItem{
		ID:              "1724528700",
		StartTime:       time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC),
		EndTime:         time.Date(2026, 8, 26, 18, 0, 30, 0, time.UTC),
		DurationSeconds: 30,
		EventType:       "motion",
		FileSizeBytes:   1048576,
		ThumbnailURL:    "/api/sdcard/events/1724528700/thumbnail.jpg",
		VideoURL:        "/api/sdcard/events/1724528700/video.mp4",
	})

	// Also verify with empty ThumbnailURL (e.g. L 625 CAM SC model)
	c.PublishRecordingEvent(storage.RecordingItem{
		ID:              "1724528701",
		StartTime:       time.Date(2026, 8, 26, 18, 1, 0, 0, time.UTC),
		EndTime:         time.Date(2026, 8, 26, 18, 1, 30, 0, time.UTC),
		DurationSeconds: 30,
		EventType:       "ai_person",
		VideoURL:        "/api/sdcard/events/1724528701/video.mp4",
	})

	// Verify uninitialized/empty item is safely ignored without panic
	c.PublishRecordingEvent(storage.RecordingItem{})
	c.PublishRecordingEvent(storage.RecordingItem{ID: ""})
	c.PublishRecordingEvent(storage.RecordingItem{ID: "123", StartTime: time.Time{}})
}

func TestMultiCameraMQTT(t *testing.T) {
	var cam1LampMode string
	var cam2LampMode string

	cb1 := Callbacks{
		SetLampMode: func(mode string) error {
			cam1LampMode = mode
			return nil
		},
	}
	cb2 := Callbacks{
		SetLampMode: func(mode string) error {
			cam2LampMode = mode
			return nil
		},
	}

	c := NewClient(Config{
		TopicPrefix: "steinel",
		DeviceID:    "de-cam1",
	}, cb1, nil)

	// Register camera 2
	c.RegisterCamera(CameraDevice{
		Name:      "Garten",
		DeviceID:  "de-cam2",
		Model:     "L 620 CAM",
		Callbacks: cb2,
	})

	// Send command to Camera 1
	msg1 := &mockMessage{
		topic:   "steinel/de-cam1/light/set",
		payload: []byte("ON"),
	}
	c.handleCommand(nil, msg1)

	if cam1LampMode != "on" {
		t.Errorf("expected cam1 lamp mode 'on', got %q", cam1LampMode)
	}
	if cam2LampMode != "" {
		t.Errorf("expected cam2 lamp mode unaffected, got %q", cam2LampMode)
	}

	// Send command to Camera 2
	msg2 := &mockMessage{
		topic:   "steinel/de-cam2/light/set",
		payload: []byte("OFF"),
	}
	c.handleCommand(nil, msg2)

	if cam2LampMode != "off" {
		t.Errorf("expected cam2 lamp mode 'off', got %q", cam2LampMode)
	}
}

func TestFormatDisplayName(t *testing.T) {
	// 1. Single-camera legacy default (Name is empty) -> Steinel <Model> (<DeviceID>)
	assert.Equal(t, "Steinel L 625 CAM SC (de-xxxxxxx)", FormatDisplayName("L 625 CAM SC", "de-xxxxxxx", ""))
	assert.Equal(t, "Steinel L 620 CAM (steinel-l620)", FormatDisplayName("L 620 CAM", "steinel-l620", ""))

	// 2. Custom name without "Steinel" prefix -> "Steinel " + Name
	assert.Equal(t, "Steinel Einfahrt", FormatDisplayName("L 625 CAM SC", "de-xxxxxxx", "Einfahrt"))

	// 3. Custom name with "Steinel" prefix -> Name preserved without duplication
	assert.Equal(t, "Steinel Garten", FormatDisplayName("L 620 CAM", "steinel-l620", "Steinel Garten"))
	assert.Equal(t, "steinel terrasse", FormatDisplayName("L 620 CAM", "steinel-l620", "steinel terrasse"))
}

func TestUpdateCameraDeviceInfo_MultiCam(t *testing.T) {
	c := NewClient(Config{
		TopicPrefix: "steinel",
	}, Callbacks{}, nil)

	// Register 3 cameras with empty DeviceID (before Nabto P2P discovery)
	c.RegisterCamera(CameraDevice{
		Path:  "steinel-hinterhof",
		Name:  "Hinterhof",
		Model: "L 625 CAM SC",
	})
	c.RegisterCamera(CameraDevice{
		Path:  "steinel-einfahrt",
		Name:  "Einfahrt",
		Model: "L 625 CAM SC",
	})
	c.RegisterCamera(CameraDevice{
		Path:  "steinel-haustuer",
		Name:  "Haustür",
		Model: "L 625 CAM SC",
	})

	// Verify all 3 cameras are registered independently in the map
	c.camerasMu.RLock()
	assert.Equal(t, 3, len(c.cameras))
	cam1 := c.cameras["steinel-hinterhof"]
	cam2 := c.cameras["steinel-einfahrt"]
	cam3 := c.cameras["steinel-haustuer"]
	c.camerasMu.RUnlock()

	assert.NotNil(t, cam1)
	assert.NotNil(t, cam2)
	assert.NotNil(t, cam3)
	assert.Equal(t, "Hinterhof", cam1.Name)
	assert.Equal(t, "Einfahrt", cam2.Name)
	assert.Equal(t, "Haustür", cam3.Name)

	// Update Camera 1
	c.UpdateCameraDeviceInfo("steinel-hinterhof", "de-tq8d2vve", "pr-qtatbtbi")

	// Verify Camera 1 updated, but Camera 2 and Camera 3 remain untouched
	c.camerasMu.RLock()
	assert.Equal(t, "de-tq8d2vve", cam1.DeviceID)
	assert.Equal(t, "steinel_de_tq8d2vve", cam1.nodeID)
	assert.Equal(t, "steinel/de-tq8d2vve", cam1.baseTopic)
	assert.Equal(t, "Hinterhof", cam1.Name)

	assert.Equal(t, "", cam2.DeviceID)
	assert.Equal(t, "Einfahrt", cam2.Name)
	assert.Equal(t, "", cam3.DeviceID)
	assert.Equal(t, "Haustür", cam3.Name)
	c.camerasMu.RUnlock()

	// Update Camera 2
	c.UpdateCameraDeviceInfo("steinel-einfahrt", "de-z5f8hvyx", "pr-qtatbtbi")

	c.camerasMu.RLock()
	assert.Equal(t, "de-z5f8hvyx", cam2.DeviceID)
	assert.Equal(t, "steinel_de_z5f8hvyx", cam2.nodeID)
	assert.Equal(t, "steinel/de-z5f8hvyx", cam2.baseTopic)
	assert.Equal(t, "Einfahrt", cam2.Name)

	// Ensure Camera 1 was NOT overwritten by Camera 2
	assert.Equal(t, "de-tq8d2vve", cam1.DeviceID)
	assert.Equal(t, "steinel/de-tq8d2vve", cam1.baseTopic)
	assert.Equal(t, "Hinterhof", cam1.Name)
	c.camerasMu.RUnlock()

	// Update Camera 3
	c.UpdateCameraDeviceInfo("steinel-haustuer", "de-e3dftz9a", "pr-qtatbtbi")

	c.camerasMu.RLock()
	assert.Equal(t, "de-e3dftz9a", cam3.DeviceID)
	assert.Equal(t, "steinel_de_e3dftz9a", cam3.nodeID)
	assert.Equal(t, "steinel/de-e3dftz9a", cam3.baseTopic)
	assert.Equal(t, "Haustür", cam3.Name)

	// Check display names
	assert.Equal(t, "Steinel Hinterhof", FormatDisplayName(cam1.Model, cam1.DeviceID, cam1.Name))
	assert.Equal(t, "Steinel Einfahrt", FormatDisplayName(cam2.Model, cam2.DeviceID, cam2.Name))
	assert.Equal(t, "Steinel Haustür", FormatDisplayName(cam3.Model, cam3.DeviceID, cam3.Name))
	c.camerasMu.RUnlock()
}

func TestSingleCamera_NoDuplicateDiscovery(t *testing.T) {
	// Replicates the exact initialization in app.go for a single camera with known DeviceID
	c := NewClient(Config{
		TopicPrefix: "steinel",
		DeviceID:    "de-m4yfowbr",
	}, Callbacks{}, nil)

	// In app.go, camera 0 is registered via RegisterCamera with its path (e.g. "steinel")
	c.RegisterCamera(CameraDevice{
		Path:      "steinel",
		Name:      "",
		DeviceID:  "de-m4yfowbr",
		Model:     "L 625 CAM SC",
		Callbacks: Callbacks{SetLampMode: func(string) error { return nil }},
	})

	// Must contain exactly 1 camera, NOT 2 (prevent duplicate discovery on connect)
	c.camerasMu.RLock()
	assert.Equal(t, 1, len(c.cameras), "Expected exactly 1 camera device in map, got %d", len(c.cameras))
	cam := c.cameras["steinel"]
	c.camerasMu.RUnlock()

	assert.NotNil(t, cam)
	assert.Equal(t, "de-m4yfowbr", cam.DeviceID)
	assert.Equal(t, "steinel_de_m4yfowbr", cam.nodeID)
	assert.Equal(t, "steinel/de-m4yfowbr", cam.baseTopic)
}
