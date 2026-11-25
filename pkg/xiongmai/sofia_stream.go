package xiongmai

import (
	"bufio"
	"context"
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
	"github.com/pion/rtp"
)

// defaultSofiaVideoSilenceTimeout bounds how long readLoop tolerates an active media socket
// without any incoming video records (0xFC/0xFE/0xFD), e.g. audio-only or metadata-only zombie state.
const defaultSofiaVideoSilenceTimeout = 15 * time.Second

// defaultSofiaKeyframeWaitTimeout bounds how long readLoop waits for an I-frame while P-frames
// are gated (!gotKeyframe), recovering if the camera encoder stops emitting keyframes altogether.
const defaultSofiaKeyframeWaitTimeout = 30 * time.Second

// VideoWriter represents an RTSP stream or server that accepts H.264/H.265 RTP video packets.
type VideoWriter interface {
	WriteVideoPacket(pkt *rtp.Packet)
}

// SofiaStream handles streaming H.264/H.265 video natively from the camera's Xiongmai Sofia NetIP
// port (TCP 34567) using OPMonitor (MsgID 1410 / 1412), completely bypassing RTSP port 554.
type SofiaStream struct {
	cameraIP            string
	port                int
	streamType          string
	client              *Client
	rtspServer          VideoWriter
	mediaConn           net.Conn
	streamStartTime     time.Time
	lastPacketTime      atomic.Int64
	lastVideoRecordTime atomic.Int64
	videoSilenceTimeout time.Duration
	keyframeWaitTimeout time.Duration
	running             atomic.Bool
	stopChan            chan struct{}
	wg                  sync.WaitGroup
	// Lock order: opMu -> mu -> tsMu (readLoop never acquires opMu).
	opMu sync.Mutex // serializes Start and Stop lifecycle transitions (including wg.Wait)
	mu   sync.Mutex // guards mediaConn, stopChan, streamType, and timeout overrides

	// Timestamp & Frame timing
	fps           int
	tsMu          sync.Mutex
	currentTS     uint32
	lastFrameTime time.Time
	streamBuf     []byte
	nalusBuf      [][]byte

	// Video packetization state (guarded by tsMu)
	packetizer         videoPacketizer
	activeStreamType   string        // snapshot of streamType for the active session
	detectedCodec      string        // last detected codec ("h264" or "h265"), preserved across on-demand restarts
	hasLoggedCodec     bool          // whether initial codec detection has been logged
	gotKeyframe        bool          // P-frames are dropped until the first I-frame of a session
	keyframeNotify     chan struct{} // closed (and set to nil) when a keyframe is dispatched
	keyframeWaitStart  time.Time
	lastKeyframeTime   time.Time
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
		cameraIP:            cameraIP,
		port:                port,
		streamType:          st,
		client:              client,
		rtspServer:          rtspServer,
		videoSilenceTimeout: defaultSofiaVideoSilenceTimeout,
		keyframeWaitTimeout: defaultSofiaKeyframeWaitTimeout,
		fps:                 fps,
		keyframeNotify:      make(chan struct{}),
		audioCodec:          audioCodec,
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
	if s.keyframeNotify == nil {
		s.keyframeNotify = make(chan struct{})
	}
	s.keyframeWaitStart = time.Now()
	s.droppedBeforeKey = 0
	s.tsMu.Unlock()
}

// AwaitNextKeyframeIfStale gates P-frame forwarding until the next I-frame only if the last
// delivered keyframe is older than minAge. This avoids dropping P-frames right after an
// initial keyframe or an automatic H.264->H.265 codec switch.
func (s *SofiaStream) AwaitNextKeyframeIfStale(minAge time.Duration) {
	s.tsMu.Lock()
	if s.gotKeyframe && (s.lastKeyframeTime.IsZero() || time.Since(s.lastKeyframeTime) >= minAge) {
		s.gotKeyframe = false
		if s.keyframeNotify == nil {
			s.keyframeNotify = make(chan struct{})
		}
		s.keyframeWaitStart = time.Now()
		s.droppedBeforeKey = 0
	}
	s.tsMu.Unlock()
}

// WaitFirstKeyframe blocks until the active SofiaStream has processed and dispatched at least
// one keyframe (including any H.264/H.265 SDP codec and parameter-set switch), or until the
// stream stops, ctx is canceled, or timeout elapses.
func (s *SofiaStream) WaitFirstKeyframe(ctx context.Context, timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	s.mu.Lock()
	stopCh := s.stopChan
	running := s.running.Load()
	s.mu.Unlock()
	if !running || stopCh == nil {
		return false
	}

	s.tsMu.Lock()
	if s.gotKeyframe {
		s.tsMu.Unlock()
		return true
	}
	if s.keyframeNotify == nil {
		s.keyframeNotify = make(chan struct{})
	}
	notifyCh := s.keyframeNotify
	s.tsMu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-notifyCh:
		return true
	case <-stopCh:
		return false
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
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
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if s.running.Load() {
		return nil
	}
	// Wait for any previously exiting readLoop (e.g. after an unexpected media socket drop)
	// to finish teardown and OPMonitor Stop before dialing a new media session.
	s.wg.Wait()

	if s.client == nil || !s.client.IsLoggedIn() {
		return errors.New("xiongmai: cannot start sofia video stream: control client is not logged in")
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

	stType := s.StreamType()

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
	respHdr, err := decodeMediaHeader(respHdrBuf)
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
	_ = conn.SetDeadline(time.Time{})
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
	startRespBytes, err := s.client.SendPacket(MsgMonitorStartReq, startPayload)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to send OPMonitor Start: %w", err)
	}
	var startResp struct {
		Ret int `json:"Ret"`
	}
	cleanStartResp := strings.TrimRight(string(startRespBytes), "\x00\r\n ")
	if err := json.Unmarshal([]byte(cleanStartResp), &startResp); err == nil {
		if startResp.Ret != 100 && startResp.Ret != 0 {
			_ = conn.Close()
			return fmt.Errorf("opmonitor start rejected by camera (ret=%d)", startResp.Ret)
		}
	}
	logger.Debug("Xiongmai Stream", "✅ OPMonitor Start acknowledged (Ret=%d), video stream triggered", startResp.Ret)

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

	now := time.Now()
	s.tsMu.Lock()
	if s.streamStartTime.IsZero() {
		s.streamStartTime = now
	}
	s.streamBuf = nil
	s.packetizer = pktz
	s.activeStreamType = stType
	s.gotKeyframe = false
	if s.keyframeNotify == nil {
		s.keyframeNotify = make(chan struct{})
	}
	s.keyframeWaitStart = now
	s.lastKeyframeTime = time.Time{}
	s.isCodecUnsupported = false
	s.droppedBeforeKey = 0
	s.payloadCount = 0
	if s.audioSSRC == 0 {
		s.audioSSRC = uint32(now.UnixNano())
	}
	s.audioTimestamp = 0
	fps := s.fps
	s.tsMu.Unlock()

	s.lastPacketTime.Store(0)
	s.lastVideoRecordTime.Store(0)
	s.mu.Lock()
	s.mediaConn = conn
	s.stopChan = make(chan struct{})
	s.running.Store(true)
	s.wg.Add(1)
	s.mu.Unlock()

	go s.readLoop(ctx, conn)

	logger.Info("Xiongmai Stream", "▶️ Sofia native video streaming pipeline active (Port %d, FPS %d)", s.port, fps)
	return nil
}

// readLoop continuously reads binary Sofia packets from the media socket,
// reassembles H.264/H.265 video frames, and forwards them to the RTSP server.
func (s *SofiaStream) readLoop(ctx context.Context, conn net.Conn) {
	defer func() {
		_ = conn.Close()

		s.mu.Lock()
		isCurrentSession := s.mediaConn == conn
		var unexpectedExit bool
		var sessionStopChan chan struct{}
		if isCurrentSession {
			s.mediaConn = nil
			sessionStopChan = s.stopChan
			s.stopChan = nil
			unexpectedExit = s.running.CompareAndSwap(true, false)
		}
		s.mu.Unlock()
		if sessionStopChan != nil {
			close(sessionStopChan)
		}

		if isCurrentSession {
			s.tsMu.Lock()
			s.streamBuf = nil
			s.tsMu.Unlock()
		}

		if unexpectedExit {
			if ctx.Err() == nil {
				logger.Warn("Xiongmai Stream", "⚠️ Sofia video stream readLoop exited unexpectedly; marking stream inactive for recovery")
				s.sendMonitorStop()
			} else {
				logger.Debug("Xiongmai Stream", "⏹️ Sofia video stream readLoop stopped (context canceled: %v)", ctx.Err())
			}
		}

		s.wg.Done()
	}()

	s.mu.Lock()
	stopChan := s.stopChan
	silenceTimeout := s.videoSilenceTimeout
	keyframeTimeout := s.keyframeWaitTimeout
	s.mu.Unlock()
	if stopChan == nil || !s.running.Load() {
		return
	}
	if silenceTimeout <= 0 {
		silenceTimeout = defaultSofiaVideoSilenceTimeout
	}
	if keyframeTimeout <= 0 {
		keyframeTimeout = defaultSofiaKeyframeWaitTimeout
	}
	readWait := min(5*time.Second, silenceTimeout)
	sessionStart := time.Now()

	var firstPacketOnce sync.Once
	rd := bufio.NewReaderSize(conn, 64*1024)
	hdrBuf := make([]byte, HeaderLength)
	var payloadBuf []byte

	for {
		select {
		case <-ctx.Done():
			return
		case <-stopChan:
			return
		default:
		}

		if s.isVideoSilent(sessionStart, silenceTimeout, keyframeTimeout) {
			logger.Warn("Xiongmai Stream", "⚠️ Sofia video stream stalled (no video records for >%v or no keyframe for >%v); restarting stream", silenceTimeout, keyframeTimeout)
			return
		}

		// Read 20-byte Sofia header
		_ = conn.SetReadDeadline(time.Now().Add(readWait))
		if _, err := io.ReadFull(rd, hdrBuf); err != nil {
			if s.running.Load() {
				logger.Warn("Xiongmai Stream", "⚠️ Sofia media socket read header error: %v", err)
			}
			return
		}

		hdr, err := decodeMediaHeader(hdrBuf)
		if err != nil {
			logger.Debug("Xiongmai Stream", "⚠️ Lost DVRIP framing on media socket (%v), resynchronizing: %s", err, logger.FormatBinary(hdrBuf))
			_ = conn.SetReadDeadline(time.Now().Add(readWait))
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
			if s.keyframeNotify == nil {
				s.keyframeNotify = make(chan struct{})
			}
			s.keyframeWaitStart = time.Now()
			s.tsMu.Unlock()
		}

		if cap(payloadBuf) < int(hdr.DataLength) {
			payloadBuf = make([]byte, hdr.DataLength)
		}
		payload := payloadBuf[:hdr.DataLength]
		_ = conn.SetReadDeadline(time.Now().Add(readWait))
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

func (s *SofiaStream) isVideoSilent(sessionStart time.Time, silenceTimeout, keyframeTimeout time.Duration) bool {
	s.tsMu.Lock()
	unsupported := s.isCodecUnsupported
	waitingForKey := !s.gotKeyframe
	keyWaitStart := s.keyframeWaitStart
	s.tsMu.Unlock()
	if unsupported {
		return false
	}
	if silenceTimeout > 0 {
		lastRecNS := s.lastVideoRecordTime.Load()
		ref := sessionStart
		if lastRecNS > 0 {
			ref = time.Unix(0, lastRecNS)
		}
		if time.Since(ref) >= silenceTimeout {
			return true
		}
	}
	if keyframeTimeout > 0 && waitingForKey {
		ref := sessionStart
		if !keyWaitStart.IsZero() {
			ref = keyWaitStart
		}
		if time.Since(ref) >= keyframeTimeout {
			return true
		}
	}
	return false
}

// sendMonitorStop sends an OPMonitor Stop command on the control socket if still logged in.
func (s *SofiaStream) sendMonitorStop() {
	if s.client == nil || !s.client.IsLoggedIn() {
		return
	}

	s.tsMu.Lock()
	stType := s.activeStreamType
	s.tsMu.Unlock()
	if stType == "" {
		stType = s.StreamType()
	}

	sessionHex := fmt.Sprintf("0x%010x", s.client.GetSessionID())
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

// Stop gracefully terminates the Sofia video streaming session and closes the media socket.
func (s *SofiaStream) Stop() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	wasRunning := s.running.CompareAndSwap(true, false)
	stopChan := s.stopChan
	s.stopChan = nil
	conn := s.mediaConn
	s.mediaConn = nil
	s.mu.Unlock()

	if stopChan != nil {
		close(stopChan)
	}
	if conn != nil {
		_ = conn.Close()
	}

	// Wait for any active or previously exiting readLoop goroutine to finish before returning.
	s.wg.Wait()

	if !wasRunning {
		return nil
	}

	// Send encrypted OPMonitor Stop on control socket
	s.sendMonitorStop()

	// Clear the reassembly buffer for the closed media socket while keeping the audio
	// transcoder, packetizer, and RTP clock anchor intact for subsequent on-demand sessions.
	s.tsMu.Lock()
	s.streamBuf = nil
	s.nalusBuf = nil
	s.tsMu.Unlock()

	logger.Info("Xiongmai Stream", "⏹️ Sofia native video streaming stopped")
	return nil
}

// Close stops streaming and releases the audio transcoder's C resources.
func (s *SofiaStream) Close() error {
	err := s.Stop()
	s.tsMu.Lock()
	tr := s.audioTranscoder
	s.audioTranscoder = nil
	s.tsMu.Unlock()
	if tr != nil {
		tr.Close()
	}
	return err
}

// IsRunning returns true if the Sofia video stream is actively running.
func (s *SofiaStream) IsRunning() bool {
	return s.running.Load()
}
