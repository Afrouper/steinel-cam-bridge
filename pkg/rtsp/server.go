package rtsp

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtpmpeg4audio"
	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"
)

type AudioBackchannelHandler func(pkt *rtp.Packet) error
type OnPlayHandler func()

type Server struct {
	server                  *gortsplib.Server
	stream                  *gortsplib.ServerStream
	session                 *description.Session
	videoFormat             *format.H264
	audioCodec              string
	audioFormat             format.Format
	aacRTPEncoder           *rtpmpeg4audio.Encoder
	backchannelFormat       *format.G711
	videoMedia              *description.Media
	audioMedia              *description.Media
	backchannelMedia        *description.Media
	pathName                string
	port                    int
	udpConn                 *net.UDPConn
	audioBackchannelHandler AudioBackchannelHandler
	onPlayHandler           OnPlayHandler
	backchannelPacketCount  atomic.Uint64
	started                 bool
	mu                      sync.RWMutex
}

func NewServer(port int, pathName string, audioCodec string) (*Server, error) {
	if port == 0 {
		port = 8554
	}
	pathName = strings.TrimPrefix(pathName, "/")
	if pathName == "" {
		pathName = "steinel"
	}
	audioCodec = strings.ToLower(strings.TrimSpace(audioCodec))
	if audioCodec == "" {
		audioCodec = "aac"
	}

	// 1. Setup H.264 video format (Main Live Feed)
	vFormat := &format.H264{
		PayloadTyp:        96,
		PacketizationMode: 1,
	}
	vMedia := &description.Media{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{vFormat},
	}

	// 2. Setup Audio format (AAC or PCMU)
	var aFormat format.Format
	var aacEncoder *rtpmpeg4audio.Encoder

	if audioCodec == "pcmu" {
		aFormat = &format.G711{
			MULaw:        true,
			SampleRate:   8000,
			ChannelCount: 1,
		}
	} else {
		audioCodec = "aac"
		aacConf := &mpeg4audio.Config{
			Type:         mpeg4audio.ObjectTypeAACLC,
			SampleRate:   16000,
			ChannelCount: 1,
		}
		aacFmt := &format.MPEG4Audio{
			PayloadTyp:       97,
			Config:           aacConf,
			SizeLength:       13,
			IndexLength:      3,
			IndexDeltaLength: 3,
		}
		var err error
		aacEncoder, err = aacFmt.CreateEncoder()
		if err != nil {
			return nil, fmt.Errorf("failed to create aac rtp encoder: %w", err)
		}
		aFormat = aacFmt
	}

	aMedia := &description.Media{
		Type:    description.MediaTypeAudio,
		Formats: []format.Format{aFormat},
	}

	// 3. Setup PCMU Audio Backchannel format (Client Microphone -> Camera Speaker)
	bcFormat := &format.G711{
		MULaw:        true,
		SampleRate:   8000,
		ChannelCount: 1,
	}
	bcMedia := &description.Media{
		Type:          description.MediaTypeAudio,
		Formats:       []format.Format{bcFormat},
		IsBackChannel: true,
	}

	meds := &description.Session{
		Medias: []*description.Media{vMedia, aMedia, bcMedia},
	}

	s := &Server{
		session:           meds,
		videoFormat:       vFormat,
		audioCodec:        audioCodec,
		audioFormat:       aFormat,
		aacRTPEncoder:     aacEncoder,
		backchannelFormat: bcFormat,
		videoMedia:        vMedia,
		audioMedia:        aMedia,
		backchannelMedia:  bcMedia,
		pathName:          pathName,
		port:              port,
	}

	srv := &gortsplib.Server{
		Handler:        s,
		RTSPAddress:    fmt.Sprintf(":%d", port),
		WriteQueueSize: 4096,
	}

	s.server = srv
	return s, nil
}

func (s *Server) SetAudioBackchannelHandler(handler AudioBackchannelHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audioBackchannelHandler = handler
}

func (s *Server) SetOnPlayHandler(handler OnPlayHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPlayHandler = handler
}

func (s *Server) Start() error {
	log.Printf("[RTSP] Server listening at rtsp://0.0.0.0:%d/%s (Profile T Audio Backchannel enabled)", s.port, s.pathName)
	if err := s.server.Start(); err != nil {
		return err
	}

	s.mu.Lock()
	s.started = true
	s.stream = gortsplib.NewServerStream(s.server, s.session)
	s.mu.Unlock()

	// Start dedicated UDP Backchannel Listener on port (e.g. 8554/udp)
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", s.port))
	if err == nil {
		conn, err := net.ListenUDP("udp", udpAddr)
		if err == nil {
			s.mu.Lock()
			s.udpConn = conn
			s.mu.Unlock()
			go s.readUDPBackchannelLoop(conn)
			log.Printf("[RTSP] 🎙️ UDP Audio Backchannel receiver listening on 0.0.0.0:%d/udp", s.port)
		} else {
			log.Printf("[RTSP] ⚠️ Failed to bind UDP backchannel receiver on port %d: %v", s.port, err)
		}
	}

	return nil
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.udpConn != nil {
		_ = s.udpConn.Close()
		s.udpConn = nil
	}
	if s.stream != nil {
		s.stream.Close()
		s.stream = nil
	}
	if s.server != nil && s.started {
		s.server.Close()
		s.server = nil
		s.started = false
	}
}

func (s *Server) handleBackchannelPacket(medi *description.Media, pkt *rtp.Packet, source string) {
	if medi == nil || medi == s.backchannelMedia || medi.IsBackChannel {
		cnt := s.backchannelPacketCount.Add(1)
		if cnt == 1 || cnt%100 == 0 {
			log.Printf("[RTSP] 🎙️ Forwarding audio backchannel (%s) RTP packets (%d pkts, payload: %d bytes)", source, cnt, len(pkt.Payload))
		}

		s.mu.RLock()
		handler := s.audioBackchannelHandler
		s.mu.RUnlock()

		if handler != nil {
			_ = handler(pkt)
		}
	}
}

func (s *Server) readUDPBackchannelLoop(conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if n < 12 { // Minimum valid RTP header length
			continue
		}

		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}

		s.handleBackchannelPacket(s.backchannelMedia, pkt, "UDP")
	}
}

func (s *Server) checkPath(p string) bool {
	p = strings.TrimPrefix(p, "/")
	cleanPath := strings.TrimPrefix(s.pathName, "/")
	return p == cleanPath || p == cleanPath+"/main" || p == cleanPath+"/sub"
}

// WriteVideoPacket forwards a raw H.264 RTP packet to all connected RTSP clients
func (s *Server) WriteVideoPacket(pkt *rtp.Packet) {
	s.mu.RLock()
	st := s.stream
	s.mu.RUnlock()

	if st == nil {
		return
	}
	pkt.PayloadType = 96
	_ = st.WritePacketRTP(s.videoMedia, pkt)
}

// GetAudioCodec returns the configured audio codec ("aac" or "pcmu")
func (s *Server) GetAudioCodec() string {
	return s.audioCodec
}

// WriteAACFrame writes an encoded AAC Access Unit (AU) with Presentation Timestamp (PTS)
func (s *Server) WriteAACFrame(au []byte, pts time.Duration) {
	s.mu.Lock()
	st := s.stream
	enc := s.aacRTPEncoder
	s.mu.Unlock()

	if st == nil || enc == nil || len(au) == 0 {
		return
	}

	pkts, err := enc.Encode([][]byte{au})
	if err != nil {
		return
	}

	// 16000 Hz RTP clock rate for AAC
	ts := uint32(pts.Seconds() * 16000)
	for _, pkt := range pkts {
		pkt.Timestamp = ts
		_ = st.WritePacketRTP(s.audioMedia, pkt)
	}
}

// WriteAudioPacket forwards a PCMU RTP packet to all connected RTSP clients
func (s *Server) WriteAudioPacket(pkt *rtp.Packet) {
	s.mu.RLock()
	st := s.stream
	s.mu.RUnlock()

	if st == nil {
		return
	}
	pkt.PayloadType = 0
	_ = st.WritePacketRTP(s.audioMedia, pkt)
}

// --- gortsplib Server Callbacks ---

func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if !s.checkPath(ctx.Path) {
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	s.mu.RLock()
	st := s.stream
	s.mu.RUnlock()

	return &base.Response{
		StatusCode: base.StatusOK,
	}, st, nil
}

func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if !s.checkPath(ctx.Path) {
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	if ctx.Session != nil {
		ctx.Session.OnPacketRTPAny(func(medi *description.Media, _ format.Format, pkt *rtp.Packet) {
			s.handleBackchannelPacket(medi, pkt, "TCP/Interleaved")
		})
	}

	s.mu.RLock()
	st := s.stream
	s.mu.RUnlock()

	return &base.Response{
		StatusCode: base.StatusOK,
	}, st, nil
}

func (s *Server) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	log.Printf("[RTSP] ▶️ Client connected and playing stream (%s)", ctx.Path)

	s.mu.RLock()
	handler := s.onPlayHandler
	s.mu.RUnlock()
	if handler != nil {
		go handler()
	}
	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (s *Server) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	log.Printf("[RTSP] 🎙️ OnRecord called on %s", ctx.Path)
	if ctx.Session != nil {
		ctx.Session.OnPacketRTPAny(func(medi *description.Media, _ format.Format, pkt *rtp.Packet) {
			s.handleBackchannelPacket(medi, pkt, "RECORD")
		})
	}
	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (s *Server) OnSessionClose(_ *gortsplib.ServerHandlerOnSessionCloseCtx) {
	log.Printf("[RTSP] ⏹️ Client disconnected")
}
