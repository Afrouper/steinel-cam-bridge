package webrtc

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"

	"github.com/pion/rtcp"
	pion "github.com/pion/webrtc/v4"
)

// readVideoLoop reads H.264 video RTP packets from Pion and forwards them to the RTSP server.
func (b *Bridge) readVideoLoop(ctx context.Context, track *pion.TrackRemote, cancel context.CancelFunc) {
	logger.Info("Video", "🎬 1080p H.264 video stream active")
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pkt, _, err := track.ReadRTP()
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("Video", "⚠️ Video track read ended: %v. Terminating session...", err)
				cancel()
			}
			return
		}

		b.lastVideoPacket.Store(time.Now().UnixNano())
		if b.rtspServer != nil {
			b.rtspServer.WriteVideoPacket(pkt)
		}
	}
}

// readAudioLoop reads PCMU audio RTP packets from Pion, optionally transcodes to AAC-LC, and forwards to RTSP server.
func (b *Bridge) readAudioLoop(ctx context.Context, track *pion.TrackRemote) {
	var codec string
	if b.rtspServer != nil {
		codec = b.rtspServer.GetAudioCodec()
	}
	if codec == "aac" {
		logger.Info("Audio", "🔊 Transcoding audio: G.711u (8kHz) -> AAC-LC (16kHz) (Microphone -> Clients)")
		transcoder := audio.NewTranscoder(func(au []byte, pts time.Duration) {
			if b.rtspServer != nil {
				b.rtspServer.WriteAACFrame(au, pts)
			}
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
		logger.Info("Audio", "🔊 PCMU audio stream active (Microphone -> Clients)")
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

			if b.rtspServer != nil {
				b.rtspServer.WriteAudioPacket(pkt)
			}
		}
	}
}

// runWatchdogLoop monitors incoming video packet timestamps and cancels the session if silence is detected.
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
				logger.Warn("Watchdog", "⚠️ Silence detected: No video packets received within 8s of session start. Camera might be unresponsive. Triggering session reset...")
				cancel()
				return
			}

			lastTime := time.Unix(0, lastNano)
			silence := time.Since(lastTime)
			if silence > 6*time.Second {
				logger.Warn("Watchdog", "⚠️ Silence detected: No video packets received for %.1fs (threshold 6s). Camera might be rebooting. Triggering session reset...", silence.Seconds())
				cancel()
				return
			}
		}
	}
}

// RequestKeyframe sends an immediate RTCP Picture Loss Indication (PLI) to the camera.
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

// runPLILoop periodically sends Picture Loss Indications (PLIs) to ensure keyframe availability for incoming clients.
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
