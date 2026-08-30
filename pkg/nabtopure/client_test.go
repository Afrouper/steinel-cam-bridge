package nabtopure

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
)

func TestKeyGenerationAndFingerprint(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	cert, err := GenerateSelfSignedCert(key)
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatalf("expected non-empty certificate")
	}

	fp, err := ComputeFingerprint(key)
	if err != nil {
		t.Fatalf("ComputeFingerprint failed: %v", err)
	}
	if len(fp) != 64 {
		t.Fatalf("expected 64 hex characters (SHA256), got %d (%s)", len(fp), fp)
	}
}

func TestDTLSHandshakeMock(t *testing.T) {
	// 1. Generate Server ECC Key & Cert
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("failed to generate server key: %v", err)
	}
	serverCert, err := GenerateSelfSignedCert(serverKey)
	if err != nil {
		t.Fatalf("failed to generate server cert: %v", err)
	}

	// 2. Start mock DTLS 1.2 UDP Server
	serverAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	//nolint:staticcheck
	serverConfig := &dtls.Config{
		Certificates:         []tls.Certificate{serverCert},
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		ClientAuth:           dtls.RequestClientCert,
	}

	//nolint:staticcheck
	listener, err := dtls.Listen("udp", serverAddr, serverConfig)
	if err != nil {
		t.Fatalf("dtls.Listen failed: %v", err)
	}
	defer func() { _ = listener.Close() }()

	actualPort := listener.Addr().(*net.UDPAddr).Port

	serverErrChan := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrChan <- err
			return
		}
		defer func() { _ = conn.Close() }()

		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil {
			serverErrChan <- err
			return
		}
		// Echo back
		_, err = conn.Write(buf[:n])
		serverErrChan <- err
	}()

	// 3. Connect client using nabtopure.Client
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test.key")

	client, err := NewClient(&Config{
		CameraIP:   "127.0.0.1",
		CameraPort: actualPort,
		KeyPath:    keyPath,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	if err := client.Connect(); err != nil {
		t.Fatalf("client.Connect failed: %v", err)
	}

	// Verify key file was saved
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key file was not created: %v", err)
	}

	// Test writing data through DTLS
	testMsg := []byte("PING_NABTO_COAP")
	if _, err := client.dtlsConn.Write(testMsg); err != nil {
		t.Fatalf("dtlsConn.Write failed: %v", err)
	}

	recvBuf := make([]byte, 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	readDone := make(chan error, 1)
	go func() {
		n, err := client.dtlsConn.Read(recvBuf)
		if err == nil && string(recvBuf[:n]) == string(testMsg) {
			readDone <- nil
		} else {
			readDone <- err
		}
	}()

	select {
	case <-ctx.Done():
		t.Fatalf("timeout waiting for DTLS echo response")
	case err := <-readDone:
		if err != nil {
			t.Fatalf("read echo failed: %v", err)
		}
	}

	if err := <-serverErrChan; err != nil {
		t.Fatalf("server encountered error: %v", err)
	}
}
