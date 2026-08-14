package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"steinel-cam-bridge/pkg/nabto"
	"steinel-cam-bridge/pkg/rtsp"

	"github.com/pion/rtcp"
	pion "github.com/pion/webrtc/v4"
)

type Bridge struct {
	nabtoClient     *nabto.Client
	stream          *nabto.Stream
	rtspServer      *rtsp.Server
	resolution      string
	pliInterval     time.Duration
	pc              *pion.PeerConnection
	dc              *pion.DataChannel
	videoSSRC       uint32
	audioSSRC       uint32
	videoTrack      *pion.TrackRemote
	lastVideoPacket atomic.Int64
	mu              sync.Mutex
}

func NewBridge(client *nabto.Client, stream *nabto.Stream, rtspServer *rtsp.Server, resolution string, pliInterval time.Duration) *Bridge {
	if resolution == "" {
		resolution = "1080p"
	}
	if pliInterval == 0 {
		pliInterval = 3 * time.Second
	}
	return &Bridge{
		nabtoClient: client,
		stream:      stream,
		rtspServer:  rtspServer,
		resolution:  resolution,
		pliInterval: pliInterval,
	}
}

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

	// 1. Send TURN_REQUEST
	turnReq := &SignalMessage{Type: TypeTurnRequest}
	turnReqBytes, _ := MarshalSignalMessage(turnReq)
	if err := b.stream.WriteMsg(turnReqBytes); err != nil {
		return fmt.Errorf("failed to send turn request: %w", err)
	}

	// 2. Receive TURN_RESPONSE
	respBytes, err := b.stream.ReadMsg()
	if err != nil {
		return fmt.Errorf("failed to read turn response: %w", err)
	}

	turnResp, err := UnmarshalSignalMessage(respBytes)
	if err != nil || turnResp.Type != TypeTurnResponse {
		return fmt.Errorf("invalid turn response: %v", err)
	}

	// 3. Configure Pion WebRTC
	var turnPayload TurnResponsePayload
	_ = json.Unmarshal([]byte(turnResp.Data), &turnPayload)

	var iceServers []pion.ICEServer
	for _, s := range turnPayload.ICEServers {
		iceServers = append(iceServers, pion.ICEServer{
			URLs:           s.URLs,
			Username:       s.Username,
			Credential:     s.Credential,
			CredentialType: pion.ICECredentialTypePassword,
		})
	}
	for _, s := range turnPayload.Servers {
		url := fmt.Sprintf("turn:%s:%d", s.Hostname, s.Port)
		iceServers = append(iceServers, pion.ICEServer{
			URLs:           []string{url},
			Username:       s.Username,
			Credential:     s.Password,
			CredentialType: pion.ICECredentialTypePassword,
		})
	}

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
	b.mu.Lock()
	b.pc = pc
	b.mu.Unlock()
	defer pc.Close()

	// Default Video SSRC for Steinel CAM is 1
	atomic.StoreUint32(&b.videoSSRC, 1)

	// WebRTC connection state listener
	pc.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		log.Printf("[WebRTC] Peer connection state changed to: %s", state)
		if state == pion.PeerConnectionStateFailed || state == pion.PeerConnectionStateDisconnected || state == pion.PeerConnectionStateClosed {
			log.Printf("[WebRTC] ⚠️ Connection dropped (%s). Terminating session...", state)
			sessCancel()
		}
	})

	pc.OnICEConnectionStateChange(func(state pion.ICEConnectionState) {
		if state == pion.ICEConnectionStateFailed || state == pion.ICEConnectionStateDisconnected || state == pion.ICEConnectionStateClosed {
			log.Printf("[WebRTC] ⚠️ ICE connection dropped (%s). Terminating session...", state)
			sessCancel()
		}
	})

	// Track handlers
	pc.OnTrack(func(track *pion.TrackRemote, receiver *pion.RTPReceiver) {
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
		_ = b.stream.WriteMsg(candBytes)
	})

	// 4. Create DataChannel "test"
	dc, err := pc.CreateDataChannel("test", nil)
	if err != nil {
		return fmt.Errorf("failed to create data channel: %w", err)
	}
	b.dc = dc

	dc.OnOpen(func() {
		// Request 1080p resolution
		videoSetting := map[string]interface{}{
			"action":     "all",
			"message_id": "1",
			"resp":       "set_video_setting",
			"set_video_setting": map[string]interface{}{
				"quality": b.resolution,
			},
		}
		settingJSON, _ := json.Marshal(videoSetting)
		dc.SendText(string(settingJSON))

		// Request media tracks via CoAP
		go func() {
			time.Sleep(200 * time.Millisecond)
			_, _ = b.nabtoClient.RequestTracks()
		}()
	})

	// 5. Create initial OFFER and wait for ICE gathering (Vanilla ICE)
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("failed to create offer: %w", err)
	}

	gatherComplete := pion.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("failed to set local description: %w", err)
	}
	<-gatherComplete

	cleanSDP := sanitizeSDP(pc.LocalDescription().SDP)
	offerJSON, _ := json.Marshal(SDPWrapper{Type: "offer", SDP: cleanSDP})

	offerMsg := &SignalMessage{
		Type: TypeOffer,
		Data: string(offerJSON),
		Metadata: &SignalMessageMetadata{
			NoTrickle: true,
		},
	}
	offerMsgBytes, _ := MarshalSignalMessage(offerMsg)
	if err := b.stream.WriteMsg(offerMsgBytes); err != nil {
		return fmt.Errorf("failed to send offer: %w", err)
	}

	// Start RTCP PLI keyframe loop
	go b.runPLILoop(sessCtx)

	// Start RTP Silence Watchdog
	go b.runWatchdogLoop(sessCtx, sessCancel)

	// 6. Main Signaling Receive Loop
	for {
		if sessCtx.Err() != nil {
			return nil
		}

		raw, err := b.stream.ReadMsg()
		if err != nil {
			if sessCtx.Err() != nil {
				return nil
			}
			return err
		}

		msg, err := UnmarshalSignalMessage(raw)
		if err != nil {
			continue
		}

		switch msg.Type {
		case TypeAnswer:
			var sdpWrap SDPWrapper
			if err := json.Unmarshal([]byte(msg.Data), &sdpWrap); err != nil {
				continue
			}
			if pc.SignalingState() == pion.SignalingStateHaveLocalOffer {
				_ = pc.SetRemoteDescription(pion.SessionDescription{
					Type: pion.SDPTypeAnswer,
					SDP:  sdpWrap.SDP,
				})
			}

		case TypeOffer:
			var sdpWrap SDPWrapper
			if err := json.Unmarshal([]byte(msg.Data), &sdpWrap); err != nil {
				continue
			}

			if err := pc.SetRemoteDescription(pion.SessionDescription{
				Type: pion.SDPTypeOffer,
				SDP:  sdpWrap.SDP,
			}); err != nil {
				continue
			}

			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				continue
			}

			ansGatherComplete := pion.GatheringCompletePromise(pc)
			if err := pc.SetLocalDescription(answer); err != nil {
				continue
			}
			<-ansGatherComplete

			cleanAnsSDP := fixAnswerSDP(pc.LocalDescription().SDP)
			ansJSON, _ := json.Marshal(SDPWrapper{Type: "answer", SDP: cleanAnsSDP})
			ansMsg := &SignalMessage{
				Type: TypeAnswer,
				Data: string(ansJSON),
				Metadata: &SignalMessageMetadata{
					NoTrickle: true,
				},
			}
			ansBytes, _ := MarshalSignalMessage(ansMsg)
			_ = b.stream.WriteMsg(ansBytes)

		case TypeICECandidate:
			var candWrap ICECandidateWrapper
			if err := json.Unmarshal([]byte(msg.Data), &candWrap); err == nil && candWrap.Candidate != "" {
				sdpMid := candWrap.SDPMid
				var sdpMLineIndex uint16 = 0
				pc.AddICECandidate(pion.ICECandidateInit{
					Candidate:     candWrap.Candidate,
					SDPMid:        &sdpMid,
					SDPMLineIndex: &sdpMLineIndex,
				})
			}
		}
	}
}

func (b *Bridge) readVideoLoop(ctx context.Context, track *pion.TrackRemote, cancel context.CancelFunc) {
	log.Printf("[Video] 🎬 1080p H.264 video stream active")
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pkt, _, err := track.ReadRTP()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[Video] ⚠️ Video track read ended: %v. Terminating session...", err)
				cancel()
			}
			return
		}

		b.lastVideoPacket.Store(time.Now().UnixNano())
		b.rtspServer.WriteVideoPacket(pkt)
	}
}

func (b *Bridge) readAudioLoop(ctx context.Context, track *pion.TrackRemote) {
	log.Printf("[Audio] 🔊 PCMU audio stream active")
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}

		b.rtspServer.WriteAudioPacket(pkt)
	}
}

func (b *Bridge) runWatchdogLoop(ctx context.Context, cancel context.CancelFunc) {
	// Initial grace period to allow ICE negotiation, track setup, and initial frame delivery
	select {
	case <-ctx.Done():
		return
	case <-time.After(8 * time.Second):
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lastNano := b.lastVideoPacket.Load()
			if lastNano == 0 {
				// No video packet received after grace period
				log.Printf("[Watchdog] ⚠️ Silence detected: No video packets received within 8s of session start. Camera might be unresponsive. Triggering session reset...")
				cancel()
				return
			}

			lastTime := time.Unix(0, lastNano)
			silence := time.Since(lastTime)
			if silence > 6*time.Second {
				log.Printf("[Watchdog] ⚠️ Silence detected: No video packets received for %.1fs (threshold 6s). Camera might be rebooting. Triggering session reset...", silence.Seconds())
				cancel()
				return
			}
		}
	}
}

func (b *Bridge) runPLILoop(ctx context.Context) {
	// Fast initial burst of PLIs to request immediate keyframe
	time.Sleep(500 * time.Millisecond)
	for i := 0; i < 4; i++ {
		select {
		case <-ctx.Done():
			return
		default:
			b.mu.Lock()
			pc := b.pc
			b.mu.Unlock()
			if pc != nil {
				_ = pc.WriteRTCP([]rtcp.Packet{
					&rtcp.PictureLossIndication{
						MediaSSRC: 1,
					},
				})
			}
			time.Sleep(250 * time.Millisecond)
		}
	}

	ticker := time.NewTicker(b.pliInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ssrc := atomic.LoadUint32(&b.videoSSRC)
			if ssrc > 0 {
				b.mu.Lock()
				pc := b.pc
				b.mu.Unlock()
				if pc != nil {
					_ = pc.WriteRTCP([]rtcp.Packet{
						&rtcp.PictureLossIndication{
							MediaSSRC: ssrc,
						},
					})
				}
			}
		}
	}
}

func sanitizeSDP(sdp string) string {
	lines := strings.Split(sdp, "\r\n")
	var cleanLines []string

	for _, line := range lines {
		// Strip candidate lines pointing to local loopback or mdns
		if strings.HasPrefix(line, "a=candidate:") && strings.Contains(line, "127.0.0.1") {
			continue
		}
		cleanLines = append(cleanLines, line)
	}

	return strings.Join(cleanLines, "\r\n")
}

func fixAnswerSDP(sdp string) string {
	sdp = sanitizeSDP(sdp)
	sdp = strings.ReplaceAll(sdp, "a=setup:active", "a=setup:passive")
	return sdp
}
