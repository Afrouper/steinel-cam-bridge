package rtsp

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

type AudioBackchannelHandler func(pkt *rtp.Packet) error
type OnPlayHandler func()

type Server struct {
	server                  *gortsplib.Server
	stream                  *gortsplib.ServerStream
	session                 *description.Session
	videoFormat             *format.H264
	audioFormat             *format.G711
	backchannelFormat       *format.G711
	videoMedia              *description.Media
	audioMedia              *description.Media
	backchannelMedia        *description.Media
	pathName                string
	port                    int
	audioBackchannelHandler AudioBackchannelHandler
	onPlayHandler           OnPlayHandler
	mu                      sync.RWMutex
}

func NewServer(port int, pathName string) (*Server, error) {
	if port == 0 {
		port = 8554
	}
	pathName = strings.TrimPrefix(pathName, "/")
	if pathName == "" {
		pathName = "steinel"
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

	// 2. Setup PCMU (G.711u) audio format (Camera Microphone -> Clients)
	aFormat := &format.G711{
		MULaw:        true,
		SampleRate:   8000,
		ChannelCount: 1,
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
		audioFormat:       aFormat,
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
		UDPRTPAddress:  fmt.Sprintf(":%d", port),
		UDPRTCPAddress: fmt.Sprintf(":%d", port+1),
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
	s.stream = gortsplib.NewServerStream(s.server, s.session)
	s.mu.Unlock()
	return nil
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stream != nil {
		s.stream.Close()
		s.stream = nil
	}
	if s.server != nil {
		s.server.Close()
		s.server = nil
	}
}

func (s *Server) checkPath(p string) bool {
	p = strings.TrimPrefix(p, "/")
	cleanPath := strings.TrimPrefix(s.pathName, "/")
	return p == cleanPath || p == cleanPath+"/main" || p == cleanPath+"/sub"
}

// WriteVideoPacket forwards a raw H.264 RTP packet to all connected RTSP clients
func (s *Server) WriteVideoPacket(pkt *rtp.Packet) {
	s.mu.Lock()
	st := s.stream
	if len(pkt.Payload) > 0 && (s.videoFormat.SPS == nil || s.videoFormat.PPS == nil) {
		nalType := pkt.Payload[0] & 0x1F
		if nalType == 7 { // SPS
			s.videoFormat.SPS = append([]byte(nil), pkt.Payload...)
		} else if nalType == 8 { // PPS
			s.videoFormat.PPS = append([]byte(nil), pkt.Payload...)
		} else if nalType == 24 { // STAP-A
			payload := pkt.Payload[1:]
			for len(payload) >= 2 {
				nalLen := int(payload[0])<<8 | int(payload[1])
				payload = payload[2:]
				if len(payload) < nalLen {
					break
				}
				nal := payload[:nalLen]
				payload = payload[nalLen:]
				nType := nal[0] & 0x1F
				if nType == 7 {
					s.videoFormat.SPS = append([]byte(nil), nal...)
				} else if nType == 8 {
					s.videoFormat.PPS = append([]byte(nil), nal...)
				}
			}
		}
	}
	s.mu.Unlock()

	if st == nil {
		return
	}
	pkt.Header.PayloadType = s.videoFormat.PayloadTyp
	st.WritePacketRTP(s.videoMedia, pkt)
}

// WriteAudioPacket forwards a PCMU RTP packet to all connected RTSP clients
func (s *Server) WriteAudioPacket(pkt *rtp.Packet) {
	s.mu.RLock()
	st := s.stream
	s.mu.RUnlock()

	if st == nil {
		return
	}
	pkt.Header.PayloadType = 0
	st.WritePacketRTP(s.audioMedia, pkt)
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
	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (s *Server) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	log.Printf("[RTSP] ⏹️ Client disconnected")
}
