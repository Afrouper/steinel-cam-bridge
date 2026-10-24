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

// LuxTable defines the 26-level twilight threshold lookup table used by the Steinel CAM app (DetectSettingActivity.java).
var LuxTable = [26]int{
	2, 3, 5, 7, 10, 18, 30, 38, 50, 58, 70, 85, 100,
	125, 150, 175, 200, 250, 300, 400, 500, 750, 1000, 1300, 1600, 2000,
}

// Helper to map 0-25 character conversion value to realistic Lux values according to the app lookup table.
// In the MCU, the index is inverted: progress = 25 - conv (e.g. 'n'=13 -> 25-13=12 -> 100 Lux; 'k'=10 -> 25-10=15 -> 175 Lux).
func mapLuxValue(conv int) int {
	progress := 25 - conv
	if progress < 0 {
		progress = 0
	} else if progress >= len(LuxTable) {
		progress = len(LuxTable) - 1
	}
	return LuxTable[progress]
}

// ParseMCUString parses a 12-character MCU response string into an MCUConfig struct.
// Example: "BubzbzfzazOU" or "BpbkbzacazOU"
func ParseMCUString(s string) (*MCUConfig, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "B") || !strings.HasSuffix(s, "U") || len(s) < 11 {
		return nil, fmt.Errorf("invalid MCU string format: %q (expected B...U with length >= 11)", s)
	}

	cfg := &MCUConfig{
		RawString: s,
	}

	// Index 1: Distance / PIR Sensitivity ('k'=10 Min -> 3m, 'p'=15 Med -> 6m, 'u'=20 Max -> 10m)
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
	cfg.HighlightDelaySec = hldVal * 60
	if cfg.HighlightDelaySec <= 0 {
		cfg.HighlightDelaySec = 60
	}

	// Index 3: Twilight Lux
	luxVal := Conversion(s[3])
	cfg.TwilightLux = mapLuxValue(luxVal)

	// Index 4: Mode (1 / 'b' = Sensor Auto, 3 / 'd' = Continuous / Dauerlicht, 4 / 'e' = Test)
	cfg.Mode = Conversion(s[4])

	// Index 5: Indicator LED
	cfg.Indicator = Conversion(s[5])

	// Index 6: Lowlight (Nightlight Dimming: 'a' = Off/0%, else 10-40%)
	lowVal := Conversion(s[6])
	if lowVal == 0 {
		cfg.Lowlight = 0
	} else {
		prog := lowVal - 6
		if prog < 0 {
			prog = 0
		}
		cfg.Lowlight = ((prog * 30) / 19) + 10
		if cfg.Lowlight > 50 {
			cfg.Lowlight = 50
		}
	}

	// Index 7: Highlight (Main Light Dimming: 30% to 100%, step: progress * 4%)
	highVal := Conversion(s[7])
	if highVal <= 7 {
		cfg.Highlight = 30
	} else {
		cfg.Highlight = highVal * 4
		if cfg.Highlight > 100 {
			cfg.Highlight = 100
		}
	}

	// Index 8: Lowlight Duration (lld: 'a' = all night / 24h)
	lldVal := Conversion(s[8])
	if lldVal == 0 || lldVal >= 20 {
		cfg.LowlightDuration = "all_night"
	} else {
		cfg.LowlightDuration = fmt.Sprintf("%dh", lldVal/2)
	}

	// Index 9: Sensitivity Level
	cfg.SensitivityLevel = Conversion(s[9])

	return cfg, nil
}

// BuildQueryMCUCommand returns the command to request the current MCU state: "BFbU"
func BuildQueryMCUCommand() string {
	return "BFbU"
}

// BuildSetLuxCommand builds the MCU command to set the twilight threshold: "BX<char>U"
func BuildSetLuxCommand(lux int) string {
	bestIdx := 0
	minDiff := 999999
	for i, val := range LuxTable {
		diff := val - lux
		if diff < 0 {
			diff = -diff
		}
		if diff < minDiff {
			minDiff = diff
			bestIdx = i
		}
	}
	conv := 25 - bestIdx
	return fmt.Sprintf("BX%cU", CharConversion(conv))
}

// BuildSetLowlightCommand builds the MCU command to set the nightlight brightness: "BL<char>U"
func BuildSetLowlightCommand(percent int) string {
	if percent <= 0 {
		return "BLaU"
	}
	prog := ((percent - 10) * 19) / 30
	if prog < 0 {
		prog = 0
	} else if prog > 19 {
		prog = 19
	}
	return fmt.Sprintf("BL%cU", CharConversion(prog+6))
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
		idx = 20 // 'u' (Maximum)
	} else if dist >= 5 {
		idx = 15 // 'p' (Medium)
	} else {
		idx = 10 // 'k' (Minimum)
	}
	return fmt.Sprintf("BD%cU", CharConversion(idx))
}

// BuildSetHighlightCommand builds the MCU command to set the main light brightness: "BH<char>U"
func BuildSetHighlightCommand(percent int) string {
	idx := percent / 4
	if idx < 7 {
		idx = 7
	} else if idx > 25 {
		idx = 25
	}
	return fmt.Sprintf("BH%cU", CharConversion(idx))
}

// BuildSetHighlightDelayCommand builds the MCU command to set the main light delay: "BT<char>U"
func BuildSetHighlightDelayCommand(seconds int) string {
	minutes := seconds / 60
	if minutes < 1 {
		minutes = 1
	}
	return fmt.Sprintf("BT%cU", CharConversion(minutes))
}

// BuildSetLightModeCommand builds the MCU command to set the light mode: "BM<char>U"
// On: Continuous Light (Mode 3, 'd') -> "BMdU"
// Off: Sensor Automatic (Mode 1, 'b') -> "BMbU"
func BuildSetLightModeCommand(on bool) string {
	if on {
		return "BMdU"
	}
	return "BMbU"
}

// BuildSetTestModeCommand builds the MCU command for sensor walk test mode (Mode 4, 'e'): "BMeU"
func BuildSetTestModeCommand() string {
	return "BMeU"
}

// IsLightOn returns true if the MCU is currently in continuous light mode (Mode 3, 'd').
func (c *MCUConfig) IsLightOn() bool {
	if c == nil {
		return false
	}
	return c.Mode == 3
}
