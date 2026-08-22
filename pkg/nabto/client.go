package nabto

/*
#cgo CFLAGS: -I${SRCDIR}/../../.sdk/include -I/usr/local/include -I/usr/include
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/../../.sdk/lib -L/usr/local/lib -lnabto_client -lpthread
#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/../../.sdk/lib -L/usr/local/lib -lnabto_client -lpthread
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../.sdk/lib -L/usr/lib -L/usr/lib/x86_64-linux-gnu -L/usr/local/lib -lnabto_client -lpthread
#cgo linux,arm64 LDFLAGS: -L${SRCDIR}/../../.sdk/lib -L/usr/lib -L/usr/lib/aarch64-linux-gnu -L/usr/aarch64-linux-gnu/lib -L/usr/local/lib -lnabto_client -lpthread
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/../../.sdk/lib -lnabto_client

#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <arpa/inet.h>
#include <sys/socket.h>
#include "nabto/nabto_client.h"

static void send_mdns_wakeup_c(const char* camera_ip, int port) {
    int s = socket(AF_INET, SOCK_DGRAM, 0);
    if (s < 0) return;

    struct sockaddr_in addr_unicast, addr_mcast, addr_nabto;

    memset(&addr_unicast, 0, sizeof(addr_unicast));
    addr_unicast.sin_family = AF_INET;
    addr_unicast.sin_port = htons(5353);
    inet_pton(AF_INET, camera_ip, &addr_unicast.sin_addr);

    memset(&addr_mcast, 0, sizeof(addr_mcast));
    addr_mcast.sin_family = AF_INET;
    addr_mcast.sin_port = htons(5353);
    inet_pton(AF_INET, "224.0.0.251", &addr_mcast.sin_addr);

    memset(&addr_nabto, 0, sizeof(addr_nabto));
    addr_nabto.sin_family = AF_INET;
    addr_nabto.sin_port = htons(port);
    inet_pton(AF_INET, camera_ip, &addr_nabto.sin_addr);

    unsigned char mdns_query[] = {
        0x00,0x00,0x00,0x00,0x00,0x01,0x00,0x00,0x00,0x00,0x00,0x00,
        0x17,0x70,0x72,0x2d,0x71,0x74,0x61,0x74,0x62,0x74,0x62,0x69,
        0x2d,0x64,0x65,0x2d,0x6d,0x34,0x79,0x66,0x6f,0x77,0x62,0x72,
        0x05,0x6c,0x6f,0x63,0x61,0x6c,0x00,0x00,0xff,0x00,0x01
    };

    for (int i = 0; i < 4; i++) {
        sendto(s, mdns_query, sizeof(mdns_query), 0, (struct sockaddr*)&addr_unicast, sizeof(addr_unicast));
        sendto(s, mdns_query, sizeof(mdns_query), 0, (struct sockaddr*)&addr_mcast, sizeof(addr_mcast));
        sendto(s, mdns_query, sizeof(mdns_query), 0, (struct sockaddr*)&addr_nabto, sizeof(addr_nabto));
        usleep(40000);
    }
    close(s);
    usleep(250000);
}
*/
import "C"
import (
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"
)

type Config struct {
	CameraIP   string
	CameraPort int
	ProductID  string
	DeviceID   string
	SCT        string
	PairPwd    string
	KeyPath    string
	IsBeta     bool
	ClientName string
}

type Client struct {
	cfg        *Config
	ctx        *C.NabtoClient
	conn       *C.NabtoClientConnection
	privateKey string
	mu         sync.Mutex
}

func NewClient(cfg *Config) (*Client, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	if cfg.CameraPort == 0 {
		cfg.CameraPort = 5592
	}
	if cfg.ProductID == "" {
		cfg.ProductID = "pr-xxxxx"
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID = "de-xxxxxxx"
	}
	if cfg.SCT == "" {
		cfg.SCT = "JDCIETbBRn0Y"
	}
	if cfg.PairPwd == "" {
		cfg.PairPwd = "xxxx"
	}

	ctx := C.nabto_client_new()
	if ctx == nil {
		return nil, fmt.Errorf("failed to create NabtoClient context")
	}

	return &Client{
		cfg: cfg,
		ctx: ctx,
	}, nil
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil && c.ctx != nil {
		fut := C.nabto_client_future_new(c.ctx)
		if fut != nil {
			C.nabto_client_connection_close(c.conn, fut)
			C.nabto_client_future_wait(fut)
			C.nabto_client_future_free(fut)
		}
		C.nabto_client_connection_free(c.conn)
		c.conn = nil
	}

	if c.ctx != nil {
		C.nabto_client_stop(c.ctx)
		C.nabto_client_free(c.ctx)
		c.ctx = nil
	}
}

// Connect establishes the Nabto Edge P2P tunnel to the camera and handles auto-pairing
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Load or generate private key
	keyBytes, err := os.ReadFile(c.cfg.KeyPath)
	isNewKey := false
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[Nabto] ⚠️ Warning: Could not read key file '%s': %v", c.cfg.KeyPath, err)
		}
		log.Printf("[Nabto] Key file not found at %s. Generating new EC private key...", c.cfg.KeyPath)
		var cKey *C.char
		errCode := C.nabto_client_create_private_key(c.ctx, &cKey)
		if errCode != C.NABTO_CLIENT_EC_OK || cKey == nil {
			return fmt.Errorf("failed to generate EC private key")
		}
		c.privateKey = C.GoString(cKey)
		C.nabto_client_string_free(cKey)
		isNewKey = true
	} else {
		c.privateKey = string(keyBytes)
	}

	cIP := C.CString(c.cfg.CameraIP)
	defer C.free(unsafe.Pointer(cIP))

	const maxRetries = 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.Printf("[Nabto] Sending mDNS wake-up to %s...", c.cfg.CameraIP)
		C.send_mdns_wakeup_c(cIP, C.int(c.cfg.CameraPort))

		conn := C.nabto_client_connection_new(c.ctx)
		if conn == nil {
			return fmt.Errorf("failed to create Nabto connection object")
		}

		cProd := C.CString(c.cfg.ProductID)
		cDev := C.CString(c.cfg.DeviceID)
		cSCT := C.CString(c.cfg.SCT)
		cPrivKey := C.CString(c.privateKey)
		serverURL := C.CString(fmt.Sprintf("https://%s.devices.nabto.net", c.cfg.ProductID))

		C.nabto_client_connection_set_product_id(conn, cProd)
		C.nabto_client_connection_set_device_id(conn, cDev)
		C.nabto_client_connection_set_server_connect_token(conn, cSCT)
		C.nabto_client_connection_set_private_key(conn, cPrivKey)
		C.nabto_client_connection_set_server_url(conn, serverURL)

		C.free(unsafe.Pointer(cProd))
		C.free(unsafe.Pointer(cDev))
		C.free(unsafe.Pointer(cSCT))
		C.free(unsafe.Pointer(cPrivKey))
		C.free(unsafe.Pointer(serverURL))

		C.nabto_client_connection_enable_direct_candidates(conn)
		C.nabto_client_connection_add_direct_candidate(conn, cIP, C.uint16_t(c.cfg.CameraPort))
		C.nabto_client_connection_end_of_direct_candidates(conn)

		log.Printf("[Nabto] Connecting to camera (attempt %d/%d)...", attempt, maxRetries)
		fut := C.nabto_client_future_new(c.ctx)
		C.nabto_client_connection_connect(conn, fut)
		C.nabto_client_future_wait(fut)
		errCode := C.nabto_client_future_error_code(fut)
		C.nabto_client_future_free(fut)

		if errCode == C.NABTO_CLIENT_EC_OK {
			log.Printf("[Nabto] ✅ Connected successfully!")
			c.conn = conn

			if isNewKey {
				if c.cfg.PairPwd == "" || c.cfg.PairPwd == "xxxx" {
					log.Printf("[IAM] ⚠️ Warning: New client key generated, but no QR code ('qr_code') is configured. Initial pairing requires a valid QR code!")
				} else {
					log.Printf("[IAM] Performing initial pairing for new client key...")
					if err := c.pairPassword(c.cfg.PairPwd); err != nil {
						log.Printf("[IAM] ❌ Initial pairing failed: %v", err)
						return fmt.Errorf("initial pairing failed: %w", err)
					}
					if err := os.WriteFile(c.cfg.KeyPath, []byte(c.privateKey), 0600); err != nil {
						log.Printf("[!] Warning: Could not save key to %s: %v", c.cfg.KeyPath, err)
					} else {
						log.Printf("[IAM] ✅ Saved paired key to: %s", c.cfg.KeyPath)
					}
				}
			}
			return nil
		}

		errMsg := C.GoString(C.nabto_client_error_get_message(errCode))
		log.Printf("[Nabto] Attempt %d failed: %s. Retrying in 2s...", attempt, errMsg)
		C.nabto_client_connection_free(conn)
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("connection failed after %d attempts", maxRetries)
}

func encodeUsernameCBOR(username string) []byte {
	header := []byte{0xa1, 0x68, 'U', 's', 'e', 'r', 'n', 'a', 'm', 'e'}
	uBytes := []byte(username)
	if len(uBytes) < 24 {
		header = append(header, byte(0x60+len(uBytes)))
	} else {
		header = append(header, 0x78, byte(len(uBytes)))
	}
	return append(header, uBytes...)
}

func generateDynamicUsername(isBeta bool, customName string) string {
	if customName != "" {
		return customName
	}
	b := make([]byte, 3)
	if _, err := io.ReadFull(crand.Reader, b); err != nil {
		binary.LittleEndian.PutUint32(b, uint32(time.Now().UnixNano()))
	}
	suffix := fmt.Sprintf("%02x%02x%02x", b[0], b[1], b[2])
	if isBeta {
		return fmt.Sprintf("steinel-bridge-beta-%s", suffix)
	}
	return fmt.Sprintf("steinel-bridge-%s", suffix)
}

func (c *Client) pairPassword(password string) error {
	cPwd := C.CString(password)
	cEmpty := C.CString("")
	defer C.free(unsafe.Pointer(cPwd))
	defer C.free(unsafe.Pointer(cEmpty))

	fut := C.nabto_client_future_new(c.ctx)
	C.nabto_client_connection_password_authenticate(c.conn, cEmpty, cPwd, fut)
	C.nabto_client_future_wait(fut)
	errCode := C.nabto_client_future_error_code(fut)
	C.nabto_client_future_free(fut)

	if errCode != C.NABTO_CLIENT_EC_OK {
		return fmt.Errorf("password authentication failed: %s", C.GoString(C.nabto_client_error_get_message(errCode)))
	}

	const maxPairAttempts = 3
	var lastStatus C.uint16_t

	for attempt := 1; attempt <= maxPairAttempts; attempt++ {
		username := generateDynamicUsername(c.cfg.IsBeta, c.cfg.ClientName)
		cborPayload := encodeUsernameCBOR(username)

		cMethod := C.CString("POST")
		cPath := C.CString("/iam/pairing/password-open")

		coap := C.nabto_client_coap_new(c.conn, cMethod, cPath)
		C.free(unsafe.Pointer(cMethod))
		C.free(unsafe.Pointer(cPath))

		if coap == nil {
			return fmt.Errorf("failed to create CoAP pairing request")
		}

		C.nabto_client_coap_set_request_payload(
			coap,
			C.NABTO_CLIENT_COAP_CONTENT_FORMAT_APPLICATION_CBOR,
			unsafe.Pointer(&cborPayload[0]),
			C.size_t(len(cborPayload)),
		)

		fut = C.nabto_client_future_new(c.ctx)
		C.nabto_client_coap_execute(coap, fut)
		C.nabto_client_future_wait(fut)
		errCode = C.nabto_client_future_error_code(fut)
		C.nabto_client_future_free(fut)

		var statusCode C.uint16_t
		if errCode == C.NABTO_CLIENT_EC_OK {
			C.nabto_client_coap_get_response_status_code(coap, &statusCode)
		}
		C.nabto_client_coap_free(coap)

		log.Printf("[IAM] Pairing response status for '%s': %d", username, statusCode)
		lastStatus = statusCode

		if statusCode == 201 || statusCode == 200 {
			log.Printf("[IAM] ✅ Successfully paired as '%s'", username)
			return nil
		}

		if statusCode != 409 {
			break
		}
		log.Printf("[IAM] ⚠️ Username '%s' already registered (409 Conflict). Retrying with new ID (attempt %d/%d)...", username, attempt, maxPairAttempts)
	}

	return fmt.Errorf("pairing returned status code %d", lastStatus)
}

// GetSignalingPort queries /p2p/webrtc-info via CoAP to get the SignalingStreamPort
func (c *Client) GetSignalingPort() (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cMethod := C.CString("GET")
	cPath := C.CString("/p2p/webrtc-info")
	defer C.free(unsafe.Pointer(cMethod))
	defer C.free(unsafe.Pointer(cPath))

	coap := C.nabto_client_coap_new(c.conn, cMethod, cPath)
	if coap == nil {
		return 0, fmt.Errorf("failed to create CoAP request")
	}
	defer C.nabto_client_coap_free(coap)

	fut := C.nabto_client_future_new(c.ctx)
	C.nabto_client_coap_execute(coap, fut)
	C.nabto_client_future_wait(fut)
	errCode := C.nabto_client_future_error_code(fut)
	C.nabto_client_future_free(fut)

	if errCode != C.NABTO_CLIENT_EC_OK {
		return 0, fmt.Errorf("coap execution error: %s", C.GoString(C.nabto_client_error_get_message(errCode)))
	}

	var statusCode C.uint16_t
	C.nabto_client_coap_get_response_status_code(coap, &statusCode)

	var payload unsafe.Pointer
	var payloadLen C.size_t
	C.nabto_client_coap_get_response_payload(coap, &payload, &payloadLen)

	if statusCode == 205 && payload != nil && payloadLen > 0 {
		respStr := C.GoStringN((*C.char)(payload), C.int(payloadLen))
		log.Printf("[CoAP] /p2p/webrtc-info response: %s", respStr)

		var port uint32
		if _, err := fmt.Sscanf(respStr, "{\"SignalingStreamPort\":%d}", &port); err == nil && port > 0 {
			return port, nil
		}
		// Fallback search
		if idx := strings.Index(respStr, "SignalingStreamPort"); idx >= 0 {
			if colon := strings.Index(respStr[idx:], ":"); colon >= 0 {
				_, _ = fmt.Sscanf(respStr[idx+colon+1:], "%d", &port)
				if port > 0 {
					return port, nil
				}
			}
		}
	}

	return 0, fmt.Errorf("invalid response from /p2p/webrtc-info (status %d)", statusCode)
}

// RequestTracks sends CoAP POST /webrtc/tracks to request video and audio streams
func (c *Client) RequestTracks() (uint16, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cMethod := C.CString("POST")
	cPath := C.CString("/webrtc/tracks")
	defer C.free(unsafe.Pointer(cMethod))
	defer C.free(unsafe.Pointer(cPath))

	coap := C.nabto_client_coap_new(c.conn, cMethod, cPath)
	if coap == nil {
		return 0, fmt.Errorf("failed to create CoAP request")
	}
	defer C.nabto_client_coap_free(coap)

	payload := []byte("{\"tracks\": [\"frontdoor-video\", \"frontdoor-audio\"]}")
	C.nabto_client_coap_set_request_payload(
		coap,
		C.NABTO_CLIENT_COAP_CONTENT_FORMAT_APPLICATION_JSON,
		unsafe.Pointer(&payload[0]),
		C.size_t(len(payload)),
	)

	fut := C.nabto_client_future_new(c.ctx)
	C.nabto_client_coap_execute(coap, fut)
	C.nabto_client_future_wait(fut)
	errCode := C.nabto_client_future_error_code(fut)
	C.nabto_client_future_free(fut)

	var statusCode C.uint16_t
	if errCode == C.NABTO_CLIENT_EC_OK {
		C.nabto_client_coap_get_response_status_code(coap, &statusCode)
		log.Printf("[CoAP] /webrtc/tracks response status=%d", statusCode)
		if statusCode == 401 {
			if c.cfg.PairPwd != "" && c.cfg.PairPwd != "xxxx" {
				log.Printf("[CoAP] ❌ /webrtc/tracks returned 401 Unauthorized: Stored key '%s' is not authorized by camera IAM. Auto-removing invalid key file to re-pair with configured QR code on next connect...", c.cfg.KeyPath)
				_ = os.Remove(c.cfg.KeyPath)
			} else {
				log.Printf("[CoAP] ❌ /webrtc/tracks returned 401 Unauthorized: Stored key '%s' is not authorized by camera IAM! Re-pairing requires a valid 'qr_code' in your configuration.", c.cfg.KeyPath)
			}
		}
		return uint16(statusCode), nil
	}

	return 0, fmt.Errorf("coap execute error: %s", C.GoString(C.nabto_client_error_get_message(errCode)))
}

// OpenSignalingStream opens a Nabto virtual stream for WebRTC signaling
func (c *Client) OpenSignalingStream(port uint32) (*Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stream := C.nabto_client_stream_new(c.conn)
	if stream == nil {
		return nil, fmt.Errorf("failed to create stream object")
	}

	fut := C.nabto_client_future_new(c.ctx)
	C.nabto_client_stream_open(stream, fut, C.uint32_t(port))
	C.nabto_client_future_wait(fut)
	errCode := C.nabto_client_future_error_code(fut)
	C.nabto_client_future_free(fut)

	if errCode != C.NABTO_CLIENT_EC_OK {
		C.nabto_client_stream_free(stream)
		return nil, fmt.Errorf("stream open failed: %s", C.GoString(C.nabto_client_error_get_message(errCode)))
	}

	return &Stream{
		ctx:    c.ctx,
		stream: stream,
	}, nil
}

// Stream represents a Nabto Virtual Stream with 4-byte LE length-prefixed framing
type Stream struct {
	ctx    *C.NabtoClient
	stream *C.NabtoClientStream
	mu     sync.Mutex
}

func (s *Stream) Abort() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stream != nil {
		C.nabto_client_stream_abort(s.stream)
	}
}

func (s *Stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stream != nil {
		C.nabto_client_stream_abort(s.stream)
		C.nabto_client_stream_free(s.stream)
		s.stream = nil
	}
}

// ReadMsg reads a 4-byte LE length-prefixed message from the stream
func (s *Stream) ReadMsg() ([]byte, error) {
	var lenBuf [4]byte
	var readLen C.size_t

	fut := C.nabto_client_future_new(s.ctx)
	C.nabto_client_stream_read_all(s.stream, fut, unsafe.Pointer(&lenBuf[0]), 4, &readLen)
	C.nabto_client_future_wait(fut)
	errCode := C.nabto_client_future_error_code(fut)
	C.nabto_client_future_free(fut)

	if errCode != C.NABTO_CLIENT_EC_OK || readLen != 4 {
		return nil, io.EOF
	}

	payloadLen := binary.LittleEndian.Uint32(lenBuf[:])
	if payloadLen == 0 || payloadLen > (1<<20) {
		return nil, fmt.Errorf("invalid frame length %d", payloadLen)
	}

	payload := make([]byte, payloadLen)
	fut = C.nabto_client_future_new(s.ctx)
	C.nabto_client_stream_read_all(s.stream, fut, unsafe.Pointer(&payload[0]), C.size_t(payloadLen), &readLen)
	C.nabto_client_future_wait(fut)
	errCode = C.nabto_client_future_error_code(fut)
	C.nabto_client_future_free(fut)

	if errCode != C.NABTO_CLIENT_EC_OK || readLen != C.size_t(payloadLen) {
		return nil, io.EOF
	}

	return payload, nil
}

// WriteMsg writes a 4-byte LE length-prefixed message to the stream
func (s *Stream) WriteMsg(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	wire := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(wire[:4], uint32(len(payload)))
	copy(wire[4:], payload)

	fut := C.nabto_client_future_new(s.ctx)
	C.nabto_client_stream_write(s.stream, fut, unsafe.Pointer(&wire[0]), C.size_t(len(wire)))
	C.nabto_client_future_wait(fut)
	errCode := C.nabto_client_future_error_code(fut)
	C.nabto_client_future_free(fut)

	if errCode != C.NABTO_CLIENT_EC_OK {
		return fmt.Errorf("stream write error: %s", C.GoString(C.nabto_client_error_get_message(errCode)))
	}

	return nil
}
