//go:build !cgo

package nabto

import (
	"fmt"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

func init() {
	Register("cgo", func(cfg *Config) (Driver, error) {
		logger.Error("Nabto", "❌ C-SDK Nabto driver is not supported in non-CGO builds")
		return nil, fmt.Errorf("nabto: C-SDK driver requires CGO support; please recompile with CGO_ENABLED=1 or configure 'nabto_driver: pure'")
	})
}
