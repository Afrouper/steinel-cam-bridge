package main

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppVersionDefault(t *testing.T) {
	assert.NotEmpty(t, AppVersion)
}

func TestFlagsRegistered(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	registerFlags(fs)

	flags := []string{
		"qr", "ip", "type", "user", "pass", "key", "res",
		"port", "path", "onvif", "reset-pairing",
		"mqtt-broker", "mqtt-user", "mqtt-pass", "mqtt-topic", "mqtt-disc",
		"audio-codec", "sync-interval", "sdcard-sync-interval",
		"nabto-driver", "use-cgo", "log-level", "beta",
	}

	for _, name := range flags {
		f := fs.Lookup(name)
		assert.NotNil(t, f, "Expected flag -%s to be defined", name)
	}
}
