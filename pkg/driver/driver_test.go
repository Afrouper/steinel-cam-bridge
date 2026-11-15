package driver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/driver"
	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
)

func TestFactoryInstantiation(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.NabtoConfig.CameraIP = "127.0.0.1"

	bus := events.NewBus()

	// L620
	d620, err := driver.New(cfg, true, nil, bus, nil)
	require.NoError(t, err)
	assert.NotNil(t, d620)
	assert.Implements(t, (*driver.CameraDriver)(nil), d620)

	// L625
	d625, err := driver.New(cfg, false, nil, bus, nil)
	require.NoError(t, err)
	assert.NotNil(t, d625)
	assert.Implements(t, (*driver.CameraDriver)(nil), d625)
}

func TestSetLowlightTime_Interface(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.NabtoConfig.CameraIP = "127.0.0.1"
	bus := events.NewBus()

	d620, err := driver.New(cfg, true, nil, bus, nil)
	require.NoError(t, err)

	// L620 offline will return error from client, but must not panic or crash
	_ = d620.SetLowlightTime(4)
	_ = d620.SetLowlightTime(12)
	_ = d620.SetLowlightTime(0)
	_ = d620.SetLowlightTime(14400) // seconds fallback
	_ = d620.SetLowlightTime(240)   // minutes fallback

	d625, err := driver.New(cfg, false, nil, bus, nil)
	require.NoError(t, err)

	// L625 offline returns ErrCameraOffline safely
	assert.ErrorIs(t, d625.SetLowlightTime(4), driver.ErrCameraOffline)
	assert.ErrorIs(t, d625.SetLowlightTime(12), driver.ErrCameraOffline)
	assert.ErrorIs(t, d625.SetLowlightTime(0), driver.ErrCameraOffline)
	assert.ErrorIs(t, d625.SetLowlightTime(43200), driver.ErrCameraOffline)
}
