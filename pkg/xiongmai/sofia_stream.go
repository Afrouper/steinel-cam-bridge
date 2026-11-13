package xiongmai

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/bluenviron/mediacommon/pkg/codecs/h264"
	"github.com/pion/rtp"
)

// VideoWriter represents an RTSP stream or server that accepts H.264 RTP video packets.
type VideoWriter interface {
	WriteVideoPacket(pkt *rtp.Packet)
}

// SofiaStream handles streaming H.264 video natively from the camera's Xiongmai Sofia NetIP
// port (TCP 34567) using OPMonitor (MsgID 1410 / 1412), completely bypassing RTSP port 554.
type SofiaStream struct {
	cameraIP        string
	port            int
	streamType      string
	client          *Client
	rtspServer      VideoWriter
	mediaConn       net.Conn
	encoder         *rtph264.Encoder
	streamStartTime time.Time
	lastPacketTime  atomic.Int64
	running         atomic.Bool
	stopChan        chan struct{}
	wg              sync.WaitGroup
	mu              sync.Mutex

	// Timestamp & Frame timing
	fps           int
	tsMu          sync.Mutex
	currentTS     uint32
	lastFrameTime time.Time
	streamBuf     []byte

	// Audio pipeline
	audioTranscoder *audio.Transcoder
	audioCodec      string
	audioSeq        uint16
	audioSSRC       uint32
}

// NewSofiaStream creates a new Sofia native video streamer for Steinel L 620 / Xiongmai cameras.
// Optional streamType can be "Main" (1080p/720p) or "Extra1" (360p sub stream); defaults to "Main".
func NewSofiaStream(cameraIP string, port int, client *Client, rtspServer VideoWriter, streamType ...string) *SofiaStream {
	if port <= 0 {
		port = DefaultPort
	}
	st := "Main"
	if len(streamType) > 0 && streamType[0] != "" {
		st = streamType[0]
	}
	fps := 15
	if client != nil {
		if cFPS := client.GetVideoFPS(); cFPS > 0 {
			fps = cFPS
		}
	}

	audioCodec := "aac"
	if ac, ok := rtspServer.(interface{ GetAudioCodec() string }); ok {
		audioCodec = ac.GetAudioCodec()
	}
	var transcoder *audio.Transcoder
	if audioCodec != "pcmu" {
		if aacWriter, ok := rtspServer.(interface {
			WriteAACFrame(au []byte, pts time.Duration)
		}); ok {
			transcoder = audio.NewTranscoder(func(au []byte, pts time.Duration) {
				aacWriter.WriteAACFrame(au, pts)
			})
		}
	}

	return &SofiaStream{
		cameraIP:        cameraIP,
		port:            port,
		streamType:      st,
		client:          client,
		rtspServer:      rtspServer,
		fps:             fps,
		audioCodec:      audioCodec,
		audioTranscoder: transcoder,
	}
}

// SetFPS sets the camera framerate (used for monotonic RTP timestamp spacing).
func (s *SofiaStream) SetFPS(fps int) {
	s.tsMu.Lock()
	defer s.tsMu.Unlock()
	if fps > 0 {
		s.fps = fps
	}
}

// FPS returns the configured framerate.
func (s *SofiaStream) FPS() int {
	s.tsMu.Lock()
	defer s.tsMu.Unlock()
	return s.fps
}

// StreamType returns the active Sofia OPMonitor stream type ("Main" or "Extra1").
func (s *SofiaStream) StreamType() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamType == "" {
		return "Main"
	}
	return s.streamType
}

// SetStreamType sets the desired Sofia OPMonitor stream type ("Main" or "Extra1").
func (s *SofiaStream) SetStreamType(streamType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if streamType == "" {
		streamType = "Main"
	}
	s.streamType = streamType
}

// Start establishes the secondary TCP media connection, claims the media channel,
// issues the encrypted OPMonitor Start command on the control socket, and begins streaming.
func (s *SofiaStream) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running.Load() {
		return nil
	}
	if s.client == nil || !s.client.IsLoggedIn() {
		return errors.New("cannot start Sofia video stream: control client is not logged in")
	}

	sessionID := s.client.GetSessionID()
	sessionHex := fmt.Sprintf("0x%010x", sessionID)

	targetAddr := fmt.Sprintf("%s:%d", s.cameraIP, s.port)
	if s.client != nil && s.client.addr != "" {
		targetAddr = s.client.addr
	}
	logger.Info("Xiongmai Stream", "🎥 Connecting to Sofia media port %s (Session: %s)...", targetAddr, sessionHex)

	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to Sofia media port: %w", err)
	}

	stType := s.streamType
	if stType == "" {
		stType = "Main"
	}

	// Step 1: OPMonitor Media Claim (MsgID 1413) on media socket
	claimReq := OPMonitorReq{
		Name: "OPMonitor",
		OPMonitor: OPMonitorInfo{
			Action:  "Claim",
			Action1: "Start",
			Parameter: &OPMonitorParam{
				Channel:    0,
				CombinMode: "NONE",
				StreamType: stType,
				TransMode:  "TCP",
			},
		},
		SessionID: sessionHex,
	}

	claimPayload, err := json.Marshal(claimReq)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to marshal OPMonitor Claim JSON: %w", err)
	}
	claimPayload = append(claimPayload, 0x00)

	claimHdr := Header{
		Magic:      HeaderMagic,
		Channel:    0x00,
		SessionID:  sessionID,
		Sequence:   24, // Standard sequence for media claim
		TotalPkt:   0x00,
		CurPkt:     0x00,
		MsgID:      MsgMonitorClaimReq, // 1413
		DataLength: uint32(len(claimPayload)),
	}

	claimPkt := append(claimHdr.Encode(), claimPayload...)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(claimPkt); err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to send OPMonitor Claim: %w", err)
	}

	// Read Claim response (20-byte header + JSON body)
	respHdrBuf := make([]byte, HeaderLength)
	if _, err := io.ReadFull(conn, respHdrBuf); err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to read OPMonitor Claim response header: %w", err)
	}
	respHdr, err := DecodeHeader(respHdrBuf)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("invalid OPMonitor Claim response header: %w", err)
	}
	if respHdr.MsgID != MsgMonitorClaimResp {
		_ = conn.Close()
		return fmt.Errorf("unexpected Claim response MsgID %d (expected %d)", respHdr.MsgID, MsgMonitorClaimResp)
	}

	respBody := make([]byte, respHdr.DataLength)
	if _, err := io.ReadFull(conn, respBody); err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to read OPMonitor Claim response body: %w", err)
	}

	var claimResp struct {
		Ret int `json:"Ret"`
	}
	cleanResp := strings.TrimRight(string(respBody), "\x00\r\n ")
	if err := json.Unmarshal([]byte(cleanResp), &claimResp); err == nil {
		if claimResp.Ret != 100 && claimResp.Ret != 0 {
			_ = conn.Close()
			return fmt.Errorf("OPMonitor Claim rejected by camera with Ret=%d", claimResp.Ret)
		}
	}
	logger.Debug("Xiongmai Stream", "✅ OPMonitor Claim acknowledged on media socket (Ret=%d)", claimResp.Ret)

	// Step 2: OPMonitor Start (MsgID 1410) on Control Socket (AES encrypted with CommunicateKey)
	startReq := OPMonitorReq{
		Name: "OPMonitor",
		OPMonitor: OPMonitorInfo{
			Action: "Start",
			Parameter: &OPMonitorParam{
				Channel:    0,
				CombinMode: "NONE",
				StreamType: stType,
				TransMode:  "TCP",
			},
		},
		SessionID: sessionHex,
	}
	startPayload, err := json.Marshal(startReq)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to marshal OPMonitor Start JSON: %w", err)
	}

	if s.client.IsDataEncryptionAES() {
		logger.Debug("Xiongmai Stream", "📡 Sending encrypted OPMonitor Start (MsgID 1410, AES-128-CBC) on control socket...")
	} else {
		logger.Debug("Xiongmai Stream", "📡 Sending OPMonitor Start (MsgID 1410) on control socket...")
	}
	_, err = s.client.SendPacket(MsgMonitorStartReq, startPayload)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to send OPMonitor Start: %w", err)
	}
	logger.Debug("Xiongmai Stream", "✅ OPMonitor Start acknowledged, video stream triggered")

	// Step 3: Initialize RTP H.264 Encoder
	enc := &rtph264.Encoder{
		PayloadType:       96,
		PacketizationMode: 1,
	}
	if err := enc.Init(); err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to initialize RTP H.264 encoder: %w", err)
	}
	s.encoder = enc

	s.mediaConn = conn
	s.stopChan = make(chan struct{})
	if s.streamStartTime.IsZero() {
		s.streamStartTime = time.Now()
	}
	s.tsMu.Lock()
	s.currentTS = 0
	s.lastFrameTime = time.Time{}
	s.streamBuf = nil
	s.audioSSRC = uint32(time.Now().UnixNano())
	s.audioSeq = 0
	s.tsMu.Unlock()
	s.running.Store(true)

	s.wg.Add(1)
	go s.readLoop(ctx)

	logger.Info("Xiongmai Stream", "▶️ Sofia native video streaming pipeline active (Port %d, FPS %d)", s.port, s.fps)
	return nil
}

// readLoop continuously reads binary Sofia packets from the media socket,
// reassembles H.264 video frames, and forwards them to the RTSP server.
func (s *SofiaStream) readLoop(ctx context.Context) {
	defer s.wg.Done()

	var firstPacketOnce sync.Once
	hdrBuf := make([]byte, HeaderLength)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		default:
		}

		// Read 20-byte Sofia header
		_ = s.mediaConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(s.mediaConn, hdrBuf); err != nil {
			if s.running.Load() {
				logger.Warn("Xiongmai Stream", "⚠️ Sofia media socket read header error: %v", err)
			}
			return
		}

		hdr, err := DecodeHeader(hdrBuf)
		if err != nil {
			logger.Trace("Xiongmai Stream", "⚠️ Malformed Sofia header on media socket: %v", err)
			continue
		}

		if hdr.DataLength > 1024*1024 { // 1 MB sanity limit
			logger.Warn("Xiongmai Stream", "⚠️ Oversized Sofia media payload (%d bytes), resetting stream", hdr.DataLength)
			return
		}

		payload := make([]byte, hdr.DataLength)
		_ = s.mediaConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(s.mediaConn, payload); err != nil {
			if s.running.Load() {
				logger.Warn("Xiongmai Stream", "⚠️ Sofia media socket read payload error: %v", err)
			}
			return
		}

		// Process video stream packets (MsgID 1412: MsgMonitorData)
		if hdr.MsgID == MsgMonitorData {
			firstPacketOnce.Do(func() {
				logger.Info("Xiongmai Stream", "▶️ Streaming active: received video packets from camera via Sofia port %d", s.port)
			})
			s.handleMediaPayload(payload)
		}
	}
}

// handleMediaPayload appends incoming Sofia media data to the stream buffer and triggers demuxing.
func (s *SofiaStream) handleMediaPayload(payload []byte) {
	if len(payload) == 0 {
		return
	}
	// Skip JSON control responses that might be interleaved on the media socket
	if payload[0] == '{' {
		return
	}

	s.tsMu.Lock()
	defer s.tsMu.Unlock()

	s.streamBuf = append(s.streamBuf, payload...)
	s.processStreamBufferLocked()
}

// findNextSofiaRecord searches for the next MPEG-PS / Sofia record start code (0x00 0x00 0x01 [0xF8-0xFE]).
func findNextSofiaRecord(buf []byte) int {
	for i := 0; i+3 < len(buf); i++ {
		if buf[i] == 0x00 && buf[i+1] == 0x00 && buf[i+2] == 0x01 {
			marker := buf[i+3]
			if marker >= 0xF8 && marker <= 0xFE {
				return i
			}
		}
	}
	return -1
}

// processStreamBufferLocked parses Xiongmai media markers (I-Frame, P-Frame, Audio) using exact record lengths
// and demuxes H.264 Annex-B data immediately without waiting for the next frame delimiter.
func (s *SofiaStream) processStreamBufferLocked() {
	for {
		if len(s.streamBuf) < 4 {
			break
		}

		// Resynchronize on MPEG-PS / Sofia record start code (0x00 0x00 0x01)
		if !bytes.HasPrefix(s.streamBuf, []byte{0x00, 0x00, 0x01}) {
			idx := bytes.Index(s.streamBuf, []byte{0x00, 0x00, 0x01})
			if idx > 0 {
				s.streamBuf = s.streamBuf[idx:]
			} else {
				if len(s.streamBuf) > 2 {
					s.streamBuf = s.streamBuf[len(s.streamBuf)-2:]
				}
				break
			}
			if len(s.streamBuf) < 4 {
				break
			}
		}

		pType := s.streamBuf[3]
		var totalRecordSize int
		var headerLen int

		switch pType {
		case 0xFC, 0xFE:
			// Keyframe / I-Frame
			headerLen = 16
			if len(s.streamBuf) < headerLen {
				return
			}
			payloadSize := int(binary.LittleEndian.Uint32(s.streamBuf[12:16]))
			if payloadSize > 0 {
				totalRecordSize = headerLen + payloadSize
			} else {
				// Fallback: search for next Sofia record start code
				if nextIdx := findNextSofiaRecord(s.streamBuf[headerLen:]); nextIdx >= 0 {
					totalRecordSize = headerLen + nextIdx
				} else {
					return
				}
			}

		case 0xFD:
			// Inter-frame / P-Frame
			headerLen = 8
			if len(s.streamBuf) < headerLen {
				return
			}
			payloadSize := int(binary.LittleEndian.Uint32(s.streamBuf[4:8]))
			if payloadSize > 0 {
				totalRecordSize = headerLen + payloadSize
			} else {
				// Fallback: search for next Sofia record start code
				if nextIdx := findNextSofiaRecord(s.streamBuf[headerLen:]); nextIdx >= 0 {
					totalRecordSize = headerLen + nextIdx
				} else {
					return
				}
			}

		case 0xFA, 0xF9:
			// Audio frame (G.711)
			headerLen = 8
			if len(s.streamBuf) < headerLen {
				return
			}
			payloadSize := int(binary.LittleEndian.Uint16(s.streamBuf[6:8]))
			if payloadSize > 0 {
				totalRecordSize = headerLen + payloadSize
			} else {
				// Fallback: search for next Sofia record start code
				if nextIdx := findNextSofiaRecord(s.streamBuf[headerLen:]); nextIdx >= 0 {
					totalRecordSize = headerLen + nextIdx
				} else {
					return
				}
			}

		default:
			// Unknown marker byte: advance past the start code to resynchronize
			s.streamBuf = s.streamBuf[3:]
			continue
		}

		if totalRecordSize <= headerLen || totalRecordSize > 2*1024*1024 {
			s.streamBuf = s.streamBuf[3:]
			continue
		}

		if len(s.streamBuf) < totalRecordSize {
			// Record not complete yet, wait for more data
			break
		}

		record := s.streamBuf[:totalRecordSize]
		s.streamBuf = s.streamBuf[totalRecordSize:]

		if pType == 0xFC || pType == 0xFE || pType == 0xFD {
			h264Data := record[headerLen:]
			s.dispatchFrameLocked(h264Data)
		} else if pType == 0xFA || pType == 0xF9 {
			audioData := record[headerLen:]
			s.dispatchAudioLocked(audioData)
		}
	}

	if len(s.streamBuf) == 0 {
		if cap(s.streamBuf) > 256*1024 {
			s.streamBuf = nil
		} else {
			s.streamBuf = s.streamBuf[:0]
		}
	}
}

// dispatchFrameLocked unmarshals Annex-B H.264 NAL units, encodes them into standard RTP packets,
// assigns strictly monotonic presentation timestamps, and forwards them to the RTSP server.
func (s *SofiaStream) dispatchFrameLocked(data []byte) {
	if len(data) == 0 || s.rtspServer == nil || s.encoder == nil {
		return
	}

	// Resynchronize on first Annex-B start code if leading padding exists
	if !bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01}) && !bytes.HasPrefix(data, []byte{0x00, 0x00, 0x00, 0x01}) {
		if idx := bytes.Index(data, []byte{0x00, 0x00, 0x01}); idx > 0 {
			data = data[idx:]
		}
	}

	nalus, err := h264.AnnexBUnmarshal(data)
	if err != nil || len(nalus) == 0 {
		logger.Trace("Xiongmai Stream", "⚠️ Failed to unmarshal Annex-B NALUs (%d bytes): %v", len(data), err)
		return
	}

	// Extract in-band SPS and PPS to update RTSP SDP sprop-parameter-sets
	var sps, pps []byte
	for _, nalu := range nalus {
		if len(nalu) > 0 {
			switch h264.NALUType(nalu[0] & 0x1F) {
			case h264.NALUTypeSPS:
				if sps == nil {
					sps = nalu
				}
			case h264.NALUTypePPS:
				if pps == nil {
					pps = nalu
				}
			}
		}
	}
	if len(sps) > 0 && len(pps) > 0 {
		if pw, ok := s.rtspServer.(interface {
			SetH264Parameters(sps, pps []byte)
		}); ok {
			pw.SetH264Parameters(sps, pps)
		}
	}

	pkts, err := s.encoder.Encode(nalus)
	if err != nil || len(pkts) == 0 {
		logger.Trace("Xiongmai Stream", "⚠️ Failed to RTP-encode NALUs: %v", err)
		return
	}

	ts := s.generateTimestampLocked()
	now := time.Now()
	s.lastPacketTime.Store(now.UnixNano())

	for _, pkt := range pkts {
		pkt.Timestamp = ts
		s.rtspServer.WriteVideoPacket(pkt)
	}
}

// dispatchAudioLocked processes camera microphone audio (8 kHz G.711 A-law) and forwards it to the RTSP server.
func (s *SofiaStream) dispatchAudioLocked(audioData []byte) {
	if len(audioData) == 0 || s.rtspServer == nil {
		return
	}
	if s.audioTranscoder != nil {
		// Xiongmai microphone default is G.711 A-law (PCMA)
		_ = s.audioTranscoder.ProcessPCMA(audioData)
	} else if audioWriter, ok := s.rtspServer.(interface {
		WriteAudioPacket(pkt *rtp.Packet)
	}); ok {
		pcmuPayload := audio.TranscodePCMAToPCMU(audioData)
		now := time.Now()
		ts := uint32(now.Sub(s.streamStartTime).Seconds() * 8000)
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    0,
				SequenceNumber: s.audioSeq,
				Timestamp:      ts,
				SSRC:           s.audioSSRC,
			},
			Payload: pcmuPayload,
		}
		s.audioSeq++
		audioWriter.WriteAudioPacket(pkt)
	}
}

// generateTimestampLocked generates strictly monotonic 90 kHz RTP timestamps based on camera FPS.
func (s *SofiaStream) generateTimestampLocked() uint32 {
	now := time.Now()
	fps := s.fps
	if fps <= 0 {
		fps = 15
	}
	nominalStep := uint32(90000 / fps)

	if s.lastFrameTime.IsZero() {
		s.lastFrameTime = now
		s.currentTS = 0
		return s.currentTS
	}

	elapsed := now.Sub(s.lastFrameTime)
	ticks := uint32(elapsed.Seconds() * 90000)

	// Ensure strict monotonicity: step at least nominalStep
	if ticks < nominalStep {
		ticks = nominalStep
	} else if ticks > 10*nominalStep {
		// Cap sudden large pause jumps to 1 second
		ticks = 90000
	}

	s.currentTS += ticks
	s.lastFrameTime = now
	return s.currentTS
}

// Stop gracefully terminates the Sofia video streaming session and closes the media socket.
func (s *SofiaStream) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running.CompareAndSwap(true, false) {
		return nil
	}

	if s.stopChan != nil {
		close(s.stopChan)
	}

	if s.mediaConn != nil {
		_ = s.mediaConn.Close()
		s.mediaConn = nil
	}

	// Send encrypted OPMonitor Stop on control socket
	if s.client != nil && s.client.IsLoggedIn() {
		sessionHex := fmt.Sprintf("0x%010x", s.client.GetSessionID())
		stType := s.streamType
		if stType == "" {
			stType = "Main"
		}
		stopReq := OPMonitorReq{
			Name: "OPMonitor",
			OPMonitor: OPMonitorInfo{
				Action: "Stop",
				Parameter: &OPMonitorParam{
					Channel:    0,
					CombinMode: "NONE",
					StreamType: stType,
					TransMode:  "TCP",
				},
			},
			SessionID: sessionHex,
		}
		if stopPayload, err := json.Marshal(stopReq); err == nil {
			logger.Debug("Xiongmai Stream", "⏹️ Sending OPMonitor Stop on control socket...")
			_, _ = s.client.SendPacket(MsgMonitorStartReq, stopPayload)
		}
	}

	s.wg.Wait()

	if s.audioTranscoder != nil {
		s.audioTranscoder.Close()
	}

	s.tsMu.Lock()
	s.streamBuf = nil
	s.currentTS = 0
	s.lastFrameTime = time.Time{}
	s.tsMu.Unlock()

	logger.Info("Xiongmai Stream", "⏹️ Sofia native video streaming stopped")
	return nil
}

// IsRunning returns true if the Sofia video stream is actively running.
func (s *SofiaStream) IsRunning() bool {
	return s.running.Load()
}
