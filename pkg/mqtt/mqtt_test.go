package mqtt

import (
	"testing"
)

func TestNewClient(t *testing.T) {
	cfg := Config{
		Broker:   "tcp://127.0.0.1:1883",
		DeviceID: "de-xxxxxxx",
	}
	cb := Callbacks{}
	c := NewClient(cfg, cb)

	if c.nodeID != "steinel_de_m4yfowbr" {
		t.Errorf("Expected nodeID steinel_de_m4yfowbr, got %s", c.nodeID)
	}
	if c.baseTopic != "steinel/de-xxxxxxx" {
		t.Errorf("Expected baseTopic steinel/de-xxxxxxx, got %s", c.baseTopic)
	}
}
