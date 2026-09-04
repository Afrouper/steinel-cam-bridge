package nabtopure

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
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

// Config is an alias to nabto.Config.
type Config = nabto.Config

// Client is the Pure-Go implementation of the Nabto Edge client driver.
type Client struct {
	cfg           *Config
	privateKey    *ecdsa.PrivateKey
	dtlsConn      *dtls.Conn
	udpConn       net.PacketConn
	coapClient    *CoAPClient
	currentStream *Stream
	readerClose   chan struct{}
	mu            sync.Mutex
	writeMu       sync.Mutex
	closeOnce     sync.Once
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

	c.closeOnce = sync.Once{}

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
	c.readerClose = make(chan struct{})
	go c.packetReaderLoop()
	go c.keepAliveLoop()

	// Auto-discover ProductID and DeviceID via CoAP /iam/pairing if not configured
	if c.cfg.DeviceID == "" || c.cfg.ProductID == "" {
		req := NewRequest(CodeGET, "/iam/pairing", 0, nil)
		if resp, err := c.coapClient.Execute(req, 2*time.Second); err == nil && (resp.StatusCode() == 205 || resp.StatusCode() == 200) {
			pid, did := extractPairingIDs(resp.Payload)
			if did != "" && c.cfg.DeviceID == "" {
				c.cfg.DeviceID = did
			}
			if pid != "" && c.cfg.ProductID == "" {
				c.cfg.ProductID = pid
			}
		}
	}

	log.Printf("[NabtoPure] 📷 Camera Connected: DeviceID=%s | ProductID=%s", c.cfg.DeviceID, c.cfg.ProductID)

	return nil
}

func extractPairingIDs(payload []byte) (productId, deviceId string) {
	str := string(payload)
	if idx := strings.Index(str, "ProductId"); idx >= 0 {
		sub := str[idx+9:]
		for i := 0; i+11 <= len(sub); i++ {
			if strings.HasPrefix(sub[i:], "pr-") {
				productId = sub[i : i+11]
				break
			}
		}
	}
	if idx := strings.Index(str, "DeviceId"); idx >= 0 {
		sub := str[idx+8:]
		for i := 0; i+11 <= len(sub); i++ {
			if strings.HasPrefix(sub[i:], "de-") {
				deviceId = sub[i : i+11]
				break
			}
		}
	}
	return
}

func (c *Client) writeDTLS(data []byte, timeout time.Duration) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	conn := c.dtlsConn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("connection closed")
	}

	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := conn.Write(data)
	return err
}

func (c *Client) keepAliveLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.readerClose:
			return
		case <-ticker.C:
			kaReq := make([]byte, 18)
			kaReq[0] = 0x04
			kaReq[1] = 0x01 // CT_KEEP_ALIVE_REQUEST
			if err := c.writeDTLS(kaReq, 1*time.Second); err != nil {
				return
			}
		}
	}
}

func (c *Client) packetReaderLoop() {
	buf := make([]byte, 4096)
	for {
		c.mu.Lock()
		conn := c.dtlsConn
		c.mu.Unlock()

		if conn == nil {
			return
		}

		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if c.cfg.Debug {
				log.Printf("[NabtoPure] ⚠️ DTLS connection ended: %v", err)
			}
			c.Close()
			return
		}
		if n == 0 {
			continue
		}

		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		firstByte := pkt[0]
		if firstByte >= 0x40 && firstByte <= 0x7F {
			// CoAP packet
			c.mu.Lock()
			coap := c.coapClient
			c.mu.Unlock()
			if coap != nil {
				coap.HandleIncomingPacket(pkt)
			}
		} else if firstByte == StreamAppDataType {
			// Stream packet (0x05)
			c.mu.Lock()
			stream := c.currentStream
			c.mu.Unlock()
			if stream != nil {
				stream.HandleIncomingPacket(pkt)
			}
		} else if firstByte == 0x04 {
			// KeepAlive: 0x04, type (0x01 = req, 0x02 = resp), payload (16 bytes)
			if len(pkt) >= 2 && pkt[1] == 0x01 {
				resp := make([]byte, len(pkt))
				copy(resp, pkt)
				resp[1] = 0x02 // CT_KEEP_ALIVE_RESPONSE
				_ = c.writeDTLS(resp, 1*time.Second)
			}
		}
	}
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

// Close closes the underlying DTLS and network connections cleanly.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		// 1. Atomically take connection references and clear them
		c.mu.Lock()
		if c.readerClose != nil {
			close(c.readerClose)
			c.readerClose = nil
		}
		stream := c.currentStream
		c.currentStream = nil
		coap := c.coapClient
		c.coapClient = nil
		conn := c.dtlsConn
		c.dtlsConn = nil
		udp := c.udpConn
		c.udpConn = nil
		c.mu.Unlock()

		// 2. Abort active stream
		if stream != nil {
			stream.Close()
		}

		// 3. Abort pending CoAP calls
		if coap != nil {
			coap.Close()
		}

		// 4. Cleanly terminate DTLS connection if open (send close_notify with 500ms deadline)
		if conn != nil {
			_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
			_ = conn.Close()
		}

		// 5. Unblock any pending socket reads immediately by setting past deadline, then close UDP
		if udp != nil {
			_ = udp.SetDeadline(time.Now())
			_ = udp.Close()
		}
	})
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
		log.Printf("[NabtoPure] ❌ CoAP /p2p/webrtc-info request failed: %v", err)
		return 0, fmt.Errorf("CoAP /p2p/webrtc-info failed: %w", err)
	}

	if resp.StatusCode() != 205 && resp.StatusCode() != 200 {
		log.Printf("[NabtoPure] ⚠️ CoAP /p2p/webrtc-info returned unexpected status: %s", resp.StatusString())
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

	log.Printf("[NabtoPure] ❌ Could not parse SignalingStreamPort from camera response: %s", respStr)
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
		log.Printf("[NabtoPure] ❌ CoAP /webrtc/tracks failed: %v", err)
		return 0, fmt.Errorf("CoAP /webrtc/tracks failed: %w", err)
	}

	log.Printf("[NabtoPure] 🎥 CoAP /webrtc/tracks response status: %s", resp.StatusString())
	return uint16(resp.StatusCode()), nil
}

// OpenSignalingStream opens a virtual Nabto streaming channel over the DTLS connection.
func (c *Client) OpenSignalingStream(port uint32) (nabto.StreamDriver, error) {
	c.mu.Lock()
	if c.dtlsConn == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("client not connected")
	}
	stream := NewStream(c, 0, port)
	c.currentStream = stream
	c.mu.Unlock()

	if err := stream.Open(10 * time.Second); err != nil {
		log.Printf("[NabtoPure] ❌ Failed to open virtual signaling stream on port %d: %v", port, err)
		return nil, err
	}
	return stream, nil
}

func (c *Client) writeRawStream(data []byte) error {
	return c.writeDTLS(data, 5*time.Second)
}
