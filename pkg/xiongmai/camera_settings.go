package xiongmai

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

// Timestamp overlay corner positions matching the bridge-wide OSD convention.
const (
	OSDOff         = 0
	OSDTopLeft     = 1
	OSDBottomLeft  = 2
	OSDTopRight    = 3
	OSDBottomRight = 4
)

// SD card recording mode identifiers matching the bridge-wide RecordMode convention.
const (
	RecordModeCamera      = 2
	RecordModeCameraOrPIR = 3
	RecordModeNone        = 4
)

// SofiaDeviceStatus holds diagnostic and configuration values queried from
// Xiongmai Sofia system and configuration endpoints.
type SofiaDeviceStatus struct {
	FirmwareVer   string
	SerialNo      string
	SDTotalMB     int
	SDFreeMB      int
	SDStatus      string
	SpeakerVolume int
	OSDPosition   int
	DetectMode    string
	RecordMode    int
	SirenEnabled  bool
	SirenKnown    bool
}

// QueryDeviceStatus reads firmware, SD card storage, audio volume, OSD timestamp,
// motion detection, and recording mode from the camera's Sofia endpoints.
// Unreachable optional endpoints leave their corresponding fields at sentinel values
// (SpeakerVolume = -1, OSDPosition = -1) so callers only apply known fields.
// Individual unsupported endpoints are skipped; only a dropped connection aborts early.
func (c *Client) QueryDeviceStatus() (*SofiaDeviceStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || !c.isLoggedIn {
		return nil, errors.New("xiongmai: client not connected")
	}

	st := &SofiaDeviceStatus{
		SpeakerVolume: -1,
		OSDPosition:   -1,
	}

	queries := []func(*SofiaDeviceStatus){
		c.queryVolumeLocked,
		c.queryVideoWidgetLocked,
		c.queryMotionDetectLocked,
		c.queryRecordConfigLocked,
		c.querySystemInfoLocked,
		c.queryStorageInfoLocked,
	}
	for _, query := range queries {
		if c.conn == nil || !c.isLoggedIn {
			return st, errors.New("xiongmai: connection lost during device status query")
		}
		query(st)
	}
	if c.conn == nil || !c.isLoggedIn {
		return st, errors.New("xiongmai: connection lost during device status query")
	}

	return st, nil
}

func (c *Client) querySystemInfoLocked(st *SofiaDeviceStatus) {
	raw, err := c.getSysInfoObjectLocked("SystemInfo")
	if err != nil {
		logger.Debug("Xiongmai", "SystemInfo query skipped: %v", err)
		return
	}
	if fw, ok := raw["SoftWareVersion"].(string); ok && fw != "" {
		st.FirmwareVer = fw
	}
	if sn, ok := raw["SerialNo"].(string); ok && sn != "" {
		st.SerialNo = sn
	}
}

func (c *Client) queryStorageInfoLocked(st *SofiaDeviceStatus) {
	reqPayload, err := json.Marshal(map[string]interface{}{
		"Name":      "StorageInfo",
		"SessionID": fmt.Sprintf("0x%08X", c.sessionID),
	})
	if err != nil {
		return
	}
	resp, err := c.sendPacketLocked(MsgSysManagerReq, reqPayload)
	if err != nil {
		logger.Debug("Xiongmai", "StorageInfo query skipped: %v", err)
		return
	}

	var parsed struct {
		Ret         int `json:"Ret"`
		StorageInfo []struct {
			Partition []struct {
				DriverType  int    `json:"DirverType"`
				IsCurrent   bool   `json:"IsCurrent"`
				Status      int    `json:"Status"`
				TotalSpace  string `json:"TotalSpace"`
				RemainSpace string `json:"RemainSpace"`
			} `json:"Partition"`
		} `json:"StorageInfo"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil || (parsed.Ret != 100 && parsed.Ret != 0) {
		return
	}

	bestTotal := 0
	bestFree := 0
	bestStatus := -1
	bestDriverType := -1
	for _, disk := range parsed.StorageInfo {
		for _, part := range disk.Partition {
			total := parseSofiaHexOrDecInt(part.TotalSpace)
			if total <= 0 {
				continue
			}
			free := parseSofiaHexOrDecInt(part.RemainSpace)
			// Prefer primary recording partition (DirverType == 0) over others, then largest partition.
			if bestTotal == 0 ||
				(part.DriverType == 0 && bestDriverType != 0) ||
				((part.DriverType == 0) == (bestDriverType == 0) && total > bestTotal) {
				bestTotal = total
				bestFree = free
				bestStatus = part.Status
				bestDriverType = part.DriverType
			}
		}
	}

	if bestTotal <= 0 {
		st.SDStatus = "none"
		return
	}
	if bestFree > bestTotal {
		bestFree = bestTotal
	}
	st.SDTotalMB = bestTotal
	st.SDFreeMB = bestFree
	if bestStatus == 0 {
		st.SDStatus = "ok"
	} else {
		st.SDStatus = "error"
	}
}

func (c *Client) queryVolumeLocked(st *SofiaDeviceStatus) {
	obj, err := c.getConfigObjectLocked("fVideo.Volume.[0]", "fVideo.Volume")
	if err != nil {
		return
	}
	if vol, ok := jsonNumberToInt(obj["LeftVolume"]); ok && vol >= 0 && vol <= 100 {
		st.SpeakerVolume = vol
		return
	}
	if vol, ok := jsonNumberToInt(obj["RightVolume"]); ok && vol >= 0 && vol <= 100 {
		st.SpeakerVolume = vol
	}
}

func (c *Client) queryVideoWidgetLocked(st *SofiaDeviceStatus) {
	obj, err := c.getConfigObjectLocked("AVEnc.VideoWidget.[0]", "AVEnc.VideoWidget")
	if err != nil {
		return
	}
	timeAttr, ok := obj["TimeTitleAttribute"].(map[string]interface{})
	if !ok {
		return
	}
	encodeBlend, _ := timeAttr["EncodeBlend"].(bool)
	if !encodeBlend {
		st.OSDPosition = OSDOff
		return
	}
	st.OSDPosition = OSDTopRight
	if relPos, ok := timeAttr["RelativePos"].([]interface{}); ok && len(relPos) >= 2 {
		x, xOK := jsonNumberToInt(relPos[0])
		y, yOK := jsonNumberToInt(relPos[1])
		if xOK && yOK {
			st.OSDPosition = relativePosToOSDPosition(x, y)
		}
	}
}

func (c *Client) queryMotionDetectLocked(st *SofiaDeviceStatus) {
	obj, err := c.getConfigObjectLocked("Detect.MotionDetect.[0]", "Detect.MotionDetect")
	if err != nil {
		return
	}
	if enabled, ok := obj["Enable"].(bool); ok {
		if enabled {
			st.DetectMode = "ai"
		} else {
			st.DetectMode = "off"
		}
	}
	if eh, ok := obj["EventHandler"].(map[string]interface{}); ok {
		voiceOn, _ := eh["VoiceEnable"].(bool)
		beepOn, _ := eh["BeepEnable"].(bool)
		st.SirenEnabled = voiceOn || beepOn
		st.SirenKnown = true
	}
}

func (c *Client) queryRecordConfigLocked(st *SofiaDeviceStatus) {
	obj, err := c.getConfigObjectLocked("Record.[0]", "Record")
	if err != nil {
		return
	}
	modeStr, _ := obj["RecordMode"].(string)
	if strings.EqualFold(modeStr, "Closed") || strings.EqualFold(modeStr, "Off") {
		st.RecordMode = RecordModeNone
	} else if modeStr != "" {
		st.RecordMode = RecordModeCameraOrPIR
	}
}

// SetSpeakerVolume sets the camera speaker output volume (0-100%) via Sofia fVideo.Volume.[0].
func (c *Client) SetSpeakerVolume(percent int) error {
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	obj, err := c.getConfigObjectLocked("fVideo.Volume.[0]", "fVideo.Volume")
	if err != nil || obj == nil {
		obj = map[string]interface{}{
			"AudioMode": "Single",
		}
	}
	obj["LeftVolume"] = percent
	obj["RightVolume"] = percent

	if err := c.setConfigObjectLocked("fVideo.Volume.[0]", obj); err != nil {
		return fmt.Errorf("xiongmai: set speaker volume failed: %w", err)
	}
	logger.Info("Xiongmai", "🔊 Set speaker volume to %d%%", percent)
	return nil
}

// SetTimestampPosition switches the timestamp overlay off (0) or moves it to a corner (1-4)
// via Sofia AVEnc.VideoWidget.[0].TimeTitleAttribute.
func (c *Client) SetTimestampPosition(position int) error {
	if position < OSDOff || position > OSDBottomRight {
		return fmt.Errorf("xiongmai: invalid timestamp position %d (allowed: 0-4)", position)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	obj, err := c.getConfigObjectLocked("AVEnc.VideoWidget.[0]", "AVEnc.VideoWidget")
	if err != nil {
		return fmt.Errorf("xiongmai: read AVEnc.VideoWidget.[0] failed: %w", err)
	}

	timeAttr, _ := obj["TimeTitleAttribute"].(map[string]interface{})
	if timeAttr == nil {
		timeAttr = map[string]interface{}{
			"BackColor":  "0x80000000",
			"FrontColor": "0xF0FFFFFF",
		}
	}

	if position == OSDOff {
		timeAttr["EncodeBlend"] = false
		timeAttr["PreviewBlend"] = false
	} else {
		timeAttr["EncodeBlend"] = true
		timeAttr["PreviewBlend"] = true
		w, h := 255, 24
		if existingPos, ok := timeAttr["RelativePos"].([]interface{}); ok && len(existingPos) >= 4 {
			if ew, okW := jsonNumberToInt(existingPos[2]); okW && ew > 0 {
				w = ew
			}
			if eh, okH := jsonNumberToInt(existingPos[3]); okH && eh > 0 {
				h = eh
			}
		}
		x, y := osdPositionToRelativePos(position)
		timeAttr["RelativePos"] = []int{x, y, w, h}
	}
	obj["TimeTitleAttribute"] = timeAttr

	if err := c.setConfigObjectLocked("AVEnc.VideoWidget.[0]", obj); err != nil {
		return fmt.Errorf("xiongmai: set timestamp position failed: %w", err)
	}
	logger.Info("Xiongmai", "🕒 Set timestamp overlay position to %d", position)
	return nil
}

// SetDetectMode enables ("ai") or disables ("off") camera-side motion detection
// via Sofia Detect.MotionDetect.[0].
func (c *Client) SetDetectMode(mode string) error {
	var enable bool
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ai", "on", "true", "1":
		enable = true
	case "off", "aus", "false", "0":
		enable = false
	default:
		return fmt.Errorf("xiongmai: unknown detection mode %q", mode)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	obj, err := c.getConfigObjectLocked("Detect.MotionDetect.[0]", "Detect.MotionDetect")
	if err != nil {
		return fmt.Errorf("xiongmai: read Detect.MotionDetect.[0] failed: %w", err)
	}
	obj["Enable"] = enable

	if err := c.setConfigObjectLocked("Detect.MotionDetect.[0]", obj); err != nil {
		return fmt.Errorf("xiongmai: set detection mode failed: %w", err)
	}
	logger.Info("Xiongmai", "🎯 Set camera motion detection: enable=%v (mode=%q)", enable, mode)
	return nil
}

// SetRecordMode configures the SD card recording mode (4 = off / "Closed", 2 or 3 = motion / "ConfigRecord")
// via Sofia Record.[0] and Detect.MotionDetect.[0].
func (c *Client) SetRecordMode(mode int) error {
	if mode != RecordModeNone && mode != RecordModeCamera && mode != RecordModeCameraOrPIR {
		return fmt.Errorf("xiongmai: invalid record mode %d (allowed: 2, 3, 4)", mode)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	recObj, err := c.getConfigObjectLocked("Record.[0]", "Record")
	if err != nil {
		return fmt.Errorf("xiongmai: read Record.[0] failed: %w", err)
	}

	enableRecord := mode != RecordModeNone
	if enableRecord {
		recObj["RecordMode"] = "ConfigRecord"
	} else {
		recObj["RecordMode"] = "Closed"
	}

	if err := c.setConfigObjectLocked("Record.[0]", recObj); err != nil {
		return fmt.Errorf("xiongmai: set Record.[0] failed: %w", err)
	}

	if mdObj, mdErr := c.getConfigObjectLocked("Detect.MotionDetect.[0]", "Detect.MotionDetect"); mdErr == nil && mdObj != nil {
		if eh, ok := mdObj["EventHandler"].(map[string]interface{}); ok {
			eh["RecordEnable"] = enableRecord
			mdObj["EventHandler"] = eh
			_ = c.setConfigObjectLocked("Detect.MotionDetect.[0]", mdObj)
		}
	}

	logger.Info("Xiongmai", "💾 Set SD card recording mode to %d (RecordMode=%s)", mode, recObj["RecordMode"])
	return nil
}

// SetSiren enables or disables the camera's motion alarm voice/buzzer output
// in Sofia Detect.MotionDetect.[0].EventHandler.
func (c *Client) SetSiren(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	obj, err := c.getConfigObjectLocked("Detect.MotionDetect.[0]", "Detect.MotionDetect")
	if err != nil {
		return fmt.Errorf("xiongmai: read Detect.MotionDetect.[0] failed: %w", err)
	}
	eh, _ := obj["EventHandler"].(map[string]interface{})
	if eh == nil {
		eh = make(map[string]interface{})
	}
	eh["VoiceEnable"] = on
	eh["BeepEnable"] = on
	obj["EventHandler"] = eh

	if err := c.setConfigObjectLocked("Detect.MotionDetect.[0]", obj); err != nil {
		return fmt.Errorf("xiongmai: set siren state failed: %w", err)
	}
	logger.Info("Xiongmai", "🚨 Set motion alarm siren/voice state: on=%v", on)
	return nil
}

// Reboot sends an OPMachine Reboot command (MsgID 1450) to restart the camera.
func (c *Client) Reboot() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || !c.isLoggedIn {
		return errors.New("xiongmai: client not connected")
	}

	reqPayload, err := json.Marshal(map[string]interface{}{
		"Name": "OPMachine",
		"OPMachine": map[string]interface{}{
			"Action": "Reboot",
		},
		"SessionID": fmt.Sprintf("0x%08X", c.sessionID),
	})
	if err != nil {
		return err
	}

	if _, err := c.sendPacketLocked(MsgTimeSettingReq, reqPayload); err != nil {
		return fmt.Errorf("xiongmai: reboot command failed: %w", err)
	}
	logger.Info("Xiongmai", "🔄 Camera reboot command acknowledged")
	return nil
}

func (c *Client) getSysInfoObjectLocked(name string) (map[string]interface{}, error) {
	if c.conn == nil || !c.isLoggedIn {
		return nil, errors.New("xiongmai: client not connected")
	}
	reqPayload, err := json.Marshal(map[string]interface{}{
		"Name":      name,
		"SessionID": fmt.Sprintf("0x%08X", c.sessionID),
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.sendPacketLocked(MsgSysManagerReq, reqPayload)
	if err != nil {
		return nil, err
	}
	return extractSofiaObject(resp, name)
}

type sofiaRetError struct {
	key string
	ret int
}

func (e *sofiaRetError) Error() string {
	return fmt.Sprintf("camera returned ret=%d for %s", e.ret, e.key)
}

func (c *Client) getConfigObjectLocked(primaryName, fallbackName string) (map[string]interface{}, error) {
	if c.conn == nil || !c.isLoggedIn {
		return nil, errors.New("xiongmai: client not connected")
	}
	obj, err := c.queryConfigByNameLocked(primaryName)
	if err == nil && obj != nil {
		return obj, nil
	}
	var retErr *sofiaRetError
	if fallbackName != "" && fallbackName != primaryName && errors.As(err, &retErr) {
		return c.queryConfigByNameLocked(fallbackName)
	}
	return nil, err
}

func (c *Client) queryConfigByNameLocked(name string) (map[string]interface{}, error) {
	reqPayload, err := json.Marshal(map[string]interface{}{
		"Name":      name,
		"SessionID": fmt.Sprintf("0x%08X", c.sessionID),
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.sendPacketLocked(MsgConfigGetReq, reqPayload)
	if err != nil {
		return nil, err
	}
	return extractSofiaObject(resp, name)
}

func (c *Client) setConfigObjectLocked(name string, value map[string]interface{}) error {
	if c.conn == nil || !c.isLoggedIn {
		return errors.New("xiongmai: client not connected")
	}
	reqPayload, err := json.Marshal(map[string]interface{}{
		"Name":      name,
		"SessionID": fmt.Sprintf("0x%08X", c.sessionID),
		name:        value,
	})
	if err != nil {
		return err
	}
	resp, err := c.sendPacketLocked(MsgConfigSetReq, reqPayload)
	if err != nil {
		return err
	}
	var res struct {
		Ret int `json:"Ret"`
	}
	if err := json.Unmarshal(resp, &res); err != nil {
		return err
	}
	if res.Ret != 100 && res.Ret != 0 {
		return fmt.Errorf("camera returned ret=%d for %s", res.Ret, name)
	}
	return nil
}

func extractSofiaObject(resp []byte, key string) (map[string]interface{}, error) {
	var top map[string]interface{}
	if err := json.Unmarshal(resp, &top); err != nil {
		return nil, err
	}
	if ret, ok := jsonNumberToInt(top["Ret"]); ok && ret != 100 && ret != 0 {
		return nil, &sofiaRetError{key: key, ret: ret}
	}
	raw, exists := top[key]
	if !exists {
		return nil, fmt.Errorf("missing key %s in response", key)
	}
	if obj, ok := raw.(map[string]interface{}); ok {
		return obj, nil
	}
	if arr, ok := raw.([]interface{}); ok && len(arr) > 0 {
		if obj, ok := arr[0].(map[string]interface{}); ok {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("unexpected type for %s in response", key)
}

func parseSofiaHexOrDecInt(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		if v, err := strconv.ParseInt(s[2:], 16, 32); err == nil && v >= 0 && v <= math.MaxInt32 {
			return int(v)
		}
	}
	if v, err := strconv.Atoi(s); err == nil && v >= 0 {
		return v
	}
	return 0
}

func jsonNumberToInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// relativePosToOSDPosition maps Xiongmai 0..8192 screen coordinates to a corner index (1..4).
func relativePosToOSDPosition(x, y int) int {
	const midpoint = 4096
	if x < midpoint {
		if y < midpoint {
			return OSDTopLeft
		}
		return OSDBottomLeft
	}
	if y < midpoint {
		return OSDTopRight
	}
	return OSDBottomRight
}

// osdPositionToRelativePos maps a corner index (1..4) to Xiongmai 0..8192 coordinates.
func osdPositionToRelativePos(position int) (int, int) {
	switch position {
	case OSDTopLeft:
		return 512, 512
	case OSDBottomLeft:
		return 512, 7552
	case OSDBottomRight:
		return 5166, 7552
	default:
		return 5166, 512
	}
}
