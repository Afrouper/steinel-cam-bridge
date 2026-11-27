package xiongmai

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMediaStream tracks packets received via VideoWriter.WriteVideoPacket and WriteAudioPacket.
type mockMediaStream struct {
	mu           sync.Mutex
	videoPackets []*rtp.Packet
	audioPackets []*rtp.Packet
	sps          []byte
	pps          []byte
}

func (m *mockMediaStream) WriteVideoPacket(pkt *rtp.Packet) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.videoPackets = append(m.videoPackets, pkt)
}

func (m *mockMediaStream) WriteAudioPacket(pkt *rtp.Packet) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audioPackets = append(m.audioPackets, pkt)
}

func (m *mockMediaStream) GetAudioCodec() string {
	return "pcmu"
}

func (m *mockMediaStream) SetH264Parameters(sps, pps []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sps = sps
	m.pps = pps
}

func (m *mockMediaStream) getPackets() []*rtp.Packet {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]*rtp.Packet, len(m.videoPackets))
	copy(res, m.videoPackets)
	return res
}

func (m *mockMediaStream) getAudioPackets() []*rtp.Packet {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]*rtp.Packet, len(m.audioPackets))
	copy(res, m.audioPackets)
	return res
}

func TestSofiaStreamLifecycle(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := mustTCPPort(t, l)

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
								var iFrameData []byte
								iFrameData = append(iFrameData, spsNalu...)
								iFrameData = append(iFrameData, ppsNalu...)
								iFrameData = append(iFrameData, idrNalu...)
								binary.LittleEndian.PutUint32(iFramePayload[12:16], uint32(len(iFrameData)))
								iFramePayload = append(iFramePayload, iFrameData...)

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
								audioPayload = append(audioPayload, make([]byte, 4)...)
								audioData := make([]byte, 12)
								binary.LittleEndian.PutUint16(audioPayload[6:8], uint16(len(audioData)))
								audioPayload = append(audioPayload, audioData...)
								vHdrAudio := Header{
									Magic:      HeaderMagic,
									Channel:    1,
									SessionID:  0x7b,
									Sequence:   2,
									MsgID:      MsgMonitorData, // 1412
									DataLength: uint32(len(audioPayload)),
								}
								_, _ = mConn.Write(append(vHdrAudio.Encode(), audioPayload...))

								// 3. Send P-Frame Packet: 8-byte record header + P-frame NALU
								var pFramePayload []byte
								pFramePayload = append(pFramePayload, MarkerPFrame...)    // 4 bytes
								pFramePayload = append(pFramePayload, make([]byte, 4)...) // 4 bytes pad -> 8 bytes total
								binary.LittleEndian.PutUint32(pFramePayload[4:8], uint32(len(pNalu)))
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

								// 4. Send another P-Frame
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
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
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

	// Initialize H.264 video packetizer
	pktz, err := newVideoPacketizer(videoCodecH264)
	require.NoError(t, err)
	sofiaStream.packetizer = pktz

	// 1. Feed partial I-Frame record: 16 bytes header + SPS/PPS + partial IDR
	spsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x96, 0x35, 0x40}
	ppsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x3c, 0x80}
	idrNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10, 0xff}

	var iFrameData []byte
	iFrameData = append(iFrameData, spsNalu...)
	iFrameData = append(iFrameData, ppsNalu...)
	iFrameData = append(iFrameData, idrNalu...)

	var iFrameChunk []byte
	iFrameChunk = append(iFrameChunk, MarkerIFrame...)
	iFrameChunk = append(iFrameChunk, make([]byte, 12)...) // 16 bytes total record header
	binary.LittleEndian.PutUint32(iFrameChunk[12:16], uint32(len(iFrameData)))
	iFrameChunk = append(iFrameChunk, spsNalu...)
	iFrameChunk = append(iFrameChunk, ppsNalu...)
	iFrameChunk = append(iFrameChunk, idrNalu[:4]...) // partial IDR

	sofiaStream.handleMediaPayload(iFrameChunk)
	assert.Empty(t, mockStream.getPackets(), "partial I-frame must not be dispatched prematurely")

	// 2. Feed Continuation chunk (rest of IDR)
	continuationChunk := idrNalu[4:]
	sofiaStream.handleMediaPayload(continuationChunk)
	packets := mockStream.getPackets()
	require.NotEmpty(t, packets, "expected completed I-frame to be dispatched")
	assert.Equal(t, uint8(96), packets[0].PayloadType)
	assert.NotEmpty(t, mockStream.sps, "SPS should be injected into RTSP stream")
	assert.NotEmpty(t, mockStream.pps, "PPS should be injected into RTSP stream")
	iFrameTS := packets[0].Timestamp

	// 3. Feed Audio chunk: should be dispatched to audioWriter
	var audioChunk []byte
	audioChunk = append(audioChunk, MarkerAudio...)
	audioChunk = append(audioChunk, make([]byte, 4)...)
	audioData := []byte{0x01, 0x02, 0x03, 0x04}
	binary.LittleEndian.PutUint16(audioChunk[6:8], uint16(len(audioData)))
	audioChunk = append(audioChunk, audioData...)
	sofiaStream.handleMediaPayload(audioChunk)

	audioPkts1 := mockStream.getAudioPackets()
	require.Len(t, audioPkts1, 1, "audio chunk should be dispatched to audioWriter")

	// 4. Feed Metadata chunk (MarkerMeta 00 00 01 F9): must be safely consumed without being dispatched to audio (Issue #74)
	var metaChunk []byte
	metaChunk = append(metaChunk, MarkerMeta...)
	metaChunk = append(metaChunk, make([]byte, 4)...)
	metaData := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	binary.LittleEndian.PutUint16(metaChunk[6:8], uint16(len(metaData)))
	metaChunk = append(metaChunk, metaData...)
	sofiaStream.handleMediaPayload(metaChunk)

	assert.Len(t, mockStream.getAudioPackets(), 1, "0xF9 metadata packet must be ignored and not dispatched as audio to prevent clicking noise")

	// 5. Feed second Audio chunk: verify monotonic timestamp increment by payload length
	sofiaStream.handleMediaPayload(audioChunk)
	audioPkts2 := mockStream.getAudioPackets()
	require.Len(t, audioPkts2, 2, "second audio chunk should be dispatched")
	assert.Equal(t, audioPkts1[0].Timestamp+uint32(len(audioData)), audioPkts2[1].Timestamp, "audio RTP timestamps must increment monotonically by sample count")

	// 6. Feed P-Frame record: 8 bytes header + P-frame NALU
	pNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x01, 0x02, 0x03}
	var pFrameChunk []byte
	pFrameChunk = append(pFrameChunk, MarkerPFrame...)
	pFrameChunk = append(pFrameChunk, make([]byte, 4)...) // 8 bytes total record header
	binary.LittleEndian.PutUint32(pFrameChunk[4:8], uint32(len(pNalu)))
	pFrameChunk = append(pFrameChunk, pNalu...)

	sofiaStream.handleMediaPayload(pFrameChunk)

	// Verify that the P-frame was dispatched immediately and has a strictly monotonic timestamp
	allPackets := mockStream.getPackets()
	require.Greater(t, len(allPackets), len(packets), "P-frame should have been dispatched immediately")
	lastPkt := allPackets[len(allPackets)-1]
	assert.Greater(t, lastPkt.Timestamp, iFrameTS, "P-frame RTP timestamp must be strictly greater than I-frame timestamp")
}

func TestSofiaStreamLifecycle_EncryptedAES(t *testing.T) {
	// Generate mock RSA key pair for testing
	privKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)

	modHex := fmt.Sprintf("%0256X", privKey.N)
	mockPubKeyStr := fmt.Sprintf("%s,010001", modHex)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := mustTCPPort(t, l)

	var capturedCommKey []byte
	var claimReceived atomic.Bool
	var startReceivedEncrypted atomic.Bool
	var stopReceivedEncrypted atomic.Bool

	spsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x96, 0x35, 0x40}
	ppsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x3c, 0x80}
	idrNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10, 0xff}
	pNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x01, 0x02, 0x03}

	var mediaConn net.Conn
	var mediaConnMu sync.Mutex

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}

			go func(c net.Conn) {
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
					case MsgMonitorClaimReq: // 1413
						if strings.Contains(cleanPayload, `"Action":"Claim"`) && strings.Contains(cleanPayload, `"Action1":"Start"`) {
							// Media socket claim
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
						} else {
							// Security claim probing RSA
							claimResp := MonitorClaimResp{
								Ret:         100,
								Bits:        1024,
								EncryptAlgo: "RSA_V1.5",
								PublicKey:   mockPubKeyStr,
								LoginEncryptionType: LoginEncryptionType{
									RSA: true,
								},
							}
							respBytes, _ := json.Marshal(claimResp)
							respTerm := append(respBytes, 0x00)
							rHdr := Header{
								Magic:      HeaderMagic,
								Channel:    1,
								SessionID:  0x0001869F,
								Sequence:   hdr.Sequence,
								MsgID:      MsgMonitorClaimResp,
								DataLength: uint32(len(respTerm)),
							}
							_, _ = c.Write(append(rHdr.Encode(), respTerm...))
						}

					case MsgLoginReq: // 1000
						var loginReq struct {
							CommunicateKey string `json:"CommunicateKey"`
							LoginType      string `json:"LoginType"`
						}
						_ = json.Unmarshal([]byte(cleanPayload), &loginReq)
						if loginReq.CommunicateKey != "" {
							encBytes, _ := hex.DecodeString(loginReq.CommunicateKey)
							//nolint:staticcheck // Mandated by Xiongmai hardware protocol specification (EncryptAlgo: RSA_V1.5)
							decKey, decErr := rsa.DecryptPKCS1v15(rand.Reader, privKey, encBytes)
							if decErr == nil {
								capturedCommKey = decKey
							}
						}

						resp := LoginResp{
							Name:          "OPUserLogin",
							Ret:           100,
							SessionID:     "0x0000007b",
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

					case MsgMonitorStartReq: // 1410
						// Assert that channel is 1 (encrypted) and payload is AES-128-CBC Base64
						if hdr.Channel == 1 && len(cleanPayload) > 0 && cleanPayload[0] != '{' && len(capturedCommKey) == 16 {
							decB64, err := base64.StdEncoding.DecodeString(cleanPayload)
							if err == nil {
								plain, decErr := decryptAES128CBC(capturedCommKey, decB64)
								if decErr == nil {
									plainStr := string(plain)
									if strings.Contains(plainStr, `"Action":"Start"`) {
										startReceivedEncrypted.Store(true)
									} else if strings.Contains(plainStr, `"Action":"Stop"`) {
										stopReceivedEncrypted.Store(true)
									}
								}
							}
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
							MsgID:      MsgMonitorStartResp,
							DataLength: uint32(len(respTerm)),
						}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

						// Stream video packets
						if startReceivedEncrypted.Load() && !stopReceivedEncrypted.Load() {
							go func() {
								time.Sleep(50 * time.Millisecond)
								mediaConnMu.Lock()
								mConn := mediaConn
								mediaConnMu.Unlock()
								if mConn == nil {
									return
								}

								// I-Frame
								var iFramePayload []byte
								iFramePayload = append(iFramePayload, MarkerIFrame...)
								iFramePayload = append(iFramePayload, make([]byte, 12)...)
								var iFrameData []byte
								iFrameData = append(iFrameData, spsNalu...)
								iFrameData = append(iFrameData, ppsNalu...)
								iFrameData = append(iFrameData, idrNalu...)
								binary.LittleEndian.PutUint32(iFramePayload[12:16], uint32(len(iFrameData)))
								iFramePayload = append(iFramePayload, iFrameData...)

								vHdr1 := Header{
									Magic:      HeaderMagic,
									Channel:    1,
									SessionID:  0x7b,
									Sequence:   1,
									MsgID:      MsgMonitorData,
									DataLength: uint32(len(iFramePayload)),
								}
								_, _ = mConn.Write(append(vHdr1.Encode(), iFramePayload...))

								time.Sleep(10 * time.Millisecond)

								// P-Frame
								var pFramePayload []byte
								pFramePayload = append(pFramePayload, MarkerPFrame...)
								pFramePayload = append(pFramePayload, make([]byte, 4)...)
								binary.LittleEndian.PutUint32(pFramePayload[4:8], uint32(len(pNalu)))
								pFramePayload = append(pFramePayload, pNalu...)

								vHdr2 := Header{
									Magic:      HeaderMagic,
									Channel:    1,
									SessionID:  0x7b,
									Sequence:   2,
									MsgID:      MsgMonitorData,
									DataLength: uint32(len(pFramePayload)),
								}
								_, _ = mConn.Write(append(vHdr2.Encode(), pFramePayload...))
							}()
						}

					default:
						respPayload := []byte(`{"Ret":100}`)
						respTerm := append(respPayload, 0x00)
						respHdr := &Header{
							Magic:      HeaderMagic,
							Channel:    hdr.Channel,
							SessionID:  hdr.SessionID,
							Sequence:   hdr.Sequence,
							MsgID:      hdr.MsgID + 1,
							DataLength: uint32(len(respTerm)),
						}
						_, _ = c.Write(append(respHdr.Encode(), respTerm...))
					}
				}
			}(conn)
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "testpwd")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err = client.Connect(ctx)
	require.NoError(t, err)
	require.True(t, client.IsLoggedIn())
	assert.True(t, client.IsDataEncryptionAES(), "client should have activated AES encryption after RSA login")

	mockStream := &mockMediaStream{}
	sofiaStream := NewSofiaStream("127.0.0.1", port, client, mockStream)

	err = sofiaStream.Start(ctx)
	require.NoError(t, err)
	assert.True(t, sofiaStream.IsRunning())

	assert.Eventually(t, func() bool {
		return claimReceived.Load() && startReceivedEncrypted.Load()
	}, 2*time.Second, 50*time.Millisecond, "expected encrypted OPMonitor Start to be received")

	assert.Eventually(t, func() bool {
		return len(mockStream.getPackets()) > 0
	}, 2*time.Second, 50*time.Millisecond, "expected RTP packets from video stream")

	err = sofiaStream.Stop()
	require.NoError(t, err)
	assert.False(t, sofiaStream.IsRunning())
	assert.True(t, stopReceivedEncrypted.Load(), "expected encrypted OPMonitor Stop to be received")
}

func TestSofiaStream_StreamType(t *testing.T) {
	mockStream := &mockMediaStream{}

	// 1. Default stream type should be "Main"
	sDefault := NewSofiaStream("127.0.0.1", 34567, nil, mockStream)
	assert.Equal(t, "Main", sDefault.StreamType())

	// 2. Custom stream type "Extra1"
	sExtra := NewSofiaStream("127.0.0.1", 34567, nil, mockStream, "Extra1")
	assert.Equal(t, "Extra1", sExtra.StreamType())

	// 3. Dynamic mutation via SetStreamType
	sDefault.SetStreamType("Extra1")
	assert.Equal(t, "Extra1", sDefault.StreamType())

	sDefault.SetStreamType("Main")
	assert.Equal(t, "Main", sDefault.StreamType())

	// 4. Empty string defaults back to "Main"
	sDefault.SetStreamType("")
	assert.Equal(t, "Main", sDefault.StreamType())
}

func TestSofiaStream_MonotonicTimestamps_NoDrift(t *testing.T) {
	sofiaStream := NewSofiaStream("127.0.0.1", 34567, nil, nil)
	sofiaStream.fps = 6
	start := time.Now()
	sofiaStream.streamStartTime = start

	// Generate 50 consecutive timestamps simulating bursts at same wall-clock time
	var timestamps []uint32
	sofiaStream.tsMu.Lock()
	for i := 0; i < 50; i++ {
		ts := sofiaStream.generateTimestampLocked()
		timestamps = append(timestamps, ts)
	}
	sofiaStream.tsMu.Unlock()

	// 1. Strict monotonicity check
	for i := 1; i < len(timestamps); i++ {
		assert.Greater(t, timestamps[i], timestamps[i-1], "Each consecutive timestamp must be strictly greater")
	}

	// 2. Verify no runaway drift: 50 timestamps within <10ms should not race ahead to hundreds of thousands
	lastTS := timestamps[len(timestamps)-1]
	assert.Less(t, lastTS, uint32(10000), "Timestamps must not drift ahead into the future during burst delivery")
}

func TestTalkClient_ResetAndGetAddr(t *testing.T) {
	c := NewClient("127.0.0.1", 34567, "admin", "")
	assert.Equal(t, "127.0.0.1:34567", c.GetAddr())

	tc := NewTalkClient(c)
	tc.mu.Lock()
	tc.talkActive = true
	tc.mu.Unlock()

	tc.Reset()

	tc.mu.Lock()
	active := tc.talkActive
	tc.mu.Unlock()
	assert.False(t, active)
}

func TestSofiaStream_ConcurrentStopAndReadLoop(t *testing.T) {
	c1, c2 := net.Pipe()
	defer func() { _ = c2.Close() }()

	s := NewSofiaStream("127.0.0.1", 34567, nil, nil)
	s.mediaConn = c1
	s.stopChan = make(chan struct{})
	s.running.Store(true)

	s.wg.Add(1)
	go s.readLoop(context.Background(), c1)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Stop()
		}()
	}
	wg.Wait()
	assert.False(t, s.IsRunning())
}

// codecStream is a media stream that reports a fixed audio codec and accepts AAC frames.
type codecStream struct {
	*mockMediaStream
	codec string
}

func (c *codecStream) GetAudioCodec() string                   { return c.codec }
func (c *codecStream) WriteAACFrame(_ []byte, _ time.Duration) {}

// The AAC transcoder is only needed when the stream really publishes AAC. With
// "pcmu" the audio is passed through, with "none" there is no audio track.
func TestSofiaStream_TranscoderOnlyForAAC(t *testing.T) {
	for codec, want := range map[string]bool{"aac": true, "pcmu": false, "none": false} {
		ms := &mockMediaStream{}
		s := NewSofiaStream("127.0.0.1", 34567, nil, &codecStream{mockMediaStream: ms, codec: codec})
		assert.Equal(t, want, s.audioTranscoder != nil, "audio_codec %s", codec)
		if codec == "none" {
			s.tsMu.Lock()
			s.dispatchAudioLocked([]byte{0x55, 0xAA, 0x55, 0xAA})
			s.tsMu.Unlock()
			assert.Empty(t, ms.getAudioPackets(), "audio_codec none must not dispatch PCMU packets")
		}
	}
}

func TestSofiaStream_HEVCDetectionAndKeyframeGate(t *testing.T) {
	writer := &mockVideoParamsWriter{codec: videoCodecH264}
	s := NewSofiaStream("127.0.0.1", 34567, nil, writer)
	pktz, err := newVideoPacketizer(videoCodecH264)
	require.NoError(t, err)
	s.packetizer = pktz

	// 1. P-frames before the first keyframe must be dropped
	pFrameData := []byte{0x00, 0x00, 0x00, 0x01, 0x02, 0x01, 0xd0, 0x01, 0x02}
	var pFrameRecord []byte
	pFrameRecord = append(pFrameRecord, MarkerPFrame...)
	pFrameRecord = append(pFrameRecord, make([]byte, 4)...)
	binary.LittleEndian.PutUint32(pFrameRecord[4:8], uint32(len(pFrameData)))
	pFrameRecord = append(pFrameRecord, pFrameData...)

	s.handleMediaPayload(pFrameRecord)
	assert.Empty(t, writer.getPackets(), "P-frame before first keyframe must be dropped")
	assert.Equal(t, uint64(1), s.droppedBeforeKey)

	// 2. Idle JSON response on media socket must be skipped
	s.handleMediaPayload([]byte(`{"Name":"OPMonitor","Ret":100}` + "\x00"))
	assert.Empty(t, s.streamBuf)

	// 3. HEVC I-frame split into two chunks where the second chunk starts with 0x7B ('{')
	vps := []byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xac, 0x09}
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xa0, 0x02, 0x80, 0x80, 0x2d, 0x16, 0x59, 0x59, 0xa4, 0x93, 0x2b, 0xc0, 0x5a, 0x80, 0x80, 0x80, 0x82}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}
	idrPart1 := []byte{0x00, 0x00, 0x00, 0x01, 0x26, 0x01, 0xaf, 0x08}
	idrPart2 := []byte{'{', 0x11, 0x22, '}'} // binary continuation starting with '{' and ending with '}'

	var iFrameData []byte
	iFrameData = append(iFrameData, vps...)
	iFrameData = append(iFrameData, sps...)
	iFrameData = append(iFrameData, pps...)
	iFrameData = append(iFrameData, idrPart1...)
	iFrameData = append(iFrameData, idrPart2...)

	var iFrameChunk1 []byte
	iFrameChunk1 = append(iFrameChunk1, MarkerIFrame...)
	iFrameChunk1 = append(iFrameChunk1, sofiaCodecH265, 6, 240, 135, 0, 0, 0, 0, 0, 0, 0, 0) // 16-byte record header
	binary.LittleEndian.PutUint32(iFrameChunk1[12:16], uint32(len(iFrameData)))
	iFrameChunk1 = append(iFrameChunk1, iFrameData[:len(iFrameData)-len(idrPart2)]...)

	s.handleMediaPayload(iFrameChunk1)
	assert.Empty(t, writer.getPackets(), "partial HEVC I-frame must wait for continuation")

	// Feed continuation starting with '{' (0x7B): must NOT be dropped as JSON!
	s.handleMediaPayload(idrPart2)

	pkts := writer.getPackets()
	require.NotEmpty(t, pkts, "completed HEVC I-frame must be packetized and dispatched")
	assert.Equal(t, videoCodecH265, writer.codec, "RTSP stream must be switched to H.265")
	assert.Equal(t, vps[4:], writer.vps)
	assert.Equal(t, sps[4:], writer.sps)
	assert.Equal(t, pps[4:], writer.pps)

	// 4. Subsequent P-frame must now be dispatched
	prevCount := len(pkts)
	s.handleMediaPayload(pFrameRecord)
	assert.Greater(t, len(writer.getPackets()), prevCount, "P-frame after keyframe must be dispatched")
}

func TestResyncMediaHeader(t *testing.T) {
	validHdr := Header{
		Magic:      HeaderMagic,
		Channel:    0,
		SessionID:  0x7b,
		Sequence:   5,
		MsgID:      MsgMonitorData,
		DataLength: 128,
	}
	garbage := []byte{0x12, 0x34, 0x56, 0xFF, 0x99, 0x88, 0x77}
	stream := append(garbage, validHdr.Encode()...)

	rd := strings.NewReader(string(stream[HeaderLength:]))
	hdrBuf := append([]byte(nil), stream[:HeaderLength]...)

	hdr, err := resyncMediaHeader(rd, hdrBuf)
	require.NoError(t, err)
	require.NotNil(t, hdr)
	assert.Equal(t, MsgMonitorData, hdr.MsgID)
	assert.Equal(t, uint32(128), hdr.DataLength)
}

type mockAACStream struct {
	mockMediaStream
	aacFrames int
}

func (m *mockAACStream) GetAudioCodec() string { return "aac" }

func (m *mockAACStream) WriteAACFrame(_ []byte, _ time.Duration) {
	m.mu.Lock()
	m.aacFrames++
	m.mu.Unlock()
}

func TestSofiaStream_AudioSurvivesOnDemandStop(t *testing.T) {
	writer := &mockAACStream{}
	s := NewSofiaStream("127.0.0.1", 34567, nil, writer)
	require.NotNil(t, s.audioTranscoder)

	// Simulate an on-demand Stop() cycle
	s.running.Store(true)
	require.NoError(t, s.Stop())

	// Feed enough 8 kHz PCMA audio chunks after Stop() to produce at least one 1024-sample AAC frame
	audioPayload := make([]byte, 640)
	var audioRecord []byte
	audioRecord = append(audioRecord, MarkerAudio...)
	audioRecord = append(audioRecord, make([]byte, 4)...)
	binary.LittleEndian.PutUint16(audioRecord[6:8], uint16(len(audioPayload)))
	audioRecord = append(audioRecord, audioPayload...)

	for i := 0; i < 4; i++ {
		s.handleMediaPayload(audioRecord)
	}

	writer.mu.Lock()
	frames := writer.aacFrames
	writer.mu.Unlock()
	assert.Greater(t, frames, 0, "AAC audio transcoder must remain functional across on-demand Stop() cycles")
}

func buildTestH264IFramePacket(sessionID, seq uint32) []byte {
	spsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x96, 0x35, 0x40}
	ppsNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x3c, 0x80}
	idrNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10, 0xff}

	var iFrameData []byte
	iFrameData = append(iFrameData, spsNalu...)
	iFrameData = append(iFrameData, ppsNalu...)
	iFrameData = append(iFrameData, idrNalu...)

	var iFramePayload []byte
	iFramePayload = append(iFramePayload, MarkerIFrame...)
	iFramePayload = append(iFramePayload, make([]byte, 12)...)
	binary.LittleEndian.PutUint32(iFramePayload[12:16], uint32(len(iFrameData)))
	iFramePayload = append(iFramePayload, iFrameData...)

	hdr := Header{
		Magic:      HeaderMagic,
		Channel:    1,
		SessionID:  sessionID,
		Sequence:   seq,
		MsgID:      MsgMonitorData,
		DataLength: uint32(len(iFramePayload)),
	}
	return append(hdr.Encode(), iFramePayload...)
}

type recordingOnDemandRTSPServer struct {
	mockOnDemandRTSPServer
	videoPackets atomic.Int64
}

func (m *recordingOnDemandRTSPServer) WriteVideoPacket(_ *rtp.Packet) {
	m.videoPackets.Add(1)
}

func mustTCPPort(t *testing.T, l net.Listener) int {
	t.Helper()
	tcpAddr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return tcpAddr.Port
}

func TestSofiaStream_ReadLoopExitClearsRunningAndAllowsRestart(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := mustTCPPort(t, l)

	var claimCount atomic.Int32
	var startCount atomic.Int32
	var stopCount atomic.Int32
	var mediaMu sync.Mutex
	var latestMediaConn net.Conn

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
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
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{
							Name:          "OPUserLogin",
							Ret:           100,
							SessionID:     "0x0000007b",
							AliveInterval: 20,
							DeviceType:    "IPC",
						})
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorClaimReq:
						if strings.Contains(cleanPayload, `"Action1":"Start"`) {
							claimCount.Add(1)
							mediaMu.Lock()
							latestMediaConn = c
							mediaMu.Unlock()
						}

						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorStartReq:
						isStop := strings.Contains(cleanPayload, `"Action":"Stop"`)
						isStart := strings.Contains(cleanPayload, `"Action":"Start"`)
						if isStop {
							stopCount.Add(1)
						}

						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, Channel: hdr.Channel, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgMonitorStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

						if isStart {
							sessionNum := startCount.Add(1)
							mediaMu.Lock()
							mConn := latestMediaConn
							mediaMu.Unlock()
							if mConn != nil {
								go func(mc net.Conn, seq uint32, drop bool) {
									time.Sleep(20 * time.Millisecond)
									_, _ = mc.Write(buildTestH264IFramePacket(0x7b, seq))
									if drop {
										time.Sleep(20 * time.Millisecond)
										_ = mc.Close()
									}
								}(mConn, uint32(sessionNum), sessionNum == 1)
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

	client := NewClient("127.0.0.1", port, "admin", "testpwd")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	mockStream := &mockMediaStream{}
	sofiaStream := NewSofiaStream("127.0.0.1", port, client, mockStream)

	// Session 1: starts, delivers an I-frame, then the camera closes the media socket abruptly.
	require.NoError(t, sofiaStream.Start(ctx))
	require.Eventually(t, func() bool {
		return len(mockStream.getPackets()) >= 1
	}, 2*time.Second, 20*time.Millisecond, "expected initial I-frame packets before drop")

	// readLoop must clear running state automatically and notify camera via OPMonitor Stop.
	require.Eventually(t, func() bool {
		return !sofiaStream.IsRunning()
	}, 2*time.Second, 20*time.Millisecond, "SofiaStream must clear running state when readLoop exits unexpectedly")
	require.Eventually(t, func() bool {
		return stopCount.Load() >= 1
	}, 2*time.Second, 20*time.Millisecond, "unexpected readLoop exit must send OPMonitor Stop on control socket")

	packetsBeforeRestart := len(mockStream.getPackets())

	// Session 2: restarting must succeed and deliver fresh video packets.
	require.NoError(t, sofiaStream.Start(ctx))
	assert.True(t, sofiaStream.IsRunning())
	assert.Equal(t, int32(2), claimCount.Load())

	require.Eventually(t, func() bool {
		return len(mockStream.getPackets()) > packetsBeforeRestart
	}, 2*time.Second, 20*time.Millisecond, "restarted SofiaStream must deliver new video packets")

	require.NoError(t, sofiaStream.Stop())
	assert.False(t, sofiaStream.IsRunning())
}

func TestDriver_OnDemandAutoRecoversAfterSofiaMediaDrop(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := mustTCPPort(t, l)

	var claimCount atomic.Int32
	var startCount atomic.Int32
	var mediaMu sync.Mutex
	var latestMediaConn net.Conn

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
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
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x0A"})
						respTerm := append(respData, 0x0A, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgTransStartReq:
						respData, _ := json.Marshal(map[string]interface{}{"Ret": 100, "SessionID": "0x0000000A"})
						respTerm := append(respData, 0x0A, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgTransStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorClaimReq:
						if strings.Contains(cleanPayload, `"Action1":"Start"`) {
							claimCount.Add(1)
							mediaMu.Lock()
							latestMediaConn = c
							mediaMu.Unlock()
						}

						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorStartReq:
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgMonitorStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

						if strings.Contains(cleanPayload, `"Action":"Start"`) {
							sessionNum := startCount.Add(1)
							mediaMu.Lock()
							mConn := latestMediaConn
							mediaMu.Unlock()
							if mConn != nil {
								go func(mc net.Conn, seq uint32, drop bool) {
									time.Sleep(20 * time.Millisecond)
									_, _ = mc.Write(buildTestH264IFramePacket(0x0A, seq))
									if drop {
										time.Sleep(20 * time.Millisecond)
										_ = mc.Close()
									}
								}(mConn, uint32(sessionNum), sessionNum == 1)
							}
						}

					default:
						respTerm := []byte(`{"Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					}
				}
			}(conn)
		}
	}()

	mockStream := &recordingOnDemandRTSPServer{}
	// Continuously active RTSP client (Frigate 24/7 pattern — never drops to 0).
	mockStream.clients.Store(1)

	d := NewDriver("127.0.0.1", "admin", "", "1080p", mockStream, nil)
	d.client.addr = fmt.Sprintf("127.0.0.1:%d", port)
	d.sofiaStream.port = port
	defer func() { _ = d.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()

	require.NoError(t, d.Start(ctx))

	// Media socket #1 drops right after sending its I-frame. superviseOnDemandLoop must
	// automatically detect the dead stream and restart it on media socket #2.
	require.Eventually(t, func() bool {
		return claimCount.Load() >= 2 && mockStream.videoPackets.Load() >= 2 && d.sofiaStream.IsRunning()
	}, 4*time.Second, 50*time.Millisecond, "on-demand supervisor must auto-recover SofiaStream after media socket drop")
}

func TestDriver_SetResolutionKeepsSofiaStreamRunning(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := mustTCPPort(t, l)

	var mediaMu sync.Mutex
	var latestMediaConn net.Conn
	startedConns := make(chan net.Conn, 4)

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
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
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x0A"})
						respTerm := append(respData, 0x0A, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgTransStartReq:
						respData, _ := json.Marshal(map[string]interface{}{"Ret": 100, "SessionID": "0x0000000A"})
						respTerm := append(respData, 0x0A, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgTransStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorClaimReq:
						if strings.Contains(cleanPayload, `"Action1":"Start"`) {
							mediaMu.Lock()
							latestMediaConn = c
							mediaMu.Unlock()
						}

						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorStartReq:
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgMonitorStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

						if strings.Contains(cleanPayload, `"Action":"Start"`) {
							mediaMu.Lock()
							mConn := latestMediaConn
							mediaMu.Unlock()
							if mConn != nil {
								startedConns <- mConn
							}
						}

					default:
						respTerm := []byte(`{"Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					}
				}
			}(conn)
		}
	}()

	mockStream := &recordingOnDemandRTSPServer{}
	mockStream.clients.Store(1)

	d := NewDriver("127.0.0.1", "admin", "", "1080p", mockStream, nil)
	d.client.addr = fmt.Sprintf("127.0.0.1:%d", port)
	d.sofiaStream.port = port
	defer func() { _ = d.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.NoError(t, d.Start(ctx))
	select {
	case <-startedConns:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial Sofia media stream start")
	}
	require.True(t, d.sofiaStream.IsRunning())

	// Switch resolution: the restarted readLoop must use the driver's long-lived context,
	// not a short-lived context canceled when SetResolution returns.
	require.NoError(t, d.SetResolution("360p"))

	var switchedConn net.Conn
	select {
	case switchedConn = <-startedConns:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for switched Sofia media stream start")
	}

	// Allow readLoop to execute its initial select check after SetResolution returned.
	time.Sleep(50 * time.Millisecond)
	require.True(t, d.sofiaStream.IsRunning(), "SofiaStream must remain running after SetResolution returns")

	_, err = switchedConn.Write(buildTestH264IFramePacket(0x0A, 2))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return mockStream.videoPackets.Load() >= 1 && d.sofiaStream.IsRunning()
	}, 2*time.Second, 20*time.Millisecond, "restarted SofiaStream after SetResolution must continue receiving frames")
}

func TestSofiaStream_AudioOnlySilenceTriggersRecovery(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := mustTCPPort(t, l)

	var stopCount atomic.Int32
	var mediaMu sync.Mutex
	var latestMediaConn net.Conn

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
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
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x0000007b"})
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorClaimReq:
						if strings.Contains(cleanPayload, `"Action1":"Start"`) {
							mediaMu.Lock()
							latestMediaConn = c
							mediaMu.Unlock()
						}
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorStartReq:
						if strings.Contains(cleanPayload, `"Action":"Stop"`) {
							stopCount.Add(1)
						}
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, Channel: hdr.Channel, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgMonitorStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

						if strings.Contains(cleanPayload, `"Action":"Start"`) {
							mediaMu.Lock()
							mConn := latestMediaConn
							mediaMu.Unlock()
							if mConn != nil {
								// Stream ONLY audio packets (0xFA) every 20ms so socket reads never time out,
								// simulating an audio-only / metadata-only zombie stream with zero video frames.
								go func(mc net.Conn) {
									audioRecord := append([]byte(nil), MarkerAudio...)
									audioRecord = append(audioRecord, 0x0E, 0x02, 0x10, 0x00)
									audioRecord = append(audioRecord, make([]byte, 16)...)
									for seq := uint32(1); seq <= 50; seq++ {
										vHdr := Header{
											Magic:      HeaderMagic,
											Channel:    1,
											SessionID:  0x7b,
											Sequence:   seq,
											MsgID:      MsgMonitorData,
											DataLength: uint32(len(audioRecord)),
										}
										if _, wErr := mc.Write(append(vHdr.Encode(), audioRecord...)); wErr != nil {
											return
										}
										time.Sleep(20 * time.Millisecond)
									}
								}(mConn)
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

	client := NewClient("127.0.0.1", port, "admin", "testpwd")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	mockStream := &mockMediaStream{}
	sofiaStream := NewSofiaStream("127.0.0.1", port, client, mockStream)
	sofiaStream.mu.Lock()
	sofiaStream.videoSilenceTimeout = 150 * time.Millisecond
	sofiaStream.mu.Unlock()

	require.NoError(t, sofiaStream.Start(ctx))
	require.True(t, sofiaStream.IsRunning())

	require.Eventually(t, func() bool {
		return !sofiaStream.IsRunning() && stopCount.Load() >= 1
	}, 2*time.Second, 20*time.Millisecond, "audio-only stream without video frames must trigger silence watchdog and OPMonitor Stop")
}

func TestSofiaStream_StartRejectsNonZeroErrorRet(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := mustTCPPort(t, l)

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
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

					switch hdr.MsgID {
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x0000007b"})
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorClaimReq:
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorStartReq:
						// Reject OPMonitor Start with Ret=127 (max stream connections reached)
						respTerm := []byte(`{"Name":"OPMonitor","Ret":127}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, Channel: hdr.Channel, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgMonitorStartResp, DataLength: uint32(len(respTerm))}
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

	client := NewClient("127.0.0.1", port, "admin", "testpwd")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	sofiaStream := NewSofiaStream("127.0.0.1", port, client, &mockMediaStream{})
	err = sofiaStream.Start(ctx)
	require.Error(t, err)
	assert.ErrorContains(t, err, "ret=127")
	assert.False(t, sofiaStream.IsRunning())
}

func TestSofiaStream_PendingStreamBufCappedOnZeroPayloadSize(t *testing.T) {
	s := NewSofiaStream("127.0.0.1", 34567, nil, &mockMediaStream{})
	s.gotKeyframe = true

	// Feed a malformed I-frame record header with payloadSize == 0 and no subsequent record marker.
	headerOnly := bytes.Clone(MarkerIFrame)
	headerOnly = append(headerOnly, make([]byte, 12)...) // payloadSize == 0 at bytes 12..16
	s.handleMediaPayload(headerOnly)

	chunk := make([]byte, 256*1024)
	for i := range chunk {
		chunk[i] = 0xAA // never matches 00 00 01 [F8..FE]
	}
	for range 9 { // 9 * 256 KB = 2.25 MB > maxSofiaStreamBuf (2 MB)
		s.handleMediaPayload(chunk)
	}

	s.tsMu.Lock()
	bufLen := len(s.streamBuf)
	gotKey := s.gotKeyframe
	s.tsMu.Unlock()

	assert.LessOrEqual(t, bufLen, 3, "oversized unclosed payloadSize==0 buffer must be reset to prevent unbounded memory growth")
	assert.False(t, gotKey)
}

type orderRecordingWriter struct {
	mockVideoParamsWriter
	paramsPresentAtSwitch bool
}

func (w *orderRecordingWriter) SetVideoCodec(codec string) bool {
	w.mu.Lock()
	w.paramsPresentAtSwitch = len(w.vps) > 0 && len(w.sps) > 0 && len(w.pps) > 0
	changed := w.codec != codec
	w.codec = codec
	w.mu.Unlock()
	return changed
}

func TestSofiaStream_PublishesH265ParametersBeforeSetVideoCodec(t *testing.T) {
	writer := &orderRecordingWriter{}
	writer.codec = videoCodecH264
	s := NewSofiaStream("127.0.0.1", 34567, nil, writer)
	pktz, err := newVideoPacketizer(videoCodecH264)
	require.NoError(t, err)
	s.packetizer = pktz

	vps := []byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60}
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}
	idr := []byte{0x00, 0x00, 0x00, 0x01, 0x26, 0x01, 0xaf, 0x08}

	var iFrameData []byte
	iFrameData = append(iFrameData, vps...)
	iFrameData = append(iFrameData, sps...)
	iFrameData = append(iFrameData, pps...)
	iFrameData = append(iFrameData, idr...)

	var rec []byte
	rec = append(rec, MarkerIFrame...)
	rec = append(rec, sofiaCodecH265, 6, 240, 135, 0, 0, 0, 0, 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(rec[12:16], uint32(len(iFrameData)))
	rec = append(rec, iFrameData...)

	s.handleMediaPayload(rec)

	writer.mu.Lock()
	presentAtSwitch := writer.paramsPresentAtSwitch
	switchedCodec := writer.codec
	writer.mu.Unlock()

	assert.Equal(t, videoCodecH265, switchedCodec)
	assert.True(t, presentAtSwitch, "VPS/SPS/PPS must be published before SetVideoCodec creates the new RTSP stream")
}

func TestDriver_OnDemandFallsBackToRTSPIngestAfterRepeatedSofiaStreamFailures(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := mustTCPPort(t, l)
	var startAttempts atomic.Int32

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
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
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x0A"})
						respTerm := append(respData, 0x0A, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgTransStartReq:
						respData, _ := json.Marshal(map[string]interface{}{"Ret": 100, "SessionID": "0x0000000A"})
						respTerm := append(respData, 0x0A, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgTransStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorClaimReq:
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorStartReq:
						if strings.Contains(cleanPayload, `"Action":"Start"`) {
							startAttempts.Add(1)
						}
						respTerm := []byte(`{"Name":"OPMonitor","Ret":127}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgMonitorStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					default:
						respTerm := []byte(`{"Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					}
				}
			}(conn)
		}
	}()

	mockStream := &recordingOnDemandRTSPServer{}
	mockStream.clients.Store(0) // Idle at boot -> Driver.Start assumes Sofia is available and leaves d.ingest == nil.

	d := NewDriver("127.0.0.1", "admin", "", "1080p", mockStream, nil)
	d.client.addr = fmt.Sprintf("127.0.0.1:%d", port)
	d.sofiaStream.port = port
	d.onDemandTickInterval = 25 * time.Millisecond
	d.initialStreamBackoff = 25 * time.Millisecond
	defer func() { _ = d.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.NoError(t, d.Start(ctx))
	d.ingestMu.Lock()
	initialIngest := d.ingest
	d.ingestMu.Unlock()
	require.Nil(t, initialIngest, "fallback ingest must start nil when Sofia login succeeds with 0 clients")

	// Client connects later, and OPMonitor Start is repeatedly rejected by camera firmware.
	mockStream.clients.Store(1)

	require.Eventually(t, func() bool {
		d.ingestMu.Lock()
		hasFallback := d.ingest != nil
		d.ingestMu.Unlock()
		return startAttempts.Load() >= maxSofiaStreamFailuresBeforeFallback && hasFallback
	}, 2*time.Second, 25*time.Millisecond, "repeated OPMonitor failures must activate runtime RTSP fallback ingest")
}

func TestSofiaStream_PFramesKeepSilenceWatchdogAliveDuringLongGOP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := mustTCPPort(t, l)
	var mediaMu sync.Mutex
	var latestMediaConn net.Conn

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
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
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x0000007b"})
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorClaimReq:
						if strings.Contains(cleanPayload, `"Action1":"Start"`) {
							mediaMu.Lock()
							latestMediaConn = c
							mediaMu.Unlock()
						}
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorStartReq:
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, Channel: hdr.Channel, SessionID: 0x7b, Sequence: hdr.Sequence, MsgID: MsgMonitorStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

						if strings.Contains(cleanPayload, `"Action":"Start"`) {
							mediaMu.Lock()
							mConn := latestMediaConn
							mediaMu.Unlock()
							if mConn != nil {
								// Stream continuous P-frames (0xFD) without an I-frame: must NOT trip the
								// 120ms video-record silence timeout, and must recover once keyframeWaitTimeout (420ms) expires.
								go func(mc net.Conn) {
									pNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x01, 0x02}
									pRec := append([]byte(nil), MarkerPFrame...)
									pRec = append(pRec, make([]byte, 4)...)
									binary.LittleEndian.PutUint32(pRec[4:8], uint32(len(pNalu)))
									pRec = append(pRec, pNalu...)
									for seq := uint32(1); seq <= 30; seq++ {
										vHdr := Header{Magic: HeaderMagic, Channel: 1, SessionID: 0x7b, Sequence: seq, MsgID: MsgMonitorData, DataLength: uint32(len(pRec))}
										if _, wErr := mc.Write(append(vHdr.Encode(), pRec...)); wErr != nil {
											return
										}
										time.Sleep(25 * time.Millisecond)
									}
								}(mConn)
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

	client := NewClient("127.0.0.1", port, "admin", "testpwd")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	sofiaStream := NewSofiaStream("127.0.0.1", port, client, &mockMediaStream{})
	sofiaStream.mu.Lock()
	sofiaStream.videoSilenceTimeout = 120 * time.Millisecond
	sofiaStream.keyframeWaitTimeout = 420 * time.Millisecond
	sofiaStream.mu.Unlock()

	require.NoError(t, sofiaStream.Start(ctx))
	time.Sleep(220 * time.Millisecond)
	assert.True(t, sofiaStream.IsRunning(), "incoming P-frames during a long GOP must not trip videoSilenceTimeout")

	require.Eventually(t, func() bool {
		return !sofiaStream.IsRunning()
	}, 2*time.Second, 25*time.Millisecond, "stream stuck on P-frames past keyframeWaitTimeout must eventually recover")
}

func TestRequestKeyframe_SecondViewerDoesNotDropExistingViewerPFrames(t *testing.T) {
	mockStream := &recordingOnDemandRTSPServer{}
	d := NewDriver("127.0.0.1", "admin", "", "1080p", mockStream, nil)
	defer func() { _ = d.Close() }()

	// Simulate an active stream that already delivered an I-frame to viewer #1.
	d.sofiaStream.running.Store(true)
	d.mu.Lock()
	d.idleSinceLastClient = false
	d.mu.Unlock()

	iFramePkt := buildTestH264IFramePacket(0x7b, 1)
	d.sofiaStream.handleMediaPayload(iFramePkt[HeaderLength:])
	require.Greater(t, mockStream.videoPackets.Load(), int64(0))

	// Viewer #2 connects: RequestKeyframe must NOT clear gotKeyframe or drop P-frames for viewer #1.
	mockStream.clients.Store(2)
	d.RequestKeyframe()

	before := mockStream.videoPackets.Load()
	pNalu := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x01, 0x02}
	pRec := append([]byte(nil), MarkerPFrame...)
	pRec = append(pRec, make([]byte, 4)...)
	binary.LittleEndian.PutUint32(pRec[4:8], uint32(len(pNalu)))
	pRec = append(pRec, pNalu...)
	d.sofiaStream.handleMediaPayload(pRec)

	assert.Greater(t, mockStream.videoPackets.Load(), before, "joining a second viewer must not drop P-frames for existing viewers")
}

func TestDriver_SuperviseOnDemandLoop_FallbackThrottlingAndControlOutage(t *testing.T) {
	t.Run("does not thrash OPMonitor Start while fallback ingest is active", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = l.Close() }()

		port := mustTCPPort(t, l)
		var startAttempts atomic.Int32

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
						cleanPayload := strings.TrimRight(string(payload), "\x00\r\n ")

						switch hdr.MsgID {
						case MsgLoginReq:
							respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x0A"})
							respTerm := append(respData, 0x00)
							rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
							_, _ = c.Write(append(rHdr.Encode(), respTerm...))
						case MsgMonitorClaimReq:
							respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
							rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(respTerm))}
							_, _ = c.Write(append(rHdr.Encode(), respTerm...))
						case MsgMonitorStartReq:
							if strings.Contains(cleanPayload, `"Action":"Start"`) {
								startAttempts.Add(1)
							}
							respTerm := []byte(`{"Name":"OPMonitor","Ret":127}` + "\x00")
							rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: MsgMonitorStartResp, DataLength: uint32(len(respTerm))}
							_, _ = c.Write(append(rHdr.Encode(), respTerm...))
						default:
							respTerm := []byte(`{"Ret":100}` + "\x00")
							rHdr := Header{Magic: HeaderMagic, SessionID: 0x0A, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respTerm))}
							_, _ = c.Write(append(rHdr.Encode(), respTerm...))
						}
					}
				}(conn)
			}
		}()

		mockStream := &recordingOnDemandRTSPServer{}
		mockStream.clients.Store(0)

		d := NewDriver("127.0.0.1", "admin", "", "1080p", mockStream, nil)
		d.client.addr = fmt.Sprintf("127.0.0.1:%d", port)
		d.sofiaStream.port = port
		d.onDemandTickInterval = 20 * time.Millisecond
		d.initialStreamBackoff = 15 * time.Millisecond
		d.sofiaProbeInterval = 5 * time.Second
		defer func() { _ = d.Close() }()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		require.NoError(t, d.Start(ctx))

		mockStream.clients.Store(1)
		d.RequestKeyframe()

		require.Eventually(t, func() bool {
			return d.hasFallbackIngest()
		}, 2*time.Second, 20*time.Millisecond)

		attemptsAtFallback := startAttempts.Load()
		time.Sleep(150 * time.Millisecond)
		assert.Equal(t, attemptsAtFallback, startAttempts.Load(), "active fallback ingest must suppress rapid OPMonitor Start retries")
	})

	t.Run("activates fallback ingest when control socket is down while clients wait", func(t *testing.T) {
		mockStream := &recordingOnDemandRTSPServer{}
		mockStream.clients.Store(1)

		d := NewDriver("127.0.0.1", "admin", "", "1080p", mockStream, nil)
		d.onDemandTickInterval = 15 * time.Millisecond
		d.controlOutageGrace = 45 * time.Millisecond
		d.running = true
		defer func() { _ = d.Close() }()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go d.superviseOnDemandLoop(ctx)

		require.Eventually(t, func() bool {
			return d.hasFallbackIngest()
		}, 2*time.Second, 15*time.Millisecond, "prolonged control-socket outage with active clients must activate fallback RTSP ingest")
	})
}
