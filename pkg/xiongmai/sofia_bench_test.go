package xiongmai

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noopVideoWriter struct{}

func (noopVideoWriter) WriteVideoPacket(_ *rtp.Packet)          {}
func (noopVideoWriter) SetVideoCodec(_ string) bool             { return true }
func (noopVideoWriter) SetH265Parameters(_, _, _ []byte)        {}
func (noopVideoWriter) SetH264Parameters(_, _ []byte)           {}
func (noopVideoWriter) GetAudioCodec() string                   { return "none" }
func (noopVideoWriter) WriteAudioPacket(_ *rtp.Packet)          {}
func (noopVideoWriter) WriteAACFrame(_ []byte, _ time.Duration) {}

func buildLarge1080pHEVCIFrameRecord(bodyBytes int) []byte {
	vps := []byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0x95, 0x98, 0x09}
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5, 0x96, 0x56, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01, 0xe0, 0x80}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
	idrHdr := []byte{0x00, 0x00, 0x00, 0x01, 0x26, 0x01, 0xaf, 0x08}
	idrBody := bytes.Repeat([]byte{0x55, 0xAA, 0x33, 0xCC}, bodyBytes/4)

	var au []byte
	au = append(au, vps...)
	au = append(au, sps...)
	au = append(au, pps...)
	au = append(au, idrHdr...)
	au = append(au, idrBody...)

	rec := make([]byte, 16+len(au))
	copy(rec[:4], MarkerIFrame)
	rec[4] = 0x13
	rec[5] = 6
	binary.LittleEndian.PutUint32(rec[12:16], uint32(len(au)))
	copy(rec[16:], au)
	return rec
}

func TestSplitAnnexBNALUs_ThreeAndFourByteStartCodes(t *testing.T) {
	raw := []byte{
		0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0xAA,
		0x00, 0x00, 0x01, 0x42, 0x01, 0xBB, 0x00, // trailing zero inside NAL followed by 4-byte start code:
		0x00, 0x00, 0x00, 0x01, 0x44, 0x01, 0xCC,
	}
	nalus, ok := splitAnnexBNALUs(nil, raw)
	require.True(t, ok)
	require.Len(t, nalus, 3)
	assert.Equal(t, []byte{0x40, 0x01, 0xAA}, nalus[0])
	assert.Equal(t, []byte{0x42, 0x01, 0xBB, 0x00}, nalus[1])
	assert.Equal(t, []byte{0x44, 0x01, 0xCC}, nalus[2])
}

func TestSofiaStream_StreamBufCompactsWithoutCapacityLoss(t *testing.T) {
	s := NewSofiaStream("127.0.0.1", DefaultPort, nil, noopVideoWriter{})
	pktz, err := newVideoPacketizer(videoCodecH265)
	require.NoError(t, err)
	s.packetizer = pktz
	s.payloadCount = sofiaDiagPayloads + 1

	iRec := buildLarge1080pHEVCIFrameRecord(64 * 1024)
	// Warm buffer once with a keyframe + partial next frame tail
	partialTail := iRec[:512]
	combined := append(append([]byte(nil), iRec...), partialTail...)

	s.handleMediaPayload(combined)
	capAfterWarmup := cap(s.streamBuf)
	require.Equal(t, len(partialTail), len(s.streamBuf))

	// Feed remaining bytes + another partial tail repeatedly: backing capacity must stay stable.
	restPlusTail := append(append([]byte(nil), iRec[512:]...), partialTail...)
	for range 20 {
		s.handleMediaPayload(restPlusTail)
		assert.Equal(t, capAfterWarmup, cap(s.streamBuf), "streamBuf capacity must not grow once warmed")
	}
}

func TestDriver_ReconnectSession_WakesSupervisorImmediately(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	port := mustTCPPort(t, ln)

	var logins atomic.Int32
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				hdrBuf := make([]byte, HeaderLength)
				for {
					if _, rErr := io.ReadFull(c, hdrBuf); rErr != nil {
						return
					}
					hdr, dErr := DecodeHeaderValue(hdrBuf)
					if dErr != nil {
						return
					}
					payload := make([]byte, hdr.DataLength)
					if _, rErr := io.ReadFull(c, payload); rErr != nil {
						return
					}
					switch hdr.MsgID {
					case MsgLoginReq:
						seqNum := logins.Add(1)
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: fmt.Sprintf("0x%08x", seqNum)})
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: uint32(seqNum), Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					default:
						respTerm := []byte(`{"Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, Channel: hdr.Channel, SessionID: hdr.SessionID, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					}
				}
			}(conn)
		}
	}()

	bus := events.NewBus()
	d := NewDriver("127.0.0.1", "admin", "", "1080p", &recordingOnDemandRTSPServer{}, bus)
	d.client.addr = fmt.Sprintf("127.0.0.1:%d", port)
	d.sofiaStream.port = port
	defer func() { _ = d.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, d.Start(ctx))
	require.Equal(t, int32(1), logins.Load())

	// Trigger manual reconnect: sofiaWakeCh must wake superviseSofiaLoop well under the 15s ticker!
	start := time.Now()
	require.NoError(t, d.ReconnectSession())
	require.Eventually(t, func() bool {
		return logins.Load() >= 2 && d.client.IsLoggedIn()
	}, 2*time.Second, 20*time.Millisecond, "ReconnectSession must reconnect immediately without waiting for 15s heartbeat ticker")
	assert.Less(t, time.Since(start), 2*time.Second)
}

func BenchmarkSofiaDemuxAndPacketize_1080pHEVC(b *testing.B) {
	s := NewSofiaStream("127.0.0.1", DefaultPort, nil, noopVideoWriter{})
	pktz, err := newVideoPacketizer(videoCodecH265)
	if err != nil {
		b.Fatal(err)
	}
	s.packetizer = pktz
	s.payloadCount = sofiaDiagPayloads + 1

	iRec := buildLarge1080pHEVCIFrameRecord(128 * 1024)
	// Warm buffer once
	s.handleMediaPayload(iRec)

	b.ReportAllocs()
	b.SetBytes(int64(len(iRec)))
	b.ResetTimer()
	for range b.N {
		s.handleMediaPayload(iRec)
	}
}

func BenchmarkSplitAnnexBNALUs_128KB(b *testing.B) {
	iRec := buildLarge1080pHEVCIFrameRecord(128 * 1024)
	au := iRec[16:]
	var dst [][]byte
	dst, _ = splitAnnexBNALUs(dst, au)

	b.ReportAllocs()
	b.SetBytes(int64(len(au)))
	b.ResetTimer()
	for range b.N {
		var ok bool
		dst, ok = splitAnnexBNALUs(dst[:0], au)
		if !ok {
			b.Fatal("split failed")
		}
	}
}
