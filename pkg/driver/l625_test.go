package driver_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Afrouper/steinel-cam-bridge/pkg/driver"
)

func TestCalculateBackoff(t *testing.T) {
	tests := []struct {
		failures int
		expected time.Duration
	}{
		{failures: -1, expected: 15 * time.Second},
		{failures: 0, expected: 15 * time.Second},
		{failures: 1, expected: 15 * time.Second},
		{failures: 2, expected: 30 * time.Second},
		{failures: 3, expected: 60 * time.Second},
		{failures: 4, expected: 120 * time.Second},
		{failures: 5, expected: 120 * time.Second},
		{failures: 10, expected: 120 * time.Second},
		{failures: 100, expected: 120 * time.Second},
	}

	for _, tt := range tests {
		actual := driver.CalculateBackoff(tt.failures)
		assert.Equal(t, tt.expected, actual, "CalculateBackoff(%d)", tt.failures)
	}
}

func TestProbeNetworkHealth(t *testing.T) {
	// 1. Empty IP should safely no-op
	driver.ProbeNetworkHealth(t.Context(), "")

	// 2. Loopback IP should succeed
	driver.ProbeNetworkHealth(t.Context(), "127.0.0.1")

	// 3. Unreachable IP should log warning without panicking
	driver.ProbeNetworkHealth(t.Context(), "192.0.2.1")
}
