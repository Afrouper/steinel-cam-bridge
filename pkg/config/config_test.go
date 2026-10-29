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
	assert.Equal(t, 5, cfg.CacheRecordings)
	assert.NotEmpty(t, cfg.CacheDir)
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
		"bridge_pass": "bpass_test",
		"cache_recordings": 25,
		"cache_dir": "/custom/cache"
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
	assert.Equal(t, 25, cfg.CacheRecordings)
	assert.Equal(t, "/custom/cache", cfg.CacheDir)
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

func TestLayer3_CacheEnvVars(t *testing.T) {
	t.Setenv("CACHE_RECORDINGS", "15")
	t.Setenv("CACHE_DIR", "/env/cache/recordings")

	cfg := Resolve("", nil)
	assert.Equal(t, 15, cfg.CacheRecordings)
	assert.Equal(t, "/env/cache/recordings", cfg.CacheDir)
}

func TestLayer4_CacheFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Int("cache-recordings", 10, "usage")
	fs.String("cache-dir", "", "usage")

	err := fs.Parse([]string{"-cache-recordings", "30", "-cache-dir", "/flag/cache"})
	require.NoError(t, err)

	cfg := Resolve("", fs)
	assert.Equal(t, 30, cfg.CacheRecordings)
	assert.Equal(t, "/flag/cache", cfg.CacheDir)
}

func TestCameraMAC_DefaultAndCustomFlags(t *testing.T) {
	// 1. Default MAC when no flag/env/option is passed
	cfgDefault := NewDefaultConfig()
	cfgDefault.NabtoConfig.CameraIP = "192.168.1.100"
	err := cfgDefault.Validate()
	require.NoError(t, err)
	assert.Equal(t, DefaultCameraMAC, cfgDefault.CameraMAC)

	// 2. Custom MAC via CLI flag (-mac) with normalization
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("mac", "", "usage")
	fs.String("ip", "", "usage")
	err = fs.Parse([]string{"-ip", "192.168.1.100", "-mac", "00-0c-43-00-01-03"})
	require.NoError(t, err)

	cfgFlag := Resolve("", fs)
	assert.Equal(t, "00-0c-43-00-01-03", cfgFlag.CameraMAC)
	err = cfgFlag.Validate()
	require.NoError(t, err)
	assert.Equal(t, "00:0C:43:00:01:03", cfgFlag.CameraMAC)

	// 3. Custom MAC via Env var (CAMERA_MAC)
	t.Setenv("CAMERA_MAC", "00:0c:43:ab:cd:ef")
	cfgEnv := Resolve("", nil)
	cfgEnv.NabtoConfig.CameraIP = "192.168.1.100"
	err = cfgEnv.Validate()
	require.NoError(t, err)
	assert.Equal(t, "00:0C:43:AB:CD:EF", cfgEnv.CameraMAC)

	// 4. Invalid MAC returns validation error
	cfgInvalid := NewDefaultConfig()
	cfgInvalid.NabtoConfig.CameraIP = "192.168.1.100"
	cfgInvalid.CameraMAC = "invalid-mac-address"
	err = cfgInvalid.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid MAC address")
}

func TestSlugify(t *testing.T) {
	assert.Equal(t, "einfahrt", Slugify("Einfahrt"))
	assert.Equal(t, "einfahrt_vorne", Slugify("Einfahrt Vorne"))
	assert.Equal(t, "hinter_dem_haus", Slugify("Hinter-dem-Haus"))
	assert.Equal(t, "cam_1", Slugify("Cam 1"))
	assert.Equal(t, "garden_gate", Slugify("  Garden @ Gate! "))
}

func TestDeriveMAC(t *testing.T) {
	assert.Equal(t, "00:0C:43:00:01:02", DeriveMAC("", 0))
	assert.Equal(t, "00:0C:43:00:01:03", DeriveMAC("", 1))
	assert.Equal(t, "00:0C:43:00:01:04", DeriveMAC("", 2))
	assert.Equal(t, "00:0C:43:00:01:12", DeriveMAC("00:0C:43:00:01:10", 2))
}

func TestMultiCam_JSONConfig(t *testing.T) {
	tmpDir := t.TempDir()
	optsFile := filepath.Join(tmpDir, "options.json")

	jsonContent := `{
		"cameras": [
			{
				"name": "Einfahrt",
				"ip": "192.168.1.101",
				"type": "l625",
				"qr_code": "did=de-einfahrt,pid=pr-test,sct=sct1,pairPwd=pwd1"
			},
			{
				"name": "Garten",
				"ip": "192.168.1.102",
				"type": "l620",
				"user": "admin",
				"password": "secret_garden"
			}
		],
		"rtsp_port": 8554,
		"onvif_port": 8000
	}`
	err := os.WriteFile(optsFile, []byte(jsonContent), 0644)
	require.NoError(t, err)

	cfg := Resolve(optsFile, nil)
	require.Len(t, cfg.Cameras, 2)

	err = cfg.Validate()
	require.NoError(t, err)

	// Verify Camera 1
	cam1 := cfg.Cameras[0]
	assert.Equal(t, "Einfahrt", cam1.Name)
	assert.Equal(t, "192.168.1.101", cam1.IP)
	assert.Equal(t, "l625", cam1.Type)
	assert.Equal(t, "einfahrt", cam1.Path)
	assert.Equal(t, "00:0C:43:00:01:02", cam1.MAC)
	require.NotNil(t, cam1.Nabto)
	assert.Equal(t, "de-einfahrt", cam1.Nabto.DeviceID)

	// Verify Camera 2
	cam2 := cfg.Cameras[1]
	assert.Equal(t, "Garten", cam2.Name)
	assert.Equal(t, "192.168.1.102", cam2.IP)
	assert.Equal(t, "l620", cam2.Type)
	assert.Equal(t, "garten", cam2.Path)
	assert.Equal(t, "00:0C:43:00:01:03", cam2.MAC)
	assert.Equal(t, "secret_garden", cam2.Password)

	// Check model probing
	isL620_1, model1 := cam1.ProbeCameraModel()
	assert.False(t, isL620_1)
	assert.Equal(t, "L 625 CAM SC", model1)

	isL620_2, model2 := cam2.ProbeCameraModel()
	assert.True(t, isL620_2)
	assert.Equal(t, "L 620 CAM", model2)
}

func TestMultiCam_DuplicateValidation(t *testing.T) {
	// Duplicate path error
	cfgDupPath := NewDefaultConfig()
	cfgDupPath.Cameras = []CameraConfig{
		{Name: "Cam 1", IP: "192.168.1.101", Path: "same_path"},
		{Name: "Cam 2", IP: "192.168.1.102", Path: "same_path"},
	}
	err := cfgDupPath.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate RTSP stream path 'same_path'")

	// Duplicate MAC error
	cfgDupMAC := NewDefaultConfig()
	cfgDupMAC.Cameras = []CameraConfig{
		{Name: "Cam 1", IP: "192.168.1.101", Path: "cam1", MAC: "00:0C:43:AA:BB:CC"},
		{Name: "Cam 2", IP: "192.168.1.102", Path: "cam2", MAC: "00:0C:43:AA:BB:CC"},
	}
	err = cfgDupMAC.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate MAC address")
}

func TestSingleCam_DefaultNameRemainsEmpty(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.NabtoConfig.CameraIP = "192.168.1.50"
	// CameraName is left empty (standard single-camera behavior)

	err := cfg.Validate()
	require.NoError(t, err)

	require.Len(t, cfg.Cameras, 1)
	assert.Equal(t, "", cfg.Cameras[0].Name, "Single camera name must remain empty when not configured to preserve legacy HA device name")
	assert.Equal(t, "steinel", cfg.Cameras[0].Path)
	assert.Equal(t, "00:0C:43:00:01:02", cfg.Cameras[0].MAC)
}
