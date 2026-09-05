package nabto

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDriver implements Driver for testing.
type mockDriver struct {
	name string
}

func (m *mockDriver) DriverName() string                                    { return m.name }
func (m *mockDriver) Connect() error                                        { return nil }
func (m *mockDriver) Close()                                                {}
func (m *mockDriver) GetSignalingPort() (uint32, error)                     { return 0, nil }
func (m *mockDriver) RequestTracks() (uint16, error)                        { return 0, nil }
func (m *mockDriver) OpenSignalingStream(port uint32) (StreamDriver, error) { return nil, nil }

func TestResolveDriverType(t *testing.T) {
	// Standard resolution
	assert.Equal(t, "cgo", ResolveDriverType(""))
	assert.Equal(t, "cgo", ResolveDriverType("cgo"))
	assert.Equal(t, "cgo", ResolveDriverType("CGO"))
	assert.Equal(t, "cgo", ResolveDriverType("auto"))
	assert.Equal(t, "cgo", ResolveDriverType("sdk"))
	assert.Equal(t, "pure", ResolveDriverType("pure"))
	assert.Equal(t, "pure", ResolveDriverType("pure-go"))
	assert.Equal(t, "pure", ResolveDriverType("native"))
	assert.Equal(t, "custom", ResolveDriverType(" Custom "))

	// Environment variable overrides
	orig := os.Getenv("USE_CGO_NABTO")
	defer func() {
		if orig != "" {
			_ = os.Setenv("USE_CGO_NABTO", orig)
		} else {
			_ = os.Unsetenv("USE_CGO_NABTO")
		}
	}()

	_ = os.Setenv("USE_CGO_NABTO", "false")
	assert.Equal(t, "pure", ResolveDriverType("cgo"))

	_ = os.Setenv("USE_CGO_NABTO", "0")
	assert.Equal(t, "pure", ResolveDriverType(""))
}

func TestRegisterAndNew(t *testing.T) {
	mockFactory := func(cfg *Config) (Driver, error) {
		return &mockDriver{name: "test-mock"}, nil
	}

	Register("test-mock", mockFactory)

	// Verify duplicate panic
	assert.Panics(t, func() {
		Register("test-mock", mockFactory)
	})

	// Verify Drivers() list
	drivers := Drivers()
	assert.Contains(t, drivers, "test-mock")

	// Instantiate through New
	drv, err := New("test-mock", &Config{})
	require.NoError(t, err)
	require.NotNil(t, drv)
	assert.Equal(t, "test-mock", drv.DriverName())

	// Unknown driver
	_, err = New("nonexistent-driver", &Config{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unregistered driver")
}
