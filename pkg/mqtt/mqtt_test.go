package mqtt

import (
	"testing"
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
	c := NewClient(cfg, cb)

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
	c := NewClient(Config{DeviceID: "de-test"}, cb)

	msg := &mockMessage{
		topic:   "steinel/de-test/highlight/set",
		payload: []byte("75"),
	}
	c.handleCommand(nil, msg)

	if highlightVal != 75 {
		t.Errorf("Expected highlight 75, got %d", highlightVal)
	}
}
