package webrtc

import (
	"fmt"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"

	"github.com/pion/rtp"
)

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

	offset := 0
	for len(b.backchannelBuf)-offset >= frameSize {
		chunk := make([]byte, frameSize)
		copy(chunk, b.backchannelBuf[offset:offset+frameSize])
		offset += frameSize

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
			logger.Info("Audio Backchannel", "🎙️ Two-way audio active: forwarding to camera speaker")
		} else if cnt%50 == 0 {
			logger.Debug("Audio Backchannel", "🎙️ Forwarded 20ms PCMU frame #%d to camera speaker (seq=%d, ts=%d)",
				cnt, outPkt.SequenceNumber, outPkt.Timestamp)
		} else {
			logger.Trace("Audio Backchannel", "Forwarded 20ms frame #%d (seq=%d, ts=%d)", cnt, outPkt.SequenceNumber, outPkt.Timestamp)
		}

		if err := track.WriteRTP(outPkt); err != nil {
			logger.Warn("Audio Backchannel", "⚠️ WriteRTP error: %v", err)
			rem := copy(b.backchannelBuf, b.backchannelBuf[offset:])
			b.backchannelBuf = b.backchannelBuf[:rem]
			return err
		}
	}

	if offset > 0 {
		rem := copy(b.backchannelBuf, b.backchannelBuf[offset:])
		b.backchannelBuf = b.backchannelBuf[:rem]
	}

	return nil
}
