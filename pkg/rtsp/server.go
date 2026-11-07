package rtsp

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/auth"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/pion/rtp"
)

type AudioBackchannelHandler func(pkt *rtp.Packet) error
type OnPlayHandler func()

// Server manages the RTSP server, authentication, and multiple camera streams.
type Server struct {
	server           *gortsplib.Server
	port             int
	authUser         string
	authPass         string
	realm            string
	nonce            string
	udpConn          *net.UDPConn
	started          bool
	streamsMu        sync.RWMutex
	streams          map[string]*Stream
	primaryStream    *Stream
	backchannelMedia *description.Media
	sessionStreams   sync.Map // *gortsplib.ServerSession -> *Stream
	mu               sync.RWMutex
}

// NewServer creates a new RTSP Server listening on the specified port.
func NewServer(port int, pathName string, audioCodec string, authUser string, authPass string) (*Server, error) {
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

	var nonce string
	if authUser != "" {
		var err error
		nonce, err = auth.GenerateNonce()
		if err != nil {
			return nil, fmt.Errorf("failed to generate rtsp auth nonce: %w", err)
		}
	}

	st, err := NewStream(pathName, audioCodec)
	if err != nil {
		return nil, err
	}

	s := &Server{
		port:             port,
		authUser:         authUser,
		authPass:         authPass,
		realm:            "Steinel CAM Bridge",
		nonce:            nonce,
		streams:          make(map[string]*Stream),
		primaryStream:    st,
		backchannelMedia: st.backchannelMedia,
	}
	s.streams[pathName] = st

	srv := &gortsplib.Server{
		Handler:        s,
		RTSPAddress:    fmt.Sprintf(":%d", port),
		WriteQueueSize: 4096,
		Listen:         s.listenInterceptor,
	}
	s.server = srv

	return s, nil
}

func (s *Server) listenInterceptor(network, address string) (net.Listener, error) {
	l, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	return &interceptingListener{
		Listener: l,
		server:   s,
	}, nil
}

// RegisterStream adds or retrieves an RTSP stream for the specified path and audio codec.
func (s *Server) RegisterStream(pathName string, audioCodec string) (*Stream, error) {
	pathName = strings.TrimPrefix(pathName, "/")
	if pathName == "" {
		return nil, fmt.Errorf("stream path cannot be empty")
	}

	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()

	if st, ok := s.streams[pathName]; ok {
		return st, nil
	}

	st, err := NewStream(pathName, audioCodec)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	started := s.started
	srv := s.server
	s.mu.RUnlock()

	if started && srv != nil {
		st.initServerStream(srv)
	}

	s.streams[pathName] = st
	if s.primaryStream == nil {
		s.primaryStream = st
	}
	logger.Info("RTSP", "📹 Registered RTSP stream at path '/%s' (codec: %s)", pathName, st.GetAudioCodec())
	return st, nil
}

// GetStream returns the Stream instance registered for a given path, or nil if not found.
func (s *Server) GetStream(pathName string) *Stream {
	pathName = strings.TrimPrefix(pathName, "/")
	s.streamsMu.RLock()
	defer s.streamsMu.RUnlock()
	return s.streams[pathName]
}

// PrimaryStream returns the primary / default Stream instance.
func (s *Server) PrimaryStream() *Stream {
	s.streamsMu.RLock()
	defer s.streamsMu.RUnlock()
	return s.primaryStream
}

func (s *Server) checkPath(p string) bool {
	return s.findStreamForPath(p) != nil
}

// findStreamForPath maps a requested RTSP URI path to its corresponding Stream.
func (s *Server) findStreamForPath(reqPath string) *Stream {
	reqPath = strings.TrimPrefix(reqPath, "/")
	if idx := strings.Index(reqPath, "?"); idx >= 0 {
		reqPath = reqPath[:idx]
	}

	s.streamsMu.RLock()
	defer s.streamsMu.RUnlock()

	// 1. Direct match on full path
	if st, ok := s.streams[reqPath]; ok {
		return st
	}

	// 2. Base segment match (e.g. "einfahrt/main", "einfahrt/trackID=0")
	parts := strings.Split(reqPath, "/")
	basePath := parts[0]
	if st, ok := s.streams[basePath]; ok {
		return st
	}

	// 3. Aliases: "", "live", "steinel", "cam" -> fallback to primary stream
	if basePath == "" || basePath == "live" || basePath == "steinel" || basePath == "cam" {
		return s.primaryStream
	}

	return nil
}

// Start begins serving RTSP connections and initializes all registered streams.
func (s *Server) Start() error {
	s.streamsMu.RLock()
	var paths []string
	for p := range s.streams {
		paths = append(paths, "/"+p)
	}
	s.streamsMu.RUnlock()

	if s.authUser != "" {
		logger.Info("RTSP", "Server listening at rtsp://0.0.0.0:%d (paths: %v, Auth: user '%s', Profile T Audio Backchannel enabled)", s.port, paths, s.authUser)
	} else {
		logger.Info("RTSP", "Server listening at rtsp://0.0.0.0:%d (paths: %v, Profile T Audio Backchannel enabled)", s.port, paths)
	}

	if err := s.server.Start(); err != nil {
		return err
	}

	s.mu.Lock()
	s.started = true
	srv := s.server
	s.mu.Unlock()

	s.streamsMu.RLock()
	for _, st := range s.streams {
		st.initServerStream(srv)
	}
	s.streamsMu.RUnlock()

	// Start dedicated UDP Backchannel Listener on port (e.g. 8554/udp)
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", s.port))
	if err == nil {
		conn, err := net.ListenUDP("udp", udpAddr)
		if err == nil {
			s.mu.Lock()
			s.udpConn = conn
			s.mu.Unlock()
			go s.readUDPBackchannelLoop(conn)
			logger.Info("RTSP", "🎙️ UDP Audio Backchannel receiver listening on 0.0.0.0:%d/udp", s.port)
		} else {
			logger.Warn("RTSP", "⚠️ Failed to bind UDP backchannel receiver on port %d: %v", s.port, err)
		}
	}

	return nil
}

// Close gracefully stops the RTSP server and closes all stream resources.
func (s *Server) Close() {
	s.mu.Lock()
	udpConn := s.udpConn
	s.udpConn = nil
	srv := s.server
	s.server = nil
	started := s.started
	s.started = false
	s.mu.Unlock()

	if udpConn != nil {
		_ = udpConn.Close()
	}

	s.streamsMu.RLock()
	for _, st := range s.streams {
		st.Close()
	}
	s.streamsMu.RUnlock()

	if srv != nil && started {
		srv.Close()
	}
}

func (s *Server) handleBackchannelPacket(medi *description.Media, pkt *rtp.Packet, source string, sess *gortsplib.ServerSession) {
	var targetStream *Stream
	if sess != nil {
		if val, ok := s.sessionStreams.Load(sess); ok {
			targetStream = val.(*Stream)
		}
	}
	if targetStream == nil {
		targetStream = s.primaryStream
	}
	if targetStream != nil {
		targetStream.handleBackchannelPacket(medi, pkt, source, sess)
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

		// Route UDP packet to active talk stream, or fallback to primary stream
		var targetStream *Stream
		s.streamsMu.RLock()
		for _, st := range s.streams {
			st.backchannelMu.Lock()
			if st.activeTalkSession != nil {
				targetStream = st
				st.backchannelMu.Unlock()
				break
			}
			st.backchannelMu.Unlock()
		}
		if targetStream == nil {
			targetStream = s.primaryStream
		}
		s.streamsMu.RUnlock()

		if targetStream != nil {
			targetStream.handleBackchannelPacket(nil, pkt, "UDP", nil)
		}
	}
}

// validateAuth validates incoming RTSP requests against configured authUser and authPass.
func (s *Server) validateAuth(req *base.Request, conn *gortsplib.ServerConn, sess *gortsplib.ServerSession) (bool, *base.Response) {
	if s.authUser == "" {
		return true, nil
	}

	if req.Header["Authorization"] == nil {
		if sess != nil && sess.UserData() == true {
			if conn != nil {
				conn.SetUserData(true)
			}
			return true, nil
		}
		if conn != nil && conn.UserData() == true {
			if sess != nil {
				sess.SetUserData(true)
			}
			return true, nil
		}
	}

	err := auth.Validate(
		req,
		s.authUser,
		s.authPass,
		[]auth.ValidateMethod{auth.ValidateMethodBasic, auth.ValidateMethodDigestMD5, auth.ValidateMethodSHA256},
		s.realm,
		s.nonce,
	)
	if err != nil {
		logger.Debug("RTSP", "🔒 Client auth challenge/failure for %s (%s): %v", req.Method, req.URL, err)
		return false, &base.Response{
			StatusCode: base.StatusUnauthorized,
			Header: base.Header{
				"WWW-Authenticate": auth.GenerateWWWAuthenticate(
					[]auth.ValidateMethod{auth.ValidateMethodBasic, auth.ValidateMethodDigestMD5, auth.ValidateMethodSHA256},
					s.realm,
					s.nonce,
				),
			},
		}
	}

	if sess != nil {
		sess.SetUserData(true)
	}
	if conn != nil {
		conn.SetUserData(true)
	}
	return true, nil
}

// --- gortsplib Server Callbacks ---

func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if ok, resp := s.validateAuth(ctx.Request, ctx.Conn, nil); !ok {
		return resp, nil, nil
	}

	logger.Trace("RTSP", "DESCRIBE request for path: %s", ctx.Path)
	st := s.findStreamForPath(ctx.Path)
	if st == nil {
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	st.mu.RLock()
	sStream := st.serverStream
	st.mu.RUnlock()

	return &base.Response{
		StatusCode: base.StatusOK,
	}, sStream, nil
}

func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if ok, resp := s.validateAuth(ctx.Request, ctx.Conn, ctx.Session); !ok {
		return resp, nil, nil
	}

	logger.Trace("RTSP", "SETUP request for path: %s", ctx.Path)
	st := s.findStreamForPath(ctx.Path)
	if st == nil {
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	if ctx.Session != nil {
		s.sessionStreams.Store(ctx.Session, st)
	}

	st.mu.RLock()
	sStream := st.serverStream
	st.mu.RUnlock()

	return &base.Response{
		StatusCode: base.StatusOK,
	}, sStream, nil
}

func (s *Server) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	if ok, resp := s.validateAuth(ctx.Request, ctx.Conn, ctx.Session); !ok {
		return resp, nil
	}

	var targetStream *Stream
	if ctx.Session != nil {
		if val, ok := s.sessionStreams.Load(ctx.Session); ok {
			targetStream = val.(*Stream)
		}
	}
	if targetStream == nil {
		targetStream = s.findStreamForPath(ctx.Path)
	}

	if targetStream != nil {
		if ctx.Session != nil {
			s.sessionStreams.Store(ctx.Session, targetStream)
		}
		targetStream.handlePlay(ctx.Path, ctx.Session)
	}

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (s *Server) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	if ok, resp := s.validateAuth(ctx.Request, ctx.Conn, ctx.Session); !ok {
		return resp, nil
	}

	logger.Debug("RTSP", "🎙️ OnRecord called on %s", ctx.Path)
	var targetStream *Stream
	if ctx.Session != nil {
		if val, ok := s.sessionStreams.Load(ctx.Session); ok {
			targetStream = val.(*Stream)
		}
	}
	if targetStream == nil {
		targetStream = s.findStreamForPath(ctx.Path)
	}

	if targetStream != nil && ctx.Session != nil {
		for _, medi := range ctx.Session.SetuppedMedias() {
			cmedia := medi
			for _, forma := range medi.Formats {
				ctx.Session.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
					targetStream.handleBackchannelPacket(cmedia, pkt, "RECORD", ctx.Session)
				})
			}
		}
	}

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (s *Server) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	if ctx.Session == nil {
		return
	}

	var targetStream *Stream
	if val, ok := s.sessionStreams.LoadAndDelete(ctx.Session); ok {
		targetStream = val.(*Stream)
	}

	if targetStream != nil {
		if _, loaded := targetStream.playingSessions.LoadAndDelete(ctx.Session); loaded {
			count := targetStream.activeClients.Add(-1)
			if count < 0 {
				targetStream.activeClients.Store(0)
				count = 0
			}
			logger.Debug("RTSP", "⏹️ Client disconnected from '%s' (active clients: %d)", targetStream.pathName, count)
		}

		targetStream.backchannelMu.Lock()
		if targetStream.activeTalkSession == ctx.Session {
			targetStream.activeTalkSession = nil
			logger.Info("RTSP", "🎙️ Active audio backchannel session closed on '%s', releasing talk lock", targetStream.pathName)
		}
		targetStream.backchannelMu.Unlock()
	} else {
		// Fallback check across all streams
		s.streamsMu.RLock()
		for _, st := range s.streams {
			if _, loaded := st.playingSessions.LoadAndDelete(ctx.Session); loaded {
				count := st.activeClients.Add(-1)
				if count < 0 {
					st.activeClients.Store(0)
					count = 0
				}
				logger.Debug("RTSP", "⏹️ Client disconnected from '%s' (fallback) (active clients: %d)", st.pathName, count)
			}
			st.backchannelMu.Lock()
			if st.activeTalkSession == ctx.Session {
				st.activeTalkSession = nil
			}
			st.backchannelMu.Unlock()
		}
		s.streamsMu.RUnlock()
	}
}

// --- Backward-Compatible Server Delegation Methods ---

func (s *Server) SetAudioBackchannelHandler(handler AudioBackchannelHandler) {
	if s.primaryStream != nil {
		s.primaryStream.SetAudioBackchannelHandler(handler)
	}
}

func (s *Server) SetOnPlayHandler(handler OnPlayHandler) {
	if s.primaryStream != nil {
		s.primaryStream.SetOnPlayHandler(handler)
	}
}

func (s *Server) ActiveClients() int64 {
	s.streamsMu.RLock()
	defer s.streamsMu.RUnlock()
	var total int64
	for _, st := range s.streams {
		total += st.ActiveClients()
	}
	return total
}

func (s *Server) WriteVideoPacket(pkt *rtp.Packet) {
	if s.primaryStream != nil {
		s.primaryStream.WriteVideoPacket(pkt)
	}
}

func (s *Server) GetAudioCodec() string {
	if s.primaryStream != nil {
		return s.primaryStream.GetAudioCodec()
	}
	return "aac"
}

func (s *Server) WriteAACFrame(au []byte, pts time.Duration) {
	if s.primaryStream != nil {
		s.primaryStream.WriteAACFrame(au, pts)
	}
}

func (s *Server) WriteAudioPacket(pkt *rtp.Packet) {
	if s.primaryStream != nil {
		s.primaryStream.WriteAudioPacket(pkt)
	}
}
