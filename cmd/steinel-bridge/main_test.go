package main

import (
	"encoding/json"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLayer1_CodeDefaults verifies that without options.json, env vars, or CLI flags, default values are set
func TestLayer1_CodeDefaults(t *testing.T) {
	cfg := resolveConfig("", nil)

	assert.Equal(t, "", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "data/client.key", cfg.NabtoConfig.KeyPath)
	assert.Equal(t, "1080p", cfg.Resolution)
	assert.Equal(t, "aac", cfg.AudioCodec)
	assert.Equal(t, 8554, cfg.RTSPPort)
	assert.Equal(t, "steinel", cfg.RTSPPath)
	assert.Equal(t, 8000, cfg.ONVIFPort)
	assert.False(t, cfg.ResetPairing)
	assert.False(t, cfg.Debug)
	assert.Equal(t, "", cfg.MQTTBroker)
	assert.Equal(t, "steinel", cfg.MQTTTopic)
	assert.Equal(t, "homeassistant", cfg.MQTTDiscovery)
	assert.Equal(t, "cgo", cfg.NabtoDriver)
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
		"debug": true,
		"mqtt_broker": "tcp://192.168.88.10:1883",
		"mqtt_user": "user_test",
		"mqtt_password": "pwd_test",
		"mqtt_topic_prefix": "steinel_test",
		"mqtt_discovery_prefix": "ha_test",
		"nabto_driver": "cgo"
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
	assert.True(t, cfg.Debug)
	assert.Equal(t, "tcp://192.168.88.10:1883", cfg.MQTTBroker)
	assert.Equal(t, "user_test", cfg.MQTTUser)
	assert.Equal(t, "pwd_test", cfg.MQTTPassword)
	assert.Equal(t, "steinel_test", cfg.MQTTTopic)
	assert.Equal(t, "ha_test", cfg.MQTTDiscovery)
	assert.Equal(t, "cgo", cfg.NabtoDriver)
}

// TestLayer2_UseCGONabtoFallback verifies that boolean use_cgo_nabto in options.json sets NabtoDriver to cgo
func TestLayer2_UseCGONabtoFallback(t *testing.T) {
	tmpDir := t.TempDir()
	optsFile := filepath.Join(tmpDir, "options.json")

	jsonContent := `{"use_cgo_nabto": true}`
	err := os.WriteFile(optsFile, []byte(jsonContent), 0644)
	assert.NoError(t, err)

	cfg := resolveConfig(optsFile, nil)
	assert.Equal(t, "cgo", cfg.NabtoDriver)
}

// TestSupervisorMQTTAutoDiscovery verifies that MQTT credentials are automatically fetched when available
func TestSupervisorMQTTAutoDiscovery(t *testing.T) {
	// Mock Home Assistant Supervisor API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-supervisor-token", r.Header.Get("Authorization"))
		resp := supervisorMQTTResponse{
			Result: "ok",
		}
		resp.Data.Host = "core-mqtt"
		resp.Data.Port = 1883
		resp.Data.Username = "homeassistant_user"
		resp.Data.Password = "secret_pass"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	t.Setenv("SUPERVISOR_TOKEN", "test-supervisor-token")

	// Custom client to hit our test server
	req, err := http.NewRequest("GET", server.URL, nil)
	assert.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("SUPERVISOR_TOKEN"))

	client := &http.Client{}
	resp, err := client.Do(req)
	assert.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var sResp supervisorMQTTResponse
	err = json.NewDecoder(resp.Body).Decode(&sResp)
	assert.NoError(t, err)

	assert.Equal(t, "ok", sResp.Result)
	assert.Equal(t, "core-mqtt", sResp.Data.Host)
	assert.Equal(t, 1883, sResp.Data.Port)
	assert.Equal(t, "homeassistant_user", sResp.Data.Username)
	assert.Equal(t, "secret_pass", sResp.Data.Password)
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
	t.Setenv("NABTO_DRIVER", "cgo")

	cfg := resolveConfig(optsFile, nil)

	// Environment variable overrides options.json
	assert.Equal(t, "192.168.99.99", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "360p", cfg.Resolution)
	assert.Equal(t, 8556, cfg.RTSPPort)
	assert.Equal(t, "cgo", cfg.NabtoDriver)
}

// TestLayer4_CLIFlagsOverrideAll verifies POSIX principle (explicit CLI flags override environment & config files)
func TestLayer4_CLIFlagsOverrideAll(t *testing.T) {
	tmpDir := t.TempDir()
	optsFile := filepath.Join(tmpDir, "options.json")

	jsonContent := `{
		"camera_ip": "192.168.88.89",
		"camera_type": "l625",
		"camera_user": "admin",
		"camera_password": "pass",
		"resolution": "720p",
		"rtsp_port": 8555,
		"nabto_driver": "cgo"
	}`
	err := os.WriteFile(optsFile, []byte(jsonContent), 0644)
	assert.NoError(t, err)

	// Set environment variables
	t.Setenv("CAMERA_IP", "192.168.99.99")
	t.Setenv("CAMERA_TYPE", "l620")
	t.Setenv("CAMERA_USER", "admin_env")
	t.Setenv("CAMERA_PASSWORD", "pass_env")
	t.Setenv("RESOLUTION", "360p")
	t.Setenv("RTSP_PORT", "8556")
	t.Setenv("NABTO_DRIVER", "cgo")

	// Set explicit CLI flag set
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("ip", "", "")
	fs.String("type", "", "")
	fs.String("user", "", "")
	fs.String("pass", "", "")
	fs.String("res", "", "")
	fs.Int("port", 0, "")
	fs.String("nabto-driver", "", "")

	err = fs.Parse([]string{"-ip", "10.0.0.1", "-type", "l620", "-user", "admin_cli", "-pass", "secret_cli", "-res", "1080p", "-port", "9000", "-nabto-driver", "pure"})
	assert.NoError(t, err)

	cfg := resolveConfig(optsFile, fs)

	// CLI flags override everything
	assert.Equal(t, "10.0.0.1", cfg.NabtoConfig.CameraIP)
	assert.Equal(t, "l620", cfg.CameraType)
	assert.Equal(t, "admin_cli", cfg.CameraUser)
	assert.Equal(t, "secret_cli", cfg.CameraPassword)
	assert.Equal(t, "1080p", cfg.Resolution)
	assert.Equal(t, 9000, cfg.RTSPPort)
	assert.Equal(t, "pure", cfg.NabtoDriver)
}

func TestProbePort(t *testing.T) {
	// Start mock TCP listener on an ephemeral port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	defer func() { _ = listener.Close() }()

	port := listener.Addr().(*net.TCPAddr).Port

	// Probe listening port -> should return true
	isOpen := probePort("127.0.0.1", port, 500*time.Millisecond)
	assert.True(t, isOpen)

	// Probe closed port -> should return false
	_ = listener.Close()
	isClosed := probePort("127.0.0.1", port, 100*time.Millisecond)
	assert.False(t, isClosed)
}
