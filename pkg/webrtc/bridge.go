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

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mcu"
	"github.com/Afrouper/steinel-cam-bridge/pkg/nabto"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"

	"github.com/google/uuid"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

type Bridge struct {
	nabtoClient      *nabto.Client
	stream           *nabto.Stream
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
	debug            bool
	mu               sync.Mutex
}

func NewBridge(client *nabto.Client, stream *nabto.Stream, rtspServer *rtsp.Server, resolution string, pliInterval time.Duration, debug bool) *Bridge {
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
		debug:       debug,
	}

	// Initialize SD Card Manager using DataChannel JSON command dispatcher
	b.sdcardManager = NewSDCardManager(b.sendJSONCmd)

	// Register audio backchannel handler with RTSP server
	if rtspServer != nil {
		rtspServer.SetAudioBackchannelHandler(b.WriteAudioBackchannel)
	}

	return b
}

func (b *Bridge) GetSDCardManager() *SDCardManager {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sdcardManager
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
		_ = b.stream.WriteMsg(candBytes)
	})

	// 4. Create DataChannel "test"
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
		log.Printf("[DataChannel] 📡 DataChannel 'test' opened. Configuring initial video quality: %s", b.resolution)

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
			if b.debug {
				log.Printf("[WebRTC Signaling] 📥 Received SDP Answer from camera")
			}
			if pc.SignalingState() == pion.SignalingStateHaveLocalOffer {
				if err := pc.SetRemoteDescription(pion.SessionDescription{
					Type: pion.SDPTypeAnswer,
					SDP:  sdpWrap.SDP,
				}); err != nil {
					log.Printf("[WebRTC Signaling] ⚠️ SetRemoteDescription (Answer) error: %v", err)
				} else if b.debug {
					logTransceivers(pc)
				}
			}

		case TypeOffer:
			var sdpWrap SDPWrapper
			if err := json.Unmarshal([]byte(msg.Data), &sdpWrap); err != nil {
				continue
			}
			if b.debug {
				log.Printf("[WebRTC Signaling] 📥 Received renegotiation SDP Offer from camera")
			}

			if err := pc.SetRemoteDescription(pion.SessionDescription{
				Type: pion.SDPTypeOffer,
				SDP:  sdpWrap.SDP,
			}); err != nil {
				log.Printf("[WebRTC Signaling] ⚠️ SetRemoteDescription (Offer) error: %v", err)
				continue
			}

			// Attach local audioSendTrack to the camera's audio transceiver (frontdoor-audio)
			for _, tr := range pc.GetTransceivers() {
				if tr.Kind() == pion.RTPCodecTypeAudio {
					if _, err := pc.AddTrack(b.audioSendTrack); err != nil {
						if tr.Sender() != nil {
							_ = tr.Sender().ReplaceTrack(b.audioSendTrack)
						}
					}
					if b.debug {
						log.Printf("[WebRTC] 🎙️ Attached audioSendTrack to camera audio transceiver (mid=%s)", tr.Mid())
					}
				}
			}

			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				log.Printf("[WebRTC Signaling] ⚠️ CreateAnswer error: %v", err)
				continue
			}

			ansGatherComplete := pion.GatheringCompletePromise(pc)
			if err := pc.SetLocalDescription(answer); err != nil {
				log.Printf("[WebRTC Signaling] ⚠️ SetLocalDescription (Answer) error: %v", err)
				continue
			}
			<-ansGatherComplete
			if b.debug {
				logTransceivers(pc)
			}

			var tracks []MetadataTrack
			if msg.Metadata != nil && len(msg.Metadata.Tracks) > 0 {
				for _, t := range msg.Metadata.Tracks {
					tracks = append(tracks, MetadataTrack{
						Mid:     t.Mid,
						TrackID: t.TrackID,
						Error:   "OK",
					})
				}
			}

			cleanAnsSDP := fixAnswerSDP(pc.LocalDescription().SDP)
			ansJSON, _ := json.Marshal(SDPWrapper{Type: "answer", SDP: cleanAnsSDP})
			ansMsg := &SignalMessage{
				Type: TypeAnswer,
				Data: string(ansJSON),
				Metadata: &SignalMessageMetadata{
					NoTrickle: true,
					Status:    "OK",
					Tracks:    tracks,
				},
			}
			ansBytes, _ := MarshalSignalMessage(ansMsg)
			_ = b.stream.WriteMsg(ansBytes)

		case TypeICECandidate:
			var candWrap ICECandidateWrapper
			if err := json.Unmarshal([]byte(msg.Data), &candWrap); err == nil && candWrap.Candidate != "" {
				sdpMid := candWrap.SDPMid
				var sdpMLineIndex uint16
				_ = pc.AddICECandidate(pion.ICECandidateInit{
					Candidate:     candWrap.Candidate,
					SDPMid:        &sdpMid,
					SDPMLineIndex: &sdpMLineIndex,
				})
			}
		}
	}
}

func logTransceivers(pc *pion.PeerConnection) {
	for _, tr := range pc.GetTransceivers() {
		kind := tr.Kind().String()
		mid := tr.Mid()
		dir := tr.Direction().String()
		var trackInfo string
		if tr.Receiver() != nil && tr.Receiver().Track() != nil {
			trackInfo += fmt.Sprintf("rxTrack=%s (ID=%s) ", tr.Receiver().Track().Kind().String(), tr.Receiver().Track().ID())
		}
		if tr.Sender() != nil && tr.Sender().Track() != nil {
			trackInfo += fmt.Sprintf("txTrack=%s (ID=%s) ", tr.Sender().Track().Kind().String(), tr.Sender().Track().ID())
		}
		log.Printf("[WebRTC] 📡 Transceiver mid=%s kind=%s direction=%s %s", mid, kind, dir, trackInfo)
	}
}

// WriteAudioBackchannel forwards incoming PCMU audio samples to the camera speaker via WebRTC.
// It chunks incoming audio into standard 20ms (160 bytes at 8000Hz) RTP frames with consecutive timestamps and sequence numbers.
func (b *Bridge) WriteAudioBackchannel(pkt *rtp.Packet) error {
	b.mu.Lock()
	track := b.audioSendTrack
	b.mu.Unlock()

	if track == nil {
		return fmt.Errorf("audio send track not active")
	}

	if len(pkt.Payload) == 0 {
		return nil
	}

	b.backchannelMu.Lock()
	defer b.backchannelMu.Unlock()

	// Append incoming audio payload to buffer
	b.backchannelBuf = append(b.backchannelBuf, pkt.Payload...)

	// Target 20ms frame size for 8kHz 8-bit mono PCMU: 160 bytes
	const frameSize = 160
	const timestampStep = 160

	for len(b.backchannelBuf) >= frameSize {
		chunk := make([]byte, frameSize)
		copy(chunk, b.backchannelBuf[:frameSize])
		b.backchannelBuf = b.backchannelBuf[frameSize:]

		outPkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    0, // PCMU
				SequenceNumber: b.backchannelSeq,
				Timestamp:      b.backchannelTs,
			},
			Payload: chunk,
		}
		b.backchannelSeq++
		b.backchannelTs += timestampStep

		cnt := b.backchannelCount.Add(1)
		if cnt == 1 {
			log.Printf("[Audio Backchannel] 🎙️ Two-way audio active: forwarding to camera speaker")
		} else if b.debug && cnt%50 == 0 {
			log.Printf("[Audio Backchannel] 🎙️ Forwarded 20ms PCMU frame #%d to camera speaker (seq=%d, ts=%d)",
				cnt, outPkt.SequenceNumber, outPkt.Timestamp)
		}

		if err := track.WriteRTP(outPkt); err != nil {
			log.Printf("[Audio Backchannel] ⚠️ WriteRTP error: %v", err)
			return err
		}
	}

	return nil
}

// SetResolution sends a DataChannel command to change video quality dynamically
func (b *Bridge) SetResolution(resolution string) error {
	b.mu.Lock()
	b.resolution = resolution
	dc := b.dc
	b.mu.Unlock()

	if dc == nil {
		return fmt.Errorf("data channel not available")
	}

	cmd := map[string]interface{}{
		"from":   "Android",
		"cmd":    "set_video_setting",
		"msgid":  uuid.New().String(),
		"action": "all",
		"info": map[string]interface{}{
			"quality": map[string]interface{}{
				"sub1": map[string]interface{}{
					"resolution": resolution,
				},
			},
		},
	}
	data, _ := json.Marshal(cmd)
	log.Printf("[DataChannel] 🎦 Requesting camera resolution: %s", resolution)
	return dc.Send(data)
}

// SendCommand sends an arbitrary JSON command over the DataChannel
func (b *Bridge) SendCommand(cmdName string, info map[string]interface{}) error {
	b.mu.Lock()
	dc := b.dc
	b.mu.Unlock()

	if dc == nil {
		return fmt.Errorf("data channel not available")
	}

	cmd := map[string]interface{}{
		"from":  "Android",
		"cmd":   cmdName,
		"msgid": uuid.New().String(),
		"info":  info,
	}
	data, _ := json.Marshal(cmd)
	log.Printf("[DataChannel] 📤 Sending command '%s': %s", cmdName, string(data))
	return dc.Send(data)
}

// SendMCUCommand sends a raw Hex command to the MCU via tran_ctl
func (b *Bridge) SendMCUCommand(cmdHex string) error {
	b64, err := mcu.BuildCommand(cmdHex)
	if err != nil {
		return err
	}

	b.mu.Lock()
	dc := b.dc
	b.mu.Unlock()

	if dc == nil {
		return fmt.Errorf("data channel not available")
	}

	cmd := map[string]interface{}{
		"from":  "Android",
		"cmd":   "tran_ctl",
		"msgid": uuid.New().String(),
		"info": map[string]interface{}{
			"data": b64,
		},
	}
	data, _ := json.Marshal(cmd)
	return dc.SendText(string(data))
}

// SetLampState turns the lamp on, off or auto
func (b *Bridge) SetLampState(mode string) error {
	switch strings.ToLower(mode) {
	case "on", "1":
		return b.SendMCUCommand(mcu.CmdLightOn)
	case "off", "0":
		return b.SendMCUCommand(mcu.CmdLightOff)
	case "auto", "2":
		return b.SendMCUCommand(mcu.CmdLightAuto)
	default:
		return fmt.Errorf("unknown lamp mode: %s (use on, off, auto)", mode)
	}
}

func (b *Bridge) sendJSONCmd(cmdName string, info map[string]interface{}) error {
	b.mu.Lock()
	dc := b.dc
	b.mu.Unlock()

	if dc == nil {
		return fmt.Errorf("data channel not available")
	}

	cmd := map[string]interface{}{
		"from":  "Android",
		"cmd":   cmdName,
		"msgid": uuid.New().String(),
	}
	if info != nil {
		infoCopy := make(map[string]interface{})
		for k, v := range info {
			if k == "action" || k == "_action" {
				if actStr, ok := v.(string); ok {
					cmd["action"] = actStr
				}
			} else {
				infoCopy[k] = v
			}
		}
		cmd["info"] = infoCopy
	}
	data, _ := json.Marshal(cmd)
	log.Printf("[DataChannel] 📤 Sending JSON command '%s'", cmdName)
	return dc.Send(data)
}

func (b *Bridge) handleDataChannelMessage(data []byte) {
	if len(data) == 0 {
		return
	}

	// 1. Check if payload is a JSON control message
	if data[0] == '{' {
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err == nil {
			// Check for SD Card JSON responses (get_event_list, get_snapshot, get_event_video)
			b.mu.Lock()
			sdm := b.sdcardManager
			b.mu.Unlock()
			if sdm != nil && sdm.HandleJSONMessage(root) {
				return
			}

			// Check for MCU tran_report
			str := string(data)
			if strVal, ok := root["resp"].(string); ok && strVal == "tran_report" || strings.Contains(str, "tran_report") {
				if infoMap, ok := root["info"].(map[string]interface{}); ok {
					if b64Data, ok := infoMap["data"].(string); ok {
						cfg, err := mcu.ParseBase64Data(b64Data)
						if err != nil {
							log.Printf("[MCU] ⚠️ Failed to parse tran_report Base64 '%s': %v", b64Data, err)
						} else if cfg != nil {
							b.onMCUStatus(cfg)
						}
					}
				}
				return
			}

			// Check for get_device_info
			if strVal, ok := root["resp"].(string); ok && strVal == "get_device_info" {
				if infoMap, ok := root["info"].(map[string]interface{}); ok {
					fw, _ := infoMap["FW_version"].(string)
					status := events.GlobalBus.GetStatus()
					status.FirmwareVer = fw
					events.GlobalBus.UpdateStatus(status)
				}
				return
			}

			// Check and log for any Motion, PIR, Alarm or Event notifications from camera
			lowerStr := strings.ToLower(str)
			if strings.Contains(lowerStr, "alarm") ||
				strings.Contains(lowerStr, "motion") ||
				strings.Contains(lowerStr, "pir") ||
				strings.Contains(lowerStr, "event") ||
				strings.Contains(lowerStr, "doorbell") {
				log.Printf("[DataChannel] 🚨 Motion / Event notification received from camera: %s", str)
				events.GlobalBus.SetMotion(true)
			} else if b.debug {
				log.Printf("[DataChannel] 📩 Received JSON message: %s", str)
			}

			return
		}
	}

	// 2. Binary chunks (e.g. video / snapshot streaming)
	b.mu.Lock()
	sdm := b.sdcardManager
	b.mu.Unlock()
	if sdm != nil {
		sdm.HandleBinaryChunk(data)
	}
}

func (b *Bridge) onMCUStatus(cfg *mcu.ConfigInfo) {
	status := events.GlobalBus.GetStatus()
	status.LampMode = cfg.Mode
	status.Lux = cfg.Lux
	status.PIRActive = cfg.PIRActive
	status.PIRSensitivity = cfg.PIRSensitivity
	status.Highlight = cfg.Highlight
	status.HighlightTime = cfg.HighlightTime
	status.Lowlight = cfg.Lowlight
	status.LowlightTime = cfg.LowlightTime
	status.ColorTemp = cfg.ColorTemp
	status.Resolution = b.resolution
	events.GlobalBus.UpdateStatus(status)

	// Motion Detection Handling (Hardware PIR + Optical Camera Detection)
	if cfg.MotionDetected || cfg.PhotosensitiveDetection {
		motionType := "PIR Sensor"
		if cfg.MotionDetected && cfg.PhotosensitiveDetection {
			motionType = "PIR + Kamera-Bilderkennung"
		} else if cfg.PhotosensitiveDetection {
			motionType = "Kamera-Bilderkennung"
		}
		log.Printf("[MCU] 🚨 Bewegung erkannt (%s)! (Lux: %d, Mode: %d)", motionType, cfg.Lux, cfg.Mode)
		events.GlobalBus.SetMotion(true)

		b.mu.Lock()
		if b.motionResetTimer != nil {
			b.motionResetTimer.Stop()
		}
		b.motionResetTimer = time.AfterFunc(10*time.Second, func() {
			log.Printf("[MCU] ⚪ Motion cleared (10s timeout)")
			events.GlobalBus.SetMotion(false)
		})
		b.mu.Unlock()
	}
}

func (b *Bridge) runMCUPollingLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Immediate first query
	_ = b.SendMCUCommand(mcu.CmdGetLightInfo)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = b.SendMCUCommand(mcu.CmdGetLightInfo)
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
	codec := b.rtspServer.GetAudioCodec()
	if codec == "aac" {
		log.Printf("[Audio] 🔊 Transcoding audio: G.711u (8kHz) -> AAC-LC (16kHz) (Microphone -> Clients)")
		transcoder := audio.NewTranscoder(func(au []byte, pts time.Duration) {
			b.rtspServer.WriteAACFrame(au, pts)
		})
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

			_ = transcoder.ProcessPCMU(pkt.Payload)
		}
	} else {
		log.Printf("[Audio] 🔊 PCMU audio stream active (Microphone -> Clients)")
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

// RequestKeyframe sends an immediate RTCP Picture Loss Indication (PLI) to the camera
func (b *Bridge) RequestKeyframe() {
	ssrc := atomic.LoadUint32(&b.videoSSRC)
	b.mu.Lock()
	pc := b.pc
	b.mu.Unlock()

	if pc != nil {
		targetSSRC := ssrc
		if targetSSRC == 0 {
			targetSSRC = 1
		}
		_ = pc.WriteRTCP([]rtcp.Packet{
			&rtcp.PictureLossIndication{
				MediaSSRC: targetSSRC,
			},
		})
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
