package rtsp

import (
	"net"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
)

func TestServerStartClose(t *testing.T) {
	// 1. Test AAC Server
	srvAAC, err := NewServer(8556, "test", "aac", "", "")
	if err != nil {
		t.Fatalf("Failed to create AAC server: %v", err)
	}
	defer srvAAC.Close()

	if err := srvAAC.Start(); err != nil {
		t.Fatalf("Failed to start AAC server: %v", err)
	}
	srvAAC.Close()

	// 2. Test PCMU Server
	srvPCMU, err := NewServer(8558, "test", "pcmu", "", "")
	if err != nil {
		t.Fatalf("Failed to create PCMU server: %v", err)
	}
	defer srvPCMU.Close()

	if err := srvPCMU.Start(); err != nil {
		t.Fatalf("Failed to start PCMU server: %v", err)
	}
}

func TestCheckPath(t *testing.T) {
	srv, err := NewServer(8559, "steinel", "aac", "", "")
	assert.NoError(t, err)
	defer srv.Close()

	// Should match root / empty URL
	assert.True(t, srv.checkPath(""))
	assert.True(t, srv.checkPath("/"))

	// Should match canonical and standard alias paths
	assert.True(t, srv.checkPath("steinel"))
	assert.True(t, srv.checkPath("/steinel"))
	assert.True(t, srv.checkPath("live"))
	assert.True(t, srv.checkPath("/live"))
	assert.True(t, srv.checkPath("cam/realmonitor"))
	assert.True(t, srv.checkPath("/cam/realmonitor?channel=1&subtype=0"))
	assert.True(t, srv.checkPath("steinel/main"))
	assert.True(t, srv.checkPath("live/sub"))

	// Should reject unrelated paths
	assert.False(t, srv.checkPath("unknown_stream"))
	assert.False(t, srv.checkPath("/other/path"))
}

func TestServerBackchannelPacketHandling(t *testing.T) {
	srv, err := NewServer(8560, "test", "pcmu", "", "")
	assert.NoError(t, err)
	defer srv.Close()

	var receivedPkt *rtp.Packet
	srv.SetAudioBackchannelHandler(func(pkt *rtp.Packet) error {
		receivedPkt = pkt
		return nil
	})

	testPkt := &rtp.Packet{
		Header: rtp.Header{
			PayloadType:    0,
			SequenceNumber: 1234,
			Timestamp:      5678,
		},
		Payload: []byte{0x01, 0x02, 0x03, 0x04},
	}

	// Trigger backchannel packet handling
	srv.handleBackchannelPacket(srv.backchannelMedia, testPkt, "TCP/Interleaved", nil)

	assert.NotNil(t, receivedPkt)
	assert.Equal(t, uint16(1234), receivedPkt.SequenceNumber)
	assert.Equal(t, uint32(5678), receivedPkt.Timestamp)
	assert.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, receivedPkt.Payload)
}

func TestClientConnectSetupPlay(t *testing.T) {
	srv, err := NewServer(8562, "test", "aac", "", "")
	assert.NoError(t, err)
	defer srv.Close()

	err = srv.Start()
	assert.NoError(t, err)

	// Connect as real RTSP client
	c := gortsplib.Client{}
	u, err := base.ParseURL("rtsp://127.0.0.1:8562/test")
	assert.NoError(t, err)

	err = c.Start(u.Scheme, u.Host)
	assert.NoError(t, err)
	defer c.Close()

	desc, _, err := c.Describe(u)
	assert.NoError(t, err)
	assert.NotNil(t, desc)

	// Setup media
	err = c.SetupAll(desc.BaseURL, desc.Medias)
	assert.NoError(t, err)

	// Play stream
	_, err = c.Play(nil)
	assert.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
}

func TestClientTCPInterleavedBackchannel(t *testing.T) {
	srv, err := NewServer(8564, "test", "aac", "", "")
	assert.NoError(t, err)
	defer srv.Close()

	var receivedPkt *rtp.Packet
	pktChan := make(chan *rtp.Packet, 1)
	srv.SetAudioBackchannelHandler(func(pkt *rtp.Packet) error {
		pktChan <- pkt
		return nil
	})

	err = srv.Start()
	assert.NoError(t, err)

	// Connect as TCP RTSP client requesting backchannels
	transport := gortsplib.TransportTCP
	c := gortsplib.Client{
		Transport:           &transport,
		RequestBackChannels: true,
	}
	u, err := base.ParseURL("rtsp://127.0.0.1:8564/test")
	assert.NoError(t, err)

	err = c.Start(u.Scheme, u.Host)
	assert.NoError(t, err)
	defer c.Close()

	desc, _, err := c.Describe(u)
	assert.NoError(t, err)
	assert.NotNil(t, desc)

	var bcMedia *description.Media
	for _, m := range desc.Medias {
		if m.IsBackChannel {
			bcMedia = m
			break
		}
	}
	assert.NotNil(t, bcMedia)

	// Setup all medias including backchannel
	err = c.SetupAll(desc.BaseURL, desc.Medias)
	assert.NoError(t, err)

	// Play
	_, err = c.Play(nil)
	assert.NoError(t, err)

	// Send an RTP packet on backchannel track
	testPkt := &rtp.Packet{
		Header: rtp.Header{
			PayloadType:    0,
			SequenceNumber: 9999,
			Timestamp:      88888,
		},
		Payload: []byte{0xDE, 0xAD, 0xBE, 0xEF},
	}

	err = c.WritePacketRTP(bcMedia, testPkt)
	assert.NoError(t, err)

	select {
	case receivedPkt = <-pktChan:
	case <-time.After(500 * time.Millisecond):
	}

	assert.NotNil(t, receivedPkt, "Backchannel packet should be received by server")
	if receivedPkt != nil {
		assert.Equal(t, uint16(9999), receivedPkt.SequenceNumber)
		assert.Equal(t, uint32(88888), receivedPkt.Timestamp)
		assert.Equal(t, []byte{0xDE, 0xAD, 0xBE, 0xEF}, receivedPkt.Payload)
	}
}

func BenchmarkInterceptingConnRead(b *testing.B) {
	srv, _ := NewServer(8566, "bench", "aac", "", "")
	defer srv.Close()

	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	iconn := newInterceptingConn(serverConn, srv)

	go func() {
		data := []byte("OPTIONS rtsp://localhost:8566 RTSP/1.0\r\nCSeq: 1\r\n\r\n")
		for {
			_, err := clientConn.Write(data)
			if err != nil {
				return
			}
		}
	}()

	p := make([]byte, 1024)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = iconn.Read(p)
	}
}

func TestServerAuthentication(t *testing.T) {
	srv, err := NewServer(8570, "authstream", "aac", "myuser", "mypass")
	assert.NoError(t, err)
	defer srv.Close()

	err = srv.Start()
	assert.NoError(t, err)

	// 1. Unauthenticated client request should fail
	cNoAuth := gortsplib.Client{}
	uNoAuth, err := base.ParseURL("rtsp://127.0.0.1:8570/authstream")
	assert.NoError(t, err)

	err = cNoAuth.Start(uNoAuth.Scheme, uNoAuth.Host)
	assert.NoError(t, err)
	defer cNoAuth.Close()

	_, _, err = cNoAuth.Describe(uNoAuth)
	assert.Error(t, err, "Unauthenticated request must fail")

	// 2. Client with wrong credentials should fail
	cWrong := gortsplib.Client{}
	uWrong, err := base.ParseURL("rtsp://myuser:wrongpass@127.0.0.1:8570/authstream")
	assert.NoError(t, err)

	err = cWrong.Start(uWrong.Scheme, uWrong.Host)
	assert.NoError(t, err)
	defer cWrong.Close()

	_, _, err = cWrong.Describe(uWrong)
	assert.Error(t, err, "Request with invalid credentials must fail")

	// 3. Client with valid credentials should succeed
	cValid := gortsplib.Client{}
	uValid, err := base.ParseURL("rtsp://myuser:mypass@127.0.0.1:8570/authstream")
	assert.NoError(t, err)

	err = cValid.Start(uValid.Scheme, uValid.Host)
	assert.NoError(t, err)
	defer cValid.Close()

	desc, _, err := cValid.Describe(uValid)
	assert.NoError(t, err, "Request with valid credentials must succeed")
	assert.NotNil(t, desc)

	err = cValid.SetupAll(desc.BaseURL, desc.Medias)
	assert.NoError(t, err)

	_, err = cValid.Play(nil)
	assert.NoError(t, err)
}

func TestServer_SingleTalkerLock(t *testing.T) {
	srv, err := NewServer(8571, "talktest", "pcmu", "", "")
	assert.NoError(t, err)
	defer srv.Close()

	var receivedCount int
	srv.SetAudioBackchannelHandler(func(pkt *rtp.Packet) error {
		receivedCount++
		return nil
	})

	sess1 := &gortsplib.ServerSession{}
	sess2 := &gortsplib.ServerSession{}

	pkt := &rtp.Packet{
		Header:  rtp.Header{PayloadType: 0},
		Payload: []byte{0x01},
	}

	// Session 1 acquires talk lock
	srv.handleBackchannelPacket(srv.backchannelMedia, pkt, "sess1", sess1)
	assert.Equal(t, 1, receivedCount)

	// Session 2 attempts to talk while session 1 is active -> must be dropped
	srv.handleBackchannelPacket(srv.backchannelMedia, pkt, "sess2", sess2)
	assert.Equal(t, 1, receivedCount, "Second session packet must be dropped while first session holds lock")

	// Session 1 closes
	srv.OnSessionClose(&gortsplib.ServerHandlerOnSessionCloseCtx{Session: sess1})

	// Now session 2 can talk
	srv.handleBackchannelPacket(srv.backchannelMedia, pkt, "sess2", sess2)
	assert.Equal(t, 2, receivedCount, "Session 2 should acquire lock after Session 1 closes")
}

func TestServer_ActiveClientsOnlyOnPlay(t *testing.T) {
	srv, err := NewServer(8572, "clienttest", "pcmu", "", "")
	assert.NoError(t, err)
	defer srv.Close()

	sessProbe := &gortsplib.ServerSession{}
	sessReal := &gortsplib.ServerSession{}

	// sessProbe connects, but only does setup (no play)
	// closing sessProbe should NOT decrement activeClients
	srv.OnSessionClose(&gortsplib.ServerHandlerOnSessionCloseCtx{Session: sessProbe})
	assert.Equal(t, int64(0), srv.ActiveClients())

	// sessReal plays
	_, err = srv.OnPlay(&gortsplib.ServerHandlerOnPlayCtx{
		Session: sessReal,
		Request: &base.Request{},
		Path:    "clienttest",
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(1), srv.ActiveClients())

	// sessProbe closes again -> activeClients must still be 1!
	srv.OnSessionClose(&gortsplib.ServerHandlerOnSessionCloseCtx{Session: sessProbe})
	assert.Equal(t, int64(1), srv.ActiveClients(), "Closing non-playing session must not decrease activeClients")

	// sessReal closes -> activeClients goes to 0
	srv.OnSessionClose(&gortsplib.ServerHandlerOnSessionCloseCtx{Session: sessReal})
	assert.Equal(t, int64(0), srv.ActiveClients())
}

func TestServer_AuthInheritance(t *testing.T) {
	srv, err := NewServer(8573, "authtest", "pcmu", "user", "pass")
	assert.NoError(t, err)
	defer srv.Close()

	sess := &gortsplib.ServerSession{}
	conn := &gortsplib.ServerConn{}

	// Authenticate session
	sess.SetUserData(true)

	// New connection without Authorization header should inherit session auth
	req := &base.Request{
		Method: base.Setup,
		Header: base.Header{},
	}
	ok, resp := srv.validateAuth(req, conn, sess)
	assert.True(t, ok)
	assert.Nil(t, resp)
	assert.Equal(t, true, conn.UserData(), "Connection should have inherited authentication from session")
}
