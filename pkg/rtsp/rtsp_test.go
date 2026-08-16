package rtsp

import (
	"testing"
)

func TestServerStartClose(t *testing.T) {
	// 1. Test AAC Server
	srvAAC, err := NewServer(8556, "test", "aac")
	if err != nil {
		t.Fatalf("Failed to create AAC server: %v", err)
	}
	defer srvAAC.Close()

	if err := srvAAC.Start(); err != nil {
		t.Fatalf("Failed to start AAC server: %v", err)
	}
	srvAAC.Close()

	// 2. Test PCMU Server
	srvPCMU, err := NewServer(8558, "test", "pcmu")
	if err != nil {
		t.Fatalf("Failed to create PCMU server: %v", err)
	}
	defer srvPCMU.Close()

	if err := srvPCMU.Start(); err != nil {
		t.Fatalf("Failed to start PCMU server: %v", err)
	}
}
