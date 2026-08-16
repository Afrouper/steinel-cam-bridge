package audio

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestG711Decode(t *testing.T) {
	// Silence in mu-law is 0xFF
	silence := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	pcm := DecodePCMU(silence)
	require.Equal(t, 4, len(pcm))
	// In mu-law, 0xFF decodes to 0 (or near 0)
	assert.Equal(t, int16(0), pcm[0])

	// Non-zero test
	data := []byte{0x00, 0x80, 0x7F, 0xFF}
	pcm = DecodePCMU(data)
	assert.Equal(t, 4, len(pcm))
}

func TestResampling(t *testing.T) {
	src := []int16{0, 100, 200, 300}
	resampled16k := Resample8kTo16k(src)
	assert.Equal(t, 8, len(resampled16k))
	assert.Equal(t, int16(0), resampled16k[0])
	assert.Equal(t, int16(50), resampled16k[1])
	assert.Equal(t, int16(100), resampled16k[2])

	resampled32k := Resample8kTo32k(src)
	assert.Equal(t, 16, len(resampled32k))
	assert.Equal(t, int16(0), resampled32k[0])
}

func TestTranscoder(t *testing.T) {
	var aacFrames [][]byte
	var aacPTS []time.Duration

	transcoder := NewTranscoder(func(au []byte, pts time.Duration) {
		aacFrames = append(aacFrames, au)
		aacPTS = append(aacPTS, pts)
	})

	// Feed 2000 bytes of PCMU audio (8000 Hz)
	// 2000 bytes * 2 (16k) = 4000 PCM samples >= 3 full AAC frames of 1024 samples
	pcmuChunk := make([]byte, 160) // 20ms chunks (standard RTP packet size)
	for i := range pcmuChunk {
		pcmuChunk[i] = 0xFF // Silence
	}

	for i := 0; i < 20; i++ {
		err := transcoder.ProcessPCMU(pcmuChunk)
		require.NoError(t, err)
	}

	// We should have received at least 1-2 AAC frames
	assert.NotEmpty(t, aacFrames)
	for _, frame := range aacFrames {
		assert.NotEmpty(t, frame)
	}
}
