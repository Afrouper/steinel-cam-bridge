package xiongmai

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/pion/rtp"
)

// TalkClient handles 2-Way Audio (Gegensprechen) using the Xiongmai OPTalk protocol on port 34567.
type TalkClient struct {
	client      *Client
	talkActive  bool
	audioFormat string
	mu          sync.Mutex
	lastAudio   time.Time
}

// NewTalkClient creates a new 2-Way Talk client.
func NewTalkClient(client *Client) *TalkClient {
	return &TalkClient{
		client: client,
	}
}

// StartTalk sends the OPTalk claim and start control message to the camera to open the speaker audio channel.
func (t *TalkClient) StartTalk() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.startTalkLocked()
}

// SendAudioPacket converts an incoming RTP G.711 audio packet from the RTSP Backchannel
// into a Xiongmai talk data frame and sends it to the camera speaker.
func (t *TalkClient) SendAudioPacket(pkt *rtp.Packet) error {
	if pkt == nil || len(pkt.Payload) == 0 {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Automatically open talk channel on first audio packet if not yet active
	if !t.talkActive {
		if err := t.startTalkLocked(); err != nil {
			return err
		}
	}

	t.lastAudio = time.Now()

	// Adapt audio format: Scrypted/HomeKit sends G.711 u-law (PCMU, PT=0),
	// while Steinel L 620 CAM speaker DAC expects G.711 A-law (PCMA, PT=8).
	audioPayload := pkt.Payload
	if (t.audioFormat == "G711_ALAW" || t.audioFormat == "") && pkt.PayloadType == 0 {
		audioPayload = audio.TranscodePCMUToPCMA(pkt.Payload)
	} else if t.audioFormat == "G711_ULAW" && pkt.PayloadType == 8 {
		audioPayload = audio.TranscodePCMAToPCMU(pkt.Payload)
	}

	t.client.mu.Lock()
	defer t.client.mu.Unlock()

	if t.client.conn == nil {
		return errors.New("connection closed")
	}

	t.client.sequence++
	hdr := Header{
		Magic:      HeaderMagic,
		Channel:    0, // Raw audio samples are unencrypted (whitelisted in notEncryptMsgIDs)
		SessionID:  t.client.sessionID,
		Sequence:   t.client.sequence,
		MsgID:      MsgTalkAudioData,
		DataLength: uint32(len(audioPayload)),
	}

	packet := append(hdr.Encode(), audioPayload...)

	_ = t.client.conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	_, err := t.client.conn.Write(packet)
	return err
}

func (t *TalkClient) startTalkLocked() error {
	if t.talkActive {
		return nil
	}

	logger.Debug("Xiongmai Talk", "🎙️ Claiming audio talk channel (MsgTalkClaimReq)...")
	logger.Trace("Xiongmai Talk", "Claiming talk channel (SessionID: 0x%08X)...", t.client.sessionID)

	// 1. Claim talk channel (MsgID 1434 / 1410)
	claimReq := OPTalkReq{
		Name: "OPTalk",
		OPTalk: OPTalkInfo{
			Action: "Claim",
		},
		SessionID: fmt.Sprintf("0x%08X", t.client.sessionID),
	}
	claimPayload, _ := json.Marshal(claimReq)
	respClaim, err := t.client.SendPacket(MsgTalkClaimV2Req, claimPayload)
	if err != nil {
		// Fallback to MsgTalkClaimReq (1410) for older firmware
		var errFallback error
		respClaim, errFallback = t.client.SendPacket(MsgTalkClaimReq, claimPayload)
		if errFallback != nil {
			return fmt.Errorf("failed to claim talk channel: %w (fallback error: %v)", err, errFallback)
		}
	}

	logger.Debug("Xiongmai Talk", "<- Talk Claim response: %s", string(respClaim))
	var claimParsed struct {
		Ret         int `json:"Ret"`
		AudioFormat struct {
			BitRate    int    `json:"BitRate"`
			SampleBit  int    `json:"SampleBit"`
			SampleRate int    `json:"SampleRate"`
			EncodeType string `json:"EncodeType"`
		} `json:"AudioFormat"`
	}
	if jsonErr := json.Unmarshal(respClaim, &claimParsed); jsonErr == nil {
		if claimParsed.AudioFormat.EncodeType != "" {
			t.audioFormat = claimParsed.AudioFormat.EncodeType
			logger.Debug("Xiongmai Talk", "🎙️ Negotiated camera speaker format: %s (%d Hz, %d-bit)",
				t.audioFormat, claimParsed.AudioFormat.SampleRate, claimParsed.AudioFormat.SampleBit)
		}
	}
	if t.audioFormat == "" {
		t.audioFormat = "G711_ALAW" // Default for L 620 / Xiongmai HI3518
	}

	// 2. Start talk upload (MsgID 1430)
	startReq := OPTalkReq{
		Name: "OPTalk",
		OPTalk: OPTalkInfo{
			Action: "Start",
		},
		SessionID: fmt.Sprintf("0x%08X", t.client.sessionID),
	}
	startPayload, _ := json.Marshal(startReq)
	startResp, err := t.client.SendPacket(MsgTalkControlReq, startPayload)
	if err != nil {
		logger.Warn("Xiongmai Talk", "⚠️ OPTalk Start failed: %v", err)
		return fmt.Errorf("failed to start talk upload: %w", err)
	}
	logger.Debug("Xiongmai Talk", "<- OPTalk Start response: %s", string(startResp))

	t.talkActive = true
	t.lastAudio = time.Now()
	logger.Info("Xiongmai Talk", "🎙️ Two-way audio active: forwarding to camera speaker (Port %d, format: %s)", DefaultPort, t.audioFormat)
	return nil
}

// StopTalk closes the active audio talk channel.
func (t *TalkClient) StopTalk() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.talkActive {
		return nil
	}

	logger.Trace("Xiongmai Talk", "Stopping talk channel (SessionID: 0x%08X)...", t.client.sessionID)
	req := OPTalkReq{
		Name: "OPTalk",
		OPTalk: OPTalkInfo{
			Action: "Stop",
		},
		SessionID: fmt.Sprintf("0x%08X", t.client.sessionID),
	}
	payload, _ := json.Marshal(req)
	_, _ = t.client.SendPacket(MsgTalkControlReq, payload)
	t.talkActive = false
	logger.Debug("Xiongmai Talk", "⏹️ Audio channel closed")
	return nil
}
