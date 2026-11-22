package xiongmai

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockIngestMediaStream struct {
	mockVideoParamsWriter
	audioCodec     string
	h265ParamCalls int
	videoPktCh     chan *rtp.Packet
	audioPktCh     chan *rtp.Packet
}

var _ rtsp.MediaStream = (*mockIngestMediaStream)(nil)

func newMockIngestMediaStream(audioCodec string) *mockIngestMediaStream {
	return &mockIngestMediaStream{
		mockVideoParamsWriter: mockVideoParamsWriter{codec: rtsp.VideoCodecH264},
		audioCodec:            audioCodec,
		videoPktCh:            make(chan *rtp.Packet, 16),
		audioPktCh:            make(chan *rtp.Packet, 16),
	}
}

func (m *mockIngestMediaStream) WriteVideoPacket(pkt *rtp.Packet) {
	m.mockVideoParamsWriter.WriteVideoPacket(pkt)
	select {
	case m.videoPktCh <- pkt:
	default:
	}
}

func (m *mockIngestMediaStream) WriteAACFrame(_ []byte, _ time.Duration) {}

func (m *mockIngestMediaStream) WriteAudioPacket(pkt *rtp.Packet) {
	m.mockVideoParamsWriter.WriteAudioPacket(pkt)
	select {
	case m.audioPktCh <- pkt:
	default:
	}
}

func (m *mockIngestMediaStream) GetAudioCodec() string                                     { return m.audioCodec }
func (m *mockIngestMediaStream) SetAudioBackchannelHandler(_ rtsp.AudioBackchannelHandler) {}
func (m *mockIngestMediaStream) SetOnPlayHandler(_ rtsp.OnPlayHandler)                     {}
func (m *mockIngestMediaStream) ActiveClients() int64                                      { return 1 }

func (m *mockIngestMediaStream) VideoCodec() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.codec
}

func (m *mockIngestMediaStream) SetH265Parameters(vps, sps, pps []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vps = bytes.Clone(vps)
	m.sps = bytes.Clone(sps)
	m.pps = bytes.Clone(pps)
	m.h265ParamCalls++
}

func (m *mockIngestMediaStream) snapshot() (codec string, vps, sps, pps []byte, h265Calls int, videoCount int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.codec, bytes.Clone(m.vps), bytes.Clone(m.sps), bytes.Clone(m.pps), m.h265ParamCalls, len(m.videoPackets)
}

func (m *mockIngestMediaStream) audioSnapshot() []*rtp.Packet {
	return m.getAudioPackets()
}

type fakeMediaPackets struct {
	media   *description.Media
	packets []*rtp.Packet
}

type fakeCameraRTSPHandler struct {
	stream     *gortsplib.ServerStream
	tracks     []fakeMediaPackets
	setupCount atomic.Int32
}

func (h *fakeCameraRTSPHandler) OnDescribe(_ *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	return &base.Response{StatusCode: base.StatusOK}, h.stream, nil
}

func (h *fakeCameraRTSPHandler) OnSetup(_ *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	h.setupCount.Add(1)
	return &base.Response{StatusCode: base.StatusOK}, h.stream, nil
}

func (h *fakeCameraRTSPHandler) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	go func() {
		// Give gortsplib a brief moment to send the PLAY 200 OK before streaming RTP packets.
		time.Sleep(15 * time.Millisecond)
		for _, tr := range h.tracks {
			for _, pkt := range tr.packets {
				_ = h.stream.WritePacketRTP(tr.media, pkt)
			}
		}
	}()
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func startFakeCameraRTSPServer(t *testing.T, desc *description.Session, tracks ...fakeMediaPackets) (int, *fakeCameraRTSPHandler) {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	handler := &fakeCameraRTSPHandler{
		tracks: tracks,
	}
	srv := &gortsplib.Server{
		Handler:     handler,
		RTSPAddress: fmt.Sprintf("127.0.0.1:%d", port),
	}
	require.NoError(t, srv.Start())
	handler.stream = gortsplib.NewServerStream(srv, desc)

	t.Cleanup(func() {
		handler.stream.Close()
		srv.Close()
	})
	return port, handler
}

func buildHEVCAggregationPacket(nalus ...[]byte) []byte {
	// RFC 7798 Aggregation Packet header: F=0, Type=48 (0x60), LayerId=0, TID=1 (0x01).
	buf := []byte{0x60, 0x01}
	for _, nalu := range nalus {
		var sz [2]byte
		binary.BigEndian.PutUint16(sz[:], uint16(len(nalu)))
		buf = append(buf, sz[:]...)
		buf = append(buf, nalu...)
	}
	return buf
}

func TestGetCandidateURLs(t *testing.T) {
	t.Parallel()

	urls := getCandidateURLs("192.168.1.100", 554, "admin", "secret123", 0)
	require.NotEmpty(t, urls)

	// Candidate #1 MUST be the clean canonical URL WITHOUT ?real_stream to avoid camera buffer overflow
	assert.Equal(t, "rtsp://192.168.1.100:554/user=admin_password=secret123_channel=1_stream=0.sdp", urls[0])

	// Candidate #2 MUST be the legacy query-parameter URL WITH ?real_stream
	assert.Equal(t, "rtsp://192.168.1.100:554/user=admin_password=secret123_channel=1_stream=0.sdp?real_stream", urls[1])

	// Fallback Xiongmai standard path
	assert.Contains(t, urls[2], "rtsp://admin:secret123@192.168.1.100:554/stream=0")

	// Verify fallback empty password URL is included at the end
	var emptyPwdFound bool
	for _, u := range urls {
		if strings.Contains(u, "password=_") {
			emptyPwdFound = true
			break
		}
	}
	assert.True(t, emptyPwdFound, "Fallback empty password candidate should be generated")
}

func TestSelectIngestVideoTrack(t *testing.T) {
	t.Parallel()

	h264Media := &description.Media{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{&format.H264{PayloadTyp: 96, PacketizationMode: 1}},
	}
	h265Media := &description.Media{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{&format.H265{PayloadTyp: 96}},
	}
	audioOnlyMedia := &description.Media{
		Type:    description.MediaTypeAudio,
		Formats: []format.Format{&format.G711{MULaw: true, SampleRate: 8000, ChannelCount: 1}},
	}

	tests := []struct {
		name          string
		medias        []*description.Media
		expectedCodec string
		expectedMedia *description.Media
	}{
		{
			name:          "selects h264 track when only h264 is offered",
			medias:        []*description.Media{h264Media, audioOnlyMedia},
			expectedCodec: rtsp.VideoCodecH264,
			expectedMedia: h264Media,
		},
		{
			name:          "selects h265 track when only h265 is offered",
			medias:        []*description.Media{h265Media, audioOnlyMedia},
			expectedCodec: rtsp.VideoCodecH265,
			expectedMedia: h265Media,
		},
		{
			name:          "prefers h264 over h265 even when h265 appears first",
			medias:        []*description.Media{h265Media, h264Media},
			expectedCodec: rtsp.VideoCodecH264,
			expectedMedia: h264Media,
		},
		{
			name:          "returns nil when no compatible video track is present",
			medias:        []*description.Media{audioOnlyMedia},
			expectedCodec: "",
			expectedMedia: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			is := assert.New(t)

			medi, fmtObj, codec := selectIngestVideoTrack(tt.medias)
			is.Equal(tt.expectedCodec, codec)
			is.Same(tt.expectedMedia, medi)
			if tt.expectedMedia == nil {
				is.Nil(fmtObj)
			} else {
				is.NotNil(fmtObj)
			}
		})
	}
}

func TestConfigureIngestVideoTrack(t *testing.T) {
	t.Parallel()

	h264SPS := []byte{0x67, 0x42, 0x00, 0x1f}
	h264PPS := []byte{0x68, 0xce, 0x3c, 0x80}
	h265VPS := []byte{0x40, 0x01, 0x0c, 0x01}
	h265SPS := []byte{0x42, 0x01, 0x01, 0x01}
	h265PPS := []byte{0x44, 0x01, 0xc1, 0x72}

	tests := []struct {
		name          string
		initialCodec  string
		videoFormat   format.Format
		expectedCodec string
		expectedVPS   []byte
		expectedSPS   []byte
		expectedPPS   []byte
	}{
		{
			name:          "h264 publishes sps and pps",
			initialCodec:  rtsp.VideoCodecH265,
			videoFormat:   &format.H264{PayloadTyp: 96, SPS: h264SPS, PPS: h264PPS, PacketizationMode: 1},
			expectedCodec: rtsp.VideoCodecH264,
			expectedSPS:   h264SPS,
			expectedPPS:   h264PPS,
		},
		{
			name:          "h265 switches codec and publishes vps sps pps",
			initialCodec:  rtsp.VideoCodecH264,
			videoFormat:   &format.H265{PayloadTyp: 96, VPS: h265VPS, SPS: h265SPS, PPS: h265PPS},
			expectedCodec: rtsp.VideoCodecH265,
			expectedVPS:   h265VPS,
			expectedSPS:   h265SPS,
			expectedPPS:   h265PPS,
		},
		{
			name:          "h265 without sdp parameters still switches codec",
			initialCodec:  rtsp.VideoCodecH264,
			videoFormat:   &format.H265{PayloadTyp: 96},
			expectedCodec: rtsp.VideoCodecH265,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			is := assert.New(t)

			writer := newMockIngestMediaStream("pcmu")
			writer.SetVideoCodec(tt.initialCodec)

			configureIngestVideoTrack(writer, tt.videoFormat, "rtsp://127.0.0.1:554/stream=0")

			codec, vps, sps, pps, _, _ := writer.snapshot()
			is.Equal(tt.expectedCodec, codec)
			is.Equal(tt.expectedVPS, vps)
			is.Equal(tt.expectedSPS, sps)
			is.Equal(tt.expectedPPS, pps)
		})
	}

	t.Run("nil writer is a safe no-op", func(t *testing.T) {
		t.Parallel()
		assert.NotPanics(t, func() {
			configureIngestVideoTrack(nil, &format.H265{PayloadTyp: 96}, "rtsp://127.0.0.1:554/stream=0")
		})
	})
}

func TestH265RTPParamTracker(t *testing.T) {
	t.Parallel()

	vps := []byte{0x40, 0x01, 0x0c, 0x01}
	sps := []byte{0x42, 0x01, 0x01, 0x01}
	pps := []byte{0x44, 0x01, 0xc1, 0x72}
	idr := []byte{0x26, 0x01, 0xaf, 0x08}

	tests := []struct {
		name          string
		maxDONDiff    int
		payloads      [][]byte
		expectedVPS   []byte
		expectedSPS   []byte
		expectedPPS   []byte
		expectedCalls int
	}{
		{
			name:          "single nal parameter sets publish once all three arrive and deduplicate repeats",
			payloads:      [][]byte{vps, sps, pps, idr, vps, sps, pps},
			expectedVPS:   vps,
			expectedSPS:   sps,
			expectedPPS:   pps,
			expectedCalls: 1,
		},
		{
			name:          "aggregation packet publishes vps sps pps",
			payloads:      [][]byte{buildHEVCAggregationPacket(vps, sps, pps, idr)},
			expectedVPS:   vps,
			expectedSPS:   sps,
			expectedPPS:   pps,
			expectedCalls: 1,
		},
		{
			name:          "aggregation packet is skipped when max don diff is non-zero",
			maxDONDiff:    4,
			payloads:      [][]byte{buildHEVCAggregationPacket(vps, sps, pps)},
			expectedCalls: 0,
		},
		{
			name:          "malformed and truncated payloads are ignored safely",
			payloads:      [][]byte{nil, {0x40}, {0x80 | 0x40, 0x01}, {0x60, 0x01, 0x00, 0x10, 0x40}},
			expectedCalls: 0,
		},
		{
			name: "single nal with illegal temporal_id_plus1 zero or non-zero layer id is rejected",
			payloads: [][]byte{
				{0x40, 0x00, 0x0c, 0x01}, // VPS with temporal_id_plus1 == 0
				{0x40, 0x09, 0x0c, 0x01}, // VPS with layer_id != 0
				sps,
				pps,
			},
			expectedCalls: 0,
		},
		{
			name: "aggregation packet sub-nalus with illegal temporal_id_plus1 zero or non-zero layer id are rejected",
			payloads: [][]byte{
				buildHEVCAggregationPacket(
					[]byte{0x40, 0x00, 0x0c, 0x01}, // VPS with temporal_id_plus1 == 0
					[]byte{0x40, 0x09, 0x0c, 0x01}, // VPS with layer_id != 0
					sps,
					pps,
				),
			},
			expectedCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			is := assert.New(t)

			writer := newMockIngestMediaStream("pcmu")
			tracker := newH265RTPParamTracker(&format.H265{PayloadTyp: 96, MaxDONDiff: tt.maxDONDiff})
			for _, p := range tt.payloads {
				tracker.observe(p, writer)
			}

			_, gotVPS, gotSPS, gotPPS, calls, _ := writer.snapshot()
			is.Equal(tt.expectedCalls, calls)
			is.Equal(tt.expectedVPS, gotVPS)
			is.Equal(tt.expectedSPS, gotSPS)
			is.Equal(tt.expectedPPS, gotPPS)
		})
	}
}

func TestRTSPIngestDialShortCircuit(t *testing.T) {
	t.Parallel()

	// Find an unused local port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // Close port so connection is refused

	ing := NewRTSPIngest("127.0.0.1", port, "admin", "secret", 0, nil, "fallback")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err = ing.runSession(ctx)
	elapsed := time.Since(start)

	require.Error(t, err)

	var dErr *dialError
	assert.True(t, errors.As(err, &dErr), "Expected dialError when port is closed")
	assert.Contains(t, dErr.host, fmt.Sprintf("127.0.0.1:%d", port))

	// Short-circuit must return quickly (single attempt) instead of trying 10+ candidates
	assert.Less(t, elapsed, 500*time.Millisecond, "Dial to closed port should short-circuit immediately")
}

func TestRTSPIngestRunSession_VideoCodecs(t *testing.T) {
	// Valid RFC 7798 HEVC parameter sets (VPS, SPS, PPS) accepted by gortsplib's SDP parser.
	vps := []byte{
		0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00,
		0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xac, 0x09,
	}
	h265SPS := []byte{
		0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03,
		0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03,
		0x00, 0x78, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5,
		0x96, 0x66, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00,
		0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01,
		0xe0, 0x80,
	}
	h265PPS := []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}
	h265IDR := []byte{0x26, 0x01, 0xaf, 0x08, 0x40, 0x11, 0x22, 0x33}

	// Valid RFC 6184 H.264 parameter sets (SPS, PPS) accepted by gortsplib's SDP parser.
	h264SPS := []byte{
		0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x01, 0x41, 0xfb, 0x01, 0x10, 0x00, 0x00,
		0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x03, 0xc0, 0xf1, 0x42, 0x99, 0x60,
	}
	h264PPS := []byte{0x68, 0xce, 0x3c, 0x80}
	h264IDR := []byte{0x65, 0x88, 0x84, 0x00, 0x10, 0xff}

	tests := []struct {
		name          string
		buildDesc     func() (*description.Session, *description.Media, []*rtp.Packet)
		expectedCodec string
		expectedVPS   []byte
		expectedSPS   []byte
		expectedPPS   []byte
		expectErr     bool
	}{
		{
			name: "h265 stream with sdp parameters switches local stream and forwards packets",
			buildDesc: func() (*description.Session, *description.Media, []*rtp.Packet) {
				medi := &description.Media{
					Type: description.MediaTypeVideo,
					Formats: []format.Format{
						&format.H265{PayloadTyp: 96, VPS: vps, SPS: h265SPS, PPS: h265PPS},
					},
				}
				pkt := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: 1,
						Timestamp:      90000,
						SSRC:           0x12345678,
						Marker:         true,
					},
					Payload: h265IDR,
				}
				return &description.Session{Medias: []*description.Media{medi}}, medi, []*rtp.Packet{pkt}
			},
			expectedCodec: rtsp.VideoCodecH265,
			expectedVPS:   vps,
			expectedSPS:   h265SPS,
			expectedPPS:   h265PPS,
		},
		{
			name: "h265 stream without sdp parameters extracts in-band vps sps pps from rtp",
			buildDesc: func() (*description.Session, *description.Media, []*rtp.Packet) {
				medi := &description.Media{
					Type:    description.MediaTypeVideo,
					Formats: []format.Format{&format.H265{PayloadTyp: 96}},
				}
				pkt := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: 2,
						Timestamp:      93600,
						SSRC:           0x22334455,
						Marker:         true,
					},
					Payload: buildHEVCAggregationPacket(vps, h265SPS, h265PPS, h265IDR),
				}
				return &description.Session{Medias: []*description.Media{medi}}, medi, []*rtp.Packet{pkt}
			},
			expectedCodec: rtsp.VideoCodecH265,
			expectedVPS:   vps,
			expectedSPS:   h265SPS,
			expectedPPS:   h265PPS,
		},
		{
			name: "h264 stream keeps h264 codec and publishes sps pps",
			buildDesc: func() (*description.Session, *description.Media, []*rtp.Packet) {
				medi := &description.Media{
					Type: description.MediaTypeVideo,
					Formats: []format.Format{
						&format.H264{PayloadTyp: 96, PacketizationMode: 1, SPS: h264SPS, PPS: h264PPS},
					},
				}
				pkt := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: 3,
						Timestamp:      97200,
						SSRC:           0x33445566,
						Marker:         true,
					},
					Payload: h264IDR,
				}
				return &description.Session{Medias: []*description.Media{medi}}, medi, []*rtp.Packet{pkt}
			},
			expectedCodec: rtsp.VideoCodecH264,
			expectedSPS:   h264SPS,
			expectedPPS:   h264PPS,
		},
		{
			name: "audio-only sdp fails cleanly without switching codec",
			buildDesc: func() (*description.Session, *description.Media, []*rtp.Packet) {
				medi := &description.Media{
					Type:    description.MediaTypeAudio,
					Formats: []format.Format{&format.G711{MULaw: true, SampleRate: 8000, ChannelCount: 1}},
				}
				return &description.Session{Medias: []*description.Media{medi}}, medi, nil
			},
			expectedCodec: rtsp.VideoCodecH264,
			expectErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)

			desc, media, packets := tt.buildDesc()
			port, _ := startFakeCameraRTSPServer(t, desc, fakeMediaPackets{media: media, packets: packets})

			writer := newMockIngestMediaStream("pcmu")
			ing := NewRTSPIngest("127.0.0.1", port, "admin", "secret", 0, writer)
			// Keep a single deterministic URL candidate pointing at our local fake server.
			ing.candidateURLs = ing.candidateURLs[:1]

			errCh := make(chan error, 1)
			go func() {
				errCh <- ing.runSession(context.Background())
			}()

			if tt.expectErr {
				select {
				case err := <-errCh:
					must.Error(err)
					is.ErrorContains(err, "no compatible H.264/H.265 video track")
				case <-time.After(3 * time.Second):
					t.Fatal("timed out waiting for runSession error")
				}
				is.Equal(tt.expectedCodec, writer.VideoCodec())
				return
			}

			select {
			case pkt := <-writer.videoPktCh:
				must.NotNil(pkt)
			case err := <-errCh:
				t.Fatalf("runSession exited before delivering video packet: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for forwarded RTP video packet")
			}

			must.NoError(ing.Close())
			select {
			case err := <-errCh:
				is.NoError(err)
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for runSession to stop after Close")
			}

			codec, gotVPS, gotSPS, gotPPS, _, videoCount := writer.snapshot()
			is.Equal(tt.expectedCodec, codec)
			is.Equal(tt.expectedVPS, gotVPS)
			is.Equal(tt.expectedSPS, gotSPS)
			is.Equal(tt.expectedPPS, gotPPS)
			is.GreaterOrEqual(videoCount, 1)
		})
	}
}

func TestNormalizeH265RTPPacket(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		payload         []byte
		wantPayload     []byte
		wantSamePointer bool
	}{
		{
			name:            "aggregation packet with temporal_id_plus1 zero is normalized without mutating caller slice",
			payload:         []byte{0x60, 0x00, 0x00, 0x02, 0x26, 0x01},
			wantPayload:     []byte{0x60, 0x01, 0x00, 0x02, 0x26, 0x01},
			wantSamePointer: false,
		},
		{
			name:            "aggregation packet with valid temporal_id_plus1 is returned untouched",
			payload:         []byte{0x60, 0x02, 0x00, 0x02, 0x26, 0x01},
			wantPayload:     []byte{0x60, 0x02, 0x00, 0x02, 0x26, 0x01},
			wantSamePointer: true,
		},
		{
			name:            "non-aggregation packet is returned untouched",
			payload:         []byte{0x26, 0x00, 0xaf, 0x08},
			wantPayload:     []byte{0x26, 0x00, 0xaf, 0x08},
			wantSamePointer: true,
		},
		{
			name:            "short payload is returned untouched",
			payload:         []byte{0x60},
			wantPayload:     []byte{0x60},
			wantSamePointer: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			is := assert.New(t)

			origPayload := bytes.Clone(tt.payload)
			in := &rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 7},
				Payload: origPayload,
			}

			got := normalizeH265RTPPacket(in)
			is.Equal(tt.wantPayload, got.Payload)
			// Verify caller slice and packet are never mutated.
			is.Equal(tt.payload, origPayload)
			is.Equal(tt.payload, in.Payload)
			if tt.wantSamePointer {
				is.Same(in, got)
			} else {
				is.NotSame(in, got)
			}
		})
	}

	t.Run("nil packet returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, normalizeH265RTPPacket(nil))
	})
}

func TestRTSPIngestRunSession_AudioAndContext(t *testing.T) {
	h264SPS := []byte{
		0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x01, 0x41, 0xfb, 0x01, 0x10, 0x00, 0x00,
		0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x03, 0xc0, 0xf1, 0x42, 0x99, 0x60,
	}
	h264PPS := []byte{0x68, 0xce, 0x3c, 0x80}
	h264IDR := []byte{0x65, 0x88, 0x84, 0x00, 0x10, 0xff}

	buildVideoAndPCMAAudio := func(pcmaPayload []byte) (*description.Session, fakeMediaPackets, fakeMediaPackets) {
		vMedia := &description.Media{
			Type: description.MediaTypeVideo,
			Formats: []format.Format{
				&format.H264{PayloadTyp: 96, PacketizationMode: 1, SPS: h264SPS, PPS: h264PPS},
			},
		}
		aMedia := &description.Media{
			Type: description.MediaTypeAudio,
			Formats: []format.Format{
				&format.G711{PayloadTyp: 8, MULaw: false, SampleRate: 8000, ChannelCount: 1},
			},
		}
		vPkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: 1,
				Timestamp:      90000,
				SSRC:           0x11112222,
				Marker:         true,
			},
			Payload: h264IDR,
		}
		aPkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    8,
				SequenceNumber: 10,
				Timestamp:      8000,
				SSRC:           0x33334444,
			},
			Payload: pcmaPayload,
		}
		return &description.Session{Medias: []*description.Media{vMedia, aMedia}},
			fakeMediaPackets{media: vMedia, packets: []*rtp.Packet{vPkt}},
			fakeMediaPackets{media: aMedia, packets: []*rtp.Packet{aPkt}}
	}

	t.Run("transcodes PCMA to PCMU when audio_codec is pcmu", func(t *testing.T) {
		is := assert.New(t)
		must := require.New(t)

		pcmaPayload := []byte{0x55, 0xd5, 0x2a, 0xaa, 0x7f, 0xff}
		desc, vTrack, aTrack := buildVideoAndPCMAAudio(pcmaPayload)
		port, handler := startFakeCameraRTSPServer(t, desc, vTrack, aTrack)

		writer := newMockIngestMediaStream("pcmu")
		ing := NewRTSPIngest("127.0.0.1", port, "admin", "secret", 0, writer)
		ing.candidateURLs = ing.candidateURLs[:1]

		errCh := make(chan error, 1)
		go func() {
			errCh <- ing.runSession(context.Background())
		}()

		select {
		case pkt := <-writer.audioPktCh:
			must.NotNil(pkt)
			is.Equal(audio.TranscodePCMAToPCMU(pcmaPayload), pkt.Payload)
		case err := <-errCh:
			t.Fatalf("runSession exited before delivering audio packet: %v", err)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for transcoded audio packet")
		}

		must.NoError(ing.Close())
		select {
		case err := <-errCh:
			is.NoError(err)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for runSession to stop after Close")
		}
		is.Equal(int32(2), handler.setupCount.Load())
	})

	t.Run("skips audio setup when audio_codec is none", func(t *testing.T) {
		is := assert.New(t)
		must := require.New(t)

		pcmaPayload := []byte{0x55, 0xd5, 0x2a, 0xaa}
		desc, vTrack, aTrack := buildVideoAndPCMAAudio(pcmaPayload)
		port, handler := startFakeCameraRTSPServer(t, desc, vTrack, aTrack)

		writer := newMockIngestMediaStream(rtsp.AudioCodecNone)
		ing := NewRTSPIngest("127.0.0.1", port, "admin", "secret", 0, writer)
		ing.candidateURLs = ing.candidateURLs[:1]

		errCh := make(chan error, 1)
		go func() {
			errCh <- ing.runSession(context.Background())
		}()

		select {
		case pkt := <-writer.videoPktCh:
			must.NotNil(pkt)
		case err := <-errCh:
			t.Fatalf("runSession exited before delivering video packet: %v", err)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for video packet")
		}

		must.NoError(ing.Close())
		select {
		case err := <-errCh:
			is.NoError(err)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for runSession to stop after Close")
		}

		is.Equal(int32(1), handler.setupCount.Load(), "audio SETUP must be skipped when audio_codec is none")
		is.Empty(writer.audioSnapshot())
	})

	t.Run("context cancellation terminates active runSession cleanly", func(t *testing.T) {
		is := assert.New(t)
		must := require.New(t)

		pcmaPayload := []byte{0x55, 0xd5}
		desc, vTrack, _ := buildVideoAndPCMAAudio(pcmaPayload)
		port, _ := startFakeCameraRTSPServer(t, desc, vTrack)

		writer := newMockIngestMediaStream("pcmu")
		ing := NewRTSPIngest("127.0.0.1", port, "admin", "secret", 0, writer)
		ing.candidateURLs = ing.candidateURLs[:1]
		defer func() { _ = ing.Close() }()

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- ing.runSession(ctx)
		}()

		select {
		case pkt := <-writer.videoPktCh:
			must.NotNil(pkt)
		case err := <-errCh:
			t.Fatalf("runSession exited before delivering video packet: %v", err)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for video packet")
		}

		cancel()
		select {
		case err := <-errCh:
			is.NoError(err)
		case <-time.After(3 * time.Second):
			t.Fatal("runSession did not exit after context cancellation")
		}
	})
}
