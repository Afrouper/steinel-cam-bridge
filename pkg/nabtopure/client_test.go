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

func TestDTLSHandshakeWithNabtoFraming(t *testing.T) {
	// 1. Generate Server ECC Key & Cert
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("failed to generate server key: %v", err)
	}
	serverCert, err := GenerateSelfSignedCert(serverKey)
	if err != nil {
		t.Fatalf("failed to generate server cert: %v", err)
	}

	// 2. Generate Client ECC Key & Cert
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("failed to generate client key: %v", err)
	}
	clientCert, err := GenerateSelfSignedCert(clientKey)
	if err != nil {
		t.Fatalf("failed to generate client cert: %v", err)
	}

	// 3. Setup Virtual Pipe with Nabto 16-byte framing
	pipeCli, pipeSrv := net.Pipe()
	defer func() { _ = pipeCli.Close() }()
	defer func() { _ = pipeSrv.Close() }()

	nabtoCliPacketConn, err := newNabtoPacketConn(&pipePacketConnAdapter{pipeCli})
	if err != nil {
		t.Fatalf("newNabtoPacketConn failed: %v", err)
	}
	nabtoSrvPacketConn := newNabtoServerPacketConn(&pipePacketConnAdapter{pipeSrv})

	//nolint:staticcheck
	serverConfig := &dtls.Config{
		Certificates:         []tls.Certificate{serverCert},
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		ClientAuth:           dtls.RequestClientCert,
		InsecureSkipVerify:   true,
		SupportedProtocols:   []string{"n5"},
	}

	//nolint:staticcheck
	clientConfig := &dtls.Config{
		Certificates:         []tls.Certificate{clientCert},
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		InsecureSkipVerify:   true,
		SupportedProtocols:   []string{"n5"},
	}

	fakeAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5592}

	serverErrChan := make(chan error, 1)
	go func() {
		//nolint:staticcheck
		srvConn, err := dtls.Server(nabtoSrvPacketConn, fakeAddr, serverConfig)
		if err != nil {
			serverErrChan <- err
			return
		}
		defer func() { _ = srvConn.Close() }()

		buf := make([]byte, 1024)
		n, err := srvConn.Read(buf)
		if err != nil {
			serverErrChan <- err
			return
		}
		// Echo back
		_, err = srvConn.Write(buf[:n])
		serverErrChan <- err
	}()

	//nolint:staticcheck
	cliConn, err := dtls.Client(nabtoCliPacketConn, fakeAddr, clientConfig)
	if err != nil {
		t.Fatalf("dtls.Client failed: %v", err)
	}
	defer func() { _ = cliConn.Close() }()

	// Test writing data through DTLS + Nabto framing
	testMsg := []byte("PING_NABTO_COAP")
	if _, err := cliConn.Write(testMsg); err != nil {
		t.Fatalf("cliConn.Write failed: %v", err)
	}

	recvBuf := make([]byte, 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	readDone := make(chan error, 1)
	go func() {
		n, err := cliConn.Read(recvBuf)
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

func TestKeyPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test.key")

	client, err := NewClient(&Config{
		CameraIP:   "127.0.0.1",
		CameraPort: 5592,
		KeyPath:    keyPath,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	key1, isNew, err := client.LoadOrGenerateKey()
	if err != nil || !isNew {
		t.Fatalf("expected new key generated, got isNew=%v err=%v", isNew, err)
	}

	// Verify key file was saved
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key file was not created: %v", err)
	}

	// Reload key
	key2, isNew2, err := client.LoadOrGenerateKey()
	if err != nil || isNew2 {
		t.Fatalf("expected existing key loaded, got isNew=%v err=%v", isNew2, err)
	}

	fp1, _ := ComputeFingerprint(key1)
	fp2, _ := ComputeFingerprint(key2)
	if fp1 != fp2 {
		t.Fatalf("fingerprints do not match: %s vs %s", fp1, fp2)
	}
}
