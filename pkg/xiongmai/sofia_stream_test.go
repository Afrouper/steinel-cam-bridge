package xiongmai

import (
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

	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMediaStream tracks packets received via VideoWriter.WriteVideoPacket.
type mockMediaStream struct {
	mu           sync.Mutex
	videoPackets []*rtp.Packet
	sps          []byte
	pps          []byte
}

func (m *mockMediaStream) WriteVideoPacket(pkt *rtp.Packet) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.videoPackets = append(m.videoPackets, pkt)
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

	// 3. Feed Audio chunk: should be safely consumed without corrupting video
	var audioChunk []byte
	audioChunk = append(audioChunk, MarkerAudio...)
	audioChunk = append(audioChunk, make([]byte, 4)...)
	audioData := []byte{0x01, 0x02, 0x03, 0x04}
	binary.LittleEndian.PutUint16(audioChunk[6:8], uint16(len(audioData)))
	audioChunk = append(audioChunk, audioData...)
	sofiaStream.handleMediaPayload(audioChunk)

	// 4. Feed P-Frame record: 8 bytes header + P-frame NALU
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

	port := l.Addr().(*net.TCPAddr).Port

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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
