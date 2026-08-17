package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLayer1_CodeDefaults verifies that without options.json, env vars, or CLI flags, default values are set
func TestLayer1_CodeDefaults(t *testing.T) {
	cfg := resolveConfig("", nil)

	assert.Equal(t, "192.168.1.100", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "data/client.key", cfg.NabtoConfig.KeyPath)
	assert.Equal(t, "1080p", cfg.Resolution)
	assert.Equal(t, "aac", cfg.AudioCodec)
	assert.Equal(t, 8554, cfg.RTSPPort)
	assert.Equal(t, "steinel", cfg.RTSPPath)
	assert.Equal(t, 8000, cfg.ONVIFPort)
	assert.False(t, cfg.ResetPairing)
	assert.Equal(t, "", cfg.MQTTBroker)
	assert.Equal(t, "steinel", cfg.MQTTTopic)
	assert.Equal(t, "homeassistant", cfg.MQTTDiscovery)
}

// TestLayer2_ConfigFileOverridesDefaults verifies that options.json overrides Layer 1 defaults
func TestLayer2_ConfigFileOverridesDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	optsFile := filepath.Join(tmpDir, "options.json")

	jsonContent := `{
		"camera_ip": "192.168.88.89",
		"qr_code": "did=de-1234567,pid=pr-76543,sct=aabbcc,pairPwd=pass123",
		"resolution": "720p",
		"audio_codec": "pcmu",
		"rtsp_port": 8555,
		"onvif_port": 8001,
		"reset_pairing": true,
		"mqtt_broker": "tcp://192.168.88.10:1883",
		"mqtt_user": "user_test",
		"mqtt_password": "pwd_test",
		"mqtt_topic_prefix": "steinel_test",
		"mqtt_discovery_prefix": "ha_test"
	}`
	err := os.WriteFile(optsFile, []byte(jsonContent), 0644)
	assert.NoError(t, err)

	cfg := resolveConfig(optsFile, nil)

	assert.Equal(t, "192.168.88.89", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "de-1234567", cfg.NabtoConfig.DeviceID)
	assert.Equal(t, "pr-76543", cfg.NabtoConfig.ProductID)
	assert.Equal(t, "aabbcc", cfg.NabtoConfig.SCT)
	assert.Equal(t, "pass123", cfg.NabtoConfig.PairPwd)
	assert.Equal(t, "720p", cfg.Resolution)
	assert.Equal(t, "pcmu", cfg.AudioCodec)
	assert.Equal(t, 8555, cfg.RTSPPort)
	assert.Equal(t, 8001, cfg.ONVIFPort)
	assert.True(t, cfg.ResetPairing)
	assert.Equal(t, "tcp://192.168.88.10:1883", cfg.MQTTBroker)
	assert.Equal(t, "user_test", cfg.MQTTUser)
	assert.Equal(t, "pwd_test", cfg.MQTTPassword)
	assert.Equal(t, "steinel_test", cfg.MQTTTopic)
	assert.Equal(t, "ha_test", cfg.MQTTDiscovery)
}

// TestLayer3_EnvironmentOverridesConfigFile verifies 12-Factor App principle (Env vars override config files)
func TestLayer3_EnvironmentOverridesConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	optsFile := filepath.Join(tmpDir, "options.json")

	jsonContent := `{
		"camera_ip": "192.168.88.89",
		"resolution": "720p",
		"rtsp_port": 8555
	}`
	err := os.WriteFile(optsFile, []byte(jsonContent), 0644)
	assert.NoError(t, err)

	// Set environment variables
	t.Setenv("CAMERA_IP", "192.168.99.99")
	t.Setenv("RESOLUTION", "360p")
	t.Setenv("RTSP_PORT", "8556")

	cfg := resolveConfig(optsFile, nil)

	// Environment variable overrides options.json
	assert.Equal(t, "192.168.99.99", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "360p", cfg.Resolution)
	assert.Equal(t, 8556, cfg.RTSPPort)
}

// TestLayer4_CLIFlagsOverrideAll verifies POSIX principle (explicit CLI flags override environment & config files)
func TestLayer4_CLIFlagsOverrideAll(t *testing.T) {
	tmpDir := t.TempDir()
	optsFile := filepath.Join(tmpDir, "options.json")

	jsonContent := `{
		"camera_ip": "192.168.88.89",
		"resolution": "720p",
		"rtsp_port": 8555
	}`
	err := os.WriteFile(optsFile, []byte(jsonContent), 0644)
	assert.NoError(t, err)

	// Set environment variables
	t.Setenv("CAMERA_IP", "192.168.99.99")
	t.Setenv("RESOLUTION", "360p")
	t.Setenv("RTSP_PORT", "8556")

	// Set explicit CLI flag set
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("ip", "", "")
	fs.String("res", "", "")
	fs.Int("port", 0, "")

	err = fs.Parse([]string{"-ip", "10.0.0.1", "-res", "1080p", "-port", "9000"})
	assert.NoError(t, err)

	cfg := resolveConfig(optsFile, fs)

	// CLI flags override everything
	assert.Equal(t, "10.0.0.1", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "1080p", cfg.Resolution)
	assert.Equal(t, 9000, cfg.RTSPPort)
}
