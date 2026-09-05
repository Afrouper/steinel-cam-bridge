package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"

	pion "github.com/pion/webrtc/v4"
)

// exchangeTurnCredentials requests TURN/ICE servers from the camera via the Nabto signaling stream.
func (b *Bridge) exchangeTurnCredentials() ([]pion.ICEServer, error) {
	// 1. Send TURN_REQUEST
	turnReq := &SignalMessage{Type: TypeTurnRequest}
	turnReqBytes, _ := MarshalSignalMessage(turnReq)
	if err := b.stream.WriteMsg(turnReqBytes); err != nil {
		return nil, fmt.Errorf("failed to send turn request: %w", err)
	}

	// 2. Receive TURN_RESPONSE
	respBytes, err := b.stream.ReadMsg()
	if err != nil {
		return nil, fmt.Errorf("failed to read turn response: %w", err)
	}

	turnResp, err := UnmarshalSignalMessage(respBytes)
	if err != nil || turnResp.Type != TypeTurnResponse {
		return nil, fmt.Errorf("invalid turn response: %v", err)
	}

	// 3. Parse ICE/TURN server configurations
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

	return iceServers, nil
}

// sendInitialOffer creates an SDP offer, waits for ICE candidate gathering (Vanilla ICE), and sends it to the camera.
func (b *Bridge) sendInitialOffer(pc *pion.PeerConnection) error {
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

	return nil
}

// runSignalingReceiveLoop handles incoming signaling messages (Answer, Renegotiation Offer, ICECandidate) from camera.
func (b *Bridge) runSignalingReceiveLoop(ctx context.Context, pc *pion.PeerConnection) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		raw, err := b.stream.ReadMsg()
		if err != nil {
			if ctx.Err() != nil {
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
			logger.Debug("WebRTC Signaling", "📥 Received SDP Answer from camera")
			logger.Trace("WebRTC Signaling", "SDP Answer payload:\n%s", sdpWrap.SDP)
			if pc.SignalingState() == pion.SignalingStateHaveLocalOffer {
				if err := pc.SetRemoteDescription(pion.SessionDescription{
					Type: pion.SDPTypeAnswer,
					SDP:  sdpWrap.SDP,
				}); err != nil {
					logger.Warn("WebRTC Signaling", "⚠️ SetRemoteDescription (Answer) error: %v", err)
				} else {
					logTransceivers(pc)
				}
			}

		case TypeOffer:
			var sdpWrap SDPWrapper
			if err := json.Unmarshal([]byte(msg.Data), &sdpWrap); err != nil {
				continue
			}
			logger.Debug("WebRTC Signaling", "📥 Received renegotiation SDP Offer from camera")
			logger.Trace("WebRTC Signaling", "SDP Offer payload:\n%s", sdpWrap.SDP)

			if err := pc.SetRemoteDescription(pion.SessionDescription{
				Type: pion.SDPTypeOffer,
				SDP:  sdpWrap.SDP,
			}); err != nil {
				logger.Warn("WebRTC Signaling", "⚠️ SetRemoteDescription (Offer) error: %v", err)
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
					logger.Debug("WebRTC", "🎙️ Attached audioSendTrack to camera audio transceiver (mid=%s)", tr.Mid())
				}
			}

			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				logger.Warn("WebRTC Signaling", "⚠️ CreateAnswer error: %v", err)
				continue
			}

			ansGatherComplete := pion.GatheringCompletePromise(pc)
			if err := pc.SetLocalDescription(answer); err != nil {
				logger.Warn("WebRTC Signaling", "⚠️ SetLocalDescription (Answer) error: %v", err)
				continue
			}
			<-ansGatherComplete
			logTransceivers(pc)

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

func logTransceivers(pc *pion.PeerConnection) {
	if !logger.IsDebug() {
		return
	}
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
		logger.Debug("WebRTC", "📡 Transceiver mid=%s kind=%s direction=%s %s", mid, kind, dir, trackInfo)
	}
}
