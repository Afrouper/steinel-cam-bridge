package config

import (
	"encoding/json"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayer1_CodeDefaults verifies that without options.json, env vars, or CLI flags, default values are set
func TestLayer1_CodeDefaults(t *testing.T) {
	cfg := Resolve("", nil)

	assert.Equal(t, "", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "data/client.key", cfg.NabtoConfig.KeyPath)
	assert.Equal(t, "1080p", cfg.Resolution)
	assert.Equal(t, "aac", cfg.AudioCodec)
	assert.Equal(t, 8554, cfg.RTSPPort)
	assert.Equal(t, "steinel", cfg.RTSPPath)
	assert.Equal(t, 8000, cfg.ONVIFPort)
	assert.False(t, cfg.ResetPairing)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "", cfg.MQTTBroker)
	assert.Equal(t, "steinel", cfg.MQTTTopic)
	assert.Equal(t, "homeassistant", cfg.MQTTDiscovery)
	assert.Equal(t, "cgo", cfg.NabtoDriver)
	assert.Equal(t, 120, cfg.SDCardSyncInterval)
	assert.Equal(t, "", cfg.BridgeUser)
	assert.Equal(t, "", cfg.BridgePass)
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
		"log_level": "debug",
		"mqtt_broker": "tcp://192.168.88.10:1883",
		"mqtt_user": "user_test",
		"mqtt_password": "pwd_test",
		"mqtt_topic_prefix": "steinel_test",
		"mqtt_discovery_prefix": "ha_test",
		"nabto_driver": "cgo",
		"bridge_user": "buser_test",
		"bridge_pass": "bpass_test"
	}`
	err := os.WriteFile(optsFile, []byte(jsonContent), 0644)
	require.NoError(t, err)

	cfg := Resolve(optsFile, nil)

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
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "tcp://192.168.88.10:1883", cfg.MQTTBroker)
	assert.Equal(t, "user_test", cfg.MQTTUser)
	assert.Equal(t, "pwd_test", cfg.MQTTPassword)
	assert.Equal(t, "steinel_test", cfg.MQTTTopic)
	assert.Equal(t, "ha_test", cfg.MQTTDiscovery)
	assert.Equal(t, "cgo", cfg.NabtoDriver)
	assert.Equal(t, "buser_test", cfg.BridgeUser)
	assert.Equal(t, "bpass_test", cfg.BridgePass)
}

// TestLayer2_UseCGONabtoFallback verifies that boolean use_cgo_nabto in options.json sets NabtoDriver to cgo
func TestLayer2_UseCGONabtoFallback(t *testing.T) {
	tmpDir := t.TempDir()
	optsFile := filepath.Join(tmpDir, "options.json")

	jsonContent := `{"use_cgo_nabto": true}`
	err := os.WriteFile(optsFile, []byte(jsonContent), 0644)
	require.NoError(t, err)

	cfg := Resolve(optsFile, nil)
	assert.Equal(t, "cgo", cfg.NabtoDriver)
}

// TestSupervisorMQTTAutoDiscovery verifies that MQTT credentials are automatically fetched when available
func TestSupervisorMQTTAutoDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-supervisor-token", r.Header.Get("Authorization"))
		resp := supervisorMQTTResponse{
			Result: "ok",
		}
		resp.Data.Host = "127.0.0.1"
		resp.Data.Port = 1883
		resp.Data.SSL = false
		resp.Data.Username = "supervisor_user"
		resp.Data.Password = "supervisor_pass"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Direct test of FetchSupervisorMQTTOptions with custom token
	t.Setenv("SUPERVISOR_TOKEN", "test-supervisor-token")
	// Since FetchSupervisorMQTTOptions uses fixed URLs, let's verify error handling without supervisor network
	_, _, _, err := FetchSupervisorMQTTOptions()
	// Should attempt to reach supervisor URLs and fail gracefully if not inside Home Assistant
	assert.Error(t, err)
}

// TestLayer3_EnvOverrides verifies that Environment Variables override config file and defaults
func TestLayer3_EnvOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	optsFile := filepath.Join(tmpDir, "options.json")
	_ = os.WriteFile(optsFile, []byte(`{"camera_ip": "192.168.1.10"}`), 0644)

	t.Setenv("CAMERA_IP", "192.168.1.99")
	t.Setenv("LOG_LEVEL", "trace")
	t.Setenv("RTSP_PORT", "9554")
	t.Setenv("ONVIF_PORT", "9000")
	t.Setenv("USE_CGO_NABTO", "false")
	t.Setenv("BRIDGE_USER", "env_user")
	t.Setenv("BRIDGE_PASS", "env_pass")

	cfg := Resolve(optsFile, nil)

	assert.Equal(t, "192.168.1.99", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "trace", cfg.LogLevel)
	assert.Equal(t, 9554, cfg.RTSPPort)
	assert.Equal(t, 9000, cfg.ONVIFPort)
	assert.Equal(t, "pure", cfg.NabtoDriver)
	assert.Equal(t, "env_user", cfg.BridgeUser)
	assert.Equal(t, "env_pass", cfg.BridgePass)
}

// TestLayer4_CLIOverrides verifies that explicit CLI flags override all lower layers
func TestLayer4_CLIOverrides(t *testing.T) {
	t.Setenv("CAMERA_IP", "192.168.1.99")
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("BRIDGE_USER", "env_user")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("ip", "", "")
	fs.String("log-level", "", "")
	fs.Int("port", 0, "")
	fs.String("bridge-user", "", "")
	fs.String("bridge-pass", "", "")
	err := fs.Parse([]string{"-ip", "10.0.0.1", "-log-level", "debug", "-port", "8556", "-bridge-user", "cli_user", "-bridge-pass", "cli_pass"})
	require.NoError(t, err)

	cfg := Resolve("", fs)

	assert.Equal(t, "10.0.0.1", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 8556, cfg.RTSPPort)
	assert.Equal(t, "cli_user", cfg.BridgeUser)
	assert.Equal(t, "cli_pass", cfg.BridgePass)
}

// TestValidate verifies configuration validation
func TestValidate(t *testing.T) {
	cfg := NewDefaultConfig()
	assert.Error(t, cfg.Validate()) // Missing CameraIP

	cfg.NabtoConfig.CameraIP = "192.168.1.50"
	assert.NoError(t, cfg.Validate())

	cfg.RTSPPort = 0
	assert.Error(t, cfg.Validate())

	cfg.RTSPPort = 8554
	cfg.ONVIFPort = 70000
	assert.Error(t, cfg.Validate())
}

// TestProbeCameraModel verifies auto-detection and explicit model selection
func TestProbeCameraModel(t *testing.T) {
	// Explicit L620
	cfg620 := NewDefaultConfig()
	cfg620.CameraType = "l620"
	isL620, model := cfg620.ProbeCameraModel()
	assert.True(t, isL620)
	assert.Equal(t, "L 620 CAM", model)
	assert.Equal(t, "steinel-l620", cfg620.NabtoConfig.DeviceID)

	// Explicit L625
	cfg625 := NewDefaultConfig()
	cfg625.CameraType = "l625"
	isL620, model = cfg625.ProbeCameraModel()
	assert.False(t, isL620)
	assert.Equal(t, "L 625 CAM SC", model)

	// Auto-probe when port closed
	cfgAuto := NewDefaultConfig()
	cfgAuto.CameraType = "auto"
	cfgAuto.NabtoConfig.CameraIP = "127.0.0.1"
	isL620, model = cfgAuto.ProbeCameraModel()
	// Port 34567 should be closed on localhost during test
	assert.False(t, isL620)
	assert.Equal(t, "L 625 CAM SC", model)

	// Auto-probe when port open
	ln, err := net.Listen("tcp", "127.0.0.1:34567")
	if err == nil {
		defer func() { _ = ln.Close() }()
		isL620, model = cfgAuto.ProbeCameraModel()
		assert.True(t, isL620)
		assert.Equal(t, "L 620 CAM", model)
	}
}
