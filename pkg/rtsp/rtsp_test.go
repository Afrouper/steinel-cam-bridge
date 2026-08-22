package rtsp

import (
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
	srvAAC, err := NewServer(8556, "test", "aac")
	if err != nil {
		t.Fatalf("Failed to create AAC server: %v", err)
	}
	defer srvAAC.Close()

	if err := srvAAC.Start(); err != nil {
		t.Fatalf("Failed to start AAC server: %v", err)
	}
	srvAAC.Close()

	// 2. Test PCMU Server
	srvPCMU, err := NewServer(8558, "test", "pcmu")
	if err != nil {
		t.Fatalf("Failed to create PCMU server: %v", err)
	}
	defer srvPCMU.Close()

	if err := srvPCMU.Start(); err != nil {
		t.Fatalf("Failed to start PCMU server: %v", err)
	}
}

func TestServerBackchannelPacketHandling(t *testing.T) {
	srv, err := NewServer(8560, "test", "pcmu")
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
	srv.handleBackchannelPacket(srv.backchannelMedia, testPkt, "TCP/Interleaved")

	assert.NotNil(t, receivedPkt)
	assert.Equal(t, uint16(1234), receivedPkt.SequenceNumber)
	assert.Equal(t, uint32(5678), receivedPkt.Timestamp)
	assert.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, receivedPkt.Payload)
}

func TestClientConnectSetupPlay(t *testing.T) {
	srv, err := NewServer(8562, "test", "aac")
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
	srv, err := NewServer(8564, "test", "aac")
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
