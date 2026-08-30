package nabtopure

import (
	"bytes"
	"net"
	"testing"
)

func TestNabtoPacketConnFraming(t *testing.T) {
	conn1, conn2 := net.Pipe()
	defer func() { _ = conn1.Close() }()
	defer func() { _ = conn2.Close() }()

	// Wrap conn1 with client nabtoPacketConn
	// Note: net.Pipe implements net.Conn, we test framing logic directly
	npc, err := newNabtoPacketConn(&pipePacketConnAdapter{conn1})
	if err != nil {
		t.Fatalf("newNabtoPacketConn failed: %v", err)
	}

	payload := []byte("DTLS_CLIENT_HELLO_PAYLOAD")
	targetAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5592}

	go func() {
		_, err := npc.WriteTo(payload, targetAddr)
		if err != nil {
			t.Errorf("WriteTo failed: %v", err)
		}
	}()

	rawBuf := make([]byte, 1024)
	n, err := conn2.Read(rawBuf)
	if err != nil {
		t.Fatalf("conn2.Read failed: %v", err)
	}

	if n != len(payload)+16 {
		t.Fatalf("expected packet length %d (16 header + %d payload), got %d", len(payload)+16, len(payload), n)
	}

	if rawBuf[0] != 0xF0 {
		t.Fatalf("expected header byte 0 to be 0xF0 (240), got 0x%02x", rawBuf[0])
	}
	if rawBuf[15] != 0x00 {
		t.Fatalf("expected channel ID byte 15 to be 0x00, got 0x%02x", rawBuf[15])
	}
	if !bytes.Equal(rawBuf[16:n], payload) {
		t.Fatalf("payload mismatch: %s vs %s", string(rawBuf[16:n]), string(payload))
	}
}

type pipePacketConnAdapter struct {
	net.Conn
}

func (p *pipePacketConnAdapter) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := p.Read(b)
	return n, p.RemoteAddr(), err
}

func (p *pipePacketConnAdapter) WriteTo(b []byte, addr net.Addr) (int, error) {
	return p.Write(b)
}
