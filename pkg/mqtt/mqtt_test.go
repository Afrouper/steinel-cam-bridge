package mqtt

import (
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
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

