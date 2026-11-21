package xiongmai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockVideoParamsWriter struct {
	mockMediaStream
	codec string
	vps   []byte
}

func (m *mockVideoParamsWriter) SetVideoCodec(codec string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := m.codec != codec
	m.codec = codec
	return changed
}

func (m *mockVideoParamsWriter) SetH265Parameters(vps, sps, pps []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vps = append([]byte(nil), vps...)
	m.sps = append([]byte(nil), sps...)
	m.pps = append([]byte(nil), pps...)
}

func TestNewVideoPacketizer(t *testing.T) {
	tests := []struct {
		name          string
		inputCodec    string
		expectedCodec string
	}{
		{
			name:          "h264 default",
			inputCodec:    videoCodecH264,
			expectedCodec: videoCodecH264,
		},
		{
			name:          "h265 packetizer",
			inputCodec:    videoCodecH265,
			expectedCodec: videoCodecH265,
		},
		{
			name:          "unknown defaults to h264",
			inputCodec:    "unknown",
			expectedCodec: videoCodecH264,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pktz, err := newVideoPacketizer(tt.inputCodec)
			require.NoError(t, err)
			require.NotNil(t, pktz)
			assert.Equal(t, tt.expectedCodec, pktz.codec())
		})
	}
}

func TestH264Packetizer_Packetize(t *testing.T) {
	pktz, err := newVideoPacketizer(videoCodecH264)
	require.NoError(t, err)

	sps := []byte{0x67, 0x42, 0x00, 0x1f, 0x96, 0x35, 0x40}
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	idr := []byte{0x65, 0x88, 0x84, 0x00, 0x10, 0xff}
	aud := []byte{0x09, 0xf0}
	forbidden := []byte{0x80 | 0x65, 0x01, 0x02}

	t.Run("filters invalid nalus and publishes sps pps", func(t *testing.T) {
		writer := &mockVideoParamsWriter{}
		pkts, err := pktz.packetize([][]byte{nil, forbidden, aud, sps, pps, idr}, writer)
		require.NoError(t, err)
		require.NotEmpty(t, pkts)
		assert.Equal(t, sps, writer.sps)
		assert.Equal(t, pps, writer.pps)
	})

	t.Run("returns nil when all nalus are filtered out", func(t *testing.T) {
		writer := &mockVideoParamsWriter{}
		pkts, err := pktz.packetize([][]byte{nil, forbidden, aud}, writer)
		require.NoError(t, err)
		assert.Empty(t, pkts)
	})
}

func TestH265Packetizer_Packetize(t *testing.T) {
	pktz, err := newVideoPacketizer(videoCodecH265)
	require.NoError(t, err)

	// HEVC NAL unit headers: (type << 1), layer 0, temporal_id_plus1 = 1 (0x01)
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xac, 0x09}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xa0, 0x02, 0x80, 0x80, 0x2d, 0x16, 0x59, 0x59, 0xa4, 0x93, 0x2b, 0xc0, 0x5a, 0x80, 0x80, 0x80, 0x82}
	pps := []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}
	idr := []byte{0x26, 0x01, 0xaf, 0x08, 0x40, 0x11, 0x22, 0x33}
	aud := []byte{0x46, 0x01, 0x50}            // type 35 (AUD) -> filtered
	filler := []byte{0x4c, 0x01, 0xff}         // type 38 (Filler) -> filtered
	reserved := []byte{0x62, 0x01, 0x00}       // type 49 (FU / >40) -> filtered
	badTemporal := []byte{0x26, 0x00, 0x01}    // temporal_id_plus1 == 0 -> filtered
	forbidden := []byte{0x80 | 0x26, 0x01, 01} // forbidden_zero_bit != 0 -> filtered

	t.Run("filters invalid nalus and publishes vps sps pps", func(t *testing.T) {
		writer := &mockVideoParamsWriter{}
		pkts, err := pktz.packetize([][]byte{
			{0x26}, // too short (<2 bytes)
			forbidden,
			badTemporal,
			aud,
			filler,
			reserved,
			vps,
			sps,
			pps,
			idr,
		}, writer)
		require.NoError(t, err)
		require.NotEmpty(t, pkts)
		assert.Equal(t, vps, writer.vps)
		assert.Equal(t, sps, writer.sps)
		assert.Equal(t, pps, writer.pps)
		for _, pkt := range pkts {
			if len(pkt.Payload) >= 2 && (pkt.Payload[0]>>1)&0x3F == 48 {
				assert.NotZero(t, pkt.Payload[1]&0x07, "RFC 7798 Aggregation Packet header must have non-zero temporal_id_plus1")
			}
		}
	})

	t.Run("returns nil when all nalus are rejected", func(t *testing.T) {
		writer := &mockVideoParamsWriter{}
		pkts, err := pktz.packetize([][]byte{{0x01}, forbidden, badTemporal, aud, filler, reserved}, writer)
		require.NoError(t, err)
		assert.Empty(t, pkts)
	})
}

func TestFirstAnnexBNALU(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{
			name:     "no start code",
			input:    []byte{0x01, 0x02, 0x03},
			expected: nil,
		},
		{
			name:     "single 4-byte start code",
			input:    []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f},
			expected: []byte{0x67, 0x42, 0x00, 0x1f},
		},
		{
			name:     "two nalus separated by 4-byte start code trims zero prefix",
			input:    []byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0xaa, 0x00, 0x00, 0x00, 0x01, 0x42, 0x01},
			expected: []byte{0x40, 0x01, 0xaa},
		},
		{
			name:     "two nalus separated by 3-byte start code",
			input:    []byte{0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x00, 0x01, 0x68, 0xce},
			expected: []byte{0x67, 0x42},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, firstAnnexBNALU(tt.input))
		})
	}
}

func TestDetectSofiaVideoCodec(t *testing.T) {
	tests := []struct {
		name      string
		codecByte byte
		payload   []byte
		expected  string
	}{
		{
			name:      "hevc vps nal header overrides h264 codec byte",
			codecByte: sofiaCodecH264,
			payload:   []byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c, 0x01},
			expected:  videoCodecH265,
		},
		{
			name:      "hevc with leading aud followed by vps and sps",
			codecByte: sofiaCodecH264,
			payload: []byte{
				0x00, 0x00, 0x00, 0x01, 0x46, 0x01, 0x50, // HEVC AUD (type 35)
				0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c, // HEVC VPS (type 32)
			},
			expected: videoCodecH265,
		},
		{
			name:      "hevc bare idr_w_radl slice",
			codecByte: 0x00,
			payload:   []byte{0x00, 0x00, 0x00, 0x01, 0x26, 0x01, 0xaf, 0x08},
			expected:  videoCodecH265,
		},
		{
			name:      "h264 sps nal header overrides h265 codec byte",
			codecByte: sofiaCodecH265,
			payload:   []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f},
			expected:  videoCodecH264,
		},
		{
			name:      "h264 with leading aud and sei followed by sps",
			codecByte: 0x00,
			payload: []byte{
				0x00, 0x00, 0x00, 0x01, 0x09, 0xf0, // H.264 AUD (type 9)
				0x00, 0x00, 0x00, 0x01, 0x06, 0x01, 0x04, // H.264 SEI (type 6)
				0x00, 0x00, 0x00, 0x01, 0x67, 0x64, 0x00, // H.264 SPS (type 7)
			},
			expected: videoCodecH264,
		},
		{
			name:      "h264 sps followed by pps with nal_ref_idc 1 (0x28 0x01) is not misclassified as hevc",
			codecByte: sofiaCodecH264,
			payload: []byte{
				0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f,
				0x00, 0x00, 0x00, 0x01, 0x28, 0x01, 0xce, 0x3c,
			},
			expected: videoCodecH264,
		},
		{
			name:      "fallback to xiongmai codec byte h265 when payload has no start code",
			codecByte: 0x13, // lower nibble 0x03
			payload:   []byte{0xaa, 0xbb},
			expected:  videoCodecH265,
		},
		{
			name:      "fallback to xiongmai codec byte h264 when payload has no start code",
			codecByte: 0x02,
			payload:   nil,
			expected:  videoCodecH264,
		},
		{
			name:      "unknown codec byte and empty payload returns empty string",
			codecByte: 0x00,
			payload:   nil,
			expected:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, detectSofiaVideoCodec(tt.codecByte, tt.payload))
		})
	}
}
