package rtsp

import (
	"encoding/binary"
	"io"
	"net"
	"sync"

	"github.com/pion/rtp"
)

type interceptingListener struct {
	net.Listener
	server *Server
}

func (l *interceptingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newInterceptingConn(conn, l.server), nil
}

type interceptingConn struct {
	net.Conn
	server *Server

	rBuf []byte
	mu   sync.Mutex
}

func newInterceptingConn(conn net.Conn, s *Server) *interceptingConn {
	return &interceptingConn{
		Conn:   conn,
		server: s,
	}
}

func (c *interceptingConn) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.rBuf) > 0 {
			n := copy(p, c.rBuf)
			c.rBuf = c.rBuf[n:]
			c.mu.Unlock()
			return n, nil
		}
		c.mu.Unlock()

		// Read from underlying TCP socket
		tmp := make([]byte, 4096)
		n, err := c.Conn.Read(tmp)
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}

		data := tmp[:n]

		// Process incoming stream: extract interleaved backchannel RTP frames
		for len(data) > 0 {
			if data[0] == '$' {
				if len(data) < 4 {
					// Incomplete header, read more
					more := make([]byte, 4-len(data))
					if _, err := io.ReadFull(c.Conn, more); err != nil {
						return 0, err
					}
					data = append(data, more...)
				}

				channel := data[1]
				frameLen := int(binary.BigEndian.Uint16(data[2:4]))

				if len(data) < 4+frameLen {
					// Incomplete payload, read remaining
					more := make([]byte, (4+frameLen)-len(data))
					if _, err := io.ReadFull(c.Conn, more); err != nil {
						return 0, err
					}
					data = append(data, more...)
				}

				payload := data[4 : 4+frameLen]
				data = data[4+frameLen:]

				// Backchannel channels are typically >= 4 and even (e.g. 4 for RTP, 5 for RTCP)
				if channel%2 == 0 {
					var pkt rtp.Packet
					if err := pkt.Unmarshal(payload); err == nil {
						// Intercepted Backchannel RTP packet from Apple Home / Scrypted
						c.server.handleBackchannelPacket(c.server.backchannelMedia, &pkt, "TCP/Interleaved")
						continue // Consume frame, do not pass to gortsplib
					}
				}

				// If not backchannel RTP, pass frame to gortsplib
				c.mu.Lock()
				frameHeader := []byte{'$', channel, byte(frameLen >> 8), byte(frameLen)}
				c.rBuf = append(c.rBuf, append(frameHeader, payload...)...)
				c.mu.Unlock()
			} else {
				// RTSP ASCII command/response: find next '$' or take all
				nextDollar := -1
				for i := 0; i < len(data); i++ {
					if data[i] == '$' {
						nextDollar = i
						break
					}
				}

				if nextDollar != -1 {
					chunk := data[:nextDollar]
					data = data[nextDollar:]
					c.mu.Lock()
					c.rBuf = append(c.rBuf, chunk...)
					c.mu.Unlock()
				} else {
					c.mu.Lock()
					c.rBuf = append(c.rBuf, data...)
					c.mu.Unlock()
					data = nil
				}
			}
		}
	}
}
