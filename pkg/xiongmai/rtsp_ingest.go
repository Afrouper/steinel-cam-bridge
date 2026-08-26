package xiongmai

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

var passwordQueryRegex = regexp.MustCompile(`_password=([^_]*)_`)

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
func getCandidateURLs(cameraIP string, port int, user, password string, streamSubtype int) []string {
	if port <= 0 {
		port = RTSPPort
	}
	if user == "" {
		user = "admin"
	}

	cleanUser := strings.TrimSpace(user)
	cleanPwd := strings.TrimSpace(password)

	return []string{
		// 1. Steinel L 620 canonical path format with ?real_stream (Confirmed working in MotionEye)
		fmt.Sprintf("rtsp://%s:%d/user=%s_password=%s_channel=1_stream=%d.sdp?real_stream",
			cameraIP, port, cleanUser, cleanPwd, streamSubtype),
		// 2. Steinel L 620 canonical path format without ?real_stream
		fmt.Sprintf("rtsp://%s:%d/user=%s_password=%s_channel=1_stream=%d.sdp",
			cameraIP, port, cleanUser, cleanPwd, streamSubtype),
		// 3. Fallback standard Xiongmai path
		fmt.Sprintf("rtsp://%s%s:%d/stream=%d",
			buildAuthPrefix(cleanUser, cleanPwd), cameraIP, port, streamSubtype),
		// 4. Fallback Generic style path
		fmt.Sprintf("rtsp://%s%s:%d/h264/ch1/main/av_stream",
			buildAuthPrefix(cleanUser, cleanPwd), cameraIP, port),
	}
}

// RTSPIngest handles reading the live H.264/G.711 stream from the camera's internal RTSP server (port 554)
// and feeding it into the local RTSP server and audio transcoder.
type RTSPIngest struct {
	candidateURLs   []string
	rtspServer      *rtsp.Server
	audioTranscoder *audio.Transcoder
	debug           bool
	closeChan       chan struct{}
	closed          atomic.Bool
	mu              sync.Mutex
	client          *gortsplib.Client
}

// NewRTSPIngest creates a new RTSP Ingest client with Steinel and Xiongmai streaming paths.
func NewRTSPIngest(cameraIP string, port int, user, password string, streamSubtype int, rtspServer *rtsp.Server, debug bool) *RTSPIngest {
	candidateURLs := getCandidateURLs(cameraIP, port, user, password, streamSubtype)

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
		debug:           debug,
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

		if ing.debug && len(ing.candidateURLs) > 0 {
			log.Printf("[Xiongmai Ingest] 🔌 Connecting to camera RTSP stream at %s", SanitizeRTSPURL(ing.candidateURLs[0]))
		}

		err := ing.runSession(ctx)
		if err != nil && !ing.closed.Load() {
			log.Printf("[Xiongmai Ingest] ⚠️ RTSP connection lost: %v (reconnecting in %v)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-ing.closeChan:
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
		} else {
			backoff = 1 * time.Second
		}
	}
}

// runSession tries the candidate RTSP URLs in sequence until one establishes streaming.
func (ing *RTSPIngest) runSession(_ context.Context) error {
	var lastErr error

	for _, rawURL := range ing.candidateURLs {
		u, err := base.ParseURL(rawURL)
		if err != nil {
			continue
		}

		transport := gortsplib.TransportTCP
		client := &gortsplib.Client{
			Transport: &transport, // Use TCP interleaved for reliable local transmission
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
				return fmt.Errorf("failed to dial RTSP server: %w", err)
			}

			desc, _, err := client.Describe(u)
			if err != nil {
				return fmt.Errorf("describe error: %w", err)
			}

			// Setup Video Media (H.264)
			var videoMedia *description.Media
			var videoFormat *format.H264
			for _, medi := range desc.Medias {
				var f *format.H264
				if medi.FindFormat(&f) {
					videoMedia = medi
					videoFormat = f
					break
				}
			}

			if videoMedia != nil {
				_, err := client.Setup(desc.BaseURL, videoMedia, 0, 0)
				if err != nil {
					log.Printf("[Xiongmai Ingest] ⚠️ Video track setup failed: %v", err)
				} else {
					client.OnPacketRTP(videoMedia, videoFormat, func(pkt *rtp.Packet) {
						if ing.rtspServer != nil {
							ing.rtspServer.WriteVideoPacket(pkt)
						}
					})
				}
			}

			// Setup Audio Media (G.711 PCMU / PCMA)
			var audioMedia *description.Media
			var audioFormat *format.G711
			for _, medi := range desc.Medias {
				var f *format.G711
				if medi.FindFormat(&f) {
					audioMedia = medi
					audioFormat = f
					break
				}
			}

			if audioMedia != nil {
				_, err := client.Setup(desc.BaseURL, audioMedia, 0, 0)
				if err != nil {
					log.Printf("[Xiongmai Ingest] ⚠️ Audio track setup failed: %v", err)
				} else {
					client.OnPacketRTP(audioMedia, audioFormat, func(pkt *rtp.Packet) {
						if ing.audioTranscoder != nil {
							_ = ing.audioTranscoder.ProcessPCMU(pkt.Payload)
						} else if ing.rtspServer != nil {
							ing.rtspServer.WriteAudioPacket(pkt)
						}
					})
				}
			}

			_, err = client.Play(nil)
			if err != nil {
				return fmt.Errorf("play error: %w", err)
			}

			log.Printf("[Xiongmai Ingest] ▶️ Streaming active from camera RTSP server via %s", SanitizeRTSPURL(rawURL))

			// Block until connection is closed or error occurs
			return client.Wait()
		}()

		if err == nil || ing.closed.Load() {
			return nil
		}

		lastErr = err
		if ing.debug {
			log.Printf("[Xiongmai Ingest] ℹ️ Candidate URL %s failed: %v", SanitizeRTSPURL(rawURL), err)
		}
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
