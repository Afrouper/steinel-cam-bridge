package h265x

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testVPS = []byte{
		0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00,
		0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xac, 0x09,
	}
	// 1920x1080 HEVC SPS
	testSPS = []byte{
		0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03,
		0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03,
		0x00, 0x78, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5,
		0x96, 0x66, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00,
		0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01,
		0xe0, 0x80,
	}
	testPPS = []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}
)

func buildTestAP(nalus ...[]byte) []byte {
	ap := []byte{0x60, 0x01} // Aggregation Packet (type 48)
	for _, nal := range nalus {
		var sz [2]byte
		binary.BigEndian.PutUint16(sz[:], uint16(len(nal)))
		ap = append(ap, sz[:]...)
		ap = append(ap, nal...)
	}
	return ap
}

func TestInspect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		payload           []byte
		expectedKeyStart  bool
		expectedIRAP      bool
		expectedIRAPStart bool
		expectedParamSet  bool
		expectedVPS       []byte
		expectedSPS       []byte
		expectedPPS       []byte
	}{
		{
			name:    "single non-irap p-slice",
			payload: []byte{0x02, 0x01, 0xd0, 0x11},
		},
		{
			name:              "single idr slice",
			payload:           []byte{0x26, 0x01, 0xaf, 0x08},
			expectedKeyStart:  true,
			expectedIRAP:      true,
			expectedIRAPStart: true,
		},
		{
			name:             "aggregation packet with vps sps pps",
			payload:          buildTestAP(testVPS, testSPS, testPPS),
			expectedKeyStart: true,
			expectedParamSet: true,
			expectedVPS:      testVPS,
			expectedSPS:      testSPS,
			expectedPPS:      testPPS,
		},
		{
			name:              "fu start fragment",
			payload:           []byte{0x62, 0x01, 0x80 | 19, 0xAA, 0xBB},
			expectedKeyStart:  true,
			expectedIRAP:      true,
			expectedIRAPStart: true,
		},
		{
			name:         "fu middle fragment",
			payload:      []byte{0x62, 0x01, 19, 0xCC, 0xDD},
			expectedIRAP: true,
		},
		{
			name:    "truncated payload",
			payload: []byte{0x40},
		},
		{
			name:    "forbidden zero bit set",
			payload: []byte{0x80 | 0x26, 0x01, 0x00},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			is := assert.New(t)

			got := Inspect(tt.payload)
			is.Equal(tt.expectedKeyStart, got.KeyStart())
			is.Equal(tt.expectedIRAP, got.IRAP)
			is.Equal(tt.expectedIRAPStart, got.IRAPStart)
			is.Equal(tt.expectedParamSet, got.ParamSet)
			is.Equal(tt.expectedVPS, got.VPS)
			is.Equal(tt.expectedSPS, got.SPS)
			is.Equal(tt.expectedPPS, got.PPS)
		})
	}
}

func TestParseSizeAndResolutionLabel(t *testing.T) {
	t.Parallel()

	w, h, err := ParseSize(testSPS)
	require.NoError(t, err)
	assert.Equal(t, 1920, w)
	assert.Equal(t, 1080, h)

	tests := []struct {
		name     string
		width    int
		height   int
		expected string
	}{
		{name: "1080p standard", width: 1920, height: 1080, expected: "1080p"},
		{name: "1088 macroblock height", width: 1920, height: 1088, expected: "1080p"},
		{name: "720p", width: 1280, height: 720, expected: "720p"},
		{name: "360p", width: 640, height: 360, expected: "360p"},
		{name: "custom size", width: 2560, height: 1440, expected: "2560x1440"},
		{name: "zero size", width: 0, height: 0, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, ResolutionLabel(tt.width, tt.height))
		})
	}
}

func TestInspectRejectsIllegalTemporalIDAndNonBaseLayerParamSets(t *testing.T) {
	t.Parallel()

	// nuh_temporal_id_plus1 == 0 violates RFC 7798 Section 1.1.4.
	illegalTemporal := []byte{0x40, 0x00, 0x0c, 0x01}
	got := Inspect(illegalTemporal)
	assert.False(t, got.ParamSet)
	assert.Nil(t, got.VPS)

	// Non-zero nuh_layer_id (layer 1: byte 1 upper 5 bits != 0) must not overwrite base-layer VPS.
	nonBaseLayerVPS := []byte{0x40, 0x09, 0x0c, 0x01}
	gotLayer1 := Inspect(nonBaseLayerVPS)
	assert.False(t, gotLayer1.ParamSet)
	assert.Nil(t, gotLayer1.VPS)
}
