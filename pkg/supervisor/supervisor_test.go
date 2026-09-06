package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/driver"
	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
)

func TestSupervisorWithDriver(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.NabtoConfig.CameraIP = "127.0.0.1"

	// Instantiate mock/concrete L620 driver
	d, err := driver.New(cfg, true, nil, events.NewBus(), nil)
	require.NoError(t, err)

	sup := New(d)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Run(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Supervisor did not exit within 5s")
	}
}

func TestSupervisorNilDriver(t *testing.T) {
	sup := New(nil)
	err := sup.Run(context.Background())
	assert.Equal(t, driver.ErrCameraOffline, err)
}
