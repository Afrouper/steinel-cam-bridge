package rtsp

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtpmpeg4audio"
	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"
)

// MediaStream defines the common interface for streaming video/audio and handling client callbacks.
// Both *Stream (individual path stream) and *Server (primary stream facade) implement MediaStream.
type MediaStream interface {
	WriteVideoPacket(pkt *rtp.Packet)
	WriteAACFrame(au []byte, pts time.Duration)
	WriteAudioPacket(pkt *rtp.Packet)
	GetAudioCodec() string
	SetAudioBackchannelHandler(handler AudioBackchannelHandler)
	SetOnPlayHandler(handler OnPlayHandler)
	ActiveClients() int64
	SetH264Parameters(sps, pps []byte)
}

var _ MediaStream = (*Stream)(nil)
var _ MediaStream = (*Server)(nil)

// Stream encapsulates the media tracks, encoding and client state for a single camera RTSP stream.
type Stream struct {
	pathName                string
	serverStream            *gortsplib.ServerStream
	session                 *description.Session
	videoFormat             *format.H264
	audioCodec              string
	audioFormat             format.Format
	aacRTPEncoder           *rtpmpeg4audio.Encoder
	backchannelFormat       *format.G711
	videoMedia              *description.Media
	audioMedia              *description.Media
	backchannelMedia        *description.Media
	audioBackchannelHandler AudioBackchannelHandler
	onPlayHandler           OnPlayHandler
	backchannelPacketCount  atomic.Uint64
	activeClients           atomic.Int64
	playingSessions         sync.Map // *gortsplib.ServerSession -> struct{}
	backchannelMu           sync.Mutex
	activeTalkSession       *gortsplib.ServerSession
	lastTalkTime            time.Time
	video                   *videoPipe
	holdStop                chan struct{}
	mu                      sync.RWMutex
}

// NewStream creates and configures a new Stream instance for a given path and audio codec.
func NewStream(pathName string, audioCodec string) (*Stream, error) {
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
			return nil, fmt.Errorf("failed to create aac rtp encoder for stream '%s': %w", pathName, err)
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

	return &Stream{
		pathName:          pathName,
		session:           meds,
		videoFormat:       vFormat,
		audioCodec:        audioCodec,
		audioFormat:       aFormat,
		aacRTPEncoder:     aacEncoder,
		backchannelFormat: bcFormat,
		videoMedia:        vMedia,
		audioMedia:        aMedia,
		backchannelMedia:  bcMedia,
		video:             newVideoPipe(),
	}, nil
}

// SetVideoOptions configures instant start for late joiners and how long the
// last keyframe is repeated while the camera is away (0 disables holding).
func (st *Stream) SetVideoOptions(instantStart bool, holdLastFrame time.Duration) {
	st.video.mu.Lock()
	defer st.video.mu.Unlock()
	st.video.instantStart = instantStart
	st.video.holdMax = holdLastFrame
}

// VideoSize returns the picture size found in the camera's H.264 stream and
// the time it was first seen (zero values while unknown).
func (st *Stream) VideoSize() (width int, height int, since time.Time) {
	st.video.mu.Lock()
	defer st.video.mu.Unlock()
	return st.video.width, st.video.height, st.video.sizeSince
}

// VideoStats describes the state of the video pipeline for diagnostics.
type VideoStats struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Live        bool   `json:"live"`
	Holding     bool   `json:"holding_last_frame"`
	HoldFrames  uint64 `json:"hold_frames"`
	JoinBursts  uint64 `json:"instant_starts"`
	Restarts    uint64 `json:"source_restarts"`
	CachedPkts  int    `json:"cached_packets"`
	CachedBytes int    `json:"cached_bytes"`
}

// VideoStats returns a snapshot of the video pipeline counters.
func (st *Stream) VideoStats() VideoStats {
	st.video.mu.Lock()
	defer st.video.mu.Unlock()
	v := st.video
	return VideoStats{
		Width:       v.width,
		Height:      v.height,
		Live:        !v.lastLiveAt.IsZero() && time.Since(v.lastLiveAt) < liveWindow,
		Holding:     v.holdActive,
		HoldFrames:  v.holdFrames,
		JoinBursts:  v.joinBursts,
		Restarts:    v.restarts,
		CachedPkts:  len(v.gop),
		CachedBytes: v.gopBytes,
	}
}

// runHoldLoop repeats the last keyframe while the camera delivers nothing, so
// that connected clients keep their session and resume without reconnecting.
func (st *Stream) runHoldLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(holdTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			st.mu.RLock()
			sStream := st.serverStream
			st.mu.RUnlock()
			if sStream == nil {
				continue
			}
			st.video.mu.Lock()
			for _, pkt := range st.video.holdFrame(now) {
				_ = sStream.WritePacketRTP(st.videoMedia, pkt)
			}
			st.video.mu.Unlock()
		}
	}
}

// initServerStream binds the stream to a running gortsplib.Server.
func (st *Stream) initServerStream(srv *gortsplib.Server) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.serverStream == nil && srv != nil {
		st.serverStream = gortsplib.NewServerStream(srv, st.session)
		st.video.mu.Lock()
		st.video.onHoldChange = func(active bool) {
			if active {
				logger.Warn("RTSP", "🧊 No video from camera on '%s': holding last picture for connected clients", st.pathName)
			} else {
				logger.Info("RTSP", "▶️ Live video resumed on '%s'", st.pathName)
			}
		}
		st.video.mu.Unlock()
		st.holdStop = make(chan struct{})
		go st.runHoldLoop(st.holdStop)
	}
}

// Close closes the underlying ServerStream.
func (st *Stream) Close() {
	st.mu.Lock()
	stream := st.serverStream
	st.serverStream = nil
	if st.holdStop != nil {
		close(st.holdStop)
		st.holdStop = nil
	}
	st.mu.Unlock()

	if stream != nil {
		stream.Close()
	}
}

// PathName returns the registered RTSP path of this stream.
func (st *Stream) PathName() string {
	return st.pathName
}

// GetAudioCodec returns the configured audio codec ("aac" or "pcmu").
func (st *Stream) GetAudioCodec() string {
	return st.audioCodec
}

// SetAudioBackchannelHandler registers a callback for received backchannel RTP packets.
func (st *Stream) SetAudioBackchannelHandler(handler AudioBackchannelHandler) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.audioBackchannelHandler = handler
}

// SetOnPlayHandler registers a callback executed when a client starts playing this stream.
func (st *Stream) SetOnPlayHandler(handler OnPlayHandler) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.onPlayHandler = handler
}

// ActiveClients returns the number of currently playing sessions for this stream.
func (st *Stream) ActiveClients() int64 {
	cnt := st.activeClients.Load()
	if cnt < 0 {
		return 0
	}
	return cnt
}

// WriteVideoPacket forwards an H.264 RTP packet to all clients connected to this stream.
func (st *Stream) WriteVideoPacket(pkt *rtp.Packet) {
	st.mu.RLock()
	sStream := st.serverStream
	st.mu.RUnlock()

	if sStream == nil {
		return
	}

	st.video.mu.Lock()
	defer st.video.mu.Unlock()
	out := st.video.ingest(pkt, time.Now())
	if out == nil {
		return
	}
	if sps, pps, ok := st.video.takeParams(); ok {
		// Advertise the parameter sets in the SDP so clients can set up their
		// decoder before the first keyframe arrives.
		st.videoFormat.SafeSetParams(sps, pps)
	}
	_ = sStream.WritePacketRTP(st.videoMedia, out)
}

// sendJoinBurst queues the current group of pictures for a session that is
// about to start playing, so it can show a picture without waiting for the
// camera's next keyframe. It must be called from within the OnPlay callback:
// gortsplib queues the packets and flushes them right after the PLAY response.
func (st *Stream) sendJoinBurst(sess *gortsplib.ServerSession) {
	if tr := sess.SetuppedTransport(); tr == nil || *tr == gortsplib.TransportUDPMulticast {
		return
	}
	hasVideo := false
	for _, m := range sess.SetuppedMedias() {
		if m == st.videoMedia {
			hasVideo = true
			break
		}
	}
	if !hasVideo {
		return
	}

	st.video.mu.Lock()
	burst := st.video.joinBurst(time.Now())
	if len(burst) == 0 {
		st.video.mu.Unlock()
		return
	}
	for _, pkt := range burst {
		_ = sess.WritePacketRTP(st.videoMedia, pkt)
	}
	logger.Debug("RTSP", "⚡ Instant start: sent %d cached packets to new client on '%s'", len(burst), st.pathName)
	// The session only receives live packets once OnPlay has returned. Keep the
	// video path blocked for that moment so no packet falls into the gap.
	go func() {
		time.Sleep(joinBurstSettleWindow)
		st.video.mu.Unlock()
	}()
}

// WriteAACFrame encodes and writes an AAC frame to all clients connected to this stream.
func (st *Stream) WriteAACFrame(au []byte, pts time.Duration) {
	st.mu.Lock()
	sStream := st.serverStream
	enc := st.aacRTPEncoder
	st.mu.Unlock()

	if sStream == nil || enc == nil || len(au) == 0 {
		return
	}

	pkts, err := enc.Encode([][]byte{au})
	if err != nil {
		return
	}

	ts := uint32((pts * 16) / time.Millisecond)
	for _, pkt := range pkts {
		pkt.Timestamp = ts
		_ = sStream.WritePacketRTP(st.audioMedia, pkt)
	}
}

// WriteAudioPacket forwards a PCMU RTP packet to all clients connected to this stream.
func (st *Stream) WriteAudioPacket(pkt *rtp.Packet) {
	st.mu.RLock()
	sStream := st.serverStream
	st.mu.RUnlock()

	if sStream == nil {
		return
	}
	pkt.PayloadType = 0
	_ = sStream.WritePacketRTP(st.audioMedia, pkt)
}

// SetH264Parameters dynamically updates the SPS and PPS parameter sets for the H.264 video track.
// Subsequent RTSP DESCRIBE requests return sprop-parameter-sets and profile-level-id in the SDP.
func (st *Stream) SetH264Parameters(sps, pps []byte) {
	if st.videoFormat != nil && len(sps) > 0 && len(pps) > 0 {
		st.videoFormat.SafeSetParams(sps, pps)
	}
}

// handlePlay tracks an active client session and triggers the OnPlay callback.
func (st *Stream) handlePlay(path string, sess *gortsplib.ServerSession) {
	if sess != nil {
		if _, loaded := st.playingSessions.LoadOrStore(sess, struct{}{}); !loaded {
			count := st.activeClients.Add(1)
			logger.Debug("RTSP", "▶️ Client connected and playing stream (%s, active clients: %d)", path, count)
			if count == 1 {
				logger.Info("RTSP", "▶️ Stream active (client connected: %s)", path)
			}
			st.sendJoinBurst(sess)
		}
	}

	st.mu.RLock()
	handler := st.onPlayHandler
	st.mu.RUnlock()
	if handler != nil {
		go handler()
	}
}

// handleBackchannelPacket handles an incoming backchannel RTP packet.
func (st *Stream) handleBackchannelPacket(medi *description.Media, pkt *rtp.Packet, source string, sess *gortsplib.ServerSession) {
	if medi == nil || medi == st.backchannelMedia || medi.IsBackChannel {
		// Single-talker exclusivity per stream
		now := time.Now()
		st.backchannelMu.Lock()
		if st.activeTalkSession == nil || (st.activeTalkSession != sess && now.Sub(st.lastTalkTime) > 2*time.Second) {
			st.activeTalkSession = sess
			st.lastTalkTime = now
			logger.Info("RTSP", "🎙️ Audio backchannel acquired by session (%s) for stream '%s'", source, st.pathName)
		} else if st.activeTalkSession != sess {
			st.backchannelMu.Unlock()
			logger.Trace("RTSP", "🎙️ Audio backchannel busy: dropping packet from secondary session (%s) for stream '%s'", source, st.pathName)
			return
		}
		st.lastTalkTime = now
		st.backchannelMu.Unlock()

		cnt := st.backchannelPacketCount.Add(1)
		if cnt == 1 {
			logger.Info("RTSP", "🎙️ Audio backchannel stream started (%s, PT=%d, SSRC=%d, path=%s)", source, pkt.PayloadType, pkt.SSRC, st.pathName)
		} else if cnt%50 == 0 {
			logger.Debug("RTSP", "🎙️ Forwarding audio backchannel (%s) packet #%d (PT=%d, SSRC=%d, Seq=%d, Payload=%d bytes, path=%s)",
				source, cnt, pkt.PayloadType, pkt.SSRC, pkt.SequenceNumber, len(pkt.Payload), st.pathName)
		} else {
			logger.Trace("RTSP", "🎙️ Audio backchannel (%s) packet #%d (PT=%d, SSRC=%d, Seq=%d, Payload=%d bytes, path=%s)",
				source, cnt, pkt.PayloadType, pkt.SSRC, pkt.SequenceNumber, len(pkt.Payload), st.pathName)
		}

		st.mu.RLock()
		handler := st.audioBackchannelHandler
		st.mu.RUnlock()

		if handler != nil {
			_ = handler(pkt)
		}
	}
}
