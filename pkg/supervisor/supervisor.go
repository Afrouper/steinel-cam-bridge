package supervisor

import (
	"context"

	"github.com/Afrouper/steinel-cam-bridge/pkg/driver"
)

// Supervisor oversees the lifecycle and execution of an active CameraDriver.
type Supervisor struct {
	driver driver.CameraDriver
}

// New creates a new generic Supervisor for any CameraDriver implementation.
func New(d driver.CameraDriver) *Supervisor {
	return &Supervisor{
		driver: d,
	}
}

// Run executes the camera driver lifecycle until the context is canceled.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.driver == nil {
		return driver.ErrCameraOffline
	}
	return s.driver.Run(ctx)
}
