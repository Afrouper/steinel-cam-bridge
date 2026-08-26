package webrtc

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSDCardGetEventList(t *testing.T) {
	var mu sync.Mutex
	var sentCmd string
	var sentInfo map[string]interface{}

	sdm := NewSDCardManager(func(cmd string, info map[string]interface{}) error {
		mu.Lock()
		sentCmd = cmd
		sentInfo = info
		mu.Unlock()
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run GetEventList in background
	errChan := make(chan error, 1)
	var result *EventListResponse

	go func() {
		res, err := sdm.GetEventList(ctx, 1723800000, 1723886400, 1, 20)
		result = res
		errChan <- err
	}()

	// Wait for command to be sent
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	cmdVal := sentCmd
	infoVal := sentInfo
	mu.Unlock()

	assert.Equal(t, "get_event_list", cmdVal)
	assert.Equal(t, float64(1), float64(infoVal["page"].(int)))
	assert.Equal(t, float64(20), float64(infoVal["limit"].(int)))

	// Simulate Camera responding with list
	camResp := map[string]interface{}{
		"resp": "get_event_list",
		"info": map[string]interface{}{
			"count": float64(2),
			"total": float64(2),
			"list": []interface{}{
				map[string]interface{}{
					"time_tag":    float64(1723812345),
					"record_time": float64(30),
					"type":        "pir",
					"name":        "20260816_201500.mp4",
				},
				map[string]interface{}{
					"time_tag":    float64(1723820000),
					"record_time": float64(45),
					"type":        "manual",
					"name":        "20260816_203000.mp4",
				},
			},
		},
	}

	handled := sdm.HandleJSONMessage(camResp)
	assert.True(t, handled)

	err := <-errChan
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, result.Count)
	assert.Equal(t, 2, len(result.List))
	assert.Equal(t, int64(1723812345), result.List[0].TimeTag)
	assert.Equal(t, 30, result.List[0].RecordTime)
	assert.Equal(t, "pir", result.List[0].Type)
	assert.Equal(t, "20260816_201500.mp4", result.List[0].Name)
}

func TestSDCardStreamVideo(t *testing.T) {
	sdm := NewSDCardManager(func(_ string, _ map[string]interface{}) error {
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var buf bytes.Buffer
	var startName string
	var startSize int64
	doneChan := make(chan error, 1)

	go func() {
		err := sdm.StreamVideo(ctx, "1723812345", &buf, func(name string, size int64) {
			startName = name
			startSize = size
		})
		doneChan <- err
	}()

	time.Sleep(50 * time.Millisecond)

	// 1. Camera sends start
	sdm.HandleJSONMessage(map[string]interface{}{
		"resp":   "get_event_video",
		"action": "start",
		"info": map[string]interface{}{
			"name":     "test_video.mp4",
			"filesize": float64(1024),
		},
	})

	// 2. Camera sends binary chunks
	chunk1 := []byte("hello-video-chunk-1-")
	chunk2 := []byte("hello-video-chunk-2")
	sdm.HandleBinaryChunk(chunk1)
	sdm.HandleBinaryChunk(chunk2)

	time.Sleep(50 * time.Millisecond)

	// 3. Camera sends end
	sdm.HandleJSONMessage(map[string]interface{}{
		"resp":   "get_event_video",
		"action": "end",
	})

	err := <-doneChan
	require.NoError(t, err)
	assert.Equal(t, "test_video.mp4", startName)
	assert.Equal(t, int64(1024), startSize)
	assert.Equal(t, "hello-video-chunk-1-hello-video-chunk-2", buf.String())
}

func TestSDCardConcurrencyLock(t *testing.T) {
	sdm := NewSDCardManager(func(_ string, _ map[string]interface{}) error {
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var buf1 bytes.Buffer
	transfer1Running := make(chan struct{})

	go func() {
		_ = sdm.StreamVideo(ctx, "1723812345", &buf1, func(_ string, _ int64) {
			close(transfer1Running)
		})
	}()

	// Wait for transfer 1 to start
	time.Sleep(30 * time.Millisecond)
	sdm.HandleJSONMessage(map[string]interface{}{
		"resp":   "get_event_video",
		"action": "start",
		"info": map[string]interface{}{
			"name":     "v1.mp4",
			"filesize": float64(500),
		},
	})
	sdm.HandleBinaryChunk([]byte("data"))

	// Try to start a concurrent second transfer -> must fail with ErrSDCardBusy
	var buf2 bytes.Buffer
	err2 := sdm.StreamVideo(ctx, "1723899999", &buf2, nil)
	assert.ErrorIs(t, err2, ErrSDCardBusy)

	// End transfer 1
	sdm.HandleJSONMessage(map[string]interface{}{
		"resp":   "get_event_video",
		"action": "end",
	})
}

func TestSDCardClientAbort(t *testing.T) {
	var mu sync.Mutex
	var lastCmd string
	var lastAction string

	sdm := NewSDCardManager(func(cmd string, info map[string]interface{}) error {
		mu.Lock()
		lastCmd = cmd
		if act, ok := info["action"].(string); ok {
			lastAction = act
		}
		mu.Unlock()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	var buf bytes.Buffer
	errChan := make(chan error, 1)

	go func() {
		err := sdm.StreamVideo(ctx, "1723812345", &buf, nil)
		errChan <- err
	}()

	time.Sleep(30 * time.Millisecond)

	// Client cancels context (e.g. browser disconnect)
	cancel()

	err := <-errChan
	assert.ErrorIs(t, err, ErrTransferAborted)

	mu.Lock()
	cmdVal := lastCmd
	actVal := lastAction
	mu.Unlock()

	assert.Equal(t, "get_event_video", cmdVal)
	assert.Equal(t, "stop", actVal)
}

func TestSDCardRecordingProvider(t *testing.T) {
	sdm := NewSDCardManager(func(cmd string, info map[string]interface{}) error {
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		time.Sleep(50 * time.Millisecond)
		sdm.HandleJSONMessage(map[string]interface{}{
			"resp": "get_event_list",
			"info": map[string]interface{}{
				"count": float64(1),
				"total": float64(1),
				"list": []interface{}{
					map[string]interface{}{
						"time_tag":    float64(1723812345),
						"record_time": float64(30),
						"type":        "motion",
						"name":        "event_1723812345.mp4",
						"filesize":    float64(4096),
					},
				},
			},
		})
	}()

	resp, err := sdm.ListRecordings(ctx, time.Unix(1723800000, 0), time.Unix(1723900000, 0), 0, 10, "")
	assert.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, resp.Count)
	assert.Equal(t, "1723812345", resp.List[0].ID)
	assert.Equal(t, "/api/sdcard/events/1723812345/video.mp4", resp.List[0].VideoURL)
	assert.Equal(t, "/api/sdcard/events/1723812345/thumbnail.jpg", resp.List[0].ThumbnailURL)

	rec, err := sdm.GetRecording(ctx, "1723812345")
	assert.NoError(t, err)
	assert.Equal(t, "1723812345", rec.ID)
}
