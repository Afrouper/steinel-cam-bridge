package xiongmai

import (
	"encoding/binary"
	"fmt"
)

// Sofia Protocol Message Header (20 bytes)
// Byte 0: 0xFF (Head magic)
// Byte 1: 0x00 (Version/Channel)
// Bytes 2-3: 0x00 0x00 (Reserved)
// Bytes 4-7: SessionID (uint32 LE)
// Bytes 8-11: SequenceNumber (uint32 LE)
// Byte 12: TotalPacket (uint8, default 0)
// Byte 13: CurPacket (uint8, default 0)
// Bytes 14-15: MsgId (uint16 LE)
// Bytes 16-19: DataLength (uint32 LE)
const (
	HeaderMagic  byte = 0xFF
	HeaderLength int  = 20
	DefaultPort  int  = 34567
	RTSPPort     int  = 554
)

// Xiongmai Message IDs
const (
	MsgLoginReq          uint16 = 1000
	MsgLoginResp         uint16 = 1001
	MsgLogoutReq         uint16 = 1002
	MsgKeepAliveReq      uint16 = 1006
	MsgKeepAliveResp     uint16 = 1007
	MsgSysManagerReq     uint16 = 1020
	MsgSysManagerResp    uint16 = 1021
	MsgConfigSetReq      uint16 = 1040
	MsgConfigSetResp     uint16 = 1041
	MsgConfigGetReq      uint16 = 1042
	MsgConfigGetResp     uint16 = 1043
	MsgTalkClaimReq      uint16 = 1410
	MsgTalkClaimResp     uint16 = 1411
	MsgTalkSendData      uint16 = 1412
	MsgTalkControlReq    uint16 = 1430
	MsgTalkControlResp   uint16 = 1431
	MsgTalkAudioData     uint16 = 1432
	MsgTalkAudioDataResp uint16 = 1433
	MsgTalkClaimV2Req    uint16 = 1434
	MsgTalkClaimV2Resp   uint16 = 1435
	MsgFileSearchReq     uint16 = 1440
	MsgFileSearchResp    uint16 = 1441
	MsgFilePlayReq       uint16 = 1442
	MsgFilePlayResp      uint16 = 1443
	MsgAlarmReq          uint16 = 1500
	MsgAlarmResp         uint16 = 1501
	MsgSearchDeviceReq   uint16 = 1530
	MsgSearchDeviceResp  uint16 = 1531
	MsgTransSendData     uint16 = 1572
	MsgTransSendResp     uint16 = 1573
	MsgTransStartReq     uint16 = 1578
	MsgTransStartResp    uint16 = 1579
)

// Header represents the 20-byte Sofia protocol message header.
type Header struct {
	Magic      byte
	Channel    byte
	Reserved   [2]byte
	SessionID  uint32
	Sequence   uint32
	TotalPkt   byte
	CurPkt     byte
	MsgID      uint16
	DataLength uint32
}

// Encode serializes the Header into a 20-byte slice.
func (h *Header) Encode() []byte {
	buf := make([]byte, HeaderLength)
	buf[0] = h.Magic
	buf[1] = h.Channel
	buf[2] = h.Reserved[0]
	buf[3] = h.Reserved[1]
	binary.LittleEndian.PutUint32(buf[4:8], h.SessionID)
	binary.LittleEndian.PutUint32(buf[8:12], h.Sequence)
	buf[12] = h.TotalPkt
	buf[13] = h.CurPkt
	binary.LittleEndian.PutUint16(buf[14:16], h.MsgID)
	binary.LittleEndian.PutUint32(buf[16:20], h.DataLength)
	return buf
}

// DecodeHeader parses a 20-byte slice into a Header struct.
func DecodeHeader(data []byte) (*Header, error) {
	if len(data) < HeaderLength {
		return nil, fmt.Errorf("data too short for header: %d < %d", len(data), HeaderLength)
	}
	if data[0] != HeaderMagic {
		return nil, fmt.Errorf("invalid header magic: 0x%02X, expected 0x%02X", data[0], HeaderMagic)
	}
	return &Header{
		Magic:      data[0],
		Channel:    data[1],
		Reserved:   [2]byte{data[2], data[3]},
		SessionID:  binary.LittleEndian.Uint32(data[4:8]),
		Sequence:   binary.LittleEndian.Uint32(data[8:12]),
		TotalPkt:   data[12],
		CurPkt:     data[13],
		MsgID:      binary.LittleEndian.Uint16(data[14:16]),
		DataLength: binary.LittleEndian.Uint32(data[16:20]),
	}, nil
}

// Login JSON Request (DVRIP / Sofia standard flat structure on MsgID 1000)
type LoginReq struct {
	EncryptType string `json:"EncryptType"`
	LoginType   string `json:"LoginType,omitempty"`
	PassWord    string `json:"PassWord"`
	UserName    string `json:"UserName"`
}

// Login JSON Response
type LoginResp struct {
	Name      string `json:"Name"`
	Ret       int    `json:"Ret"`
	SessionID string `json:"SessionID"`
}

// RTSP Config JSON for automatic enablement
type RTSPConfigReq struct {
	Name        string     `json:"Name"`
	NetWorkRTSP RTSPServer `json:"NetWork.RTSP"`
}

type RTSPServer struct {
	IsServer bool `json:"IsServer"`
}

// Light Switch JSON (FbExtraStateCtrl)
type LightCtrlReq struct {
	Name             string              `json:"Name"`
	FbExtraStateCtrl FbExtraStateCtrlVal `json:"FbExtraStateCtrl"`
}

type FbExtraStateCtrlVal struct {
	IsOn         int `json:"ison"`
	PlayVoiceTip int `json:"PlayVoiceTip,omitempty"`
}

// Serial Ports Info JSON (MCU Tunneling)
type SerialPortsReq struct {
	Name            string          `json:"Name"`
	SerialPortsInfo SerialPortsData `json:"SerialPortsInfo"`
}

type SerialPortsData struct {
	SerialPortsType int    `json:"SerialPortsType"` // 0 = RS232/MCU
	SerialPortsData string `json:"SerialPortsData"` // e.g. "BFbU"
}

// OPTalk JSON Request for 2-Way Audio Claim
type OPTalkReq struct {
	Name      string     `json:"Name"`
	OPTalk    OPTalkInfo `json:"OPTalk"`
	SessionID string     `json:"SessionID,omitempty"`
}

type OPTalkInfo struct {
	Action string `json:"Action"` // "Start", "Stop", "PauseUpload", "ResumeUpload"
}

// KeepAlive JSON
type KeepAliveReq struct {
	Name        string         `json:"Name"`
	OPKeepAlive OPKeepAliveVal `json:"OPKeepAlive"`
}

type OPKeepAliveVal struct {
	Time string `json:"Time,omitempty"`
}

// Alarm Event Push JSON
type AlarmPushResp struct {
	Name      string        `json:"Name"`
	AlarmInfo AlarmInfoData `json:"AlarmInfo,omitempty"`
	Event     string        `json:"Event,omitempty"`
	Status    string        `json:"Status,omitempty"`
}

type AlarmInfoData struct {
	Event     string `json:"Event"` // "HumanDetect", "MotionDetect"
	State     string `json:"State"` // "Start", "Stop"
	Channel   int    `json:"Channel"`
	StartTime string `json:"StartTime"`
}
