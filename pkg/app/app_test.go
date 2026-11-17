package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
)

func TestBridgeManagerOffline(t *testing.T) {
	bm := NewBridgeManager()

	// When offline (no bridge or xmDriver set)
	assert.Error(t, bm.SetLampState("on"))
	assert.Error(t, bm.SetHighlight(100))
	assert.Error(t, bm.SetHighlightTime(60))
	assert.Error(t, bm.SetLowlight(10))
	assert.Error(t, bm.SetLowlightTime(120))
	assert.Error(t, bm.SetPIRSensitivity(50))
	assert.Error(t, bm.SetLuxThreshold(50))
	assert.NoError(t, bm.SetResolution("1080p")) // nil-safe
	assert.NoError(t, bm.SetSiren(false))        // nil-safe
	assert.Nil(t, bm.GetRecordingProvider())
	assert.Error(t, bm.WriteAudioBackchannel(nil))
}

func TestAppInitValidation(t *testing.T) {
	cfg := config.NewDefaultConfig()
	// Without camera IP, New should fail
	_, err := New(cfg, "test")
	assert.Error(t, err)

	cfg.NabtoConfig.CameraIP = "127.0.0.1"
	cfg.RTSPPort = 8559
	cfg.ONVIFPort = 8009

	app, err := New(cfg, "test")
	require.NoError(t, err)
	assert.NotNil(t, app)
}

func TestAppRunAndGracefulShutdown(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.NabtoConfig.CameraIP = "127.0.0.1"
	cfg.RTSPPort = 8566
	cfg.ONVIFPort = 8016

	app, err := New(cfg, "test")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()

	// Allow server start
	time.Sleep(100 * time.Millisecond)

	// Cancel context to initiate graceful shutdown
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("App did not shut down gracefully within 5s")
	}
}

func TestMultiCameraAppRunAndShutdown(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.RTSPPort = 8576
	cfg.ONVIFPort = 8026
	cfg.Cameras = []config.CameraConfig{
		{
			Name:       "Einfahrt",
			IP:         "127.0.0.1",
			Path:       "einfahrt",
			Type:       "l625",
			Resolution: "1080p",
			AudioCodec: "aac",
			MAC:        "00:0C:43:00:01:02",
		},
		{
			Name:       "Garten",
			IP:         "127.0.0.1",
			Path:       "garten",
			Type:       "l625",
			Resolution: "720p",
			AudioCodec: "pcmu",
			MAC:        "00:0C:43:00:01:03",
		},
	}

	app, err := New(cfg, "test-multicam")
	require.NoError(t, err)
	assert.Equal(t, 2, len(app.cameras))

	// Verify RTSP streams registered
	assert.NotNil(t, app.rtspServer.GetStream("einfahrt"))
	assert.NotNil(t, app.rtspServer.GetStream("garten"))

	// Verify ONVIF channels
	channels := app.onvifServer.Channels()
	assert.Equal(t, 2, len(channels))
	assert.Equal(t, "Einfahrt", channels[0].Name)
	assert.Equal(t, "Garten", channels[1].Name)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Multi-camera App did not shut down gracefully within 5s")
	}
}

func TestAppReusesPrimaryRecordingCacheAndProvider(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.NewDefaultConfig()
	cfg.NabtoConfig.CameraIP = "127.0.0.1"
	cfg.NabtoConfig.KeyPath = filepath.Join(tmpDir, "client.key")
	cfg.CameraType = "l625"
	cfg.RTSPPort = 8586
	cfg.ONVIFPort = 8036
	cfg.CacheDir = filepath.Join(tmpDir, "recordings")
	cfg.CacheRecordings = 5
	cfg.SDCardDownloadRateLimitKBps = 250

	app, err := New(cfg, "test-cache-reuse")
	require.NoError(t, err)
	require.Len(t, app.cameras, 1)

	camProvider, ok := app.cameras[0].getRecProvider().(*storage.CachedRecordingProvider)
	require.True(t, ok, "expected primary camera recording provider to be *storage.CachedRecordingProvider")

	onvifProvider, ok := app.onvifServer.RecordingProvider().(*storage.CachedRecordingProvider)
	require.True(t, ok, "expected ONVIF server recording provider to be *storage.CachedRecordingProvider")

	channels := app.onvifServer.Channels()
	require.Len(t, channels, 1)
	channelProvider, ok := channels[0].RecordingProvider().(*storage.CachedRecordingProvider)
	require.True(t, ok, "expected ONVIF channel recording provider to be *storage.CachedRecordingProvider")

	// Verify exact same provider and cache instances are reused (no per-call re-instantiation)
	assert.Same(t, camProvider, onvifProvider)
	assert.Same(t, camProvider, channelProvider)
	assert.Same(t, camProvider.Cache(), onvifProvider.Cache())
	assert.Same(t, onvifProvider, app.onvifServer.RecordingProvider())
	assert.Equal(t, 250, onvifProvider.RateLimitKBps())

	// Seed a cached recording and an active .tmp file simulating an in-flight download
	now := time.Now().UTC().Truncate(time.Second)
	_, err = camProvider.Cache().Add(context.Background(), storage.RecordingItem{
		ID:              "701",
		StartTime:       now.Add(-time.Minute),
		EndTime:         now.Add(-30 * time.Second),
		DurationSeconds: 30,
		FileName:        "event_701.mp4",
	}, bytes.NewReader([]byte("cached-mp4-payload")))
	require.NoError(t, err)

	activeTmpPath := filepath.Join(cfg.CacheDir, app.cameras[0].config.Path, "702.mp4.tmp")
	require.NoError(t, os.WriteFile(activeTmpPath, []byte("in-flight-download"), 0644))

	handler := app.onvifServer.HTTPHandler()
	require.NotNil(t, handler)

	// 1. REST API query without ?camera= must use the shared cache and NOT wipe active .tmp files
	req := httptest.NewRequest(http.MethodGet, "/api/sdcard/events", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var items []storage.RecordingItem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, "701", items[0].ID)

	// 2. ONVIF Profile G SearchService query must also reuse the shared cache
	soapBody := `<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetRecordingSummary xmlns="http://www.onvif.org/ver10/search/wsdl"/></s:Body></s:Envelope>`
	soapReq := httptest.NewRequest(http.MethodPost, "/onvif/search_service", strings.NewReader(soapBody))
	soapRec := httptest.NewRecorder()
	handler.ServeHTTP(soapRec, soapReq)
	assert.Equal(t, http.StatusOK, soapRec.Code)
	assert.Contains(t, soapRec.Body.String(), "<tt:TotalRecordings>1</tt:TotalRecordings>")

	// Active .tmp file from concurrent download must remain untouched
	_, statErr := os.Stat(activeTmpPath)
	assert.NoError(t, statErr, "active .mp4.tmp file must not be deleted by ONVIF/REST queries")
}

func TestAppDisabledCacheDelegatesDirectly(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.NewDefaultConfig()
	cfg.NabtoConfig.CameraIP = "127.0.0.1"
	cfg.NabtoConfig.KeyPath = filepath.Join(tmpDir, "client.key")
	cfg.CameraType = "l625"
	cfg.RTSPPort = 8587
	cfg.ONVIFPort = 8037
	cfg.CacheDir = filepath.Join(tmpDir, "recordings")
	cfg.CacheRecordings = 0

	app, err := New(cfg, "test-cache-disabled")
	require.NoError(t, err)
	require.Len(t, app.cameras, 1)

	assert.Nil(t, app.cameras[0].getRecProvider())
	assert.Nil(t, app.onvifServer.RecordingProvider())
}

func TestBridgeManager_Reboot(t *testing.T) {
	bm := NewBridgeManager()
	// Offline / no driver: returns nil cleanly
	assert.NoError(t, bm.Reboot())
}
