package xiongmai

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/pion/rtp"
)

const (
	defaultTalkIdleTimeout = 5 * time.Second
	talkClaimRetryCooldown = 500 * time.Millisecond
	talkDialTimeout        = 5 * time.Second
	talkClaimMaxPayload    = 65535
	// maxTalkAudioChunkSamples caps each MsgTalkAudioData (1432) record at 320 samples
	// (40 ms at 8000 Hz -> 8-byte 00 00 01 FA header + 320 bytes = DataLength 328),
	// matching the official Steinel app's OPTalk framing so aggregated jumbo RTP packets
	// from Scrypted / Apple HomeKit (up to 1024 bytes / 160 ms) never overflow the camera DSP.
	maxTalkAudioChunkSamples = 320
)

type opTalkAudioFormat struct {
	EncodeType string `json:"EncodeType"`
	SampleRate int    `json:"SampleRate"`
	SampleBit  int    `json:"SampleBit"`
	BitRate    int    `json:"BitRate"`
}

type opTalkPayload struct {
	Name      string        `json:"Name"`
	OPTalk    opTalkCommand `json:"OPTalk"`
	SessionID string        `json:"SessionID"`
}

type opTalkCommand struct {
	Action      string             `json:"Action"`
	AudioFormat *opTalkAudioFormat `json:"AudioFormat,omitempty"`
}

// TalkClient handles 2-Way Audio (Gegensprechen) using the Xiongmai NetIP 2-socket OPTalk
// protocol on port 34567:
//  1. Secondary TCP socket claims the talk stream via MsgTalkClaimV2Req (1434 -> 1435).
//  2. Primary control socket starts/stops talk via encrypted MsgTalkControlReq (1430 -> 1431).
//  3. Secondary TCP socket streams 8-byte DVRIP-framed G.711 audio via MsgTalkAudioData (1432).
type TalkClient struct {
	client        *Client
	talkConn      net.Conn
	talkSeq       uint32
	talkActive    bool
	talkSessionID uint32
	audioFormat   string
	idleTimeout   time.Duration
	idleTimer     *time.Timer
	nextClaimAt   time.Time
	sendBuf       []byte
	mu            sync.Mutex
	lastAudio     time.Time
}

// NewTalkClient creates a new 2-Way Talk client.
func NewTalkClient(client *Client) *TalkClient {
	return &TalkClient{
		client:      client,
		idleTimeout: defaultTalkIdleTimeout,
	}
}

// StartTalk opens the secondary OPTalk media socket and starts the speaker channel.
func (t *TalkClient) StartTalk() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.startTalkLocked()
}

// buildDVRIPTalkAudioRecord wraps raw 8 kHz G.711 samples in the 8-byte Xiongmai
// DVRIP audio record header (00 00 01 FA <codec> 02 <len_le16>) expected by MsgID 1432.
func buildDVRIPTalkAudioRecord(audioFormat string, samples []byte) []byte {
	codecByte := byte(0x0E) // 0x0E = G.711 A-law (PCMA), Xiongmai default
	if audioFormat == "G711_ULAW" {
		codecByte = 0x07 // 0x07 = G.711 u-law (PCMU)
	}
	rec := make([]byte, 8+len(samples))
	copy(rec[0:4], MarkerAudio)
	rec[4] = codecByte
	rec[5] = 0x02 // 8000 Hz sample rate index
	binary.LittleEndian.PutUint16(rec[6:8], uint16(len(samples)))
	copy(rec[8:], samples)
	return rec
}

// SendAudioPacket converts an incoming RTP G.711 audio packet from the RTSP Backchannel
// into a Xiongmai DVRIP talk frame and writes it to the secondary talk TCP socket.
func (t *TalkClient) SendAudioPacket(pkt *rtp.Packet) error {
	if pkt == nil || len(pkt.Payload) == 0 {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.client == nil || !t.client.IsLoggedIn() {
		t.clearTalkStateLocked()
		return errors.New("control client is not logged in")
	}

	// Re-claim OPTalk automatically if the underlying Sofia control session reconnected with a new SessionID.
	if t.talkActive && (t.talkConn == nil || t.talkSessionID != t.client.GetSessionID()) {
		logger.Debug("Xiongmai Talk", "🔄 Sofia SessionID changed (0x%08X -> 0x%08X); re-claiming OPTalk channel",
			t.talkSessionID, t.client.GetSessionID())
		t.clearTalkStateLocked()
	}

	// Automatically open talk channel on first audio packet if not yet active
	if !t.talkActive {
		if !t.nextClaimAt.IsZero() && time.Now().Before(t.nextClaimAt) {
			return errors.New("optalk claim retry cooldown active")
		}
		if err := t.startTalkLocked(); err != nil {
			t.nextClaimAt = time.Now().Add(talkClaimRetryCooldown)
			return err
		}
		t.nextClaimAt = time.Time{}
	}

	t.lastAudio = time.Now()
	t.armIdleStopLocked()

	packet := t.encodeTalkPacketLocked(pkt.PayloadType, pkt.Payload)

	_ = t.talkConn.SetWriteDeadline(time.Now().Add(time.Second))
	_, err := t.talkConn.Write(packet)
	_ = t.talkConn.SetWriteDeadline(time.Time{})
	if err != nil {
		// Secondary talk socket failed: tear down talk state cleanly without killing
		// the primary MCU/alarm control connection; next packet will re-claim OPTalk.
		t.clearTalkStateLocked()
		return err
	}
	return nil
}

// encodeTalkPacketLocked slices src into <= maxTalkAudioChunkSamples (320-byte / 40 ms) records
// and writes each 20-byte Sofia DVRIP header, 8-byte 00 00 01 FA audio record header, and
// transcoded G.711 payload contiguously into t.sendBuf with zero heap allocations.
func (t *TalkClient) encodeTalkPacketLocked(payloadType uint8, src []byte) []byte {
	numChunks := (len(src) + maxTalkAudioChunkSamples - 1) / maxTalkAudioChunkSamples
	if numChunks == 0 {
		return nil
	}
	totalLen := numChunks*(HeaderLength+8) + len(src)
	if cap(t.sendBuf) < totalLen {
		t.sendBuf = make([]byte, totalLen)
	}
	buf := t.sendBuf[:totalLen]

	codecByte := byte(0x0E) // G.711 A-law (PCMA)
	if t.audioFormat == "G711_ULAW" {
		codecByte = 0x07 // G.711 u-law (PCMU)
	}

	writePos := 0
	for offset := 0; offset < len(src); offset += maxTalkAudioChunkSamples {
		chunk := src[offset:min(offset+maxTalkAudioChunkSamples, len(src))]
		recordLen := 8 + len(chunk)
		t.talkSeq++

		// 20-byte DVRIP Header
		hdrSlice := buf[writePos : writePos+HeaderLength]
		clear(hdrSlice)
		hdrSlice[0] = HeaderMagic
		binary.LittleEndian.PutUint32(hdrSlice[4:8], t.talkSessionID)
		binary.LittleEndian.PutUint32(hdrSlice[8:12], t.talkSeq)
		binary.LittleEndian.PutUint16(hdrSlice[14:16], MsgTalkAudioData)
		binary.LittleEndian.PutUint32(hdrSlice[16:20], uint32(recordLen))
		writePos += HeaderLength

		// 8-byte Xiongmai audio record header (00 00 01 FA <codec> 02 <len_le16>)
		rec := buf[writePos : writePos+8]
		copy(rec[0:4], MarkerAudio)
		rec[4] = codecByte
		rec[5] = 0x02 // 8000 Hz sample rate index
		binary.LittleEndian.PutUint16(rec[6:8], uint16(len(chunk)))
		writePos += 8

		// Adapt audio format: Scrypted / Apple HomeKit / go2rtc send G.711 u-law (PCMU, PT=0),
		// whereas Xiongmai L 620 / XLED cameras negotiate G.711 A-law (PCMA, 0x0E) by default.
		dstSamples := buf[writePos : writePos+len(chunk)]
		switch {
		case (t.audioFormat == "G711_ALAW" || t.audioFormat == "") && payloadType == 0:
			audio.TranscodePCMUToPCMAInto(dstSamples, chunk)
		case t.audioFormat == "G711_ULAW" && payloadType == 8:
			audio.TranscodePCMAToPCMUInto(dstSamples, chunk)
		default:
			copy(dstSamples, chunk)
		}
		writePos += len(chunk)
	}
	return buf[:writePos]
}

func (t *TalkClient) clearTalkStateLocked() {
	if t.idleTimer != nil {
		t.idleTimer.Stop()
		t.idleTimer = nil
	}
	if t.talkConn != nil {
		_ = t.talkConn.Close()
		t.talkConn = nil
	}
	t.talkActive = false
	t.talkSessionID = 0
	t.talkSeq = 0
}

func (t *TalkClient) armIdleStopLocked() {
	if t.idleTimeout <= 0 {
		return
	}
	if t.idleTimer != nil {
		t.idleTimer.Stop()
	}
	expectedSession := t.talkSessionID
	t.idleTimer = time.AfterFunc(t.idleTimeout, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if !t.talkActive || t.talkSessionID != expectedSession {
			return
		}
		if time.Since(t.lastAudio) < t.idleTimeout {
			return
		}
		t.stopTalkLocked()
	})
}

// Reset closes any active secondary talk socket and marks the talk channel inactive
// so the next outgoing audio packet re-claims OPTalk cleanly.
func (t *TalkClient) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clearTalkStateLocked()
	t.nextClaimAt = time.Time{}
}

func parseTalkRespRetAndFormat(raw []byte) (ret int, encodeType string, sampleRate int, sampleBit int, ok bool) {
	var parsed struct {
		Ret         int `json:"Ret"`
		AudioFormat struct {
			BitRate    int    `json:"BitRate"`
			SampleBit  int    `json:"SampleBit"`
			SampleRate int    `json:"SampleRate"`
			EncodeType string `json:"EncodeType"`
		} `json:"AudioFormat"`
	}
	clean := strings.TrimRight(string(raw), "\x00\r\n ")
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		return 0, "", 0, 0, false
	}
	return parsed.Ret, parsed.AudioFormat.EncodeType, parsed.AudioFormat.SampleRate, parsed.AudioFormat.SampleBit, true
}

func (t *TalkClient) startTalkLocked() error {
	if t.client == nil || !t.client.IsLoggedIn() {
		return errors.New("control client is not logged in")
	}
	sessionID := t.client.GetSessionID()
	if t.talkActive && t.talkConn != nil && t.talkSessionID == sessionID {
		return nil
	}
	t.clearTalkStateLocked()

	addr := t.client.GetAddr()
	if addr == "" {
		return errors.New("camera address is empty")
	}

	sessionHex := fmt.Sprintf("0x%08x", sessionID)
	reqFormat := &opTalkAudioFormat{
		EncodeType: "G711_ALAW",
		SampleRate: 8000,
		SampleBit:  8,
		BitRate:    64,
	}

	logger.Debug("Xiongmai Talk", "🎙️ Connecting secondary OPTalk socket to %s (SessionID: %s)...", addr, sessionHex)
	conn, err := net.DialTimeout("tcp", addr, talkDialTimeout)
	if err != nil {
		return fmt.Errorf("failed to dial talk socket: %w", err)
	}

	// 1. Claim talk channel (MsgID 1434 -> 1435) on the secondary TCP socket.
	claimPayload, err := json.Marshal(opTalkPayload{
		Name: "OPTalk",
		OPTalk: opTalkCommand{
			Action:      "Claim",
			AudioFormat: reqFormat,
		},
		SessionID: sessionHex,
	})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("marshal optalk claim: %w", err)
	}
	claimPayload = append(claimPayload, 0x00)
	claimHdr := Header{
		Magic:      HeaderMagic,
		Channel:    0,
		SessionID:  sessionID,
		Sequence:   1,
		MsgID:      MsgTalkClaimV2Req,
		DataLength: uint32(len(claimPayload)),
	}

	_ = conn.SetDeadline(time.Now().Add(talkDialTimeout))
	if _, err := conn.Write(append(claimHdr.Encode(), claimPayload...)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("send optalk claim: %w", err)
	}

	respHdrBuf := make([]byte, HeaderLength)
	if _, err := io.ReadFull(conn, respHdrBuf); err != nil {
		_ = conn.Close()
		return fmt.Errorf("read optalk claim header: %w", err)
	}
	respHdr, err := DecodeHeader(respHdrBuf)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("decode optalk claim header: %w", err)
	}
	if respHdr.MsgID != MsgTalkClaimV2Resp {
		_ = conn.Close()
		return fmt.Errorf("unexpected optalk claim response MsgID %d (expected %d)", respHdr.MsgID, MsgTalkClaimV2Resp)
	}
	if respHdr.DataLength > talkClaimMaxPayload {
		_ = conn.Close()
		return fmt.Errorf("optalk claim response too large: %d bytes", respHdr.DataLength)
	}
	respClaim := make([]byte, respHdr.DataLength)
	if _, err := io.ReadFull(conn, respClaim); err != nil {
		_ = conn.Close()
		return fmt.Errorf("read optalk claim body: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})

	logger.Debug("Xiongmai Talk", "<- Talk Claim response: %s", strings.TrimRight(string(respClaim), "\x00\r\n "))
	if ret, encType, sr, sb, ok := parseTalkRespRetAndFormat(respClaim); ok {
		if ret != 0 && ret != 100 {
			_ = conn.Close()
			return fmt.Errorf("optalk claim rejected by camera (ret=%d)", ret)
		}
		if encType != "" {
			t.audioFormat = encType
			logger.Debug("Xiongmai Talk", "🎙️ Negotiated camera speaker format from Claim: %s (%d Hz, %d-bit)", encType, sr, sb)
		}
	}

	// 2. Start talk upload (MsgID 1430 -> 1431) on the primary control socket.
	startPayload, err := json.Marshal(opTalkPayload{
		Name: "OPTalk",
		OPTalk: opTalkCommand{
			Action:      "Start",
			AudioFormat: reqFormat,
		},
		SessionID: sessionHex,
	})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("marshal optalk start: %w", err)
	}
	startResp, err := t.client.SendPacket(MsgTalkControlReq, startPayload)
	if err != nil {
		_ = conn.Close()
		logger.Warn("Xiongmai Talk", "⚠️ OPTalk Start failed: %v", err)
		return fmt.Errorf("failed to start talk upload: %w", err)
	}
	if ret, encType, sr, sb, ok := parseTalkRespRetAndFormat(startResp); ok {
		if ret != 0 && ret != 100 {
			_ = conn.Close()
			return fmt.Errorf("optalk start rejected by camera (ret=%d)", ret)
		}
		if encType != "" {
			t.audioFormat = encType
			logger.Debug("Xiongmai Talk", "🎙️ Negotiated camera speaker format: %s (%d Hz, %d-bit)", encType, sr, sb)
		}
	}
	if t.audioFormat == "" {
		t.audioFormat = "G711_ALAW"
	}
	logger.Debug("Xiongmai Talk", "<- OPTalk Start response: %s", strings.TrimRight(string(startResp), "\x00\r\n "))

	t.talkConn = conn
	t.talkSeq = 1
	t.talkActive = true
	t.talkSessionID = sessionID
	t.lastAudio = time.Now()
	t.armIdleStopLocked()
	go t.drainTalkSocket(conn, sessionID)
	logger.Info("Xiongmai Talk", "🎙️ Two-way audio active: forwarding to camera speaker (%s, format: %s)", addr, t.audioFormat)
	return nil
}

// drainTalkSocket discards any incoming bytes on the secondary talk socket and marks the
// talk channel inactive immediately if the camera closes the socket asynchronously.
func (t *TalkClient) drainTalkSocket(conn net.Conn, sessionID uint32) {
	_, _ = io.Copy(io.Discard, conn)
	t.mu.Lock()
	if t.talkConn == conn && t.talkSessionID == sessionID {
		t.clearTalkStateLocked()
	}
	t.mu.Unlock()
}

// StopTalk closes the active audio talk channel and its secondary TCP socket.
func (t *TalkClient) StopTalk() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopTalkLocked()
	return nil
}

func (t *TalkClient) stopTalkLocked() {
	if !t.talkActive {
		t.clearTalkStateLocked()
		return
	}

	sessionID := t.talkSessionID
	if t.idleTimer != nil {
		t.idleTimer.Stop()
		t.idleTimer = nil
	}
	// Send OPTalk Stop on the control socket before closing talkConn so the camera DSP
	// releases the speaker channel cleanly without lingering in Ret=503 (busy) state.
	if t.client != nil && t.client.IsLoggedIn() && t.client.GetSessionID() == sessionID {
		logger.Trace("Xiongmai Talk", "Stopping talk channel (SessionID: 0x%08x)...", sessionID)
		payload, _ := json.Marshal(opTalkPayload{
			Name: "OPTalk",
			OPTalk: opTalkCommand{
				Action: "Stop",
			},
			SessionID: fmt.Sprintf("0x%08x", sessionID),
		})
		_, _ = t.client.SendPacket(MsgTalkControlReq, payload)
	}
	t.clearTalkStateLocked()
	logger.Debug("Xiongmai Talk", "⏹️ Audio channel closed")
}
