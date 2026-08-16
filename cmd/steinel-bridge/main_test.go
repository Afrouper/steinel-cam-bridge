package main

import (
	"os"
	"path/filepath"
	"testing"

	"steinel-cam-bridge/pkg/nabto"

	"github.com/stretchr/testify/assert"
)

func TestLoadHomeAssistantOptions(t *testing.T) {
	tmpDir := t.TempDir()
	optsFile := filepath.Join(tmpDir, "options.json")

	jsonContent := `{
		"camera_ip": "192.168.88.40",
		"qr_code": "did=de-1234567,pid=pr-76543,sct=aabbcc,pairPwd=pass123",
		"resolution": "720p",
		"audio_codec": "pcmu",
		"rtsp_port": 8555,
		"onvif_port": 8001,
		"mqtt_broker": "tcp://192.168.88.10:1883",
		"mqtt_user": "user_test",
		"mqtt_password": "pwd_test",
		"mqtt_topic_prefix": "steinel_test",
		"mqtt_discovery_prefix": "ha_test"
	}`

	err := os.WriteFile(optsFile, []byte(jsonContent), 0644)
	assert.NoError(t, err)

	cfg := &nabto.Config{
		CameraIP: "192.168.1.100",
		KeyPath:  "data/client.key",
	}
	resolution := "1080p"
	audioCodec := "aac"
	mqttBroker := ""
	mqttUser := ""
	mqttPass := ""
	mqttTopic := "steinel"
	mqttDisc := "homeassistant"
	rtspPort := 8554
	onvifPort := 8000

	loadHomeAssistantOptionsFromPath(optsFile, cfg, &resolution, &audioCodec, &mqttBroker, &mqttUser, &mqttPass, &mqttTopic, &mqttDisc, &rtspPort, &onvifPort)

	assert.Equal(t, "192.168.88.40", cfg.CameraIP)
	assert.Equal(t, "de-1234567", cfg.DeviceID)
	assert.Equal(t, "pr-76543", cfg.ProductID)
	assert.Equal(t, "720p", resolution)
	assert.Equal(t, "pcmu", audioCodec)
	assert.Equal(t, 8555, rtspPort)
	assert.Equal(t, 8001, onvifPort)
	assert.Equal(t, "tcp://192.168.88.10:1883", mqttBroker)
	assert.Equal(t, "user_test", mqttUser)
	assert.Equal(t, "pwd_test", mqttPass)
	assert.Equal(t, "steinel_test", mqttTopic)
	assert.Equal(t, "ha_test", mqttDisc)
}
