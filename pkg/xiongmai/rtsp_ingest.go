package xiongmai

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

var passwordQueryRegex = regexp.MustCompile(`_password=([^_]*)_`)

// dialError indicates that the initial TCP connection to the RTSP server port failed.
type dialError struct {
	host string
	err  error
}

func (e *dialError) Error() string {
	return fmt.Sprintf("TCP dial to RTSP server %s failed: %v", e.host, e.err)
}

func (e *dialError) Unwrap() error {
	return e.err
}

// SanitizeRTSPURL safely masks password credentials in RTSP URLs (both in URL authority and query paths).
func SanitizeRTSPURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "[invalid-url]"
	}
	redacted := u.Redacted()
	return passwordQueryRegex.ReplaceAllString(redacted, "_password=***_")
}

// buildAuthPrefix builds the standard username:password@ prefix for traditional RTSP URLs.
func buildAuthPrefix(user, password string) string {
	if password != "" {
		return fmt.Sprintf("%s:%s@", user, password)
	}
	if user != "" && user != "admin" {
		return fmt.Sprintf("%s@", user)
	}
	return ""
}

// getCandidateURLs returns ordered candidate RTSP URLs for Steinel / Xiongmai cameras.
func getCandidateURLs(cameraIP string, port int, user, password string, streamSubtype int, fallbackPasswords ...string) []string {
	if port <= 0 {
		port = RTSPPort
	}
	if user == "" {
		user = "admin"
	}

	cleanUser := strings.TrimSpace(user)
	pwds := []string{strings.TrimSpace(password)}
	for _, fp := range fallbackPasswords {
		trimmed := strings.TrimSpace(fp)
		if trimmed != "" && trimmed != pwds[0] {
			pwds = append(pwds, trimmed)
		}
	}
	hasEmpty := false
	for _, p := range pwds {
		if p == "" {
			hasEmpty = true
			break
		}
	}
	if !hasEmpty {
		pwds = append(pwds, "")
	}

	var urls []string
	for _, cleanPwd := range pwds {
		urls = append(urls,
			// 1. Steinel L 620 canonical path format without ?real_stream (Clean path, prevents camera buffer overflow "buffer length exceeds 64")
			fmt.Sprintf("rtsp://%s:%d/user=%s_password=%s_channel=1_stream=%d.sdp",
				cameraIP, port, cleanUser, cleanPwd, streamSubtype),
			// 2. Steinel L 620 canonical path format with ?real_stream (Legacy / MotionEye query parameter)
			fmt.Sprintf("rtsp://%s:%d/user=%s_password=%s_channel=1_stream=%d.sdp?real_stream",
				cameraIP, port, cleanUser, cleanPwd, streamSubtype),
			// 3. Fallback standard Xiongmai path
			fmt.Sprintf("rtsp://%s%s:%d/stream=%d",
				buildAuthPrefix(cleanUser, cleanPwd), cameraIP, port, streamSubtype),
			// 4. Fallback Generic style path
			fmt.Sprintf("rtsp://%s%s:%d/h264/ch1/main/av_stream",
				buildAuthPrefix(cleanUser, cleanPwd), cameraIP, port),
			// 5. Fallback live channel path
			fmt.Sprintf("rtsp://%s%s:%d/live/ch%d",
				buildAuthPrefix(cleanUser, cleanPwd), cameraIP, port, streamSubtype),
		)
	}
	return urls
}

// RTSPIngest handles reading the live H.264/H.265 and G.711/AAC stream from the camera's
// internal RTSP server (port 554) and feeding it into the local RTSP server and audio transcoder.
type RTSPIngest struct {
	candidateURLs   []string
	rtspServer      rtsp.MediaStream
	audioTranscoder *audio.Transcoder
	closeChan       chan struct{}
	closed          atomic.Bool
	mu              sync.Mutex
	client          *gortsplib.Client
}

// NewRTSPIngest creates a new RTSP Ingest client with Steinel and Xiongmai streaming paths.
func NewRTSPIngest(cameraIP string, port int, user, password string, streamSubtype int, rtspServer rtsp.MediaStream, fallbackPasswords ...string) *RTSPIngest {
	candidateURLs := getCandidateURLs(cameraIP, port, user, password, streamSubtype, fallbackPasswords...)

	var transcoder *audio.Transcoder
	if rtspServer != nil && rtspServer.GetAudioCodec() == "aac" {
		transcoder = audio.NewTranscoder(func(au []byte, pts time.Duration) {
			rtspServer.WriteAACFrame(au, pts)
		})
	}

	return &RTSPIngest{
		candidateURLs:   candidateURLs,
		rtspServer:      rtspServer,
		audioTranscoder: transcoder,
		closeChan:       make(chan struct{}),
	}
}

// Start begins the RTSP ingest loop in the background.
func (ing *RTSPIngest) Start(ctx context.Context) error {
	go ing.ingestLoop(ctx)
	return nil
}

// ingestLoop maintains the RTSP connection to the camera with auto-reconnect.
func (ing *RTSPIngest) ingestLoop(ctx context.Context) {
	backoff := 1 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-ing.closeChan:
			return
		default:
		}

		if len(ing.candidateURLs) > 0 {
			logger.Debug("Xiongmai Ingest", "🔌 Connecting to camera RTSP stream at %s", SanitizeRTSPURL(ing.candidateURLs[0]))
		}

		err := ing.runSession(ctx)
		if err != nil && !ing.closed.Load() {
			var dErr *dialError
			if errors.As(err, &dErr) {
				// RTSP port 554 is down / refused (e.g. camera rebooting or busy)
				// Use exponential backoff (5s up to 60s) to avoid overwhelming the camera SoC
				if backoff < 5*time.Second {
					backoff = 5 * time.Second
				}
				logger.Warn("Xiongmai Ingest", "⚠️ Camera RTSP server %s unreachable: %v (reconnecting in %v)", dErr.host, dErr.err, backoff)
			} else {
				logger.Warn("Xiongmai Ingest", "⚠️ RTSP connection lost: %v (reconnecting in %v)", err, backoff)
			}

			select {
			case <-ctx.Done():
				return
			case <-ing.closeChan:
				return
			case <-time.After(backoff):
			}

			if backoff < 60*time.Second {
				backoff *= 2
				if backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
			}
		} else {
			backoff = 1 * time.Second
		}
	}
}

// selectIngestVideoTrack chooses a compatible video track from a camera SDP description.
// H.264 is preferred for widest client compatibility; H.265 (HEVC) is selected if no H.264 track exists.
func selectIngestVideoTrack(medias []*description.Media) (*description.Media, format.Format, string) {
	var h265Media *description.Media
	var h265Format *format.H265

	for _, medi := range medias {
		var f264 *format.H264
		if medi.FindFormat(&f264) {
			return medi, f264, rtsp.VideoCodecH264
		}
		var f265 *format.H265
		if h265Media == nil && medi.FindFormat(&f265) {
			h265Media = medi
			h265Format = f265
		}
	}

	if h265Media != nil {
		return h265Media, h265Format, rtsp.VideoCodecH265
	}
	return nil, nil, ""
}

// configureIngestVideoTrack aligns the local RTSP output track and SDP parameter sets with
// the video track negotiated on the camera's RTSP server.
func configureIngestVideoTrack(writer rtsp.MediaStream, videoFormat format.Format, rawURL string) {
	if writer == nil {
		return
	}

	switch f := videoFormat.(type) {
	case *format.H264:
		if writer.SetVideoCodec(rtsp.VideoCodecH264) {
			logger.Info("Xiongmai Ingest", "📹 Camera RTSP stream on %s uses H.264; switched local RTSP output to H.264", SanitizeRTSPURL(rawURL))
		}
		if sps, pps := f.SafeParams(); len(sps) > 0 && len(pps) > 0 {
			writer.SetH264Parameters(sps, pps)
		}
	case *format.H265:
		if writer.SetVideoCodec(rtsp.VideoCodecH265) {
			logger.Info("Xiongmai Ingest", "📹 Camera RTSP stream on %s uses H.265 (HEVC); switched local RTSP output to H.265", SanitizeRTSPURL(rawURL))
		} else {
			logger.Debug("Xiongmai Ingest", "📹 Camera RTSP stream on %s uses H.265 (HEVC)", SanitizeRTSPURL(rawURL))
		}
		if vps, sps, pps := f.SafeParams(); len(vps) > 0 && len(sps) > 0 && len(pps) > 0 {
			writer.SetH265Parameters(vps, sps, pps)
		}
	}
}

// normalizeH265RTPPacket ensures RFC 7798 Aggregation Packets (NALU type 48) carry a non-zero
// nuh_temporal_id_plus1 without mutating the caller's RTP packet or payload slice.
func normalizeH265RTPPacket(pkt *rtp.Packet) *rtp.Packet {
	if pkt == nil || !needsHEVCAggregationTemporalIDFix(pkt.Payload) {
		return pkt
	}
	cp := *pkt
	cp.Payload = bytes.Clone(pkt.Payload)
	cp.Payload[1] |= 0x01
	return &cp
}

// h265RTPParamTracker extracts in-band H.265 VPS/SPS/PPS parameter sets from incoming RFC 7798
// RTP packets (Single NAL and Aggregation Packets) when the camera SDP omits or updates them.
type h265RTPParamTracker struct {
	maxDONDiff int
	vps        []byte
	sps        []byte
	pps        []byte
}

func newH265RTPParamTracker(f *format.H265) *h265RTPParamTracker {
	vps, sps, pps := f.SafeParams()
	return &h265RTPParamTracker{
		maxDONDiff: f.MaxDONDiff,
		vps:        bytes.Clone(vps),
		sps:        bytes.Clone(sps),
		pps:        bytes.Clone(pps),
	}
}

func (t *h265RTPParamTracker) observe(payload []byte, writer rtsp.MediaStream) {
	if len(payload) < 2 || payload[0]&0x80 != 0 || writer == nil {
		return
	}

	nalType := hevcNALUType(payload[0])
	var updated bool

	switch {
	case nalType >= hevcNALUVPS && nalType <= hevcNALUPPS:
		if !isHEVCLayerZeroHeader(payload) {
			return
		}
		updated = t.recordParam(nalType, payload)
	case nalType == hevcNALUAggregationPacket && t.maxDONDiff == 0:
		// RFC 7798 Section 4.4.2: Aggregation Packet without DONL (sprop-max-don-diff == 0).
		offset := 2
		for offset+2 <= len(payload) {
			naluSize := int(binary.BigEndian.Uint16(payload[offset : offset+2]))
			offset += 2
			if naluSize == 0 || offset+naluSize > len(payload) {
				break
			}
			nalu := payload[offset : offset+naluSize]
			offset += naluSize
			if len(nalu) >= 2 && nalu[0]&0x80 == 0 && isHEVCLayerZeroHeader(nalu) {
				if t.recordParam(hevcNALUType(nalu[0]), nalu) {
					updated = true
				}
			}
		}
	default:
		return
	}

	if updated && len(t.vps) > 0 && len(t.sps) > 0 && len(t.pps) > 0 {
		writer.SetH265Parameters(t.vps, t.sps, t.pps)
	}
}

func (t *h265RTPParamTracker) recordParam(nalType byte, nalu []byte) bool {
	switch nalType {
	case hevcNALUVPS:
		if bytes.Equal(t.vps, nalu) {
			return false
		}
		t.vps = bytes.Clone(nalu)
		return true
	case hevcNALUSPS:
		if bytes.Equal(t.sps, nalu) {
			return false
		}
		t.sps = bytes.Clone(nalu)
		return true
	case hevcNALUPPS:
		if bytes.Equal(t.pps, nalu) {
			return false
		}
		t.pps = bytes.Clone(nalu)
		return true
	default:
		return false
	}
}

// runSession tries the candidate RTSP URLs in sequence until one establishes streaming.
func (ing *RTSPIngest) runSession(ctx context.Context) error {
	var lastErr error

	for _, rawURL := range ing.candidateURLs {
		if ctx.Err() != nil || ing.closed.Load() {
			return nil
		}
		logger.Trace("Xiongmai Ingest", "Attempting RTSP URL candidate: %s", SanitizeRTSPURL(rawURL))
		u, err := base.ParseURL(rawURL)
		if err != nil {
			continue
		}

		transport := gortsplib.TransportTCP
		client := &gortsplib.Client{
			Transport:    &transport, // Use TCP interleaved for reliable local transmission
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
		}

		ing.mu.Lock()
		ing.client = client
		ing.mu.Unlock()

		err = func() error {
			defer func() {
				ing.mu.Lock()
				if ing.client == client {
					ing.client = nil
				}
				ing.mu.Unlock()
				client.Close()
			}()

			if err := client.Start(u.Scheme, u.Host); err != nil {
				return &dialError{host: u.Host, err: err}
			}

			desc, _, err := client.Describe(u)
			if err != nil {
				var opErr *net.OpError
				if errors.As(err, &opErr) {
					return &dialError{host: u.Host, err: err}
				}
				return fmt.Errorf("describe error: %w", err)
			}

			// Log detailed SDP track information
			for i, medi := range desc.Medias {
				var formatNames []string
				for _, f := range medi.Formats {
					formatNames = append(formatNames, fmt.Sprintf("%s (pt=%d, clock=%d)", f.Codec(), f.PayloadType(), f.ClockRate()))
				}
				logger.Debug("Xiongmai Ingest", "📹 Camera SDP Track #%d [%s]: %s", i+1, medi.Type, strings.Join(formatNames, ", "))
			}

			// Setup Video Media (prefer H.264, fall back to H.265)
			videoMedia, videoFormat, videoCodec := selectIngestVideoTrack(desc.Medias)
			if videoMedia == nil {
				return fmt.Errorf("no compatible H.264/H.265 video track found in SDP on %s", SanitizeRTSPURL(rawURL))
			}

			_, err = client.Setup(desc.BaseURL, videoMedia, 0, 0)
			if err != nil {
				return fmt.Errorf("video track setup failed: %w", err)
			}

			configureIngestVideoTrack(ing.rtspServer, videoFormat, rawURL)

			var h265Params *h265RTPParamTracker
			if f265, ok := videoFormat.(*format.H265); ok {
				h265Params = newH265RTPParamTracker(f265)
			}

			var firstPacketOnce sync.Once
			var firstPacketReceived atomic.Bool
			var lastPacketTime atomic.Int64

			client.OnPacketRTP(videoMedia, videoFormat, func(pkt *rtp.Packet) {
				lastPacketTime.Store(time.Now().UnixNano())
				firstPacketOnce.Do(func() {
					firstPacketReceived.Store(true)
					logger.Info("Xiongmai Ingest", "▶️ Streaming active: received %s video packets from camera via %s", strings.ToUpper(videoCodec), SanitizeRTSPURL(rawURL))
				})
				if ing.rtspServer != nil {
					if h265Params != nil {
						h265Params.observe(pkt.Payload, ing.rtspServer)
						pkt = normalizeH265RTPPacket(pkt)
					}
					ing.rtspServer.WriteVideoPacket(pkt)
				}
			})

			// Setup Audio Media (G.711 PCMU/PCMA or AAC) unless audio is disabled
			audioDisabled := ing.rtspServer != nil && rtsp.IsAudioDisabled(ing.rtspServer.GetAudioCodec())
			var audioMedia *description.Media
			var audioFormatG711 *format.G711
			var audioFormatAAC *format.MPEG4Audio
			if !audioDisabled {
				for _, medi := range desc.Medias {
					var fG711 *format.G711
					if medi.FindFormat(&fG711) {
						audioMedia = medi
						audioFormatG711 = fG711
						break
					}
					var fAAC *format.MPEG4Audio
					if medi.FindFormat(&fAAC) {
						audioMedia = medi
						audioFormatAAC = fAAC
						break
					}
				}
			}

			if audioMedia != nil {
				_, err := client.Setup(desc.BaseURL, audioMedia, 0, 0)
				if err != nil {
					logger.Warn("Xiongmai Ingest", "⚠️ Audio track setup failed: %v", err)
				} else if audioFormatG711 != nil {
					client.OnPacketRTP(audioMedia, audioFormatG711, func(pkt *rtp.Packet) {
						if ing.audioTranscoder != nil {
							if audioFormatG711.MULaw {
								_ = ing.audioTranscoder.ProcessPCMU(pkt.Payload)
							} else {
								_ = ing.audioTranscoder.ProcessPCMA(pkt.Payload)
							}
						} else if ing.rtspServer != nil {
							outPkt := pkt
							if !audioFormatG711.MULaw {
								cp := *pkt
								cp.Payload = audio.TranscodePCMAToPCMU(pkt.Payload)
								outPkt = &cp
							}
							ing.rtspServer.WriteAudioPacket(outPkt)
						}
					})
				} else if audioFormatAAC != nil {
					client.OnPacketRTP(audioMedia, audioFormatAAC, func(pkt *rtp.Packet) {
						if ing.rtspServer != nil {
							ing.rtspServer.WriteAudioPacket(pkt)
						}
					})
				}
			}

			_, err = client.Play(nil)
			if err != nil {
				return fmt.Errorf("play error: %w", err)
			}

			logger.Debug("Xiongmai Ingest", "📡 PLAY request acknowledged by camera on %s, awaiting RTP data...", SanitizeRTSPURL(rawURL))

			// Watchdog: detect zero RTP video packets within 5 seconds of PLAY, or silence >15 seconds thereafter
			sessionCtx, sessionCancel := context.WithCancel(context.Background())
			defer sessionCancel()

			go func() {
				// Phase 1: 5-second initial frame watchdog
				select {
				case <-sessionCtx.Done():
					return
				case <-ctx.Done():
					client.Close()
					return
				case <-ing.closeChan:
					return
				case <-time.After(5 * time.Second):
					if !firstPacketReceived.Load() {
						logger.Warn("Xiongmai Ingest", "⚠️ Zero video RTP packets received within 5s after PLAY on %s — aborting candidate", SanitizeRTSPURL(rawURL))
						client.Close()
						return
					}
				}

				// Phase 2: 15-second silence watchdog while streaming
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-sessionCtx.Done():
						return
					case <-ctx.Done():
						client.Close()
						return
					case <-ing.closeChan:
						return
					case <-ticker.C:
						last := lastPacketTime.Load()
						if last > 0 && time.Since(time.Unix(0, last)) > 15*time.Second {
							logger.Warn("Xiongmai Ingest", "⚠️ RTSP video stream silence (>15s without packets) on %s — reconnecting", SanitizeRTSPURL(rawURL))
							client.Close()
							return
						}
					}
				}
			}()

			// Block until connection is closed or error occurs
			return client.Wait()
		}()

		if err == nil || ing.closed.Load() || ctx.Err() != nil {
			return nil
		}

		var dErr *dialError
		if errors.As(err, &dErr) {
			// Port 554 is closed or unreachable on the camera.
			// Do NOT iterate through remaining candidate URLs on the same unreachable port!
			logger.Debug("Xiongmai Ingest", "ℹ️ RTSP port at %s unreachable (%v) — skipping remaining candidate URLs", dErr.host, dErr.err)
			return err
		}

		lastErr = err
		logger.Debug("Xiongmai Ingest", "ℹ️ Candidate URL %s failed: %v", SanitizeRTSPURL(rawURL), err)
	}

	return lastErr
}

// Close terminates the ingest client.
func (ing *RTSPIngest) Close() error {
	if ing.closed.Swap(true) {
		return nil
	}
	close(ing.closeChan)

	ing.mu.Lock()
	client := ing.client
	ing.client = nil
	ing.mu.Unlock()

	if client != nil {
		client.Close()
	}
	if ing.audioTranscoder != nil {
		ing.audioTranscoder.Close()
	}
	return nil
}
