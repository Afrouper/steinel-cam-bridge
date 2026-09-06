package nabtopure

import (
	"net"
	"testing"
	"time"
)

type noopPacketConn struct{}

func (n *noopPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if len(p) >= 32 {
		p[0] = nabtoPrefixConnection
		copy(p[1:15], []byte("0123456789abcd"))
		p[15] = 0x00
		copy(p[16:32], []byte("0123456789abcdef"))
		return 32, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5592}, nil
	}
	return 0, nil, nil
}

func (n *noopPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	return len(p), nil
}

func (n *noopPacketConn) Close() error                       { return nil }
func (n *noopPacketConn) LocalAddr() net.Addr                { return &net.UDPAddr{Port: 12345} }
func (n *noopPacketConn) SetDeadline(t time.Time) error      { return nil }
func (n *noopPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (n *noopPacketConn) SetWriteDeadline(t time.Time) error { return nil }

func BenchmarkNabtoPacketConnWriteTo(b *testing.B) {
	npc, err := newNabtoPacketConn(&noopPacketConn{})
	if err != nil {
		b.Fatal(err)
	}
	payload := make([]byte, 512)
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5592}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := npc.WriteTo(payload, addr)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNabtoPacketConnReadFrom(b *testing.B) {
	npc, err := newNabtoPacketConn(&noopPacketConn{})
	if err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, 512)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, err := npc.ReadFrom(buf)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildACKPacket(b *testing.B) {
	s := &Stream{
		streamID:  42,
		clientSeq: 100,
		serverSeq: 200,
		serverTs:  123456,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.buildACKPacket(nil)
	}
}

func BenchmarkBuildSYNPacket(b *testing.B) {
	s := &Stream{
		streamID:  42,
		clientSeq: 1,
		port:      5592,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.buildSYNPacket()
	}
}
