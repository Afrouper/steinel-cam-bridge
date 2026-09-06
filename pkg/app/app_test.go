package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
)

func TestBridgeManagerOffline(t *testing.T) {
	bm := NewBridgeManager()

	// When offline (no bridge or xmDriver set)
	assert.Error(t, bm.SetLampState("on"))
	assert.Error(t, bm.SetHighlight(100))
	assert.Error(t, bm.SetHighlightTime(60))
	assert.Error(t, bm.SetLowlight(10))
	assert.Error(t, bm.SetLowlightTime(120))
	assert.Error(t, bm.SetPIRSensitivity(50))
	assert.Error(t, bm.SetLuxThreshold(50))
	assert.NoError(t, bm.SetResolution("1080p")) // nil-safe
	assert.NoError(t, bm.SetSiren(false))        // nil-safe
	assert.Nil(t, bm.GetRecordingProvider())
	assert.Error(t, bm.WriteAudioBackchannel(nil))
}

func TestAppInitValidation(t *testing.T) {
	cfg := config.NewDefaultConfig()
	// Without camera IP, New should fail
	_, err := New(cfg, "test")
	assert.Error(t, err)

	cfg.NabtoConfig.CameraIP = "127.0.0.1"
	cfg.RTSPPort = 8559
	cfg.ONVIFPort = 8009

	app, err := New(cfg, "test")
	require.NoError(t, err)
	assert.NotNil(t, app)
}

func TestAppRunAndGracefulShutdown(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.NabtoConfig.CameraIP = "127.0.0.1"
	cfg.RTSPPort = 8566
	cfg.ONVIFPort = 8016

	app, err := New(cfg, "test")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()

	// Allow server start
	time.Sleep(100 * time.Millisecond)

	// Cancel context to initiate graceful shutdown
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("App did not shut down gracefully within 5s")
	}
}
