package xiongmai

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTalkClient_TwoSocketHandshakePreservesControlSocketOnTalkDrop(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := l.Addr().(*net.TCPAddr).Port
	var (
		mu             sync.Mutex
		latestTalkConn net.Conn
		claimConns     int32
		startOnControl atomic.Bool
		audioFrames    atomic.Int32
	)

	go func() {
		var controlConn net.Conn
		for {
			conn, acceptErr := l.Accept()
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
					hdr, dErr := DecodeHeader(hdrBuf)
					if dErr != nil {
						return
					}
					payload := make([]byte, hdr.DataLength)
					if _, rErr := io.ReadFull(c, payload); rErr != nil {
						return
					}

					switch hdr.MsgID {
					case MsgLoginReq:
						mu.Lock()
						controlConn = c
						mu.Unlock()
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x00000010"})
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x10, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkClaimV2Req:
						mu.Lock()
						assert.NotSame(t, controlConn, c, "MsgTalkClaimV2Req (1434) must use a dedicated secondary TCP socket")
						latestTalkConn = c
						claimConns++
						mu.Unlock()
						respTerm := []byte(`{"Name":"OPTalk","Ret":100,"SessionID":"0x00000010"}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x10, Sequence: hdr.Sequence, MsgID: MsgTalkClaimV2Resp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkControlReq:
						mu.Lock()
						isCtrl := c == controlConn
						mu.Unlock()
						if isCtrl {
							startOnControl.Store(true)
						}
						respTerm := []byte(`{"AudioFormat":{"BitRate":128,"EncodeType":"G711_ALAW","SampleBit":16,"SampleRate":8000},"Ret":100,"SessionID":"0x00000010"}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, Channel: hdr.Channel, SessionID: 0x10, Sequence: hdr.Sequence + 1, MsgID: MsgTalkControlResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkAudioData:
						if len(payload) >= 8 && payload[0] == 0x00 && payload[1] == 0x00 && payload[2] == 0x01 && payload[3] == 0xFA {
							recLen := int(binary.LittleEndian.Uint16(payload[6:8]))
							if recLen == len(payload)-8 {
								audioFrames.Add(1)
							}
						}
					default:
						respTerm := []byte(`{"Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, Channel: hdr.Channel, SessionID: hdr.SessionID, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					}
				}
			}(conn)
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	talk := NewTalkClient(client)
	defer func() { _ = talk.StopTalk() }()

	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 160},
		Payload: []byte{0x55, 0x55, 0x55, 0x55},
	}

	require.NoError(t, talk.SendAudioPacket(pkt))
	assert.True(t, startOnControl.Load(), "MsgTalkControlReq (1430) must be sent on the primary control socket")
	require.Eventually(t, func() bool { return audioFrames.Load() >= 1 }, time.Second, 10*time.Millisecond)

	// Drop the secondary talk socket: primary control connection must stay alive and healthy,
	// and subsequent audio packets must transparently re-claim a new secondary talk socket.
	mu.Lock()
	tc := latestTalkConn
	mu.Unlock()
	require.NotNil(t, tc)
	_ = tc.Close()

	require.Eventually(t, func() bool {
		_ = talk.SendAudioPacket(pkt)
		mu.Lock()
		cc := claimConns
		mu.Unlock()
		return cc >= 2
	}, 2*time.Second, 20*time.Millisecond, "secondary talk socket drop must re-claim OPTalk on next audio packet")

	assert.True(t, client.IsLoggedIn(), "dropping secondary talk socket must not tear down primary control session")
	require.NoError(t, client.SendKeepAlive(), "primary control socket KeepAlive must remain healthy")
}

func TestTalkClient_StartTalkRejectsErrorRet(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := l.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			conn, acceptErr := l.Accept()
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
					hdr, dErr := DecodeHeader(hdrBuf)
					if dErr != nil {
						return
					}
					payload := make([]byte, hdr.DataLength)
					if _, rErr := io.ReadFull(c, payload); rErr != nil {
						return
					}

					switch hdr.MsgID {
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x00000010"})
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x10, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkControlReq:
						respTerm := []byte(`{"Name":"OPTalk","Ret":107}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x10, Sequence: hdr.Sequence, MsgID: MsgTalkControlResp, DataLength: uint32(len(respTerm))}
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

	client := NewClient("127.0.0.1", port, "admin", "")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	talk := NewTalkClient(client)
	err = talk.StartTalk()
	require.Error(t, err)
	assert.ErrorContains(t, err, "ret=107")
}

func TestTalkClient_ReclaimsOnSessionChangeAndAutoStopsOnIdle(t *testing.T) {
	assert.True(t, defaultNotEncryptMsgIDs[MsgTalkClaimV2Req], "MsgTalkClaimV2Req (1434) must be in defaultNotEncryptMsgIDs")
	assert.True(t, defaultNotEncryptMsgIDs[MsgTalkClaimV2Resp], "MsgTalkClaimV2Resp (1435) must be in defaultNotEncryptMsgIDs")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := l.Addr().(*net.TCPAddr).Port
	var (
		claimCount int32
		stopCount  int32
		sessionSeq uint32 = 0x20
		mu         sync.Mutex
	)

	go func() {
		for {
			conn, acceptErr := l.Accept()
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
					hdr, dErr := DecodeHeader(hdrBuf)
					if dErr != nil {
						return
					}
					payload := make([]byte, hdr.DataLength)
					if _, rErr := io.ReadFull(c, payload); rErr != nil {
						return
					}

					switch hdr.MsgID {
					case MsgLoginReq:
						mu.Lock()
						sessionSeq++
						sid := sessionSeq
						mu.Unlock()
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x00000021"})
						if sid == 0x22 {
							respData, _ = json.Marshal(LoginResp{Ret: 100, SessionID: "0x00000022"})
						}
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: sid, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkClaimV2Req:
						mu.Lock()
						claimCount++
						mu.Unlock()
						respTerm := []byte(`{"Name":"OPTalk","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: hdr.SessionID, Sequence: hdr.Sequence, MsgID: MsgTalkClaimV2Resp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkControlReq:
						var req OPTalkReq
						_ = json.Unmarshal(payload[:len(payload)-1], &req)
						if req.OPTalk.Action == "Stop" {
							mu.Lock()
							stopCount++
							mu.Unlock()
						}
						respTerm := []byte(`{"Name":"OPTalk","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: hdr.SessionID, Sequence: hdr.Sequence, MsgID: MsgTalkControlResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkAudioData:
					default:
						respTerm := []byte(`{"Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: hdr.SessionID, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					}
				}
			}(conn)
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	talk := NewTalkClient(client)
	talk.idleTimeout = 80 * time.Millisecond

	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 160},
		Payload: []byte{0x11, 0x22, 0x33, 0x44},
	}
	require.NoError(t, talk.SendAudioPacket(pkt))

	// Simulate an implicit reconnect with a new SessionID (0x22) without calling talk.Reset().
	_ = client.Close()
	client = NewClient("127.0.0.1", port, "admin", "")
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()
	talk.mu.Lock()
	talk.client = client
	talk.mu.Unlock()

	require.NoError(t, talk.SendAudioPacket(pkt))
	mu.Lock()
	claims := claimCount
	mu.Unlock()
	assert.Equal(t, int32(2), claims, "SessionID change must automatically re-claim OPTalk even without explicit Reset()")

	// Verify idle auto-stop closes OPTalk after silence.
	require.Eventually(t, func() bool {
		mu.Lock()
		stops := stopCount
		mu.Unlock()
		return stops >= 1
	}, time.Second, 15*time.Millisecond, "idle talk channel must auto-send OPTalk Stop after idleTimeout")
}

func BenchmarkTalkEncodeAudioPacket(b *testing.B) {
	tc := &TalkClient{
		talkSessionID: 0x1234,
		audioFormat:   "G711_ALAW",
	}
	samples := make([]byte, 160)
	for i := range samples {
		samples[i] = 0xFF
	}
	// Warm reusable buffer once
	_ = tc.encodeTalkPacketLocked(0, samples)

	b.ReportAllocs()
	b.SetBytes(int64(len(samples)))
	b.ResetTimer()
	for range b.N {
		_ = tc.encodeTalkPacketLocked(0, samples)
	}
}

func TestTalkClient_ScryptedJumboPacketChunking(t *testing.T) {
	type audioFrame struct {
		hdr     Header
		recHdr  [8]byte
		samples []byte
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	var (
		mu     sync.Mutex
		frames []audioFrame
	)

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
					hdr, dErr := DecodeHeader(hdrBuf)
					if dErr != nil {
						return
					}
					payload := make([]byte, hdr.DataLength)
					if _, rErr := io.ReadFull(c, payload); rErr != nil {
						return
					}
					switch hdr.MsgID {
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x00000055"})
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x55, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkClaimV2Req:
						respTerm := []byte(`{"Name":"OPTalk","Ret":100,"AudioFormat":{"EncodeType":"G711_ALAW","SampleRate":8000,"SampleBit":8,"BitRate":64}}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: hdr.SessionID, Sequence: hdr.Sequence, MsgID: MsgTalkClaimV2Resp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkControlReq:
						respTerm := []byte(`{"Name":"OPTalk","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: hdr.SessionID, Sequence: hdr.Sequence, MsgID: MsgTalkControlResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					case MsgTalkAudioData:
						if len(payload) >= 8 {
							var recHdr [8]byte
							copy(recHdr[:], payload[:8])
							samples := append([]byte(nil), payload[8:]...)
							mu.Lock()
							frames = append(frames, audioFrame{hdr: *hdr, recHdr: recHdr, samples: samples})
							mu.Unlock()
						}
					default:
						respTerm := []byte(`{"Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: hdr.SessionID, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					}
				}
			}(conn)
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	talk := NewTalkClient(client)
	defer func() { _ = talk.StopTalk() }()

	// Scrypted OnvifIntercom aggregates 8 kHz PCMU RTP frames up to 160 ms (<= 1024 bytes, e.g. 944 bytes).
	jumboPCMU := make([]byte, 944)
	for i := range jumboPCMU {
		jumboPCMU[i] = byte(i & 0xFF)
	}
	expectedPCMA := audio.TranscodePCMUToPCMA(jumboPCMU)

	require.NoError(t, talk.SendAudioPacket(&rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 7, Timestamp: 960, Marker: true},
		Payload: jumboPCMU,
	}))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(frames) == 3
	}, time.Second, 15*time.Millisecond, "944-byte Scrypted jumbo packet must be split into 3 DVRIP 1432 frames (320 + 320 + 304 bytes)")

	mu.Lock()
	gotFrames := append([]audioFrame(nil), frames...)
	mu.Unlock()

	expectedLens := []int{320, 320, 304}
	var reassembled []byte
	for i, f := range gotFrames {
		assert.Equal(t, uint32(i+2), f.hdr.Sequence)
		assert.Equal(t, uint32(8+expectedLens[i]), f.hdr.DataLength)
		assert.Equal(t, MarkerAudio, f.recHdr[0:4])
		assert.Equal(t, byte(0x0E), f.recHdr[4], "must carry PCMA codec byte 0x0E")
		assert.Equal(t, byte(0x02), f.recHdr[5], "must carry 8000 Hz sample rate byte 0x02")
		assert.Equal(t, uint16(expectedLens[i]), binary.LittleEndian.Uint16(f.recHdr[6:8]))
		assert.Len(t, f.samples, expectedLens[i])
		reassembled = append(reassembled, f.samples...)
	}
	assert.Equal(t, expectedPCMA, reassembled, "chunked PCMA samples must match exact PCMU->PCMA transcoding")
}
