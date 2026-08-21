package rtsp

import (
	"testing"

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
