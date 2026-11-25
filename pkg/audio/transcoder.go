package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio/internal/aac"
	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
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

// Transcoder converts raw G.711 (PCMU/PCMA 8000 Hz) bytes into AAC-LC Access Units (AUs) in real time.
type Transcoder struct {
	mu          sync.Mutex
	encoder     *aac.Encoder
	adtsBuf     *bytes.Buffer
	pcmReader   bytes.Reader
	pcmBuf      []byte
	prevSample  int16
	hasPrev     bool
	sampleCount uint64
	sampleRate  int
	onAACFrame  func(au []byte, pts time.Duration)
}

// NewTranscoder creates a new persistent audio transcoder.
// onAACFrame is called whenever a complete AAC frame is generated.
func NewTranscoder(onAACFrame func(au []byte, pts time.Duration)) *Transcoder {
	adtsBuf := &bytes.Buffer{}
	enc, _ := aac.NewEncoder(adtsBuf, &aac.Options{
		SampleRate:  AACSampleRate,
		BitRate:     AACBitRate,
		NumChannels: AACChannels,
	})

	return &Transcoder{
		sampleRate: AACSampleRate,
		encoder:    enc,
		adtsBuf:    adtsBuf,
		pcmBuf:     make([]byte, 0, AACFrameBytes*4),
		onAACFrame: onAACFrame,
	}
}

// Close releases any encoder resources
func (t *Transcoder) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.encoder != nil {
		_ = t.encoder.Close()
		t.encoder = nil
	}
}

// ProcessPCMU processes a chunk of G.711u (8000 Hz) audio bytes from an RTP packet.
func (t *Transcoder) ProcessPCMU(pcmuData []byte) error {
	if len(pcmuData) == 0 {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.encoder == nil {
		return fmt.Errorf("transcoder is closed")
	}

	t.appendResampledG711Locked(pcmuData, &ulawToPCM)
	return t.encodeReadyFramesLocked()
}

// ProcessPCMA processes a chunk of G.711a (8000 Hz) audio bytes from an RTP packet.
func (t *Transcoder) ProcessPCMA(pcmaData []byte) error {
	if len(pcmaData) == 0 {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.encoder == nil {
		return fmt.Errorf("transcoder is closed")
	}

	t.appendResampledG711Locked(pcmaData, &alawToPCM)
	return t.encodeReadyFramesLocked()
}

// appendResampledG711Locked decodes 8 kHz G.711 bytes via table and writes 2x linearly
// interpolated 16 kHz Little-Endian PCM samples directly into t.pcmBuf without heap allocations.
// Remembering t.prevSample eliminates 50 Hz step discontinuities across 20 ms RTP packet boundaries.
func (t *Transcoder) appendResampledG711Locked(g711Data []byte, table *[256]int16) {
	needBytes := len(g711Data) * 4 // 2 output samples * 2 bytes per input byte
	oldLen := len(t.pcmBuf)
	if cap(t.pcmBuf)-oldLen < needBytes {
		grown := make([]byte, oldLen+needBytes, max((oldLen+needBytes)*2, AACFrameBytes*4))
		copy(grown, t.pcmBuf)
		t.pcmBuf = grown
	} else {
		t.pcmBuf = t.pcmBuf[:oldLen+needBytes]
	}

	out := t.pcmBuf[oldLen : oldLen+needBytes]
	prev := t.prevSample
	if !t.hasPrev {
		prev = table[g711Data[0]]
		t.hasPrev = true
	}

	for i, b := range g711Data {
		cur := table[b]
		mid := int16((int32(prev) + int32(cur)) / 2)
		off := i * 4
		binary.LittleEndian.PutUint16(out[off:], uint16(mid))
		binary.LittleEndian.PutUint16(out[off+2:], uint16(cur))
		prev = cur
	}
	t.prevSample = prev
}

// encodeReadyFramesLocked encodes all complete 1024-sample AAC frames in t.pcmBuf. Must be called with t.mu held.
func (t *Transcoder) encodeReadyFramesLocked() error {
	consumed := 0
	for len(t.pcmBuf)-consumed >= AACFrameBytes {
		chunk := t.pcmBuf[consumed : consumed+AACFrameBytes]
		consumed += AACFrameBytes

		pts := time.Duration(t.sampleCount) * time.Second / time.Duration(t.sampleRate)
		t.sampleCount += AACFrameSamples

		t.adtsBuf.Reset()
		t.pcmReader.Reset(chunk)
		if err := t.encoder.Encode(&t.pcmReader); err != nil && err != io.EOF {
			return fmt.Errorf("failed to encode aac: %w", err)
		}

		adtsData := t.adtsBuf.Bytes()
		if len(adtsData) == 0 {
			continue
		}

		var pkts mpeg4audio.ADTSPackets
		if err := pkts.Unmarshal(adtsData); err != nil {
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

	if consumed > 0 {
		remaining := copy(t.pcmBuf, t.pcmBuf[consumed:])
		t.pcmBuf = t.pcmBuf[:remaining]
	}
	return nil
}
