package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/nabto"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"

	pion "github.com/pion/webrtc/v4"
)

// Bridge coordinates the WebRTC peer connection, media forwarding to RTSP, two-way audio, and DataChannel commands.
type Bridge struct {
	nabtoClient      nabto.Driver
	stream           nabto.StreamDriver
	rtspServer       *rtsp.Server
	resolution       string
	pliInterval      time.Duration
	pc               *pion.PeerConnection
	dc               *pion.DataChannel
	audioSendTrack   *pion.TrackLocalStaticRTP
	videoSSRC        uint32
	audioSSRC        uint32
	videoTrack       *pion.TrackRemote
	lastVideoPacket  atomic.Int64
	motionResetTimer *time.Timer
	sdcardManager    *SDCardManager
	backchannelBuf   []byte
	backchannelSeq   uint16
	backchannelTs    uint32
	backchannelCount atomic.Uint64
	backchannelMu    sync.Mutex
	mu               sync.Mutex
}

// NewBridge creates and initializes a new Bridge instance for the Steinel L 625 CAM SC.
func NewBridge(client nabto.Driver, stream nabto.StreamDriver, rtspServer *rtsp.Server, resolution string, pliInterval time.Duration) *Bridge {
	if resolution == "" {
		resolution = "1080p"
	}
	if pliInterval == 0 {
		pliInterval = 3 * time.Second
	}
	b := &Bridge{
		nabtoClient: client,
		stream:      stream,
		rtspServer:  rtspServer,
		resolution:  resolution,
		pliInterval: pliInterval,
	}

	// Initialize SD Card Manager using DataChannel JSON command dispatcher
	b.sdcardManager = NewSDCardManager(b.sendJSONCmd)

	// Register audio backchannel handler with RTSP server
	if rtspServer != nil {
		rtspServer.SetAudioBackchannelHandler(b.WriteAudioBackchannel)
	}

	return b
}

// GetSDCardManager returns the active SDCardManager instance.
func (b *Bridge) GetSDCardManager() *SDCardManager {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sdcardManager
}

// Run executes the complete WebRTC session lifecycle (TURN exchange, PeerConnection, ICE, media, and signaling loop).
func (b *Bridge) Run(ctx context.Context) error {
	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()

	// Hook session cancellation to abort stream and close peer connection immediately
	stopChan := make(chan struct{})
	go func() {
		select {
		case <-sessCtx.Done():
		case <-stopChan:
			return
		}
		if b.stream != nil {
			b.stream.Abort()
		}
		b.mu.Lock()
		if b.pc != nil {
			_ = b.pc.Close()
		}
		b.mu.Unlock()
	}()
	defer close(stopChan)

	// 1. Request TURN / ICE credentials via Nabto stream
	iceServers, err := b.exchangeTurnCredentials()
	if err != nil {
		return err
	}

	// 2. Configure Pion WebRTC
	mediaEngine := &pion.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return fmt.Errorf("failed to register default codecs: %w", err)
	}

	settingEngine := pion.SettingEngine{}
	settingEngine.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
	api := pion.NewAPI(pion.WithMediaEngine(mediaEngine), pion.WithSettingEngine(settingEngine))

	config := pion.Configuration{
		ICEServers: iceServers,
	}

	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("failed to create peer connection: %w", err)
	}

	// Create local Audio Track for Two-Way Audio (Backchannel -> Camera Speaker)
	audioSendTrack, err := pion.NewTrackLocalStaticRTP(
		pion.RTPCodecCapability{MimeType: pion.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		"audioLabel",
		"audioStream",
	)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("failed to create local audio send track: %w", err)
	}

	b.mu.Lock()
	b.pc = pc
	b.audioSendTrack = audioSendTrack
	b.mu.Unlock()
	defer func() {
		_ = pc.Close()
	}()

	// Default Video SSRC for Steinel CAM is 1
	atomic.StoreUint32(&b.videoSSRC, 1)

	// WebRTC connection state listener
	pc.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		logger.Debug("WebRTC", "Peer connection state changed to: %s", state)
		if state == pion.PeerConnectionStateFailed || state == pion.PeerConnectionStateDisconnected || state == pion.PeerConnectionStateClosed {
			logger.Warn("WebRTC", "⚠️ Connection dropped (%s). Terminating session...", state)
			sessCancel()
		}
	})

	pc.OnICEConnectionStateChange(func(state pion.ICEConnectionState) {
		if state == pion.ICEConnectionStateFailed || state == pion.ICEConnectionStateDisconnected || state == pion.ICEConnectionStateClosed {
			logger.Warn("WebRTC", "⚠️ ICE connection dropped (%s). Terminating session...", state)
			sessCancel()
		}
	})

	// Track handlers
	pc.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		if track.Kind() == pion.RTPCodecTypeVideo {
			atomic.StoreUint32(&b.videoSSRC, uint32(track.SSRC()))
			b.mu.Lock()
			b.videoTrack = track
			b.mu.Unlock()
			go b.readVideoLoop(sessCtx, track, sessCancel)
		} else if track.Kind() == pion.RTPCodecTypeAudio {
			atomic.StoreUint32(&b.audioSSRC, uint32(track.SSRC()))
			go b.readAudioLoop(sessCtx, track)
		}
	})

	pc.OnICECandidate(func(cand *pion.ICECandidate) {
		if cand == nil {
			return
		}
		cJSON := cand.ToJSON()
		if cJSON.SDPMid == nil {
			return
		}
		candWrap := ICECandidateWrapper{
			Candidate: cJSON.Candidate,
			SDPMid:    *cJSON.SDPMid,
		}
		candData, _ := json.Marshal(candWrap)
		candMsg := &SignalMessage{
			Type: TypeICECandidate,
			Data: string(candData),
		}
		candBytes, _ := MarshalSignalMessage(candMsg)
		logger.Trace("WebRTC", "Sending ICE Candidate: mid=%s", candWrap.SDPMid)
		_ = b.stream.WriteMsg(candBytes)
	})

	// 3. Create DataChannel "test"
	dc, err := pc.CreateDataChannel("test", nil)
	if err != nil {
		return fmt.Errorf("failed to create data channel: %w", err)
	}
	b.mu.Lock()
	b.dc = dc
	b.mu.Unlock()

	dc.OnMessage(func(msg pion.DataChannelMessage) {
		b.handleDataChannelMessage(msg.Data)
	})

	dc.OnOpen(func() {
		logger.Info("DataChannel", "📡 DataChannel 'test' opened. Configuring initial video quality: %s", b.resolution)

		// Request resolution
		_ = b.SetResolution(b.resolution)

		// Enable notification setting on camera
		_ = b.sendJSONCmd("set_notification_setting", map[string]interface{}{"enable": true})

		// Request media tracks via CoAP
		go func() {
			time.Sleep(200 * time.Millisecond)
			_, _ = b.nabtoClient.RequestTracks()
		}()

		// Start periodic MCU polling (every 2s)
		go b.runMCUPollingLoop(sessCtx)
	})

	// 4. Send initial OFFER
	if err := b.sendInitialOffer(pc); err != nil {
		return err
	}

	// Start RTCP PLI keyframe loop
	go b.runPLILoop(sessCtx)

	// Start RTP Silence Watchdog
	go b.runWatchdogLoop(sessCtx, sessCancel)

	// 5. Main Signaling Receive Loop
	return b.runSignalingReceiveLoop(sessCtx, pc)
}
