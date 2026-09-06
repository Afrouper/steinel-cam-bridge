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
