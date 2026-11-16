package mcu

// LuxTable defines the 26-level twilight threshold lookup table used by the Steinel CAM app (DetectSettingActivity.java).
// It maps the 26 discrete twilight steps to physical Lux values.
var LuxTable = [26]int{
	2, 3, 5, 7, 10, 18, 30, 38, 50, 58, 70, 85, 100,
	125, 150, 175, 200, 250, 300, 400, 500, 750, 1000, 1300, 1600, 2000,
}

// StepToLux maps a step index (0 to 25) to physical Lux.
// Index 0 corresponds to 2 lx (minimum threshold / maximum night sensitivity).
// Index 25 corresponds to 2000 lx (daylight mode / Tagbetrieb).
func StepToLux(step int) int {
	if step < 0 {
		step = 0
	} else if step >= len(LuxTable) {
		step = len(LuxTable) - 1
	}
	return LuxTable[step]
}

// LuxToStep finds the closest step index (0 to 25) for a given physical lux value using nearest-neighbor matching.
func LuxToStep(lux int) int {
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
	return bestIdx
}

// L625RawToLux converts a raw 16-bit integer from the L 625 MCU frame (5A0F0F...) into physical Lux.
// The L 625 MCU encodes the 26 steps on a step-of-10 grid:
// Raw = 250 - 10 * step (Index 0 [2 lx] -> 250, Index 3 [7 lx] -> 220, Index 12 [100 lx] -> 130, Index 25 [2000 lx / Tag] -> 0).
func L625RawToLux(raw int) int {
	if raw > 250 {
		return LuxTable[0] // 2 lx
	}
	if raw <= 0 {
		return LuxTable[len(LuxTable)-1] // 2000 lx (Day mode / Tagbetrieb)
	}
	// Round to the nearest step on the 10-grid
	step := (250 - raw + 5) / 10
	return StepToLux(step)
}

// L625LuxToRaw converts a physical Lux value into the 16-bit raw integer expected by the L 625 MCU (5A0304...).
// Physical Lux -> Nearest Step -> Raw = 250 - 10 * step.
// Daylight mode (>= 2000 lx or 10000 lx) maps to step 25 (Raw = 0).
func L625LuxToRaw(lux int) int {
	if lux >= 2000 {
		return 0 // Tagbetrieb
	}
	step := LuxToStep(lux)
	return 250 - (10 * step)
}
