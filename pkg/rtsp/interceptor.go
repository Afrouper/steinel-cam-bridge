package rtsp

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/pion/rtp"
)

const (
	interleavedReadTimeout = 5 * time.Second
	maxASCIIHeaderScanBuf  = 8192
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

	readBuf [4096]byte
	rBuf    []byte
	mu      sync.Mutex

	// ASCII RTSP message framing state (single reader goroutine in Read).
	// Prevents '$' characters inside RTSP URLs, credentials, headers, or SDP bodies
	// from being misinterpreted as RFC 2326 interleaved binary RTP frames.
	inASCIIRequest bool
	asciiTail      [3]byte
	asciiTailLen   int
	asciiHeaderBuf []byte
	asciiBodyRem   int

	routeMu            sync.RWMutex
	described          *gortsplib.ServerStream
	boundStream        *Stream
	boundSession       *gortsplib.ServerSession
	usedEvenChans      uint64 // bit i tracks interleaved channel pair (2*i, 2*i+1)
	backchannelRTPChan int    // -1 until negotiated in SETUP
}

func newInterceptingConn(conn net.Conn, s *Server) *interceptingConn {
	return &interceptingConn{
		Conn:               conn,
		server:             s,
		backchannelRTPChan: -1,
	}
}

func (c *interceptingConn) setDescribedStream(ss *gortsplib.ServerStream) {
	c.routeMu.Lock()
	c.described = ss
	c.routeMu.Unlock()
}

func (c *interceptingConn) describedStream() *gortsplib.ServerStream {
	c.routeMu.RLock()
	ss := c.described
	c.routeMu.RUnlock()
	return ss
}

func (c *interceptingConn) bindSession(st *Stream, sess *gortsplib.ServerSession) {
	c.routeMu.Lock()
	if st != nil {
		c.boundStream = st
	}
	if sess != nil && sess != c.boundSession {
		c.boundSession = sess
		c.backchannelRTPChan = -1
		c.usedEvenChans = 0
	}
	c.routeMu.Unlock()
}

// recordTrackSetup records a TCP interleaved channel pair negotiated during SETUP
// (either explicitly requested in the Transport header or auto-assigned sequentially
// by gortsplib) and marks it as the backchannel RTP channel when isBackchannel is true.
func (c *interceptingConn) recordTrackSetup(transportHeaders []string, isBackchannel bool) {
	ch := parseInterleavedRTPChannel(transportHeaders)

	c.routeMu.Lock()
	defer c.routeMu.Unlock()

	if ch < 0 {
		for pair := 0; pair < 64; pair++ {
			if c.usedEvenChans&(1<<uint(pair)) == 0 {
				ch = pair * 2
				break
			}
		}
	}
	if ch < 0 || ch > 254 {
		return
	}
	if pair := ch / 2; pair < 64 {
		c.usedEvenChans |= 1 << uint(pair)
	}
	if isBackchannel {
		c.backchannelRTPChan = ch
	}
}

func (c *interceptingConn) targetBackchannel(channel byte) (ok bool, st *Stream, sess *gortsplib.ServerSession) {
	c.routeMu.RLock()
	bcChan := c.backchannelRTPChan
	used := c.usedEvenChans
	st = c.boundStream
	sess = c.boundSession
	c.routeMu.RUnlock()

	if bcChan >= 0 {
		// Matches the exact TCP interleaved channel negotiated in SETUP:
		// e.g. channel 0 when Scrypted (@scrypted/onvif OnvifIntercom) opens a dedicated
		// talk-only session after falling back from UDP 461 to TCP interleaved=0-1,
		// channel 2 for video+talk, or channel 4 for combined video+audio+talk sessions.
		return int(channel) == bcChan, st, sess
	}
	if used != 0 {
		// Tracks were explicitly set up on this session without a backchannel track.
		return false, st, sess
	}
	// Fallback when no SETUP metadata was recorded (e.g. raw pipe tests):
	// video occupies 0-1 and camera audio occupies 2-3, so backchannel RTP starts at >= 4 (even).
	return channel >= 4 && channel%2 == 0, st, sess
}

func parseLeadingTrackDigits(s string) (int, bool) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	id, err := strconv.Atoi(s[:end])
	return id, err == nil
}

// extractSetupTrackID extracts the numeric trackID from an RTSP SETUP request URL
// using the same "/trackID=" suffix rules as gortsplib before it strips the suffix
// from ServerHandlerOnSetupCtx.Path and Query.
func extractSetupTrackID(u *base.URL) (int, bool) {
	if u == nil {
		return 0, false
	}
	if i := strings.LastIndex(u.RawQuery, "/trackID="); i >= 0 {
		return parseLeadingTrackDigits(u.RawQuery[i+len("/trackID="):])
	}
	if i := strings.LastIndex(u.Path, "/trackID="); i >= 0 {
		return parseLeadingTrackDigits(u.Path[i+len("/trackID="):])
	}
	if strings.HasSuffix(u.RawQuery, "/") || (len(u.Path) > 1 && strings.HasSuffix(u.Path, "/")) {
		return 0, true
	}
	return 0, false
}

// parseInterleavedRTPChannel extracts the even RTP channel number from an RTSP Transport header
// value such as "RTP/AVP/TCP;unicast;interleaved=4-5". Returns -1 if not present.
func parseInterleavedRTPChannel(headers []string) int {
	const prefix = "interleaved="
	for _, h := range headers {
		for part := range strings.SplitSeq(h, ";") {
			part = strings.TrimSpace(part)
			if len(part) <= len(prefix) || !strings.EqualFold(part[:len(prefix)], prefix) {
				continue
			}
			val := part[len(prefix):]
			if dash := strings.IndexByte(val, '-'); dash > 0 {
				val = val[:dash]
			}
			if ch, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && ch >= 0 && ch <= 254 {
				return ch
			}
		}
	}
	return -1
}

var contentLengthPrefix = []byte("content-length:")

func parseRTSPContentLength(hdrBytes []byte) int {
	for line := range bytes.SplitSeq(hdrBytes, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) <= len(contentLengthPrefix) || !bytes.EqualFold(line[:len(contentLengthPrefix)], contentLengthPrefix) {
			continue
		}
		val := bytes.TrimSpace(line[len(contentLengthPrefix):])
		if len(val) == 0 || len(val) > 9 {
			continue
		}
		n := 0
		valid := true
		for _, ch := range val {
			if ch < '0' || ch > '9' {
				valid = false
				break
			}
			n = n*10 + int(ch-'0')
		}
		if valid && n > 0 {
			return n
		}
	}
	return 0
}

func (c *interceptingConn) readFullBounded(buf []byte) error {
	_ = c.SetReadDeadline(time.Now().Add(interleavedReadTimeout))
	_, err := io.ReadFull(c.Conn, buf)
	_ = c.SetReadDeadline(time.Time{})
	return err
}

func (c *interceptingConn) consumeASCIIChunk(data []byte) []byte {
	for i, b := range data {
		if len(c.asciiHeaderBuf) < maxASCIIHeaderScanBuf {
			c.asciiHeaderBuf = append(c.asciiHeaderBuf, b)
		}
		isEnd := c.asciiTailLen == 3 &&
			c.asciiTail[0] == '\r' && c.asciiTail[1] == '\n' && c.asciiTail[2] == '\r' && b == '\n'
		if c.asciiTailLen < 3 {
			c.asciiTail[c.asciiTailLen] = b
			c.asciiTailLen++
		} else {
			c.asciiTail[0], c.asciiTail[1], c.asciiTail[2] = c.asciiTail[1], c.asciiTail[2], b
		}
		if isEnd {
			c.inASCIIRequest = false
			c.asciiTailLen = 0
			c.asciiBodyRem = parseRTSPContentLength(c.asciiHeaderBuf)
			c.asciiHeaderBuf = c.asciiHeaderBuf[:0]
			c.mu.Lock()
			c.rBuf = append(c.rBuf, data[:i+1]...)
			c.mu.Unlock()
			return data[i+1:]
		}
	}

	c.mu.Lock()
	c.rBuf = append(c.rBuf, data...)
	c.mu.Unlock()
	return nil
}

func (c *interceptingConn) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.rBuf) > 0 {
			n := copy(p, c.rBuf)
			c.rBuf = c.rBuf[n:]
			if len(c.rBuf) == 0 {
				c.rBuf = c.rBuf[:0]
			}
			c.mu.Unlock()
			return n, nil
		}
		c.mu.Unlock()

		// Read from underlying TCP socket into reusable preallocated buffer
		n, err := c.Conn.Read(c.readBuf[:])
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}

		data := c.readBuf[:n]

		// Process incoming stream: extract interleaved backchannel RTP frames at RTSP message boundaries.
		for len(data) > 0 {
			if c.asciiBodyRem > 0 {
				take := min(len(data), c.asciiBodyRem)
				c.mu.Lock()
				c.rBuf = append(c.rBuf, data[:take]...)
				c.mu.Unlock()
				c.asciiBodyRem -= take
				data = data[take:]
				continue
			}

			if c.inASCIIRequest {
				data = c.consumeASCIIChunk(data)
				continue
			}

			if data[0] == '$' {
				var hdr [4]byte
				copied := copy(hdr[:], data)
				data = data[copied:]
				if copied < 4 {
					if err := c.readFullBounded(hdr[copied:]); err != nil {
						return 0, err
					}
				}

				channel := hdr[1]
				frameLen := int(binary.BigEndian.Uint16(hdr[2:4]))

				var (
					payload      []byte
					payloadOwned bool
				)
				if len(data) >= frameLen {
					payload = data[:frameLen]
					data = data[frameLen:]
				} else {
					payload = make([]byte, frameLen)
					payloadOwned = true
					have := copy(payload, data)
					data = nil
					if err := c.readFullBounded(payload[have:]); err != nil {
						return 0, err
					}
				}

				if isBC, targetStream, targetSess := c.targetBackchannel(channel); isBC {
					// Intercepted Backchannel RTP packet (e.g. from Apple HomeKit / Scrypted OnvifIntercom).
					if payloadOwned {
						pkt := &rtp.Packet{}
						if err := pkt.Unmarshal(payload); err == nil {
							if targetStream != nil {
								targetStream.handleBackchannelPacket(targetStream.backchannelMedia, pkt, "TCP/Interleaved", targetSess)
							} else {
								c.server.handleBackchannelPacket(c.server.backchannelMedia, pkt, "TCP/Interleaved", targetSess)
							}
							continue
						}
					} else {
						var pkt rtp.Packet
						if err := pkt.Unmarshal(payload); err == nil {
							if targetStream != nil {
								targetStream.handleBackchannelPacket(targetStream.backchannelMedia, pkt.Clone(), "TCP/Interleaved", targetSess)
							} else {
								c.server.handleBackchannelPacket(c.server.backchannelMedia, pkt.Clone(), "TCP/Interleaved", targetSess)
							}
							continue // Consume frame, do not pass to gortsplib
						}
					}
				}

				// Pass non-backchannel interleaved frame (e.g. RTCP RR or ANNOUNCE media) to gortsplib.
				c.mu.Lock()
				c.rBuf = append(c.rBuf, hdr[:]...)
				c.rBuf = append(c.rBuf, payload...)
				c.mu.Unlock()
			} else {
				c.inASCIIRequest = true
				c.asciiTailLen = 0
				c.asciiHeaderBuf = c.asciiHeaderBuf[:0]
				data = c.consumeASCIIChunk(data)
			}
		}
	}
}
