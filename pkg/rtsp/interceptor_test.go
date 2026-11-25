package rtsp

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildInterleavedFrame(t *testing.T, channel byte, pkt *rtp.Packet) []byte {
	t.Helper()
	raw, err := pkt.Marshal()
	require.NoError(t, err)
	frame := make([]byte, 4+len(raw))
	frame[0] = '$'
	frame[1] = channel
	binary.BigEndian.PutUint16(frame[2:4], uint16(len(raw)))
	copy(frame[4:], raw)
	return frame
}

func TestInterceptingConn_IgnoresVideoAndMicChannels0And2(t *testing.T) {
	srv, err := NewServer(8581, "test", "aac", "", "")
	require.NoError(t, err)
	defer srv.Close()

	var backchannelCalls atomic.Int32
	srv.SetAudioBackchannelHandler(func(_ *rtp.Packet) error {
		backchannelCalls.Add(1)
		return nil
	})

	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	ic := newInterceptingConn(serverConn, srv)

	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 7, Timestamp: 160},
		Payload: []byte{0x11, 0x22, 0x33, 0x44},
	}

	// Channels 0 (video RTP) and 2 (camera mic RTP) must NOT be hijacked as talk-back audio.
	for _, ch := range []byte{0, 2} {
		frame := buildInterleavedFrame(t, ch, pkt)
		go func(f []byte) {
			_, _ = clientConn.Write(f)
		}(frame)

		out := make([]byte, len(frame)+16)
		n, rErr := ic.Read(out)
		require.NoError(t, rErr)
		assert.Equal(t, frame, out[:n], "channel %d frame must be passed through to gortsplib untouched", ch)
		assert.Zero(t, backchannelCalls.Load(), "channel %d must not trigger audio backchannel", ch)
	}

	// Channel 4 (backchannel RTP) followed by an RTSP ASCII command must intercept channel 4 and return the ASCII command.
	bcFrame := buildInterleavedFrame(t, 4, pkt)
	asciiCmd := []byte("OPTIONS * RTSP/1.0\r\nCSeq: 2\r\n\r\n")
	go func() {
		_, _ = clientConn.Write(append(bcFrame, asciiCmd...))
	}()

	out := make([]byte, 128)
	n, err := ic.Read(out)
	require.NoError(t, err)
	assert.Equal(t, asciiCmd, out[:n])
	assert.Equal(t, int32(1), backchannelCalls.Load(), "channel 4 backchannel RTP must be intercepted once")
}

func TestMultiStream_TCPInterleavedBackchannelRoutesToTargetCamera(t *testing.T) {
	srv, err := NewServer(8582, "einfahrt", "aac", "", "")
	require.NoError(t, err)
	defer srv.Close()

	gartenStream, err := srv.RegisterStream("garten", "aac")
	require.NoError(t, err)
	require.NoError(t, srv.Start())

	var einfahrtCalls atomic.Int32
	gartenPkts := make(chan *rtp.Packet, 1)

	srv.PrimaryStream().SetAudioBackchannelHandler(func(_ *rtp.Packet) error {
		einfahrtCalls.Add(1)
		return nil
	})
	gartenStream.SetAudioBackchannelHandler(func(pkt *rtp.Packet) error {
		gartenPkts <- pkt
		return nil
	})

	transport := gortsplib.TransportTCP
	c := gortsplib.Client{
		Transport:           &transport,
		RequestBackChannels: true,
	}
	u, err := base.ParseURL("rtsp://127.0.0.1:8582/garten")
	require.NoError(t, err)
	require.NoError(t, c.Start(u.Scheme, u.Host))
	defer c.Close()

	desc, _, err := c.Describe(u)
	require.NoError(t, err)

	var bcMedia *description.Media
	for _, m := range desc.Medias {
		if m.IsBackChannel {
			bcMedia = m
			break
		}
	}
	require.NotNil(t, bcMedia)

	require.NoError(t, c.SetupAll(desc.BaseURL, desc.Medias))
	_, err = c.Play(nil)
	require.NoError(t, err)

	sent := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 42, Timestamp: 320},
		Payload: []byte{0xCA, 0xFE, 0xBA, 0xBE},
	}
	require.NoError(t, c.WritePacketRTP(bcMedia, sent))

	select {
	case got := <-gartenPkts:
		assert.Equal(t, uint16(42), got.SequenceNumber)
		assert.Equal(t, []byte{0xCA, 0xFE, 0xBA, 0xBE}, got.Payload)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for backchannel RTP packet on secondary stream '/garten'")
	}
	assert.Zero(t, einfahrtCalls.Load(), "primary stream '/einfahrt' must not receive '/garten' backchannel audio")
}

func TestBackchannel_TalkOnlySessionOnChannel0AndVideoPlusTalkOnChannel2(t *testing.T) {
	srv, err := NewServer(8583, "steinel", "aac", "", "")
	require.NoError(t, err)
	defer srv.Close()
	require.NoError(t, srv.Start())

	gotPkts := make(chan *rtp.Packet, 4)
	srv.SetAudioBackchannelHandler(func(pkt *rtp.Packet) error {
		gotPkts <- pkt
		return nil
	})

	// Case 1: Dedicated talk-only RTSP session (only backchannel track set up -> interleaved=0-1).
	transport := gortsplib.TransportTCP
	c1 := gortsplib.Client{
		Transport:           &transport,
		RequestBackChannels: true,
	}
	u, err := base.ParseURL("rtsp://127.0.0.1:8583/steinel")
	require.NoError(t, err)
	require.NoError(t, c1.Start(u.Scheme, u.Host))
	defer c1.Close()

	desc1, _, err := c1.Describe(u)
	require.NoError(t, err)

	var bcMedia1 *description.Media
	for _, m := range desc1.Medias {
		if m.IsBackChannel {
			bcMedia1 = m
			break
		}
	}
	require.NotNil(t, bcMedia1)
	_, err = c1.Setup(desc1.BaseURL, bcMedia1, 0, 0)
	require.NoError(t, err)
	_, err = c1.Play(nil)
	require.NoError(t, err)

	require.NoError(t, c1.WritePacketRTP(bcMedia1, &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 101, Timestamp: 160},
		Payload: []byte{0xAA, 0xBB},
	}))

	select {
	case got := <-gotPkts:
		assert.Equal(t, uint16(101), got.SequenceNumber)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for talk-only backchannel packet on interleaved channel 0")
	}
	c1.Close()

	// Case 2: Video (0-1) + Backchannel (2-3) without subscribing to camera microphone audio.
	c2 := gortsplib.Client{
		Transport:           &transport,
		RequestBackChannels: true,
	}
	require.NoError(t, c2.Start(u.Scheme, u.Host))
	defer c2.Close()

	desc2, _, err := c2.Describe(u)
	require.NoError(t, err)

	var videoMedia2, bcMedia2 *description.Media
	for _, m := range desc2.Medias {
		if m.Type == description.MediaTypeVideo {
			videoMedia2 = m
		} else if m.IsBackChannel {
			bcMedia2 = m
		}
	}
	require.NotNil(t, videoMedia2)
	require.NotNil(t, bcMedia2)

	_, err = c2.Setup(desc2.BaseURL, videoMedia2, 0, 0)
	require.NoError(t, err)
	_, err = c2.Setup(desc2.BaseURL, bcMedia2, 0, 0)
	require.NoError(t, err)
	_, err = c2.Play(nil)
	require.NoError(t, err)

	require.NoError(t, c2.WritePacketRTP(bcMedia2, &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 202, Timestamp: 320},
		Payload: []byte{0xCC, 0xDD},
	}))

	select {
	case got := <-gotPkts:
		assert.Equal(t, uint16(202), got.SequenceNumber)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for video+talk backchannel packet on interleaved channel 2")
	}
}

func TestInterceptingConn_DollarInASCIIHeadersPassesThroughIntact(t *testing.T) {
	srv, err := NewServer(8584, "test", "aac", "", "")
	require.NoError(t, err)
	defer srv.Close()

	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	ic := newInterceptingConn(serverConn, srv)

	reqWithDollar := []byte("DESCRIBE rtsp://127.0.0.1:8584/test?key=pa$sword$123 RTSP/1.0\r\nCSeq: 1\r\nUser-Agent: Test$Agent\r\n\r\n")
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 9, Timestamp: 160},
		Payload: []byte{0x01, 0x02, 0x03, 0x04},
	}
	bcFrame := buildInterleavedFrame(t, 4, pkt)

	var bcCount atomic.Int32
	srv.SetAudioBackchannelHandler(func(_ *rtp.Packet) error {
		bcCount.Add(1)
		return nil
	})

	go func() {
		_, _ = clientConn.Write(append(reqWithDollar, bcFrame...))
	}()

	out := make([]byte, 256)
	n, err := ic.Read(out)
	require.NoError(t, err)
	assert.Equal(t, reqWithDollar, out[:n], "'$' inside RTSP ASCII request must not be parsed as an interleaved binary frame")
	assert.Equal(t, int32(1), bcCount.Load(), "interleaved frame immediately after \\r\\n\\r\\n must still be intercepted")
}

func TestServer_OnSetupRejectsStaleDescribeAfterCodecSwitch(t *testing.T) {
	srv, err := NewServer(8585, "steinel", "aac", "", "")
	require.NoError(t, err)
	defer srv.Close()
	require.NoError(t, srv.Start())

	transport := gortsplib.TransportTCP
	c := gortsplib.Client{Transport: &transport}
	u, err := base.ParseURL("rtsp://127.0.0.1:8585/steinel")
	require.NoError(t, err)
	require.NoError(t, c.Start(u.Scheme, u.Host))
	defer c.Close()

	descH264, _, err := c.Describe(u)
	require.NoError(t, err)
	require.NotEmpty(t, descH264.Medias)

	// Switch stream to H.265 between DESCRIBE and SETUP.
	require.True(t, srv.SetVideoCodec(VideoCodecH265))

	_, err = c.Setup(descH264.BaseURL, descH264.Medias[0], 0, 0)
	require.Error(t, err, "SETUP using stale H.264 DESCRIBE after H.265 switch must be rejected so client re-describes")
}

func TestServer_UDPBackchannel_DropsUnauthenticatedAndIsolatesPayload(t *testing.T) {
	// 1. Auth-enabled server with zero authenticated RTSP sessions must drop raw UDP packets.
	authSrv, err := NewServer(18587, "steinel", "aac", "admin", "secret123")
	require.NoError(t, err)
	defer authSrv.Close()

	var authCount atomic.Int32
	authSrv.SetAudioBackchannelHandler(func(_ *rtp.Packet) error {
		authCount.Add(1)
		return nil
	})
	require.NoError(t, authSrv.Start())

	rawPkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 160, SSRC: 42},
		Payload: []byte{0xAA, 0xBB, 0xCC, 0xDD},
	}
	marshaled, err := rawPkt.Marshal()
	require.NoError(t, err)

	udpConn, err := net.Dial("udp", "127.0.0.1:18587")
	require.NoError(t, err)
	defer func() { _ = udpConn.Close() }()

	_, err = udpConn.Write(marshaled)
	require.NoError(t, err)
	time.Sleep(60 * time.Millisecond)
	assert.Zero(t, authCount.Load(), "unauthenticated UDP packet must be ignored when authUser is configured and no session is active")

	// 2. Public server must clone UDP payload so subsequent packets never overwrite retained slices.
	pubSrv, err := NewServer(18588, "steinel", "aac", "", "")
	require.NoError(t, err)
	defer pubSrv.Close()

	firstPayloadCh := make(chan []byte, 1)
	pubSrv.SetAudioBackchannelHandler(func(pkt *rtp.Packet) error {
		select {
		case firstPayloadCh <- pkt.Payload:
		default:
		}
		return nil
	})
	require.NoError(t, pubSrv.Start())

	pubUDP, err := net.Dial("udp", "127.0.0.1:18588")
	require.NoError(t, err)
	defer func() { _ = pubUDP.Close() }()

	_, err = pubUDP.Write(marshaled)
	require.NoError(t, err)

	var firstPayload []byte
	select {
	case firstPayload = <-firstPayloadCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for public UDP backchannel packet")
	}

	rawPkt.SequenceNumber = 2
	rawPkt.Payload = []byte{0x11, 0x22, 0x33, 0x44}
	marshaled2, err := rawPkt.Marshal()
	require.NoError(t, err)
	_, err = pubUDP.Write(marshaled2)
	require.NoError(t, err)
	time.Sleep(40 * time.Millisecond)

	assert.Equal(t, []byte{0xAA, 0xBB, 0xCC, 0xDD}, firstPayload, "subsequent UDP reads must not mutate previously delivered RTP payload slice")
}

// TestScryptedOnvifIntercom_UDP461FallbackToTCPChannel0 reproduces the exact raw RTSP wire
// sequence sent by Scrypted's @scrypted/onvif OnvifIntercom + RtspClient:
//  1. OPTIONS
//  2. DESCRIBE with Require: www.onvif.org/ver20/backchannel
//  3. SETUP on /steinel/trackID=2 with RTP/AVP/UDP -> server returns 461 Unsupported Transport
//  4. SETUP on /steinel/trackID=2 with RTP/AVP/TCP;unicast;interleaved=0-1 on the SAME TCP socket -> 200 OK
//  5. PLAY with Session header -> 200 OK
//  6. Split TCP writes: 4-byte '$' interleaved header (channel 0) + 972-byte jumbo PCMU RTP packet (960 samples)
//  7. TEARDOWN -> 200 OK and activeTalkSession lock released
func TestScryptedOnvifIntercom_UDP461FallbackToTCPChannel0(t *testing.T) {
	const rtspPort = 18589
	srv, err := NewServer(rtspPort, "steinel", "aac", "", "")
	require.NoError(t, err)
	defer srv.Close()

	gotPkts := make(chan *rtp.Packet, 4)
	srv.SetAudioBackchannelHandler(func(pkt *rtp.Packet) error {
		gotPkts <- pkt
		return nil
	})
	require.NoError(t, srv.Start())

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", rtspPort), 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)

	sendRTSP := func(req string) (statusLine string, headers map[string]string, body string) {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, wErr := conn.Write([]byte(req))
		require.NoError(t, wErr)

		line, rErr := br.ReadString('\n')
		require.NoError(t, rErr)
		statusLine = strings.TrimSpace(line)
		headers = make(map[string]string)
		contentLen := 0
		for {
			hLine, hErr := br.ReadString('\n')
			require.NoError(t, hErr)
			hLine = strings.TrimRight(hLine, "\r\n")
			if hLine == "" {
				break
			}
			if k, v, ok := strings.Cut(hLine, ":"); ok {
				key := strings.ToLower(strings.TrimSpace(k))
				val := strings.TrimSpace(v)
				headers[key] = val
				if key == "content-length" {
					_, _ = fmt.Sscanf(val, "%d", &contentLen)
				}
			}
		}
		if contentLen > 0 {
			buf := make([]byte, contentLen)
			_, bErr := io.ReadFull(br, buf)
			require.NoError(t, bErr)
			body = string(buf)
		}
		_ = conn.SetDeadline(time.Time{})
		return statusLine, headers, body
	}

	baseURL := fmt.Sprintf("rtsp://127.0.0.1:%d/steinel", rtspPort)

	// 1. OPTIONS
	status, _, _ := sendRTSP(fmt.Sprintf("OPTIONS %s RTSP/1.0\r\nCSeq: 1\r\nUser-Agent: Scrypted\r\n\r\n", baseURL))
	assert.Contains(t, status, "200")

	// 2. DESCRIBE with ONVIF Profile T backchannel header
	status, _, sdp := sendRTSP(fmt.Sprintf(
		"DESCRIBE %s RTSP/1.0\r\nCSeq: 2\r\nAccept: application/sdp\r\nRequire: www.onvif.org/ver20/backchannel\r\n\r\n",
		baseURL,
	))
	require.Contains(t, status, "200")
	assert.Contains(t, sdp, "a=sendonly", "SDP must advertise sendonly audio backchannel media")
	assert.Contains(t, sdp, "a=control:trackID=2", "backchannel track must be advertised as trackID=2 alongside video(0) and mic(1)")

	// 3. Scrypted tries UDP SETUP first -> gortsplib returns 461 Unsupported Transport
	status, _, _ = sendRTSP(fmt.Sprintf(
		"SETUP %s/trackID=2 RTSP/1.0\r\nCSeq: 3\r\nTransport: RTP/AVP/UDP;unicast;client_port=51234-51235\r\nRequire: www.onvif.org/ver20/backchannel\r\n\r\n",
		baseURL,
	))
	assert.Contains(t, status, "461")

	// 4. Scrypted falls back to TCP interleaved=0-1 on the exact same TCP socket
	status, setupHdrs, _ := sendRTSP(fmt.Sprintf(
		"SETUP %s/trackID=2 RTSP/1.0\r\nCSeq: 4\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\nRequire: www.onvif.org/ver20/backchannel\r\n\r\n",
		baseURL,
	))
	require.Contains(t, status, "200")
	sessionHeader := setupHdrs["session"]
	require.NotEmpty(t, sessionHeader)
	sessionID, _, _ := strings.Cut(sessionHeader, ";")
	sessionID = strings.TrimSpace(sessionID)

	// 5. Scrypted sends PLAY (not RECORD!) for ONVIF Profile T backchannel
	status, _, _ = sendRTSP(fmt.Sprintf(
		"PLAY %s RTSP/1.0\r\nCSeq: 5\r\nSession: %s\r\nRequire: www.onvif.org/ver20/backchannel\r\n\r\n",
		baseURL, sessionID,
	))
	require.Contains(t, status, "200")

	// 6. Scrypted RtspClient.writeRtp writes the 4-byte '$' header and the 972-byte jumbo PCMU RTP frame in two writes
	jumboPCMU := make([]byte, 960)
	for i := range jumboPCMU {
		jumboPCMU[i] = byte((i * 3) & 0xFF)
	}
	outPkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 501, Timestamp: 960, SSRC: 0xCAFE1234, Marker: true},
		Payload: jumboPCMU,
	}
	rawRTP, err := outPkt.Marshal()
	require.NoError(t, err)

	var dollarHdr [4]byte
	dollarHdr[0] = '$'
	dollarHdr[1] = 0 // channel 0!
	binary.BigEndian.PutUint16(dollarHdr[2:4], uint16(len(rawRTP)))

	_, err = conn.Write(dollarHdr[:])
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond) // Force split TCP reads inside interceptingConn.Read
	_, err = conn.Write(rawRTP)
	require.NoError(t, err)

	select {
	case got := <-gotPkts:
		assert.Equal(t, uint8(0), got.PayloadType)
		assert.Equal(t, uint16(501), got.SequenceNumber)
		assert.Equal(t, jumboPCMU, got.Payload)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Scrypted OnvifIntercom jumbo RTP packet on interleaved channel 0")
	}

	// 7. Scrypted sends TEARDOWN when talk stops -> session lock must be released so another talk session can start
	status, _, _ = sendRTSP(fmt.Sprintf(
		"TEARDOWN %s RTSP/1.0\r\nCSeq: 6\r\nSession: %s\r\n\r\n",
		baseURL, sessionID,
	))
	assert.Contains(t, status, "200")

	require.Eventually(t, func() bool {
		st := srv.PrimaryStream()
		st.backchannelMu.Lock()
		defer st.backchannelMu.Unlock()
		return st.activeTalkSession == nil
	}, time.Second, 10*time.Millisecond, "TEARDOWN must release activeTalkSession lock")
}
