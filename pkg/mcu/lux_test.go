package mcu

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestLuxTableMapping(t *testing.T) {
	if len(LuxTable) != 26 {
		t.Fatalf("Expected 26 levels in LuxTable, got %d", len(LuxTable))
	}
	if LuxTable[0] != 2 {
		t.Errorf("Expected first step to be 2 lx, got %d", LuxTable[0])
	}
	if LuxTable[12] != 100 {
		t.Errorf("Expected step 12 to be 100 lx, got %d", LuxTable[12])
	}
	if LuxTable[25] != 2000 {
		t.Errorf("Expected step 25 to be 2000 lx, got %d", LuxTable[25])
	}
}

func TestStepToLux(t *testing.T) {
	if got := StepToLux(-5); got != 2 {
		t.Errorf("Expected StepToLux(-5) = 2, got %d", got)
	}
	if got := StepToLux(0); got != 2 {
		t.Errorf("Expected StepToLux(0) = 2, got %d", got)
	}
	if got := StepToLux(3); got != 7 {
		t.Errorf("Expected StepToLux(3) = 7, got %d", got)
	}
	if got := StepToLux(12); got != 100 {
		t.Errorf("Expected StepToLux(12) = 100, got %d", got)
	}
	if got := StepToLux(25); got != 2000 {
		t.Errorf("Expected StepToLux(25) = 2000, got %d", got)
	}
	if got := StepToLux(99); got != 2000 {
		t.Errorf("Expected StepToLux(99) = 2000, got %d", got)
	}
}

func TestLuxToStep(t *testing.T) {
	tests := []struct {
		lux      int
		wantStep int
	}{
		{2, 0},
		{3, 1},
		{5, 2},
		{7, 3},
		{10, 4},
		{30, 6},
		{100, 12},
		{1000, 22},
		{2000, 25},
		{10000, 25}, // Daylight mode clamps to max step
	}

	for _, tt := range tests {
		got := LuxToStep(tt.lux)
		if got != tt.wantStep {
			t.Errorf("LuxToStep(%d) = %d, want %d", tt.lux, got, tt.wantStep)
		}
	}
}

func TestL625RawToLux(t *testing.T) {
	// Tests based on empirical measurements from Issue #75:
	// Raw = 250 - 10 * Index
	tests := []struct {
		raw     int
		wantLux int
	}{
		{250, 2},   // Index 0: 2 lx
		{240, 3},   // Index 1: 3 lx
		{230, 5},   // Index 2: 5 lx
		{220, 7},   // Index 3: 7 lx
		{210, 10},  // Index 4: 10 lx
		{190, 30},  // Index 6: 30 lx
		{130, 100}, // Index 12: 100 lx
		{60, 400},  // Index 19: 400 lx
		{40, 750},  // Index 21: 750 lx
		{30, 1000}, // Index 22: 1000 lx
		{10, 1600}, // Index 24: 1600 lx
		{0, 2000},  // Index 25: 2000 lx (Daylight mode / Tagbetrieb)
		{-1, 2000}, // Out of range negative -> Daylight mode
		{300, 2},   // Out of range high -> Night mode
	}

	for _, tt := range tests {
		got := L625RawToLux(tt.raw)
		if got != tt.wantLux {
			t.Errorf("L625RawToLux(%d) = %d lx, want %d lx", tt.raw, got, tt.wantLux)
		}
	}
}

func TestL625LuxToRaw(t *testing.T) {
	tests := []struct {
		lux     int
		wantRaw int
	}{
		{2, 250},   // Index 0
		{7, 220},   // Index 3
		{30, 190},  // Index 6
		{100, 130}, // Index 12
		{1000, 30}, // Index 22
		{2000, 0},  // Index 25 (Tagbetrieb)
		{10000, 0}, // Day mode (10000 lx in Steinel spec)
	}

	for _, tt := range tests {
		got := L625LuxToRaw(tt.lux)
		if got != tt.wantRaw {
			t.Errorf("L625LuxToRaw(%d lx) = raw %d, want %d", tt.lux, got, tt.wantRaw)
		}
	}
}

func TestBuildSetLuxThresholdCommand(t *testing.T) {
	// For 7 lx, expected raw is 220 (0x00DC) -> Command: 5A 03 04 00 DC -> CS = (5A+03+04+00+DC)%256 = 13D%256 = 3D
	b64, err := BuildSetLuxThreshold(7)
	if err != nil {
		t.Fatalf("BuildSetLuxThreshold(7) failed: %v", err)
	}
	rawBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("Failed to decode base64: %v", err)
	}
	hexStr := hex.EncodeToString(rawBytes)
	expectedPrefix := "5a030400dc"
	if len(hexStr) < 10 || hexStr[:10] != expectedPrefix {
		t.Errorf("BuildSetLuxThreshold(7) hex = %s, expected prefix %s", hexStr, expectedPrefix)
	}

	// For Tagbetrieb (2000 lx or 10000 lx), expected raw is 0 (0x0000) -> Command: 5A 03 04 00 00
	b64Day, err := BuildSetLuxThreshold(10000)
	if err != nil {
		t.Fatalf("BuildSetLuxThreshold(10000) failed: %v", err)
	}
	rawDayBytes, _ := base64.StdEncoding.DecodeString(b64Day)
	hexDay := hex.EncodeToString(rawDayBytes)
	expectedDayPrefix := "5a03040000"
	if len(hexDay) < 10 || hexDay[:10] != expectedDayPrefix {
		t.Errorf("BuildSetLuxThreshold(10000) hex = %s, expected prefix %s", hexDay, expectedDayPrefix)
	}
}
