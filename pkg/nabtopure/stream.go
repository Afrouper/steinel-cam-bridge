package nabtopure

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/nabto"
)

// Stream protocol constants
const (
	StreamAppDataType = 0x05

	StreamFlagEmpty = 0x00
	StreamFlagACK   = 0x10
	StreamFlagRST   = 0x20
	StreamFlagFIN   = 0x40
	StreamFlagSYN   = 0x80

	ExtSegmentSizes  = 0x1000
	ExtACK           = 0x1001
	ExtDATA          = 0x1002
	ExtContentType   = 0x1003
	ExtFIN           = 0x1004
	ExtSYN           = 0x1006
	ExtNonceCap      = 0x1009
	ExtNonce         = 0x1007
	ExtNonceResponse = 0x1008
)

// Stream implements nabto.StreamDriver for WebRTC signaling over DTLS.
type Stream struct {
	client             *Client
	streamID           uint64
	port               uint32
	clientSeq          uint32
	serverSeq          uint32
	serverTs           uint32
	maxSendSegmentSize uint16
	nonce              [8]byte
	sendNonce          bool
	established        bool
	closed             bool
	closeChan          chan struct{}
	readBuf            *bytes.Buffer
	incomingChan       chan []byte
	mu                 sync.Mutex
}

var _ nabto.StreamDriver = (*Stream)(nil)

// NewStream initiates a stream on the specified port.
func NewStream(client *Client, streamID uint64, port uint32) *Stream {
	return &Stream{
		client:             client,
		streamID:           streamID,
		port:               port,
		clientSeq:          1,
		maxSendSegmentSize: 256,
		closeChan:          make(chan struct{}),
		readBuf:            new(bytes.Buffer),
		incomingChan:       make(chan []byte, 100),
	}
}

// HandleIncomingPacket is called by Client central packet dispatcher.
func (s *Stream) HandleIncomingPacket(raw []byte) {
	select {
	case s.incomingChan <- raw:
	default:
	}
}

// Open establishes the virtual Nabto stream via SYN/ACK handshake.
func (s *Stream) Open(timeout time.Duration) error {
	if s.client.cfg.Debug {
		log.Printf("[NabtoPure Stream] 🔄 Opening stream on port %d (streamID: %d)...", s.port, s.streamID)
	}

	s.clientSeq = 1 // SYN sequence number

	// Build SYN packet
	synPkt := s.buildSYNPacket()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := s.client.writeRawStream(synPkt); err != nil {
			return fmt.Errorf("failed to send SYN packet: %w", err)
		}

		var raw []byte
		select {
		case raw = <-s.incomingChan:
		case <-s.closeChan:
			return io.EOF
		case <-time.After(1 * time.Second):
			continue
		}

		hdr, extensions, err := s.parseStreamPacket(raw)
		if err != nil {
			if s.client.cfg.Debug {
				log.Printf("[NabtoPure Stream] ⚠️ parse error in Open: %v", err)
			}
			continue
		}

		s.serverTs = hdr.timestampValue
		if s.client.cfg.Debug {
			log.Printf("[NabtoPure Stream] 📥 Open received packet flags=0x%02x, ts=%d, raw hex: %x", hdr.flags, s.serverTs, raw)
		}

		if (hdr.flags & (StreamFlagSYN | StreamFlagACK)) == (StreamFlagSYN | StreamFlagACK) {
			// Extract server sequence number, max segment sizes and nonce from SYN|ACK
			for _, ext := range extensions {
				if s.client.cfg.Debug {
					log.Printf("[NabtoPure Stream] 📦 Extension 0x%04x (len %d): %x", ext.extType, len(ext.data), ext.data)
				}
				if ext.extType == ExtSYN && len(ext.data) >= 4 {
					s.serverSeq = binary.BigEndian.Uint32(ext.data[:4])
				}
				if ext.extType == ExtSegmentSizes && len(ext.data) >= 4 {
					segSize := binary.BigEndian.Uint16(ext.data[:2])
					if segSize > 0 && segSize <= 1024 {
						s.maxSendSegmentSize = segSize
					}
				}
				if ext.extType == ExtNonce && len(ext.data) >= 8 {
					copy(s.nonce[:], ext.data[:8])
					s.sendNonce = true
				}
			}

			// First data segment after SYN starts at clientSeq = 2
			s.clientSeq = 2

			// Send ACK to finalize handshake
			ackPkt := s.buildACKPacket(nil)
			if s.client.cfg.Debug {
				log.Printf("[NabtoPure Stream] 📤 Sending ACK packet (%d bytes): %x", len(ackPkt), ackPkt)
			}
			_ = s.client.writeRawStream(ackPkt)

			s.mu.Lock()
			s.established = true
			s.mu.Unlock()

			if s.client.cfg.Debug {
				log.Printf("[NabtoPure Stream] ✅ Stream established on port %d! (serverSeq: %d, maxSendSeg: %d)", s.port, s.serverSeq, s.maxSendSegmentSize)
			}
			return nil
		}
	}

	return fmt.Errorf("timeout opening stream on port %d", s.port)
}

// ReadMsg reads a 4-byte little-endian length-prefixed WebRTC signaling message.
func (s *Stream) ReadMsg() ([]byte, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, io.EOF
		}

		// Check if we already have at least 4 bytes for length prefix
		if s.readBuf.Len() >= 4 {
			lenBytes := s.readBuf.Bytes()[:4]
			msgLen := int(binary.LittleEndian.Uint32(lenBytes))

			if s.readBuf.Len() >= 4+msgLen {
				_ = s.readBuf.Next(4) // consume length
				msg := make([]byte, msgLen)
				_, _ = s.readBuf.Read(msg)
				s.mu.Unlock()
				return msg, nil
			}
		}
		s.mu.Unlock()

		// Read next stream packet from incoming channel
		var raw []byte
		select {
		case raw = <-s.incomingChan:
		case <-s.closeChan:
			return nil, io.EOF
		case <-time.After(2 * time.Second):
			// Retransmit ACK if waiting for camera
			ackPkt := s.buildACKPacket(nil)
			_ = s.client.writeRawStream(ackPkt)
			continue
		}

		hdr, extensions, err := s.parseStreamPacket(raw)
		if err != nil {
			if s.client.cfg.Debug {
				log.Printf("[NabtoPure Stream] ⚠️ parse error: %v (raw %d bytes: %x)", err, len(raw), raw)
			}
			continue
		}

		if s.client.cfg.Debug {
			log.Printf("[NabtoPure Stream] 📥 Received stream packet: %d bytes, flags=0x%02x, %d extensions", len(raw), hdr.flags, len(extensions))
		}
		s.serverTs = hdr.timestampValue

		hasNewData := false
		for _, ext := range extensions {
			if ext.extType == ExtDATA && len(ext.data) >= 4 {
				dataSeq := binary.BigEndian.Uint32(ext.data[:4])
				payload := ext.data[4:]

				if len(payload) > 0 {
					s.mu.Lock()
					s.readBuf.Write(payload)
					s.serverSeq = dataSeq
					s.mu.Unlock()
					hasNewData = true
					if s.client.cfg.Debug {
						log.Printf("[NabtoPure Stream] 📝 Received DATA %d bytes (serverSeq now %d)", len(payload), s.serverSeq)
					}
				}
			}
		}

		if hasNewData || (hdr.flags&StreamFlagACK) != 0 {
			// Acknowledge received data
			ackPkt := s.buildACKPacket(nil)
			_ = s.client.writeRawStream(ackPkt)
		}
	}
}

// WriteMsg writes a 4-byte little-endian length-prefixed WebRTC signaling message.
func (s *Stream) WriteMsg(payload []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("stream is closed")
	}
	chunkSize := int(s.maxSendSegmentSize)
	if chunkSize <= 0 {
		chunkSize = 256
	}
	s.mu.Unlock()

	// Frame with 4-byte LE length prefix
	var framed bytes.Buffer
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	framed.Write(lenBuf[:])
	framed.Write(payload)

	framedBytes := framed.Bytes()

	for offset := 0; offset < len(framedBytes); offset += chunkSize {
		end := offset + chunkSize
		if end > len(framedBytes) {
			end = len(framedBytes)
		}
		chunk := framedBytes[offset:end]

		dataPkt := s.buildACKPacket(chunk)
		if s.client.cfg.Debug {
			log.Printf("[NabtoPure Stream] 📤 Sending DATA packet (%d bytes, clientSeq=%d): %x", len(dataPkt), s.clientSeq, dataPkt)
		}
		if err := s.client.writeRawStream(dataPkt); err != nil {
			return fmt.Errorf("failed to write stream data packet: %w", err)
		}
		s.clientSeq++
	}

	return nil
}

func (s *Stream) Abort() {
	s.Close()
}

func (s *Stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.closeChan != nil {
		close(s.closeChan)
	}
}

type streamHeader struct {
	flags          uint8
	timestampValue uint32
}

type streamExt struct {
	extType uint16
	data    []byte
}

func (s *Stream) buildSYNPacket() []byte {
	buf := new(bytes.Buffer)
	buf.WriteByte(StreamAppDataType)
	writeVarUint(buf, s.streamID)

	// Stream Header (flags = SYN, timestamp = now)
	buf.WriteByte(StreamFlagSYN)
	var tsBuf [4]byte
	binary.BigEndian.PutUint32(tsBuf[:], uint32(time.Now().UnixMilli()))
	buf.Write(tsBuf[:])

	// SYN Extension (Type 0x1006, Len 4, Seq = clientSeq)
	writeExtUint32(buf, ExtSYN, s.clientSeq)

	// Segment Sizes Extension (Type 0x1000, Len 4, send = 1024, recv = 1024)
	writeExtUint16Pair(buf, ExtSegmentSizes, 1024, 1024)

	// ContentType Extension (Type 0x1003, Len 4, Port)
	writeExtUint32(buf, ExtContentType, s.port)

	return buf.Bytes()
}

func (s *Stream) buildACKPacket(data []byte) []byte {
	buf := new(bytes.Buffer)
	buf.WriteByte(StreamAppDataType)
	writeVarUint(buf, s.streamID)

	// Stream Header (flags = ACK, timestamp = now)
	buf.WriteByte(StreamFlagACK)
	var tsBuf [4]byte
	binary.BigEndian.PutUint32(tsBuf[:], uint32(time.Now().UnixMilli()))
	buf.Write(tsBuf[:])

	// Nonce Response Extension (if server sent a nonce challenge in SYN|ACK)
	if s.sendNonce {
		var nonceHdr [4]byte
		binary.BigEndian.PutUint16(nonceHdr[0:2], ExtNonceResponse)
		binary.BigEndian.PutUint16(nonceHdr[2:4], 8)
		buf.Write(nonceHdr[:])
		buf.Write(s.nonce[:])
		s.sendNonce = false
	}

	// ACK Extension (Type 0x1001, Len 16, ack = serverSeq, window = 65535, tsEcr = serverTs, delay = 0)
	writeExtACK(buf, s.serverSeq, 65535, s.serverTs, 0)

	// DATA Extension (if payload present)
	if len(data) > 0 {
		var extHdr [4]byte
		binary.BigEndian.PutUint16(extHdr[0:2], ExtDATA)
		binary.BigEndian.PutUint16(extHdr[2:4], uint16(4+len(data)))
		buf.Write(extHdr[:])

		var seqBuf [4]byte
		binary.BigEndian.PutUint32(seqBuf[:], s.clientSeq)
		buf.Write(seqBuf[:])
		buf.Write(data)
	}

	return buf.Bytes()
}

func (s *Stream) parseStreamPacket(raw []byte) (*streamHeader, []streamExt, error) {
	if len(raw) < 2 || raw[0] != StreamAppDataType {
		return nil, nil, fmt.Errorf("invalid stream packet")
	}

	offset := 1
	// Skip streamID (var_uint)
	_, varLen := readVarUint(raw[offset:])
	offset += varLen

	if len(raw) < offset+5 {
		return nil, nil, fmt.Errorf("stream packet too short for header")
	}

	hdr := &streamHeader{
		flags:          raw[offset],
		timestampValue: binary.BigEndian.Uint32(raw[offset+1 : offset+5]),
	}
	offset += 5

	var extensions []streamExt
	for offset+4 <= len(raw) {
		extType := binary.BigEndian.Uint16(raw[offset : offset+2])
		extLen := int(binary.BigEndian.Uint16(raw[offset+2 : offset+4]))
		offset += 4

		if offset+extLen > len(raw) {
			break
		}

		data := make([]byte, extLen)
		copy(data, raw[offset:offset+extLen])
		offset += extLen

		extensions = append(extensions, streamExt{
			extType: extType,
			data:    data,
		})
	}

	return hdr, extensions, nil
}

func writeVarUint(buf *bytes.Buffer, val uint64) {
	if val < (1 << 6) {
		buf.WriteByte(byte(val))
	} else if val < (1 << 14) {
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(val)|(1<<14))
		buf.Write(b[:])
	} else if val < (1 << 30) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(val)|(2<<30))
		buf.Write(b[:])
	}
}

func readVarUint(buf []byte) (uint64, int) {
	if len(buf) == 0 {
		return 0, 0
	}
	first := buf[0]
	tag := first >> 6
	switch tag {
	case 0:
		return uint64(first & 0x3F), 1
	case 1:
		if len(buf) < 2 {
			return 0, 0
		}
		val := binary.BigEndian.Uint16(buf[:2]) & 0x3FFF
		return uint64(val), 2
	case 2:
		if len(buf) < 4 {
			return 0, 0
		}
		val := binary.BigEndian.Uint32(buf[:4]) & 0x3FFFFFFF
		return uint64(val), 4
	default:
		return 0, 1
	}
}

func writeExtUint32(buf *bytes.Buffer, extType uint16, val uint32) {
	var b [8]byte
	binary.BigEndian.PutUint16(b[0:2], extType)
	binary.BigEndian.PutUint16(b[2:4], 4)
	binary.BigEndian.PutUint32(b[4:8], val)
	buf.Write(b[:])
}

func writeExtUint16Pair(buf *bytes.Buffer, extType uint16, v1, v2 uint16) {
	var b [8]byte
	binary.BigEndian.PutUint16(b[0:2], extType)
	binary.BigEndian.PutUint16(b[2:4], 4)
	binary.BigEndian.PutUint16(b[4:6], v1)
	binary.BigEndian.PutUint16(b[6:8], v2)
	buf.Write(b[:])
}

func writeExtACK(buf *bytes.Buffer, ack, window, tsEcr, delay uint32) {
	var b [20]byte
	binary.BigEndian.PutUint16(b[0:2], ExtACK)
	binary.BigEndian.PutUint16(b[2:4], 16)
	binary.BigEndian.PutUint32(b[4:8], ack)
	binary.BigEndian.PutUint32(b[8:12], window)
	binary.BigEndian.PutUint32(b[12:16], tsEcr)
	binary.BigEndian.PutUint32(b[16:20], delay)
	buf.Write(b[:])
}
