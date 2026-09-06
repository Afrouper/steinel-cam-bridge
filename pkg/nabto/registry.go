package nabto

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// DriverFactory is a function that creates a new Driver instance for a given Config.
type DriverFactory func(cfg *Config) (Driver, error)

var (
	driversMu sync.RWMutex
	drivers   = make(map[string]DriverFactory)
)

// Register registers a Nabto driver backend by name (case-insensitive).
func Register(name string, factory DriverFactory) {
	driversMu.Lock()
	defer driversMu.Unlock()
	if factory == nil {
		panic("nabto: Register driver is nil")
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if _, dup := drivers[key]; dup {
		panic("nabto: Register called twice for driver " + name)
	}
	drivers[key] = factory
}

// Drivers returns a list of the names of the registered drivers.
func Drivers() []string {
	driversMu.RLock()
	defer driversMu.RUnlock()
	list := make([]string, 0, len(drivers))
	for name := range drivers {
		list = append(list, name)
	}
	return list
}

// ResolveDriverType returns the effective driver type based on config and env vars.
func ResolveDriverType(configured string) string {
	if os.Getenv("USE_CGO_NABTO") == "false" || os.Getenv("USE_CGO_NABTO") == "0" {
		return "pure"
	}
	trimmed := strings.ToLower(strings.TrimSpace(configured))
	switch trimmed {
	case "pure", "pure-go", "native":
		return "pure"
	case "cgo", "c", "sdk", "auto", "":
		return "cgo"
	default:
		return trimmed
	}
}

// New creates and initializes a Nabto Driver instance according to driverType.
func New(driverType string, cfg *Config) (Driver, error) {
	resolved := ResolveDriverType(driverType)
	driversMu.RLock()
	factory, ok := drivers[resolved]
	driversMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("nabto: unknown or unregistered driver '%s' (configured: '%s')", resolved, driverType)
	}
	return factory(cfg)
}
