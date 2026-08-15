package rtsp

import (
	"testing"
)

func TestServerStartClose(t *testing.T) {
	srv, err := NewServer(8556, "test")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer srv.Close()

	if err := srv.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
}
