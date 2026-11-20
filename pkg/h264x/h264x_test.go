package h264x

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Parameter sets produced by x264 (Baseline) for the three camera resolutions.
const (
	sps1080 = "6742c028d900780227e5c044000003000400000300783c60c920"
	sps720  = "6742c01fd9005005bb0110000003001000000301e0f1832480"
	sps360  = "6742c016d900a02ff970110000030001000003001e0f162e48"
	pps     = "68cb8cb2"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func TestParseSizeAndLabel(t *testing.T) {
	cases := []struct {
		sps   string
		w, h  int
		label string
	}{
		{sps1080, 1920, 1080, "1080p"},
		{sps720, 1280, 720, "720p"},
		{sps360, 640, 360, "360p"},
	}
	for _, c := range cases {
		w, h, err := ParseSize(mustHex(t, c.sps))
		require.NoError(t, err)
		assert.Equal(t, c.w, w)
		assert.Equal(t, c.h, h)
		assert.Equal(t, c.label, ResolutionLabel(w, h))
	}

	assert.Equal(t, "", ResolutionLabel(0, 0))
	assert.Equal(t, "2560x1440", ResolutionLabel(2560, 1440))

	_, _, err := ParseSize([]byte{0x67})
	assert.Error(t, err)
}

func TestInspectSingleNAL(t *testing.T) {
	sps := mustHex(t, sps720)

	info := Inspect(sps)
	assert.True(t, info.ParamSet)
	assert.True(t, info.KeyStart())
	assert.Equal(t, sps, info.SPS)
	assert.False(t, info.IDR)

	info = Inspect(mustHex(t, pps))
	assert.True(t, info.ParamSet)
	assert.NotNil(t, info.PPS)

	info = Inspect([]byte{0x65, 0x88, 0x84})
	assert.True(t, info.IDR)
	assert.True(t, info.IDRStart)
	assert.True(t, info.KeyStart())

	info = Inspect([]byte{0x41, 0x9a, 0x00})
	assert.False(t, info.KeyStart())
	assert.False(t, info.IDR)

	assert.False(t, Inspect(nil).KeyStart())
}

func TestInspectSTAPA(t *testing.T) {
	sps := mustHex(t, sps1080)
	p := mustHex(t, pps)
	idr := []byte{0x65, 0x88, 0x84, 0x00}

	payload := []byte{24}
	for _, nal := range [][]byte{sps, p, idr} {
		payload = append(payload, byte(len(nal)>>8), byte(len(nal)))
		payload = append(payload, nal...)
	}

	info := Inspect(payload)
	assert.True(t, info.ParamSet)
	assert.True(t, info.IDRStart)
	assert.Equal(t, sps, info.SPS)
	assert.Equal(t, p, info.PPS)

	// Truncated aggregation unit must not panic.
	assert.NotPanics(t, func() { Inspect([]byte{24, 0x00, 0x40, 0x67}) })
	assert.NotPanics(t, func() { Inspect([]byte{24, 0x00}) })
}

func TestInspectFUA(t *testing.T) {
	// FU indicator (type 28), FU header: start bit + type 5
	info := Inspect([]byte{0x7C, 0x85, 0x88})
	assert.True(t, info.IDR)
	assert.True(t, info.IDRStart)

	// Continuation fragment of an IDR slice
	info = Inspect([]byte{0x7C, 0x05, 0x88})
	assert.True(t, info.IDR)
	assert.False(t, info.IDRStart)
	assert.False(t, info.KeyStart())

	// Start of a non-IDR slice
	info = Inspect([]byte{0x7C, 0x81, 0x88})
	assert.False(t, info.KeyStart())

	assert.NotPanics(t, func() { Inspect([]byte{0x7C}) })
}
