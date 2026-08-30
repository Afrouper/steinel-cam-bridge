package nabtopure

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CoAP Message Types
const (
	TypeCON = 0 // Confirmable
	TypeNON = 1 // Non-confirmable
	TypeACK = 2 // Acknowledgement
	TypeRST = 3 // Reset
)

// CoAP Method Codes (Class 0)
const (
	CodeEmpty  uint8 = 0
	CodeGET    uint8 = 1
	CodePOST   uint8 = 2
	CodePUT    uint8 = 3
	CodeDELETE uint8 = 4
)

// CoAP Option Numbers
const (
	OptionIfMatch       uint16 = 1
	OptionUriHost       uint16 = 3
	OptionETag          uint16 = 4
	OptionIfNoneMatch   uint16 = 5
	OptionUriPort       uint16 = 7
	OptionLocationPath  uint16 = 8
	OptionUriPath       uint16 = 11
	OptionContentFormat uint16 = 12
	OptionMaxAge        uint16 = 14
	OptionUriQuery      uint16 = 15
	OptionAccept        uint16 = 17
	OptionLocationQuery uint16 = 20
	OptionBlock2        uint16 = 23
	OptionBlock1        uint16 = 27
	OptionSize2         uint16 = 28
	OptionProxyUri      uint16 = 35
	OptionProxyScheme   uint16 = 39
	OptionSize1         uint16 = 60
)

// CoAP Content Formats
const (
	ContentFormatTextPlain       uint16 = 0
	ContentFormatApplicationJSON uint16 = 50
	ContentFormatApplicationCBOR uint16 = 60
)

// CoAP Option
type Option struct {
	Number uint16
	Value  []byte
}

// CoAP Message representation
type CoAPMessage struct {
	Type      uint8
	Code      uint8
	MessageID uint16
	Token     []byte
	Options   []Option
	Payload   []byte
}

// StatusCode returns human-readable numeric HTTP-like status (e.g. 200, 201, 205, 401, 404, 500)
func (m *CoAPMessage) StatusCode() int {
	class := int(m.Code >> 5)
	detail := int(m.Code & 0x1F)
	return class*100 + detail
}

// StatusString returns a formatted code string like "2.05 Content" or "4.01 Unauthorized"
func (m *CoAPMessage) StatusString() string {
	return fmt.Sprintf("%d.%02d (Status %d)", m.Code>>5, m.Code&0x1F, m.StatusCode())
}

// Encode serializes a CoAP message into RFC 7252 binary wire format
func (m *CoAPMessage) Encode() ([]byte, error) {
	buf := new(bytes.Buffer)

	// Byte 0: Version (1) | Type (2 bits) | Token Length (4 bits)
	tokenLen := len(m.Token)
	if tokenLen > 8 {
		return nil, fmt.Errorf("token length cannot exceed 8 bytes (got %d)", tokenLen)
	}
	b0 := (1 << 6) | ((m.Type & 0x03) << 4) | byte(tokenLen&0x0F)
	buf.WriteByte(b0)

	// Byte 1: Code
	buf.WriteByte(m.Code)

	// Bytes 2-3: Message ID (Big Endian)
	var midBuf [2]byte
	binary.BigEndian.PutUint16(midBuf[:], m.MessageID)
	buf.Write(midBuf[:])

	// Token
	if tokenLen > 0 {
		buf.Write(m.Token)
	}

	// Options (Delta encoded in ascending order)
	lastOptionNumber := uint16(0)
	for _, opt := range m.Options {
		delta := opt.Number - lastOptionNumber
		length := uint16(len(opt.Value))

		deltaNibble, deltaExt := encodeOptionValue(delta)
		lenNibble, lenExt := encodeOptionValue(length)

		headerByte := (deltaNibble << 4) | lenNibble
		buf.WriteByte(headerByte)
		buf.Write(deltaExt)
		buf.Write(lenExt)
		buf.Write(opt.Value)

		lastOptionNumber = opt.Number
	}

	// Payload Marker & Payload
	if len(m.Payload) > 0 {
		buf.WriteByte(0xFF)
		buf.Write(m.Payload)
	}

	return buf.Bytes(), nil
}

func encodeOptionValue(val uint16) (uint8, []byte) {
	if val < 13 {
		return uint8(val), nil
	} else if val < 269 {
		ext := byte(val - 13)
		return 13, []byte{ext}
	} else {
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], val-269)
		return 14, ext[:]
	}
}

// DecodeCoAPMessage parses a raw CoAP packet from wire bytes
func DecodeCoAPMessage(data []byte) (*CoAPMessage, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("CoAP message too short: %d bytes", len(data))
	}

	ver := (data[0] >> 6) & 0x03
	if ver != 1 {
		return nil, fmt.Errorf("unsupported CoAP version %d", ver)
	}

	msgType := (data[0] >> 4) & 0x03
	tokenLen := int(data[0] & 0x0F)
	code := data[1]
	msgID := binary.BigEndian.Uint16(data[2:4])

	offset := 4
	if len(data) < offset+tokenLen {
		return nil, fmt.Errorf("unexpected EOF reading token")
	}

	var token []byte
	if tokenLen > 0 {
		token = make([]byte, tokenLen)
		copy(token, data[offset:offset+tokenLen])
		offset += tokenLen
	}

	var options []Option
	currentOptionNumber := uint16(0)

	for offset < len(data) {
		if data[offset] == 0xFF {
			offset++
			break
		}

		optByte := data[offset]
		offset++

		deltaNibble := uint16((optByte >> 4) & 0x0F)
		lenNibble := uint16(optByte & 0x0F)

		delta := deltaNibble
		switch deltaNibble {
		case 13:
			if offset >= len(data) {
				return nil, io.EOF
			}
			delta = uint16(data[offset]) + 13
			offset++
		case 14:
			if offset+2 > len(data) {
				return nil, io.EOF
			}
			delta = binary.BigEndian.Uint16(data[offset:offset+2]) + 269
			offset += 2
		}

		optLen := lenNibble
		switch lenNibble {
		case 13:
			if offset >= len(data) {
				return nil, io.EOF
			}
			optLen = uint16(data[offset]) + 13
			offset++
		case 14:
			if offset+2 > len(data) {
				return nil, io.EOF
			}
			optLen = binary.BigEndian.Uint16(data[offset:offset+2]) + 269
			offset += 2
		}

		currentOptionNumber += delta

		if offset+int(optLen) > len(data) {
			return nil, fmt.Errorf("unexpected EOF reading option %d value", currentOptionNumber)
		}

		val := make([]byte, optLen)
		copy(val, data[offset:offset+int(optLen)])
		offset += int(optLen)

		options = append(options, Option{
			Number: currentOptionNumber,
			Value:  val,
		})
	}

	var payload []byte
	if offset < len(data) {
		payload = make([]byte, len(data)-offset)
		copy(payload, data[offset:])
	}

	return &CoAPMessage{
		Type:      msgType,
		Code:      code,
		MessageID: msgID,
		Token:     token,
		Options:   options,
		Payload:   payload,
	}, nil
}

// CoAPClient manages sending requests and matching responses over an underlying net.Conn (DTLS).
type CoAPClient struct {
	conn      net.Conn
	msgIDSeq  atomic.Uint32
	tokenSeq  atomic.Uint32
	mu        sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan *CoAPMessage
}

// NewCoAPClient creates a new CoAP client over an existing connection
func NewCoAPClient(conn net.Conn) *CoAPClient {
	c := &CoAPClient{
		conn:    conn,
		pending: make(map[string]chan *CoAPMessage),
	}
	c.msgIDSeq.Store(100)
	c.tokenSeq.Store(1)
	return c
}

// NewRequest builds a CoAP request for a specific path and method
func NewRequest(method uint8, path string, contentFormat uint16, payload []byte) *CoAPMessage {
	msg := &CoAPMessage{
		Type:    TypeCON,
		Code:    method,
		Payload: payload,
	}

	// Split UriPath (e.g. "/p2p/webrtc-info" -> "p2p", "webrtc-info")
	cleanPath := strings.Trim(path, "/")
	if cleanPath != "" {
		segments := strings.Split(cleanPath, "/")
		for _, seg := range segments {
			msg.Options = append(msg.Options, Option{
				Number: OptionUriPath,
				Value:  []byte(seg),
			})
		}
	}

	if contentFormat > 0 || (len(payload) > 0 && contentFormat == 0) {
		var formatBytes []byte
		if contentFormat < 256 {
			formatBytes = []byte{byte(contentFormat)}
		} else {
			formatBytes = make([]byte, 2)
			binary.BigEndian.PutUint16(formatBytes, contentFormat)
		}
		msg.Options = append(msg.Options, Option{
			Number: OptionContentFormat,
			Value:  formatBytes,
		})
	}

	return msg
}

// Execute sends a CoAP request and waits for the response
func (c *CoAPClient) Execute(req *CoAPMessage, timeout time.Duration) (*CoAPMessage, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("coap client: connection is closed")
	}

	req.MessageID = uint16(c.msgIDSeq.Add(1))
	tokenVal := c.tokenSeq.Add(1)
	req.Token = make([]byte, 4)
	binary.BigEndian.PutUint32(req.Token, tokenVal)

	tokenKey := string(req.Token)
	respChan := make(chan *CoAPMessage, 1)

	c.pendingMu.Lock()
	c.pending[tokenKey] = respChan
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, tokenKey)
		c.pendingMu.Unlock()
	}()

	raw, err := req.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode CoAP message: %w", err)
	}

	c.mu.Lock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err = c.conn.Write(raw)
	c.mu.Unlock()

	if err != nil {
		return nil, fmt.Errorf("failed to send CoAP packet: %w", err)
	}

	// Read loop with timeout
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 2048)

	for time.Now().Before(deadline) {
		_ = c.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := c.conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return nil, fmt.Errorf("failed to read CoAP response: %w", err)
		}

		resp, err := DecodeCoAPMessage(buf[:n])
		if err != nil {
			continue
		}

		// Match token
		if bytes.Equal(resp.Token, req.Token) || (resp.MessageID == req.MessageID && resp.Code != CodeEmpty) {
			return resp, nil
		}
	}

	return nil, fmt.Errorf("CoAP request to timed out after %v", timeout)
}
