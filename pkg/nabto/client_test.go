package nabto

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_CloseDuringConnect(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test.key")

	cfg := &Config{
		CameraIP:   "127.0.0.1",
		CameraPort: 54321, // non-listening port
		KeyPath:    keyPath,
	}

	client, err := NewClient(cfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	connectErrCh := make(chan error, 1)
	go func() {
		connectErrCh <- client.Connect()
	}()

	// Give connect a brief moment to start and register c.conn
	time.Sleep(50 * time.Millisecond)

	closeDone := make(chan struct{})
	go func() {
		client.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
		// Success: Close() returned cleanly without deadlocking!
	case <-time.After(3 * time.Second):
		t.Fatal("Deadlock detected: client.Close() did not return within 3s during in-flight Connect()")
	}

	select {
	case err := <-connectErrCh:
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Connect goroutine did not terminate after Close()")
	}
}

func TestClient_CloseBeforeConnect(t *testing.T) {
	client, err := NewClient(&Config{CameraIP: "127.0.0.1"})
	require.NoError(t, err)

	client.Close()
	err = client.Connect()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}
