package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/webrtc"
	"github.com/Afrouper/steinel-cam-bridge/pkg/xiongmai"
)

type mockBridgeController struct {
	bridge   *webrtc.Bridge
	xmDriver *xiongmai.Driver
}

func (m *mockBridgeController) SetBridge(b *webrtc.Bridge) {
	m.bridge = b
}

func (m *mockBridgeController) SetXMDriver(d *xiongmai.Driver) {
	m.xmDriver = d
}

type mockDeviceUpdater struct {
	deviceID  string
	productID string
}

func (m *mockDeviceUpdater) UpdateDeviceInfo(deviceID, productID string) {
	m.deviceID = deviceID
	m.productID = productID
}

func TestSupervisorContextCancellation(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.NabtoConfig.CameraIP = "127.0.0.1"

	ctrl := &mockBridgeController{}
	updater := &mockDeviceUpdater{}

	sup := New(cfg, false, "L 625 CAM SC", nil, ctrl, updater)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Run(ctx)
	}()

	// Cancel after 100ms
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Supervisor did not exit within 5s after context cancellation")
	}
}
