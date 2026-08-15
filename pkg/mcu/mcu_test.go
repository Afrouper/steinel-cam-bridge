package mcu

import (
	"testing"
)

func TestParseFrame(t *testing.T) {
	// Sample frame: 5a0f0f (6 chars) + 28 chars body + 2 chars checksum = 36 chars
	// 5a0f0f + 0102020064115000000000000000 + 1a
	frame := "5a0f0f01020200641150000000000000001a"
	cfg, err := ParseFrame(frame)
	if err != nil {
		t.Fatalf("ParseFrame failed: %v", err)
	}

	if cfg.Lux != 100 { // 0x0064 = 100
		t.Errorf("Expected Lux 100, got %d", cfg.Lux)
	}
	if !cfg.PIRActive {
		t.Errorf("Expected PIRActive true")
	}
	if !cfg.MotionDetected {
		t.Errorf("Expected MotionDetected true")
	}
}

func TestBuildCommand(t *testing.T) {
	// CmdGetLightInfo: 5A010F -> Checksum is 6A -> Full 5A010F6A -> Base64 WgEPag==
	b64, err := BuildCommand(CmdGetLightInfo)
	if err != nil {
		t.Fatalf("BuildCommand failed: %v", err)
	}
	if b64 != "WgEPag==" {
		t.Errorf("Expected WgEPag==, got %s", b64)
	}
}
