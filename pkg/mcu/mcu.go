package mcu

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

type ConfigInfo struct {
	HVersion                string
	SVersion                string
	Mode                    int
	Lux                     int
	PIRActive               bool
	MotionDetected          bool
	PhotosensitiveDetection bool
	PIRSensitivity          int
	ColorTemp               int
	Highlight               int
	HighlightTime           int
	Lowlight                int
	LowlightTime            int
}

// ParseFrame parses the 18-byte (36-hex) MCU frame starting with 5A0F0F
func ParseFrame(hexStr string) (*ConfigInfo, error) {
	hexStr = strings.ToLower(strings.TrimSpace(hexStr))
	if len(hexStr) != 36 || !strings.HasPrefix(hexStr, "5a0f0f") {
		if strings.HasPrefix(hexStr, "5a") && len(hexStr) < 36 {
			// Command ACK/Echo frame from MCU (e.g. Light, Lux, Dimmer)
			return nil, nil
		}
		return nil, fmt.Errorf("invalid MCU frame: %s", hexStr)
	}

	body := hexStr[6:34] // 28 chars
	if len(body) != 28 {
		return nil, fmt.Errorf("invalid body length: %s", body)
	}

	mode := parseHexInt(body[4:6])
	lux := parseHexInt(body[6:10])
	pirActive := body[10:11] == "1"

	tuxingVal := parseHexInt(body[11:12])
	motionDetected := (tuxingVal & 0x01) != 0
	photoDetection := (tuxingVal & 0x02) != 0

	pirSens := parseHexInt(body[12:14])
	colorTemp := parseHexInt(body[14:16])
	highlight := parseHexInt(body[16:18])
	highlightTime := parseHexInt(body[18:22])
	lowlight := parseHexInt(body[22:24])
	lowlightTime := parseHexInt(body[24:28])

	return &ConfigInfo{
		HVersion:                body[0:2],
		SVersion:                body[2:4],
		Mode:                    mode,
		Lux:                     lux,
		PIRActive:               pirActive,
		MotionDetected:          motionDetected,
		PhotosensitiveDetection: photoDetection,
		PIRSensitivity:          pirSens,
		ColorTemp:               colorTemp,
		Highlight:               highlight,
		HighlightTime:           highlightTime,
		Lowlight:                lowlight,
		LowlightTime:            lowlightTime,
	}, nil
}

func parseHexInt(hexStr string) int {
	val, err := strconv.ParseInt(hexStr, 16, 0)
	if err != nil {
		return 0
	}
	return int(val)
}

// ParseBase64Data parses a Base64-encoded MCU frame (from DataChannel JSON)
func ParseBase64Data(b64 string) (*ConfigInfo, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	hexStr := hex.EncodeToString(data)
	return ParseFrame(hexStr)
}

// Checksum calculates the Steinel MCU checksum (Sum of bytes mod 256)
func Checksum(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return sum
}

// BuildCommand creates a complete 5A hex frame with checksum and returns Base64 payload
func BuildCommand(cmdPayloadHex string) (string, error) {
	raw, err := hex.DecodeString(cmdPayloadHex)
	if err != nil {
		return "", err
	}
	cs := Checksum(raw)
	full := append(raw, cs)
	return base64.StdEncoding.EncodeToString(full), nil
}

// Helper commands
const (
	// CmdGetLightInfo: 5A 01 0F 6A -> CS D4 -> "5A010F6AD4"
	CmdGetLightInfo = "5A010F6A"
	// CmdLightOn: 5A 02 01 01 -> CS 5E
	CmdLightOn = "5A020101"
	// CmdLightOff: 5A 02 01 00 -> CS 5D
	CmdLightOff = "5A020100"
	// CmdLightAuto: 5A 02 01 02 -> CS 5F
	CmdLightAuto = "5A020102"
)

// BuildSetMode creates command for mode: 0 = Off, 1 = On, 2 = Sensor Auto
func BuildSetMode(mode int) (string, error) {
	if mode < 0 || mode > 2 {
		return "", fmt.Errorf("invalid mode %d (must be 0, 1, 2)", mode)
	}
	return BuildCommand(fmt.Sprintf("5A0201%02X", mode))
}

// BuildSetHighlight creates command for highlight brightness (10 - 100%)
func BuildSetHighlight(percent int) (string, error) {
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	return BuildCommand(fmt.Sprintf("5A0207%02X", percent))
}

// BuildSetHighlightTime creates command for highlight duration in seconds (5 - 900s)
func BuildSetHighlightTime(seconds int) (string, error) {
	if seconds < 5 {
		seconds = 5
	} else if seconds > 900 {
		seconds = 900
	}
	return BuildCommand(fmt.Sprintf("5A0308%04X", seconds))
}

// BuildSetLowlight creates command for lowlight / base light brightness (0 - 50%)
func BuildSetLowlight(percent int) (string, error) {
	if percent < 0 {
		percent = 0
	} else if percent > 50 {
		percent = 50
	}
	return BuildCommand(fmt.Sprintf("5A020A%02X", percent))
}

// BuildSetLowlightTime creates command for lowlight duration
func BuildSetLowlightTime(timeVal int) (string, error) {
	return BuildCommand(fmt.Sprintf("5A030B%04X", timeVal))
}

// BuildSetPIRSensitivity creates command for PIR sensitivity (0 - 100%)
func BuildSetPIRSensitivity(percent int) (string, error) {
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	return BuildCommand(fmt.Sprintf("5A0206%02X", percent))
}

// BuildSetLuxThreshold creates command for ambient light trigger threshold in Lux (2 - 1000 lx, 10000 = day)
func BuildSetLuxThreshold(lux int) (string, error) {
	if lux < 2 {
		lux = 2
	} else if lux > 10000 {
		lux = 10000
	}
	return BuildCommand(fmt.Sprintf("5A0304%04X", lux))
}
