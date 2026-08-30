package nabtopure

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/nabto"
	"github.com/pion/dtls/v3"
	dtlselliptic "github.com/pion/dtls/v3/pkg/crypto/elliptic"
)

// Config mirrors the connection parameters required for Nabto Edge communication.
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
	Debug      bool
}

// Client is the Pure-Go implementation of the Nabto Edge client driver.
type Client struct {
	cfg        *Config
	privateKey *ecdsa.PrivateKey
	dtlsConn   *dtls.Conn
	udpConn    net.PacketConn
	coapClient *CoAPClient
	mu         sync.Mutex
}

// Ensure Client satisfies nabto.Driver interface.
var _ nabto.Driver = (*Client)(nil)

// NewClient initializes a new pure-Go Nabto driver.
func NewClient(cfg *Config) (*Client, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	if cfg.CameraPort == 0 {
		cfg.CameraPort = 5592
	}
	return &Client{
		cfg: cfg,
	}, nil
}

// LoadOrGenerateKey loads an EC private key (secp256r1) from file or generates a new one.
func (c *Client) LoadOrGenerateKey() (*ecdsa.PrivateKey, bool, error) {
	keyBytes, err := os.ReadFile(c.cfg.KeyPath)
	if err == nil {
		block, _ := pem.Decode(keyBytes)
		if block != nil {
			if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
				return key, false, nil
			}
			if pk, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if ecKey, ok := pk.(*ecdsa.PrivateKey); ok {
					return ecKey, false, nil
				}
			}
		}
	}

	// Generate new secp256r1 ECC key
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("failed to generate EC key: %w", err)
	}

	der, err := x509.MarshalECPrivateKey(key)
	if err == nil {
		pemBytes := pem.EncodeToMemory(&pem.Block{
			Type:  "EC PRIVATE KEY",
			Bytes: der,
		})
		_ = os.WriteFile(c.cfg.KeyPath, pemBytes, 0600)
	}

	return key, true, nil
}

// GenerateSelfSignedCert generates a self-signed X.509 certificate for DTLS 1.2 client auth.
func GenerateSelfSignedCert(key *ecdsa.PrivateKey) (tls.Certificate, error) {
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "nabto-edge-client"},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(50 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(crand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to create client certificate: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}, nil
}

// ComputeFingerprint returns the lowercase SHA256 hex string of the client's public key (Nabto IAM fingerprint).
func ComputeFingerprint(key *ecdsa.PrivateKey) (string, error) {
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(pubDER)
	return hex.EncodeToString(hash[:]), nil
}

// Connect establishes the DTLS 1.2 connection over UDP to the camera.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key, isNewKey, err := c.LoadOrGenerateKey()
	if err != nil {
		return err
	}
	c.privateKey = key

	cert, err := GenerateSelfSignedCert(key)
	if err != nil {
		return fmt.Errorf("failed to generate DTLS certificate: %w", err)
	}

	fingerprint, _ := ComputeFingerprint(key)
	log.Printf("[NabtoPure] 🔑 Client ECC Fingerprint: %s (new key: %v)", fingerprint, isNewKey)

	targetAddr := fmt.Sprintf("%s:%d", c.cfg.CameraIP, c.cfg.CameraPort)
	rAddr, err := net.ResolveUDPAddr("udp", targetAddr)
	if err != nil {
		return fmt.Errorf("failed to resolve camera address: %w", err)
	}

	// Wake up camera via mDNS ping
	c.sendMDNSWAKEUP(targetAddr)

	log.Printf("[NabtoPure] 🚀 Connecting to %s via Pure-Go DTLS 1.2...", targetAddr)

	//nolint:staticcheck // dtls.Config used for client configuration
	dtlsConfig := &dtls.Config{
		Certificates:         []tls.Certificate{cert},
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		InsecureSkipVerify:   true,
		FlightInterval:       100 * time.Millisecond,
		CipherSuites: []dtls.CipherSuiteID{
			dtls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM,
			dtls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8,
			dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		},
		EllipticCurves:     []dtlselliptic.Curve{dtlselliptic.P256},
		SupportedProtocols: []string{"n5"},
	}

	rawUDP, err := net.ListenUDP("udp", nil)
	if err != nil {
		return fmt.Errorf("failed to open local UDP socket: %w", err)
	}
	c.udpConn = rawUDP

	nabtoUDP, err := newNabtoPacketConn(rawUDP)
	if err != nil {
		_ = rawUDP.Close()
		return err
	}

	//nolint:staticcheck
	conn, err := dtls.Client(nabtoUDP, rAddr, dtlsConfig)
	if err != nil {
		_ = rawUDP.Close()
		return fmt.Errorf("DTLS handshake failed with %s: %w", targetAddr, err)
	}

	log.Printf("[NabtoPure] ✅ DTLS 1.2 handshake established successfully with %s", targetAddr)
	c.dtlsConn = conn
	c.coapClient = NewCoAPClient(conn)
	return nil
}

func (c *Client) sendMDNSWAKEUP(target string) {
	conn, err := net.Dial("udp", target)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	mdnsQuery := []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x17, 0x70, 0x72, 0x2d, 0x71, 0x74, 0x61, 0x74, 0x62, 0x74, 0x62, 0x69,
		0x2d, 0x64, 0x65, 0x2d, 0x6d, 0x34, 0x79, 0x66, 0x6f, 0x77, 0x62, 0x72,
		0x05, 0x6c, 0x6f, 0x63, 0x61, 0x6c, 0x00, 0x00, 0xff, 0x00, 0x01,
	}
	for i := 0; i < 3; i++ {
		_, _ = conn.Write(mdnsQuery)
		time.Sleep(30 * time.Millisecond)
	}
}

// Close closes the underlying DTLS and network connections.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dtlsConn != nil {
		_ = c.dtlsConn.Close()
		c.dtlsConn = nil
	}
	if c.udpConn != nil {
		_ = c.udpConn.Close()
		c.udpConn = nil
	}
	c.coapClient = nil
}

// CoAPClient returns the underlying CoAPClient.
func (c *Client) CoAPClient() *CoAPClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.coapClient
}

// GetSignalingPort queries /p2p/webrtc-info via CoAP.
func (c *Client) GetSignalingPort() (uint32, error) {
	c.mu.Lock()
	coap := c.coapClient
	c.mu.Unlock()

	if coap == nil {
		return 0, fmt.Errorf("client not connected")
	}

	req := NewRequest(CodeGET, "/p2p/webrtc-info", 0, nil)
	resp, err := coap.Execute(req, 5*time.Second)
	if err != nil {
		return 0, fmt.Errorf("CoAP /p2p/webrtc-info failed: %w", err)
	}

	if resp.StatusCode() != 205 && resp.StatusCode() != 200 {
		return 0, fmt.Errorf("unexpected CoAP status %s", resp.StatusString())
	}

	respStr := string(resp.Payload)
	log.Printf("[NabtoPure] 🛰️ CoAP /p2p/webrtc-info response: %s", respStr)

	var port uint32
	if _, err := fmt.Sscanf(respStr, "{\"SignalingStreamPort\":%d}", &port); err == nil && port > 0 {
		return port, nil
	}
	if idx := strings.Index(respStr, "SignalingStreamPort"); idx >= 0 {
		if colon := strings.Index(respStr[idx:], ":"); colon >= 0 {
			_, _ = fmt.Sscanf(respStr[idx+colon+1:], "%d", &port)
			if port > 0 {
				return port, nil
			}
		}
	}

	return 0, fmt.Errorf("could not parse SignalingStreamPort from: %s", respStr)
}

// RequestTracks sends CoAP POST /webrtc/tracks.
func (c *Client) RequestTracks() (uint16, error) {
	c.mu.Lock()
	coap := c.coapClient
	c.mu.Unlock()

	if coap == nil {
		return 0, fmt.Errorf("client not connected")
	}

	payload := []byte("{\"tracks\": [\"frontdoor-video\", \"frontdoor-audio\"]}")
	req := NewRequest(CodePOST, "/webrtc/tracks", ContentFormatApplicationJSON, payload)

	resp, err := coap.Execute(req, 5*time.Second)
	if err != nil {
		return 0, fmt.Errorf("CoAP /webrtc/tracks failed: %w", err)
	}

	log.Printf("[NabtoPure] 🎥 CoAP /webrtc/tracks response status: %s", resp.StatusString())
	return uint16(resp.StatusCode()), nil
}

// OpenSignalingStream opens a virtual Nabto streaming channel over the DTLS connection.
func (c *Client) OpenSignalingStream(port uint32) (nabto.StreamDriver, error) {
	return nil, fmt.Errorf("pure-go stream open not yet implemented")
}

// Stream is the Pure-Go implementation of nabto.StreamDriver.
type Stream struct {
	conn net.Conn
	mu   sync.Mutex
}

var _ nabto.StreamDriver = (*Stream)(nil)

func (s *Stream) Abort() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func (s *Stream) Close() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func (s *Stream) ReadMsg() ([]byte, error) {
	if s.conn == nil {
		return nil, io.EOF
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(s.conn, lenBuf[:]); err != nil {
		return nil, err
	}
	msgLen := binary.LittleEndian.Uint32(lenBuf[:])
	if msgLen == 0 || msgLen > (1<<20) {
		return nil, fmt.Errorf("invalid message length %d", msgLen)
	}
	payload := make([]byte, msgLen)
	if _, err := io.ReadFull(s.conn, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *Stream) WriteMsg(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil {
		return io.ErrClosedPipe
	}
	wire := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(wire[:4], uint32(len(payload)))
	copy(wire[4:], payload)

	_, err := s.conn.Write(wire)
	return err
}
