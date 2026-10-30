package xiongmai

import (
	"bytes"
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
}

// NewSofiaStream creates a new Sofia native video streamer for Steinel L 620 / Xiongmai cameras.
func NewSofiaStream(cameraIP string, port int, client *Client, rtspServer VideoWriter) *SofiaStream {
	if port <= 0 {
		port = DefaultPort
	}
	return &SofiaStream{
		cameraIP:   cameraIP,
		port:       port,
		client:     client,
		rtspServer: rtspServer,
	}
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

	// Step 1: OPMonitor Media Claim (MsgID 1413) on media socket
	claimReq := OPMonitorReq{
		Name: "OPMonitor",
		OPMonitor: OPMonitorInfo{
			Action:  "Claim",
			Action1: "Start",
			Parameter: &OPMonitorParam{
				Channel:    0,
				CombinMode: "NONE",
				StreamType: "Main",
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
				StreamType: "Main",
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
	startPayload = append(startPayload, 0x00)

	logger.Debug("Xiongmai Stream", "📡 Sending encrypted OPMonitor Start (MsgID 1410) on control socket...")
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
	s.streamStartTime = time.Now()
	s.running.Store(true)

	s.wg.Add(1)
	go s.readLoop(ctx)

	logger.Info("Xiongmai Stream", "▶️ Sofia native video streaming pipeline active (Port %d)", s.port)
	return nil
}

// readLoop continuously reads binary Sofia packets from the media socket,
// reassembles H.264 video frames, and forwards them to the RTSP server.
func (s *SofiaStream) readLoop(ctx context.Context) {
	defer s.wg.Done()

	var firstPacketOnce sync.Once
	var frameBuf bytes.Buffer
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
			s.handleMediaPayload(payload, &frameBuf)
		}
	}
}

// handleMediaPayload parses Xiongmai media markers (I-Frame, P-Frame, Audio, Continuation)
// and demuxes H.264 Annex-B data.
func (s *SofiaStream) handleMediaPayload(payload []byte, frameBuf *bytes.Buffer) {
	if len(payload) >= 4 {
		marker := payload[0:4]

		if bytes.Equal(marker, MarkerIFrame) {
			// I-Frame / Keyframe Record:
			// Flush previous frame
			s.dispatchFrame(frameBuf.Bytes())
			frameBuf.Reset()

			// Skip 16 bytes Xiongmai record header; H.264 Annex-B begins at payload offset 16
			if len(payload) > 16 {
				frameBuf.Write(payload[16:])
			}
			return
		}

		if bytes.Equal(marker, MarkerPFrame) {
			// P-Frame Record:
			// Flush previous frame
			s.dispatchFrame(frameBuf.Bytes())
			frameBuf.Reset()

			// Skip 8 bytes Xiongmai record header; H.264 Annex-B begins at payload offset 8
			if len(payload) > 8 {
				frameBuf.Write(payload[8:])
			}
			return
		}

		if bytes.Equal(marker, MarkerAudio) {
			// Audio Record (G.711u): ignored for pure video pipeline, prevent polluting H.264 stream
			return
		}
	}

	// Continuation chunk of current video frame: forward payload unchanged
	if frameBuf.Len() > 0 {
		frameBuf.Write(payload)
	}
}

// dispatchFrame unmarshals Annex-B H.264 NAL units, encodes them into standard RTP packets,
// and forwards them to the RTSP server.
func (s *SofiaStream) dispatchFrame(data []byte) {
	if len(data) == 0 || s.rtspServer == nil || s.encoder == nil {
		return
	}

	nalus, err := h264.AnnexBUnmarshal(data)
	if err != nil || len(nalus) == 0 {
		return
	}

	pkts, err := s.encoder.Encode(nalus)
	if err != nil || len(pkts) == 0 {
		return
	}

	now := time.Now()
	if s.streamStartTime.IsZero() {
		s.streamStartTime = now
	}
	ts := uint32(now.Sub(s.streamStartTime).Seconds() * 90000)
	s.lastPacketTime.Store(now.UnixNano())

	for _, pkt := range pkts {
		pkt.Timestamp = ts
		s.rtspServer.WriteVideoPacket(pkt)
	}
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
		stopReq := OPMonitorReq{
			Name: "OPMonitor",
			OPMonitor: OPMonitorInfo{
				Action: "Stop",
				Parameter: &OPMonitorParam{
					Channel:    0,
					CombinMode: "NONE",
					StreamType: "Main",
					TransMode:  "TCP",
				},
			},
			SessionID: sessionHex,
		}
		if stopPayload, err := json.Marshal(stopReq); err == nil {
			stopPayload = append(stopPayload, 0x00)
			logger.Debug("Xiongmai Stream", "⏹️ Sending OPMonitor Stop on control socket...")
			_, _ = s.client.SendPacket(MsgMonitorStartReq, stopPayload)
		}
	}

	s.wg.Wait()
	logger.Info("Xiongmai Stream", "⏹️ Sofia native video streaming stopped")
	return nil
}

// IsRunning returns true if the Sofia video stream is actively running.
func (s *SofiaStream) IsRunning() bool {
	return s.running.Load()
}
