package webrtc

import (
	"testing"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

func TestAudioBackchannelChunking(t *testing.T) {
	track, err := pion.NewTrackLocalStaticRTP(
		pion.RTPCodecCapability{MimeType: pion.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		"audioLabel",
		"audioStream",
	)
	if err != nil {
		t.Fatalf("failed to create track: %v", err)
	}

	b := &Bridge{
		audioSendTrack: track,
	}

	// 1. Send small chunk (< 160 bytes)
	smallPkt := &rtp.Packet{
		Header:  rtp.Header{PayloadType: 0},
		Payload: make([]byte, 100),
	}
	if err := b.WriteAudioBackchannel(smallPkt); err != nil {
		t.Fatalf("unexpected error on small chunk: %v", err)
	}
	if len(b.backchannelBuf) != 100 {
		t.Fatalf("expected buffer len 100, got %d", len(b.backchannelBuf))
	}

	// 2. Send 944 bytes (e.g. Scrypted jumbo packet) -> total 1044 bytes
	// 1044 / 160 = 6 full frames (960 bytes), 84 bytes remaining
	jumboPkt := &rtp.Packet{
		Header:  rtp.Header{PayloadType: 0},
		Payload: make([]byte, 944),
	}
	if err := b.WriteAudioBackchannel(jumboPkt); err != nil {
		t.Fatalf("unexpected error on jumbo chunk: %v", err)
	}

	if b.backchannelSeq != 6 {
		t.Fatalf("expected 6 frames generated, got seq %d", b.backchannelSeq)
	}
	if b.backchannelTs != 6*160 {
		t.Fatalf("expected timestamp %d, got %d", 6*160, b.backchannelTs)
	}
	if len(b.backchannelBuf) != 84 {
		t.Fatalf("expected buffer remaining 84, got %d", len(b.backchannelBuf))
	}
}
