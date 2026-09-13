package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockUpstreamProvider implements RecordingProvider for testing
type mockUpstreamProvider struct {
	listCalls  int32
	videoCalls int32
	recordings []RecordingItem
	videoData  map[string][]byte
	listErr    error
	streamErr  error
}

func (m *mockUpstreamProvider) ListRecordings(ctx context.Context, start, end time.Time, page, limit int, eventType string) (*RecordingListResponse, error) {
	atomic.AddInt32(&m.listCalls, 1)
	if m.listErr != nil {
		return nil, m.listErr
	}
	return &RecordingListResponse{
		Count: len(m.recordings),
		Total: len(m.recordings),
		List:  m.recordings,
	}, nil
}

func (m *mockUpstreamProvider) GetRecording(ctx context.Context, id string) (*RecordingItem, error) {
	for _, r := range m.recordings {
		if r.ID == id {
			return &r, nil
		}
	}
	return nil, ErrStorageNotFound
}

func (m *mockUpstreamProvider) StreamThumbnail(ctx context.Context, id string, w io.Writer) error {
	return ErrFeatureDisabled
}

func (m *mockUpstreamProvider) StreamVideo(ctx context.Context, id string, w io.Writer, onStart func(name string, size int64)) error {
	atomic.AddInt32(&m.videoCalls, 1)
	if m.streamErr != nil {
		return m.streamErr
	}
	data, ok := m.videoData[id]
	if !ok {
		return ErrStorageNotFound
	}
	if onStart != nil {
		onStart(fmt.Sprintf("event_%s.mp4", id), int64(len(data)))
	}
	_, err := w.Write(data)
	return err
}

func TestCachedRecordingProviderCacheHit(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := NewMockExtractor()
	cache := NewRecordingCache(tempDir, 5, mockExt)

	// Pre-populate cache with 2 recordings
	now := time.Now().UTC().Truncate(time.Second)
	_, err := cache.Add(context.Background(), RecordingItem{
		ID:              "101",
		StartTime:       now.Add(-time.Minute),
		EndTime:         now.Add(-time.Minute + 30*time.Second),
		DurationSeconds: 30,
	}, bytes.NewReader([]byte("video-101")))
	require.NoError(t, err)

	_, err = cache.Add(context.Background(), RecordingItem{
		ID:              "102",
		StartTime:       now,
		EndTime:         now.Add(30 * time.Second),
		DurationSeconds: 30,
	}, bytes.NewReader([]byte("video-102")))
	require.NoError(t, err)

	upstream := &mockUpstreamProvider{}
	provider := NewCachedRecordingProvider(cache, func() RecordingProvider { return upstream })

	// 1. ListRecordings for latest items should be served from cache without calling upstream
	resp, err := provider.ListRecordings(context.Background(), time.Time{}, time.Time{}, 0, 2, "")
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Count)
	assert.Equal(t, int32(0), atomic.LoadInt32(&upstream.listCalls), "Upstream should NOT be called for cache-hit query")
	assert.Equal(t, "102", resp.List[0].ID)
	assert.Equal(t, "/api/sdcard/events/102/thumbnail.jpg", resp.List[0].ThumbnailURL)

	// 2. GetRecording cache-hit
	rec, err := provider.GetRecording(context.Background(), "101")
	require.NoError(t, err)
	assert.Equal(t, "101", rec.ID)
	assert.Equal(t, "/api/sdcard/events/101/thumbnail.jpg", rec.ThumbnailURL)

	// 3. StreamThumbnail from cache
	var thumbBuf bytes.Buffer
	err = provider.StreamThumbnail(context.Background(), "101", &thumbBuf)
	require.NoError(t, err)
	assert.Equal(t, mockExt.DummyData, thumbBuf.Bytes())

	// 4. StreamVideo from cache
	var vidBuf bytes.Buffer
	var startedName string
	var startedSize int64
	err = provider.StreamVideo(context.Background(), "101", &vidBuf, func(name string, size int64) {
		startedName = name
		startedSize = size
	})
	require.NoError(t, err)
	assert.Equal(t, "event_101.mp4", startedName)
	assert.Equal(t, int64(len("video-101")), startedSize)
	assert.Equal(t, "video-101", vidBuf.String())
	assert.Equal(t, int32(0), atomic.LoadInt32(&upstream.videoCalls), "Upstream video streaming should NOT be called for cached video")

	// 5. GetCachedVideoPath
	vPath, ok := provider.GetCachedVideoPath("101")
	assert.True(t, ok)
	assert.FileExists(t, vPath)
}

func TestCachedRecordingProviderFallbackBeyondCache(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := NewMockExtractor()
	cache := NewRecordingCache(tempDir, 2, mockExt)

	now := time.Now().UTC().Truncate(time.Second)
	_, _ = cache.Add(context.Background(), RecordingItem{ID: "201", StartTime: now}, bytes.NewReader([]byte("video-201")))

	upstream := &mockUpstreamProvider{
		recordings: []RecordingItem{
			{ID: "201", StartTime: now},
			{ID: "200", StartTime: now.Add(-time.Hour)},
			{ID: "199", StartTime: now.Add(-2 * time.Hour)},
		},
	}
	provider := NewCachedRecordingProvider(cache, func() RecordingProvider { return upstream })

	// Client requests limit=10 (greater than cache count 1) -> must query upstream
	resp, err := provider.ListRecordings(context.Background(), time.Time{}, time.Time{}, 0, 10, "")
	require.NoError(t, err)
	assert.Equal(t, 3, resp.Count)
	assert.Equal(t, int32(1), atomic.LoadInt32(&upstream.listCalls), "Upstream should be called when limit exceeds cache")

	// Item 201 should be enriched with thumbnail URL from cache
	assert.Equal(t, "201", resp.List[0].ID)
	assert.Equal(t, "/api/sdcard/events/201/thumbnail.jpg", resp.List[0].ThumbnailURL)

	// Item 200 is not in cache, so its thumbnail is empty
	assert.Equal(t, "200", resp.List[1].ID)
	assert.Equal(t, "", resp.List[1].ThumbnailURL)
}

func TestCachedRecordingProviderSyncLatest(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := NewMockExtractor()
	cache := NewRecordingCache(tempDir, 2, mockExt)

	now := time.Now().UTC().Truncate(time.Second)
	upstream := &mockUpstreamProvider{
		recordings: []RecordingItem{
			{ID: "301", StartTime: now, FileName: "event_301.mp4", DurationSeconds: 30},
			{ID: "302", StartTime: now.Add(-time.Minute), FileName: "event_302.mp4", DurationSeconds: 30},
		},
		videoData: map[string][]byte{
			"301": []byte("video-stream-301"),
			"302": []byte("video-stream-302"),
		},
	}

	provider := NewCachedRecordingProvider(cache, func() RecordingProvider { return upstream })

	// Run SyncLatest
	newlyCached, err := provider.SyncLatest(context.Background())
	require.NoError(t, err)
	assert.Len(t, newlyCached, 2)
	assert.Equal(t, 2, cache.Count())
	assert.True(t, cache.Has("301"))
	assert.True(t, cache.Has("302"))

	// Verify thumbnails were generated
	rec301, ok := cache.Get("301")
	assert.True(t, ok)
	assert.Equal(t, "/api/sdcard/events/301/thumbnail.jpg", rec301.ThumbnailURL)

	// Running sync again should not download anything new
	newlyCached2, err := provider.SyncLatest(context.Background())
	require.NoError(t, err)
	assert.Empty(t, newlyCached2)
}
