package xiongmai

import (
	"fmt"
	"strings"
)

// MCUConfig represents the parsed MCU settings from the Steinel L 620 CAM.
// In the Android app (YTDevConfigInfo.java):
// Format: B<distance><hld><lux><mode><indicator><lowlight><highlight><lld><level>U
// Example default string: "BubzbzfzazOU"
type MCUConfig struct {
	Distance          int    // PIR distance (1 - 10 meters)
	HighlightDelaySec int    // Main light on-time in seconds (e.g. 5s - 900s)
	TwilightLux       int    // Twilight threshold in lux (2 - 1000)
	Mode              int    // Sensor mode preset
	Indicator         int    // LED indicator
	Lowlight          int    // Nightlight brightness (0 - 50%)
	Highlight         int    // Main light brightness (10 - 100%)
	LowlightDuration  string // Nightlight duration (e.g. "all_night", "4h", "off")
	SensitivityLevel  int    // Sensor sensitivity level
	RawString         string // The raw 12-char string
}

// Conversion converts an ASCII character from the MCU string to an integer value ('a' -> 0, 'b' -> 1, etc.)
func Conversion(c byte) int {
	if c >= 'a' && c <= 'z' {
		return int(c - 'a')
	}
	if c >= 'A' && c <= 'Z' {
		return int(c - 'A')
	}
	return 0
}

// CharConversion converts an integer value to an ASCII character (0 -> 'a', 1 -> 'b', etc.)
func CharConversion(val int) byte {
	if val < 0 {
		val = 0
	}
	if val > 25 {
		val = 25
	}
	return byte(val + 'a')
}

// ParseMCUString parses a 12-character MCU response string into an MCUConfig struct.
// Example: "BubzbzfzazOU"
func ParseMCUString(s string) (*MCUConfig, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "B") || !strings.HasSuffix(s, "U") || len(s) < 11 {
		return nil, fmt.Errorf("invalid MCU string format: %q (expected B...U with length >= 11)", s)
	}

	cfg := &MCUConfig{
		RawString: s,
	}

	// Index 1: Distance (e.g., 'u' -> 20 -> max distance)
	distVal := Conversion(s[1])
	if distVal >= 20 {
		cfg.Distance = 10
	} else if distVal >= 15 {
		cfg.Distance = 6
	} else {
		cfg.Distance = 3
	}

	// Index 2: Highlight Delay Time ('b' -> 1 -> 60s)
	hldVal := Conversion(s[2])
	cfg.HighlightDelaySec = (hldVal + 1) * 60
	if cfg.HighlightDelaySec == 0 {
		cfg.HighlightDelaySec = 60
	}

	// Index 3: Twilight Lux
	luxVal := Conversion(s[3])
	cfg.TwilightLux = mapLuxValue(luxVal)

	// Index 4: Mode
	cfg.Mode = Conversion(s[4])

	// Index 5: Indicator LED
	cfg.Indicator = Conversion(s[5])

	// Index 6: Lowlight (Nightlight Dimming)
	cfg.Lowlight = Conversion(s[6]) * 5 // e.g. 0-50%

	// Index 7: Highlight (Main Light Dimming)
	cfg.Highlight = Conversion(s[7]) * 10
	if cfg.Highlight == 0 {
		cfg.Highlight = 100
	}

	// Index 8: Lowlight Duration (lld)
	lldVal := Conversion(s[8])
	if lldVal == 0 {
		cfg.LowlightDuration = "off"
	} else if lldVal >= 20 {
		cfg.LowlightDuration = "all_night"
	} else {
		cfg.LowlightDuration = fmt.Sprintf("%dh", lldVal/2)
	}

	// Index 9: Sensitivity Level
	cfg.SensitivityLevel = Conversion(s[9])

	return cfg, nil
}

// Helper to map 0-25 character index to realistic Lux values
func mapLuxValue(idx int) int {
	if idx <= 0 {
		return 2
	}
	if idx >= 25 {
		return 1000
	}
	// Linear interpolation approximation: 2 to 1000 Lux
	return 2 + (idx * 40)
}

// BuildQueryMCUCommand returns the command to request the current MCU state: "BFbU"
func BuildQueryMCUCommand() string {
	return "BFbU"
}

// BuildSetLuxCommand builds the MCU command to set the twilight threshold: "BX<char>U"
func BuildSetLuxCommand(lux int) string {
	idx := (lux - 2) / 40
	return fmt.Sprintf("BX%cU", CharConversion(idx))
}

// BuildSetLowlightCommand builds the MCU command to set the nightlight brightness: "BL<char>U"
func BuildSetLowlightCommand(percent int) string {
	idx := percent / 5
	return fmt.Sprintf("BL%cU", CharConversion(idx))
}

// BuildSetLowlightDurationCommand builds the MCU command for nightlight duration: "BS<char>U"
func BuildSetLowlightDurationCommand(dur int) string {
	idx := dur
	return fmt.Sprintf("BS%cU", CharConversion(idx))
}

// BuildSetDistanceCommand builds the MCU command to set the PIR detection distance: "BD<char>U"
func BuildSetDistanceCommand(dist int) string {
	var idx int
	if dist >= 8 {
		idx = 20
	} else if dist >= 5 {
		idx = 15
	} else {
		idx = 10
	}
	return fmt.Sprintf("BD%cU", CharConversion(idx))
}

// BuildSetHighlightCommand builds the MCU command to set the main light brightness: "BH<char>U"
func BuildSetHighlightCommand(percent int) string {
	idx := percent / 10
	return fmt.Sprintf("BH%cU", CharConversion(idx))
}

// BuildSetHighlightDelayCommand builds the MCU command to set the main light delay: "BT<char>U"
func BuildSetHighlightDelayCommand(seconds int) string {
	idx := (seconds / 60) - 1
	if idx < 0 {
		idx = 0
	}
	return fmt.Sprintf("BT%cU", CharConversion(idx))
}
