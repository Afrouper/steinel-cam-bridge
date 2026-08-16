package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/gen2brain/aac-go"
)

const (
	// AACSampleRate is the output sample rate for AAC (16000 Hz)
	AACSampleRate = 16000
	// AACBitRate is the output bit rate (64 kbps)
	AACBitRate = 64000
	// AACChannels is mono
	AACChannels = 1
	// AACFrameSamples is standard AAC-LC frame size (1024 samples)
	AACFrameSamples = 1024
	// AACFrameBytes is 1024 samples * 2 bytes per sample (16-bit)
	AACFrameBytes = AACFrameSamples * 2
)

// Transcoder converts raw G.711u (PCMU 8000 Hz) bytes into AAC-LC Access Units (AUs) in real time.
type Transcoder struct {
	mu          sync.Mutex
	pcmBuf      []byte
	sampleCount uint64
	sampleRate  int
	onAACFrame  func(au []byte, pts time.Duration)
}

// NewTranscoder creates a new audio transcoder.
// onAACFrame is called whenever a complete AAC frame is generated.
func NewTranscoder(onAACFrame func(au []byte, pts time.Duration)) *Transcoder {
	return &Transcoder{
		sampleRate: AACSampleRate,
		pcmBuf:     make([]byte, 0, AACFrameBytes*4),
		onAACFrame: onAACFrame,
	}
}

// ProcessPCMU processes a chunk of G.711u (8000 Hz) audio bytes from an RTP packet.
func (t *Transcoder) ProcessPCMU(pcmuData []byte) error {
	if len(pcmuData) == 0 {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// 1. Decode G.711u -> 16-bit PCM (8000 Hz)
	pcm8k := DecodePCMU(pcmuData)

	// 2. Resample 8000 Hz -> 16000 Hz (2x upsample)
	pcm16k := Resample8kTo16k(pcm8k)

	// 3. Append to PCM buffer (Little-Endian 16-bit)
	byteBuf := make([]byte, len(pcm16k)*2)
	for i, s := range pcm16k {
		binary.LittleEndian.PutUint16(byteBuf[i*2:], uint16(s))
	}
	t.pcmBuf = append(t.pcmBuf, byteBuf...)

	// 4. While we have at least one full AAC frame (1024 samples = 2048 bytes)
	for len(t.pcmBuf) >= AACFrameBytes {
		chunk := t.pcmBuf[:AACFrameBytes]
		t.pcmBuf = t.pcmBuf[AACFrameBytes:]

		// Calculate Presentation Timestamp (PTS)
		pts := time.Duration(t.sampleCount) * time.Second / time.Duration(t.sampleRate)
		t.sampleCount += AACFrameSamples

		// Encode 1024 PCM samples to AAC ADTS
		var adtsBuf bytes.Buffer
		enc, err := aac.NewEncoder(&adtsBuf, &aac.Options{
			SampleRate:  t.sampleRate,
			BitRate:     AACBitRate,
			NumChannels: AACChannels,
		})
		if err != nil {
			return fmt.Errorf("failed to create aac encoder: %w", err)
		}

		if err := enc.Encode(bytes.NewReader(chunk)); err != nil && err != io.EOF {
			enc.Close()
			return fmt.Errorf("failed to encode aac: %w", err)
		}
		enc.Close()

		adtsData := adtsBuf.Bytes()
		if len(adtsData) == 0 {
			continue
		}

		// Extract raw AAC Access Units from ADTS bitstream
		var pkts mpeg4audio.ADTSPackets
		if err := pkts.Unmarshal(adtsData); err != nil {
			// Fallback: If stripping ADTS header directly (ADTS header is 7 bytes)
			if len(adtsData) > 7 && t.onAACFrame != nil {
				t.onAACFrame(adtsData[7:], pts)
			}
			continue
		}

		for _, pkt := range pkts {
			if t.onAACFrame != nil && len(pkt.AU) > 0 {
				t.onAACFrame(pkt.AU, pts)
			}
		}
	}

	return nil
}
