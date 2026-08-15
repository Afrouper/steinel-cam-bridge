package mcu

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

type ConfigInfo struct {
	HVersion                  string
	SVersion                  string
	Mode                      int
	Lux                       int
	PIRActive                 bool
	MotionDetected            bool
	PhotosensitiveDetection   bool
	PIRSensitivity            int
	ColorTemp                 int
	Highlight                 int
	HighlightTime             int
	Lowlight                  int
	LowlightTime              int
}

// ParseFrame parses the 18-byte (36-hex) MCU frame starting with 5A0F0F
func ParseFrame(hexStr string) (*ConfigInfo, error) {
	hexStr = strings.ToLower(strings.TrimSpace(hexStr))
	if len(hexStr) != 36 || !strings.HasPrefix(hexStr, "5a0f0f") {
		return nil, fmt.Errorf("invalid MCU frame: %s", hexStr)
	}

	body := hexStr[6:34] // 28 chars
	if len(body) != 28 {
		return nil, fmt.Errorf("invalid body length: %s", body)
	}

	mode, _ := strconv.ParseInt(body[4:6], 16, 64)
	lux, _ := strconv.ParseInt(body[6:10], 16, 64)
	pirActive := body[10:11] == "1"

	tuxingVal, _ := strconv.ParseInt(body[11:12], 16, 64)
	motionDetected := (tuxingVal & 0x01) != 0
	photoDetection := (tuxingVal & 0x02) != 0

	pirSens, _ := strconv.ParseInt(body[12:14], 16, 64)
	colorTemp, _ := strconv.ParseInt(body[14:16], 16, 64)
	highlight, _ := strconv.ParseInt(body[16:18], 16, 64)
	highlightTime, _ := strconv.ParseInt(body[18:22], 16, 64)
	lowlight, _ := strconv.ParseInt(body[22:24], 16, 64)
	lowlightTime, _ := strconv.ParseInt(body[24:28], 16, 64)

	return &ConfigInfo{
		HVersion:                body[0:2],
		SVersion:                body[2:4],
		Mode:                    int(mode),
		Lux:                     int(lux),
		PIRActive:               pirActive,
		MotionDetected:          motionDetected,
		PhotosensitiveDetection: photoDetection,
		PIRSensitivity:          int(pirSens),
		ColorTemp:               int(colorTemp),
		Highlight:               int(highlight),
		HighlightTime:           int(highlightTime),
		Lowlight:                int(lowlight),
		LowlightTime:            int(lowlightTime),
	}, nil
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
	// CmdGetLightInfo: 5A 01 0F -> CS 6A -> "5A010F6A"
	CmdGetLightInfo = "5A010F"
	// CmdLightOn: 5A 02 01 01 -> CS 5E
	CmdLightOn = "5A020101"
	// CmdLightOff: 5A 02 01 00 -> CS 5D
	CmdLightOff = "5A020100"
	// CmdLightAuto: 5A 02 01 02 -> CS 5F
	CmdLightAuto = "5A020102"
)
