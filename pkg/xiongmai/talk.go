package xiongmai

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// TalkClient handles 2-Way Audio (Gegensprechen) using the Xiongmai OPTalk protocol on port 34567.
type TalkClient struct {
	client     *Client
	talkActive bool
	mu         sync.Mutex
	lastAudio  time.Time
	debug      bool
}

// NewTalkClient creates a new 2-Way Talk client.
func NewTalkClient(client *Client, debug bool) *TalkClient {
	return &TalkClient{
		client: client,
		debug:  debug,
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

	// Wrap audio payload in Sofia talk data message (MsgTalkAudioData = 1432 / 1412)
	// Xiongmai audio frames contain raw G.711 (PCMA/PCMU) samples
	hdr := Header{
		Magic:      HeaderMagic,
		Channel:    0,
		SessionID:  t.client.sessionID,
		Sequence:   t.client.sequence + 1,
		MsgID:      MsgTalkAudioData,
		DataLength: uint32(len(pkt.Payload)),
	}
	t.client.sequence++

	packet := append(hdr.Encode(), pkt.Payload...)

	if t.client.conn == nil {
		return errors.New("connection closed")
	}

	_ = t.client.conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	_, err := t.client.conn.Write(packet)
	return err
}

func (t *TalkClient) startTalkLocked() error {
	if t.talkActive {
		return nil
	}

	if t.debug {
		log.Printf("[Xiongmai Talk] 🎙️ Claiming audio talk channel (MsgTalkClaimReq)...")
	}

	// 1. Claim talk channel (MsgID 1434 / 1410)
	claimReq := OPTalkReq{
		Name: "OPTalk",
		OPTalk: OPTalkInfo{
			Action: "Claim",
		},
		SessionID: fmt.Sprintf("0x%08X", t.client.sessionID),
	}
	claimPayload, _ := json.Marshal(claimReq)
	_, err := t.client.sendPacketLocked(MsgTalkClaimV2Req, claimPayload)
	if err != nil {
		// Fallback to MsgTalkClaimReq (1410) for older firmware
		if _, errFallback := t.client.sendPacketLocked(MsgTalkClaimReq, claimPayload); errFallback != nil {
			return fmt.Errorf("failed to claim talk channel: %w", err)
		}
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
	_, _ = t.client.sendPacketLocked(MsgTalkControlReq, startPayload)

	t.talkActive = true
	t.lastAudio = time.Now()
	log.Printf("[Xiongmai Talk] 🎙️ Two-way audio active: forwarding to camera speaker (Port %d)", DefaultPort)
	return nil
}

// StopTalk closes the active audio talk channel.
func (t *TalkClient) StopTalk() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.talkActive {
		return nil
	}

	req := OPTalkReq{
		Name: "OPTalk",
		OPTalk: OPTalkInfo{
			Action: "Stop",
		},
		SessionID: fmt.Sprintf("0x%08X", t.client.sessionID),
	}
	payload, _ := json.Marshal(req)
	_, _ = t.client.sendPacketLocked(MsgTalkControlReq, payload)
	t.talkActive = false
	if t.debug {
		log.Printf("[Xiongmai Talk] ⏹️ Audio channel closed")
	}
	return nil
}
