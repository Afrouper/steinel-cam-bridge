package nabtopure

import (
	"crypto/rand"
	"fmt"
	"net"
	"time"
)

const (
	nabtoPrefixConnection = 0xF0 // 240
	nabtoHeaderSize       = 16
)

// nabtoPacketConn wraps a UDP PacketConn to add/strip the 16-byte Nabto Connection Header.
type nabtoPacketConn struct {
	conn   net.PacketConn
	connID [14]byte
}

func newNabtoPacketConn(conn net.PacketConn) (*nabtoPacketConn, error) {
	npc := &nabtoPacketConn{
		conn: conn,
	}
	if _, err := rand.Read(npc.connID[:]); err != nil {
		return nil, fmt.Errorf("failed to generate random connection ID: %w", err)
	}
	return npc, nil
}

func newNabtoServerPacketConn(conn net.PacketConn) *nabtoPacketConn {
	return &nabtoPacketConn{
		conn: conn,
	}
}

func (c *nabtoPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	buf := make([]byte, len(p)+nabtoHeaderSize+64)
	for {
		nRaw, rAddr, err := c.conn.ReadFrom(buf)
		if err != nil {
			return 0, rAddr, err
		}

		if nRaw < nabtoHeaderSize || buf[0] != nabtoPrefixConnection {
			// Skip undersized or non-Nabto connection packets and continue reading
			continue
		}

		if c.connID == [14]byte{} {
			copy(c.connID[:], buf[1:15])
		}

		// Strip 16-byte header and return payload
		payloadLen := nRaw - nabtoHeaderSize
		if payloadLen > len(p) {
			payloadLen = len(p)
		}
		copy(p, buf[nabtoHeaderSize:nabtoHeaderSize+payloadLen])
		return payloadLen, rAddr, nil
	}
}

func (c *nabtoPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	pkt := make([]byte, nabtoHeaderSize+len(p))
	pkt[0] = nabtoPrefixConnection
	copy(pkt[1:15], c.connID[:])
	pkt[15] = 0x00 // Channel ID 0

	copy(pkt[nabtoHeaderSize:], p)

	_, err = c.conn.WriteTo(pkt, addr)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *nabtoPacketConn) Close() error {
	return c.conn.Close()
}

func (c *nabtoPacketConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *nabtoPacketConn) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

func (c *nabtoPacketConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *nabtoPacketConn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}
