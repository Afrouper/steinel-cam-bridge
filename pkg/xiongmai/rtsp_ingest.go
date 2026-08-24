package xiongmai

import (
	"context"
	"fmt"
	"log"
	"net/url"
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

// SanitizeRTSPURL safely masks password credentials in RTSP URLs using standard URL parsing.
func SanitizeRTSPURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "[invalid-url]"
	}
	return u.Redacted()
}

// RTSPIngest handles reading the live H.264/G.711 stream from the camera's internal RTSP server (port 554)
// and feeding it into the local RTSP server and audio transcoder.
type RTSPIngest struct {
	rtspURL         string
	rtspServer      *rtsp.Server
	audioTranscoder *audio.Transcoder
	debug           bool
	closeChan       chan struct{}
	closed          atomic.Bool
	mu              sync.Mutex
	client          *gortsplib.Client
}

// NewRTSPIngest creates a new RTSP Ingest client for the camera's internal stream.
func NewRTSPIngest(cameraIP string, port int, user, password string, streamSubtype int, rtspServer *rtsp.Server, debug bool) *RTSPIngest {
	if port <= 0 {
		port = RTSPPort
	}
	if user == "" {
		user = "admin"
	}

	authPart := ""
	if password != "" {
		authPart = fmt.Sprintf("%s:%s@", user, password)
	} else if user != "" && user != "admin" {
		authPart = fmt.Sprintf("%s@", user)
	}

	rtspURL := fmt.Sprintf("rtsp://%s%s:%d/stream=%d", authPart, cameraIP, port, streamSubtype)

	var transcoder *audio.Transcoder
	if rtspServer != nil && rtspServer.GetAudioCodec() == "aac" {
		transcoder = audio.NewTranscoder(func(au []byte, pts time.Duration) {
			rtspServer.WriteAACFrame(au, pts)
		})
	}

	return &RTSPIngest{
		rtspURL:         rtspURL,
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

		if ing.debug {
			log.Printf("[Xiongmai Ingest] 🔌 Connecting to camera RTSP stream at %s", SanitizeRTSPURL(ing.rtspURL))
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

// runSession performs a single RTSP client session.
func (ing *RTSPIngest) runSession(_ context.Context) error {
	u, err := base.ParseURL(ing.rtspURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %w", err)
	}

	transport := gortsplib.TransportTCP
	client := &gortsplib.Client{
		Transport: &transport, // Use TCP interleaved for reliable local transmission
	}

	ing.mu.Lock()
	ing.client = client
	ing.mu.Unlock()

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

	log.Printf("[Xiongmai Ingest] ▶️ Streaming active from camera RTSP server (Port %d)", RTSPPort)

	// Wait for error or close
	return client.Wait()
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
