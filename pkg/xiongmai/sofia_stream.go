package xiongmai

import (
	"bufio"
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
	"github.com/bluenviron/mediacommon/pkg/codecs/h264"
	"github.com/pion/rtp"
)

// VideoWriter represents an RTSP stream or server that accepts H.264/H.265 RTP video packets.
type VideoWriter interface {
	WriteVideoPacket(pkt *rtp.Packet)
}

// SofiaStream handles streaming H.264/H.265 video natively from the camera's Xiongmai Sofia NetIP
// port (TCP 34567) using OPMonitor (MsgID 1410 / 1412), completely bypassing RTSP port 554.
type SofiaStream struct {
	cameraIP        string
	port            int
	streamType      string
	client          *Client
	rtspServer      VideoWriter
	mediaConn       net.Conn
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

	// Video packetization state (guarded by tsMu)
	packetizer         videoPacketizer
	activeStreamType   string // snapshot of streamType for the active session
	detectedCodec      string // last detected codec ("h264" or "h265"), preserved across on-demand restarts
	hasLoggedCodec     bool   // whether initial codec detection has been logged
	gotKeyframe        bool   // P-frames are dropped until the first I-frame of a session
	isCodecUnsupported bool   // camera codec cannot be announced by the RTSP writer: drop video
	droppedBeforeKey   uint64 // P-frames dropped while waiting for the first keyframe
	payloadCount       uint64

	// Audio pipeline (guarded by tsMu during dispatchAudioLocked)
	audioTranscoder *audio.Transcoder
	audioCodec      string
	audioSeq        uint16
	audioSSRC       uint32
	audioTimestamp  uint32
	lastAACPTS      time.Duration
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
	s := &SofiaStream{
		cameraIP:   cameraIP,
		port:       port,
		streamType: st,
		client:     client,
		rtspServer: rtspServer,
		fps:        fps,
		audioCodec: audioCodec,
	}
	if audioCodec == "aac" {
		if aacWriter, ok := rtspServer.(interface {
			WriteAACFrame(au []byte, pts time.Duration)
		}); ok {
			const aacFrameDur = time.Duration(audio.AACFrameSamples) * time.Second / time.Duration(audio.AACSampleRate)
			// The callback runs synchronously inside dispatchAudioLocked with s.tsMu held.
			s.audioTranscoder = audio.NewTranscoder(func(au []byte, _ time.Duration) {
				now := time.Now()
				if s.streamStartTime.IsZero() {
					s.streamStartTime = now
				}
				wallPTS := now.Sub(s.streamStartTime)
				if s.lastAACPTS == 0 || wallPTS-s.lastAACPTS > time.Second {
					s.lastAACPTS = wallPTS
				} else {
					s.lastAACPTS += aacFrameDur
				}
				aacWriter.WriteAACFrame(au, s.lastAACPTS)
			})
		}
	}

	return s
}

// SetFPS sets the camera framerate (used for monotonic RTP timestamp spacing).
func (s *SofiaStream) SetFPS(fps int) {
	s.tsMu.Lock()
	defer s.tsMu.Unlock()
	if fps > 0 {
		s.fps = fps
	}
}

// AwaitNextKeyframe gates video packet forwarding until the next I-frame arrives.
// Called when the first RTSP viewer connects to an already-running Sofia stream so the
// client's decoder starts cleanly on an IDR keyframe instead of mid-GOP P-frames.
func (s *SofiaStream) AwaitNextKeyframe() {
	s.tsMu.Lock()
	s.gotKeyframe = false
	s.droppedBeforeKey = 0
	s.tsMu.Unlock()
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
	if s.client != nil {
		if addr := s.client.GetAddr(); addr != "" {
			targetAddr = addr
		}
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
	if respHdr.DataLength > maxSofiaMediaPayload {
		_ = conn.Close()
		return fmt.Errorf("OPMonitor Claim response too large: %d bytes", respHdr.DataLength)
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

	// Step 3: Initialize the video packetizer on first start and reuse it across on-demand
	// restarts so RTP sequence numbers and SSRCs remain continuous.
	s.tsMu.Lock()
	pktz := s.packetizer
	initialCodec := s.detectedCodec
	s.tsMu.Unlock()
	if initialCodec == "" {
		initialCodec = videoCodecH264
	}
	if pktz == nil {
		var err error
		pktz, err = newVideoPacketizer(initialCodec)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("failed to initialize video packetizer: %w", err)
		}
	}

	s.mediaConn = conn
	s.stopChan = make(chan struct{})
	s.tsMu.Lock()
	if s.streamStartTime.IsZero() {
		s.streamStartTime = time.Now()
	}
	s.streamBuf = nil
	s.packetizer = pktz
	s.activeStreamType = stType
	s.gotKeyframe = false
	s.isCodecUnsupported = false
	s.droppedBeforeKey = 0
	s.payloadCount = 0
	if s.audioSSRC == 0 {
		s.audioSSRC = uint32(time.Now().UnixNano())
	}
	s.audioTimestamp = 0
	fps := s.fps
	s.tsMu.Unlock()
	s.running.Store(true)

	s.wg.Add(1)
	go s.readLoop(ctx, conn)

	logger.Info("Xiongmai Stream", "▶️ Sofia native video streaming pipeline active (Port %d, FPS %d)", s.port, fps)
	return nil
}

// maxSofiaMediaPayload is the sanity limit for a single DVRIP packet on the media socket.
const maxSofiaMediaPayload = 1024 * 1024

// maxSofiaResyncBytes bounds the number of bytes scanned while resynchronizing DVRIP framing.
const maxSofiaResyncBytes = 512 * 1024

// sofiaDiagPayloads is the number of initial MsgID 1412 payloads hex-dumped at debug level.
const sofiaDiagPayloads = 3

// decodeMediaHeader decodes a DVRIP header received on the media socket and validates its payload length.
func decodeMediaHeader(buf []byte) (*Header, error) {
	hdr, err := DecodeHeader(buf)
	if err != nil {
		return nil, err
	}
	if hdr.DataLength > maxSofiaMediaPayload {
		return nil, fmt.Errorf("implausible payload length %d", hdr.DataLength)
	}
	return hdr, nil
}

// resyncMediaHeader slides byte-by-byte over the media stream until a plausible DVRIP header is found.
func resyncMediaHeader(rd io.ByteReader, hdrBuf []byte) (*Header, error) {
	for i := 0; i < maxSofiaResyncBytes; i++ {
		copy(hdrBuf, hdrBuf[1:])
		b, err := rd.ReadByte()
		if err != nil {
			return nil, err
		}
		hdrBuf[len(hdrBuf)-1] = b
		if hdrBuf[0] != HeaderMagic {
			continue
		}
		hdr, err := decodeMediaHeader(hdrBuf)
		if err != nil {
			continue
		}
		isValidEnvelope := hdr.Channel <= 1 && hdr.Reserved[0] == 0 && hdr.Reserved[1] == 0 && hdr.DataLength > 0
		isMonitorMsg := hdr.MsgID == MsgMonitorData || hdr.MsgID == MsgMonitorStartResp || hdr.MsgID == MsgMonitorClaimResp
		if isValidEnvelope && isMonitorMsg {
			return hdr, nil
		}
	}
	return nil, fmt.Errorf("no valid dvrip header found within %d bytes", maxSofiaResyncBytes)
}

// readLoop continuously reads binary Sofia packets from the media socket,
// reassembles H.264/H.265 video frames, and forwards them to the RTSP server.
func (s *SofiaStream) readLoop(ctx context.Context, conn net.Conn) {
	defer func() {
		s.running.Store(false)
		s.mu.Lock()
		if s.mediaConn == conn {
			_ = conn.Close()
			s.mediaConn = nil
		}
		s.mu.Unlock()
		s.wg.Done()
	}()

	var firstPacketOnce sync.Once
	rd := bufio.NewReaderSize(conn, 64*1024)
	hdrBuf := make([]byte, HeaderLength)
	var payloadBuf []byte

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		default:
		}

		// Read 20-byte Sofia header
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(rd, hdrBuf); err != nil {
			if s.running.Load() {
				logger.Warn("Xiongmai Stream", "⚠️ Sofia media socket read header error: %v", err)
			}
			return
		}

		hdr, err := decodeMediaHeader(hdrBuf)
		if err != nil {
			logger.Debug("Xiongmai Stream", "⚠️ Lost DVRIP framing on media socket (%v), resynchronizing: %s", err, logger.FormatBinary(hdrBuf))
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			hdr, err = resyncMediaHeader(rd, hdrBuf)
			if err != nil {
				if s.running.Load() {
					logger.Warn("Xiongmai Stream", "⚠️ Sofia media socket resynchronization failed: %v", err)
				}
				return
			}
			s.tsMu.Lock()
			s.streamBuf = s.streamBuf[:0]
			s.gotKeyframe = false
			s.tsMu.Unlock()
		}

		if cap(payloadBuf) < int(hdr.DataLength) {
			payloadBuf = make([]byte, hdr.DataLength)
		}
		payload := payloadBuf[:hdr.DataLength]
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(rd, payload); err != nil {
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

	s.tsMu.Lock()
	defer s.tsMu.Unlock()

	// Skip JSON control responses that might be interleaved on the media socket when idle,
	// without ever dropping binary continuation chunks that happen to start with 0x7B ('{').
	if len(s.streamBuf) == 0 && payload[0] == '{' {
		trimmed := bytes.TrimRight(payload, "\x00\r\n ")
		if len(trimmed) >= 2 && trimmed[len(trimmed)-1] == '}' {
			return
		}
	}

	s.payloadCount++
	if s.payloadCount <= sofiaDiagPayloads {
		logger.Debug("Xiongmai Stream", "🔬 MsgID 1412 payload #%d (raw): %s", s.payloadCount, logger.FormatBinary(payload, 64))
	}

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
	for len(s.streamBuf) >= 4 {
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
			// Audio frame (0xFA) or metadata frame (0xF9)
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

		switch pType {
		case 0xFC, 0xFE, 0xFD:
			s.dispatchVideoRecordLocked(pType, record[:headerLen], record[headerLen:])
		case 0xFA:
			audioData := record[headerLen:]
			s.dispatchAudioLocked(audioData)
		case 0xF9:
			// 0xF9 is stream/encoder metadata or sync marker in Xiongmai DVRIP protocol, NOT audio samples.
			// Silently advance buffer without forwarding to audio pipeline to eliminate clicking noise (Issue #74).
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

// dispatchVideoRecordLocked routes a complete Sofia video record (I- or P-frame) through the
// active codec packetizer. Codec detection runs on every I-frame so an H.265 stream is switched
// automatically even if Simplify.Encode reported H.264.
func (s *SofiaStream) dispatchVideoRecordLocked(pType byte, hdr []byte, payload []byte) {
	if pType == 0xFC || pType == 0xFE {
		s.ensureVideoCodecLocked(hdr, payload)
		if !s.gotKeyframe && s.droppedBeforeKey > 0 {
			logger.Debug("Xiongmai Stream", "⏭️ Dropped %d P-frame(s) before the first keyframe", s.droppedBeforeKey)
		}
		s.gotKeyframe = true
	} else if !s.gotKeyframe {
		// Decoders cannot start on a P-frame: wait for the first I-frame of the session.
		s.droppedBeforeKey++
		return
	}

	if s.isCodecUnsupported {
		return
	}
	s.dispatchVideoFrameLocked(payload)
}

// ensureVideoCodecLocked selects or switches the active videoPacketizer based on an I-frame.
func (s *SofiaStream) ensureVideoCodecLocked(hdr []byte, payload []byte) {
	var codecByte, fpsByte, widthBlocks, heightBlocks byte
	if len(hdr) >= 8 {
		codecByte = hdr[4]
		fpsByte = hdr[5]
		widthBlocks = hdr[6]
		heightBlocks = hdr[7]
	}

	detected := detectSofiaVideoCodec(codecByte, payload)
	if detected == "" {
		if s.packetizer != nil {
			return
		}
		detected = videoCodecH264
	}

	codecChanged := s.packetizer == nil || s.packetizer.codec() != detected
	if !s.hasLoggedCodec || codecChanged {
		nalHdr := firstAnnexBNALU(payload)
		if len(nalHdr) > 2 {
			nalHdr = nalHdr[:2]
		}
		logger.Info("Xiongmai Stream", "🎞️ Sofia video codec detected: %s (Xiongmai record header: %s, first NAL header: %s, %dx%d @ %d fps)",
			strings.ToUpper(detected), logger.FormatBinary(hdr), logger.FormatBinary(nalHdr),
			int(widthBlocks)*8, int(heightBlocks)*8, fpsByte)

		if detected == videoCodecH265 && s.client != nil {
			stType := s.activeStreamType
			if stType == "" {
				stType = "Main"
			}
			if comp := s.client.GetVideoCompression(stType); comp != "" && !strings.Contains(strings.ToUpper(comp), "265") {
				logger.Warn("Xiongmai Stream", "⚠️ Camera reports Compression=%s for stream '%s' (Simplify.Encode), but the native Sofia stream actually carries H.265/HEVC. Announcing H.265 via RTSP.", comp, stType)
			}
		}
	}

	s.detectedCodec = detected
	s.hasLoggedCodec = true

	if !codecChanged {
		return
	}

	pktz, err := newVideoPacketizer(detected)
	if err != nil {
		logger.Error("Xiongmai Stream", "❌ Failed to initialize %s packetizer: %v", strings.ToUpper(detected), err)
		s.isCodecUnsupported = true
		return
	}

	if sw, ok := s.rtspServer.(videoCodecSwitcher); ok {
		sw.SetVideoCodec(detected)
	} else if detected != videoCodecH264 {
		logger.Error("Xiongmai Stream", "❌ Camera delivers %s but the RTSP output only supports H.264; dropping video.", strings.ToUpper(detected))
		s.isCodecUnsupported = true
		return
	}

	s.packetizer = pktz
	s.isCodecUnsupported = false
}

// dispatchVideoFrameLocked unmarshals an Annex-B frame, packetizes it via the active codec
// packetizer, assigns a strictly monotonic 90 kHz timestamp, and forwards it to the RTSP server.
func (s *SofiaStream) dispatchVideoFrameLocked(data []byte) {
	if len(data) == 0 || s.rtspServer == nil || s.packetizer == nil {
		return
	}

	// Resynchronize on first Annex-B start code if leading padding exists
	if !bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01}) && !bytes.HasPrefix(data, []byte{0x00, 0x00, 0x00, 0x01}) {
		if idx := bytes.Index(data, []byte{0x00, 0x00, 0x01}); idx > 0 {
			data = data[idx:]
		}
	}

	// Annex-B start-code framing is identical for H.264 and H.265.
	nalus, err := h264.AnnexBUnmarshal(data)
	if err != nil || len(nalus) == 0 {
		logger.Trace("Xiongmai Stream", "⚠️ Failed to unmarshal Annex-B NALUs (%d bytes): %v", len(data), err)
		return
	}

	pkts, err := s.packetizer.packetize(nalus, s.rtspServer)
	if err != nil || len(pkts) == 0 {
		if err != nil {
			logger.Trace("Xiongmai Stream", "⚠️ Failed to RTP-encode %s NALUs: %v", strings.ToUpper(s.packetizer.codec()), err)
		}
		return
	}

	ts := s.generateTimestampLocked()
	s.lastPacketTime.Store(time.Now().UnixNano())
	for _, pkt := range pkts {
		pkt.Timestamp = ts
		s.rtspServer.WriteVideoPacket(pkt)
	}
}

// dispatchAudioLocked processes camera microphone audio (8 kHz G.711 A-law) and forwards it to the RTSP server.
func (s *SofiaStream) dispatchAudioLocked(audioData []byte) {
	if len(audioData) == 0 || len(audioData) > 4096 || s.rtspServer == nil || s.audioCodec == "none" {
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
		if s.audioTimestamp == 0 {
			if s.streamStartTime.IsZero() {
				s.streamStartTime = now
			}
			s.audioTimestamp = uint32(now.Sub(s.streamStartTime).Seconds() * 8000)
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    0,
				SequenceNumber: s.audioSeq,
				Timestamp:      s.audioTimestamp,
				SSRC:           s.audioSSRC,
			},
			Payload: pcmuPayload,
		}
		s.audioSeq++
		s.audioTimestamp += uint32(len(pcmuPayload))
		audioWriter.WriteAudioPacket(pkt)
	}
}

// generateTimestampLocked generates strictly monotonic 90 kHz RTP timestamps anchored to streamStartTime.
// This prevents timestamp drift during TCP/Wi-Fi burst packet delivery while ensuring strict monotonicity.
func (s *SofiaStream) generateTimestampLocked() uint32 {
	now := time.Now()
	if s.streamStartTime.IsZero() {
		s.streamStartTime = now
	}

	wallTicks := uint32(now.Sub(s.streamStartTime).Seconds() * 90000)

	if s.lastFrameTime.IsZero() {
		s.lastFrameTime = now
		s.currentTS = wallTicks
		return s.currentTS
	}

	// RFC 3550 modular comparison: if wallTicks has not advanced beyond currentTS (due to sub-millisecond
	// TCP bursts or jitter), advance by 1 tick to preserve strict monotonicity without drifting ahead.
	if int32(wallTicks-s.currentTS) <= 0 {
		s.currentTS++
	} else {
		s.currentTS = wallTicks
	}

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

	// Clear the reassembly buffer for the closed media socket while keeping the audio
	// transcoder, packetizer, and RTP clock anchor intact for subsequent on-demand sessions.
	s.tsMu.Lock()
	s.streamBuf = nil
	s.tsMu.Unlock()

	logger.Info("Xiongmai Stream", "⏹️ Sofia native video streaming stopped")
	return nil
}

// Close stops streaming and releases the audio transcoder's C resources.
func (s *SofiaStream) Close() error {
	err := s.Stop()
	if s.audioTranscoder != nil {
		s.audioTranscoder.Close()
	}
	return err
}

// IsRunning returns true if the Sofia video stream is actively running.
func (s *SofiaStream) IsRunning() bool {
	return s.running.Load()
}
