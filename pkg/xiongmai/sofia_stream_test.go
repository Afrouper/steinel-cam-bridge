package xiongmai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMediaStream tracks packets received via VideoWriter.WriteVideoPacket.
type mockMediaStream struct {
	mu           sync.Mutex
	videoPackets []*rtp.Packet
}

func (m *mockMediaStream) WriteVideoPacket(pkt *rtp.Packet) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.videoPackets = append(m.videoPackets, pkt)
}

func (m *mockMediaStream) getPackets() []*rtp.Packet {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]*rtp.Packet, len(m.videoPackets))
	copy(res, m.videoPackets)
	return res
}

func TestSofiaStreamLifecycle(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := l.Addr().(*net.TCPAddr).Port

	var claimReceived atomic.Bool
	var startReceived atomic.Bool
	var stopReceived atomic.Bool

	// Sample Annex-B H.264 NAL units (SPS: 0x67, PPS: 0x68, IDR: 0x65, P-frame: 0x41)
	spsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x96, 0x35, 0x40}
	ppsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x3c, 0x80}
	idrNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10, 0xff}
	pNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x01, 0x02, 0x03}

	var connCount atomic.Int32
	var mediaConn net.Conn
	var mediaConnMu sync.Mutex

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			idx := connCount.Add(1)

			go func(c net.Conn, connectionIndex int32) {
				defer func() { _ = c.Close() }()
				hdrBuf := make([]byte, HeaderLength)

				for {
					if _, err := io.ReadFull(c, hdrBuf); err != nil {
						return
					}
					hdr, err := DecodeHeader(hdrBuf)
					if err != nil {
						return
					}
					payload := make([]byte, hdr.DataLength)
					if _, err := io.ReadFull(c, payload); err != nil {
						return
					}

					cleanPayload := strings.TrimRight(string(payload), "\x00\r\n ")

					switch hdr.MsgID {
					case MsgLoginReq: // 1000 on control socket
						resp := LoginResp{
							Name:          "OPUserLogin",
							Ret:           100,
							SessionID:     "0x0000007b", // 123 in hex
							AliveInterval: 20,
							DeviceType:    "IPC",
						}
						respData, _ := json.Marshal(resp)
						respTerm := append(respData, 0x00)
						rHdr := Header{
							Magic:      HeaderMagic,
							Channel:    0,
							SessionID:  0x7b,
							Sequence:   hdr.Sequence,
							MsgID:      MsgLoginResp,
							DataLength: uint32(len(respTerm)),
						}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorClaimReq: // 1413 on media socket
						claimReceived.Store(true)
						mediaConnMu.Lock()
						mediaConn = c
						mediaConnMu.Unlock()

						resp := map[string]interface{}{
							"Name":      "OPMonitor",
							"Ret":       100,
							"SessionID": "0x000000007b",
						}
						respData, _ := json.Marshal(resp)
						respTerm := append(respData, 0x00)
						rHdr := Header{
							Magic:      HeaderMagic,
							Channel:    0,
							SessionID:  0x7b,
							Sequence:   hdr.Sequence,
							MsgID:      MsgMonitorClaimResp, // 1414
							DataLength: uint32(len(respTerm)),
						}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorStartReq: // 1410 on control socket
						if strings.Contains(cleanPayload, `"Action":"Start"`) || strings.Contains(string(payload), "Start") {
							startReceived.Store(true)
						} else if strings.Contains(cleanPayload, `"Action":"Stop"`) || strings.Contains(string(payload), "Stop") {
							stopReceived.Store(true)
						}

						resp := map[string]interface{}{
							"Name": "OPMonitor",
							"Ret":  100,
						}
						respData, _ := json.Marshal(resp)
						respTerm := append(respData, 0x00)
						rHdr := Header{
							Magic:      HeaderMagic,
							Channel:    hdr.Channel,
							SessionID:  0x7b,
							Sequence:   hdr.Sequence,
							MsgID:      MsgMonitorStartResp, // 1411
							DataLength: uint32(len(respTerm)),
						}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

						// When Start is received, stream video packets over mediaConn
						if startReceived.Load() && !stopReceived.Load() {
							go func() {
								time.Sleep(50 * time.Millisecond)
								mediaConnMu.Lock()
								mConn := mediaConn
								mediaConnMu.Unlock()
								if mConn == nil {
									return
								}

								// 1. Send I-Frame Packet: 16-byte record header + SPS + PPS + IDR
								var iFramePayload []byte
								iFramePayload = append(iFramePayload, MarkerIFrame...)     // 4 bytes
								iFramePayload = append(iFramePayload, make([]byte, 12)...) // 12 bytes pad -> 16 bytes total
								iFramePayload = append(iFramePayload, spsNalu...)
								iFramePayload = append(iFramePayload, ppsNalu...)
								iFramePayload = append(iFramePayload, idrNalu...)

								vHdr1 := Header{
									Magic:      HeaderMagic,
									Channel:    1,
									SessionID:  0x7b,
									Sequence:   1,
									MsgID:      MsgMonitorData, // 1412
									DataLength: uint32(len(iFramePayload)),
								}
								_, _ = mConn.Write(append(vHdr1.Encode(), iFramePayload...))

								// 2. Send Audio Packet (MarkerAudio 00 00 01 FA): should be safely skipped
								var audioPayload []byte
								audioPayload = append(audioPayload, MarkerAudio...)
								audioPayload = append(audioPayload, make([]byte, 12)...)
								vHdrAudio := Header{
									Magic:      HeaderMagic,
									Channel:    1,
									SessionID:  0x7b,
									Sequence:   2,
									MsgID:      MsgMonitorData, // 1412
									DataLength: uint32(len(audioPayload)),
								}
								_, _ = mConn.Write(append(vHdrAudio.Encode(), audioPayload...))

								// 3. Send P-Frame Packet: 8-byte record header + P-frame NALU (flushes the I-frame)
								var pFramePayload []byte
								pFramePayload = append(pFramePayload, MarkerPFrame...)    // 4 bytes
								pFramePayload = append(pFramePayload, make([]byte, 4)...) // 4 bytes pad -> 8 bytes total
								pFramePayload = append(pFramePayload, pNalu...)

								vHdr2 := Header{
									Magic:      HeaderMagic,
									Channel:    1,
									SessionID:  0x7b,
									Sequence:   3,
									MsgID:      MsgMonitorData, // 1412
									DataLength: uint32(len(pFramePayload)),
								}
								_, _ = mConn.Write(append(vHdr2.Encode(), pFramePayload...))

								// 4. Send another P-Frame to flush the previous P-frame
								vHdr3 := Header{
									Magic:      HeaderMagic,
									Channel:    1,
									SessionID:  0x7b,
									Sequence:   4,
									MsgID:      MsgMonitorData, // 1412
									DataLength: uint32(len(pFramePayload)),
								}
								_, _ = mConn.Write(append(vHdr3.Encode(), pFramePayload...))
							}()
						}

					default:
						respPayloadWithTerm := []byte(`{"Ret":100}` + "\x00")
						respHdr := &Header{
							Magic:      HeaderMagic,
							Channel:    hdr.Channel,
							SessionID:  hdr.SessionID,
							Sequence:   hdr.Sequence,
							MsgID:      hdr.MsgID + 1,
							DataLength: uint32(len(respPayloadWithTerm)),
						}
						_, _ = c.Write(append(respHdr.Encode(), respPayloadWithTerm...))
					}
				}
			}(conn, idx)
		}
	}()

	// 1. Establish control client connection
	client := NewClient("127.0.0.1", port, "admin", "testpwd")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.Connect(ctx)
	require.NoError(t, err)
	require.True(t, client.IsLoggedIn())

	// 2. Start SofiaStream
	mockStream := &mockMediaStream{}
	sofiaStream := NewSofiaStream("127.0.0.1", port, client, mockStream)

	err = sofiaStream.Start(ctx)
	require.NoError(t, err)
	assert.True(t, sofiaStream.IsRunning())

	// 3. Verify Claim and Start were received
	assert.True(t, claimReceived.Load(), "expected OPMonitor Claim (1413) to be received on media socket")
	assert.True(t, startReceived.Load(), "expected OPMonitor Start (1410) to be received on control socket")

	// 4. Verify that video packets were parsed and delivered to RTSP server
	require.Eventually(t, func() bool {
		pkts := mockStream.getPackets()
		return len(pkts) >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected H.264 video RTP packets to be received")

	packets := mockStream.getPackets()
	assert.NotEmpty(t, packets)
	assert.Equal(t, uint8(96), packets[0].PayloadType)

	// 5. Stop SofiaStream
	err = sofiaStream.Stop()
	require.NoError(t, err)
	assert.False(t, sofiaStream.IsRunning())
	assert.True(t, stopReceived.Load(), "expected OPMonitor Stop (1410) to be received on control socket")
}

func TestSofiaStreamMediaMarkersDemuxing(t *testing.T) {
	mockStream := &mockMediaStream{}
	sofiaStream := NewSofiaStream("127.0.0.1", 34567, nil, mockStream)

	// Initialize RTP H.264 encoder
	enc := &rtph264.Encoder{
		PayloadType:       96,
		PacketizationMode: 1,
	}
	require.NoError(t, enc.Init())
	sofiaStream.encoder = enc

	var frameBuf bytes.Buffer

	// 1. Feed I-Frame record: 16 bytes header + SPS/PPS/IDR
	spsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x96, 0x35, 0x40}
	ppsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x3c, 0x80}
	idrNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10, 0xff}

	var iFrameChunk []byte
	iFrameChunk = append(iFrameChunk, MarkerIFrame...)
	iFrameChunk = append(iFrameChunk, make([]byte, 12)...) // 16 bytes total record header
	iFrameChunk = append(iFrameChunk, spsNalu...)
	iFrameChunk = append(iFrameChunk, ppsNalu...)
	iFrameChunk = append(iFrameChunk, idrNalu[:4]...) // partial IDR

	sofiaStream.handleMediaPayload(iFrameChunk, &frameBuf)
	assert.True(t, frameBuf.Len() > 0, "expected frame buffer to contain partial I-frame")

	// 2. Feed Continuation chunk (no marker, rest of IDR)
	continuationChunk := idrNalu[4:]
	sofiaStream.handleMediaPayload(continuationChunk, &frameBuf)
	assert.Equal(t, len(spsNalu)+len(ppsNalu)+len(idrNalu), frameBuf.Len())

	// 3. Feed Audio chunk: should not affect video buffer
	var audioChunk []byte
	audioChunk = append(audioChunk, MarkerAudio...)
	audioChunk = append(audioChunk, []byte{0x01, 0x02, 0x03, 0x04}...)
	prevLen := frameBuf.Len()
	sofiaStream.handleMediaPayload(audioChunk, &frameBuf)
	assert.Equal(t, prevLen, frameBuf.Len(), "audio chunk must not be appended to video frame buffer")

	// 4. Feed P-Frame record: 8 bytes header + P-frame NALU (this triggers dispatch of the completed I-frame!)
	pNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x01, 0x02, 0x03}
	var pFrameChunk []byte
	pFrameChunk = append(pFrameChunk, MarkerPFrame...)
	pFrameChunk = append(pFrameChunk, make([]byte, 4)...) // 8 bytes total record header
	pFrameChunk = append(pFrameChunk, pNalu...)

	sofiaStream.handleMediaPayload(pFrameChunk, &frameBuf)

	// Verify that the I-frame was dispatched to mockStream
	packets := mockStream.getPackets()
	require.NotEmpty(t, packets, "I-frame should have been dispatched when P-frame marker arrived")
	assert.Equal(t, uint8(96), packets[0].PayloadType)

	// Now frameBuf should hold the new P-frame
	assert.Equal(t, len(pNalu), frameBuf.Len())
}
