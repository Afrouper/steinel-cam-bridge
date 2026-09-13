package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordingCacheAddAndGet(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := NewMockExtractor()
	cache := NewRecordingCache(tempDir, 3, mockExt)

	assert.Equal(t, 0, cache.Count())

	now := time.Now().UTC().Truncate(time.Second)
	item1 := RecordingItem{
		ID:              "1001",
		StartTime:       now.Add(-2 * time.Minute),
		EndTime:         now.Add(-2*time.Minute + 30*time.Second),
		DurationSeconds: 30,
		EventType:       "motion",
	}

	videoData := []byte("fake-mp4-video-content-1001")
	saved, err := cache.Add(context.Background(), item1, bytes.NewReader(videoData))
	require.NoError(t, err)
	assert.Equal(t, "1001", saved.ID)
	assert.Equal(t, int64(len(videoData)), saved.FileSizeBytes)
	assert.Equal(t, "/api/sdcard/events/1001/video.mp4", saved.VideoURL)
	assert.Equal(t, "/api/sdcard/events/1001/thumbnail.jpg", saved.ThumbnailURL)
	assert.Equal(t, 1, cache.Count())
	assert.True(t, cache.Has("1001"))

	// Check paths
	vPath, ok := cache.GetVideoPath("1001")
	assert.True(t, ok)
	assert.FileExists(t, vPath)

	tPath, ok := cache.GetThumbnailPath("1001")
	assert.True(t, ok)
	assert.FileExists(t, tPath)

	// Verify file contents
	readVideo, err := os.ReadFile(vPath)
	require.NoError(t, err)
	assert.Equal(t, videoData, readVideo)

	readThumb, err := os.ReadFile(tPath)
	require.NoError(t, err)
	assert.Equal(t, mockExt.DummyData, readThumb)

	// Check Get
	fetched, ok := cache.Get("1001")
	assert.True(t, ok)
	assert.Equal(t, "1001", fetched.ID)
	assert.Equal(t, "/api/sdcard/events/1001/thumbnail.jpg", fetched.ThumbnailURL)
}

func TestRecordingCachePruning(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := NewMockExtractor()
	// Limit cache to 2 items
	cache := NewRecordingCache(tempDir, 2, mockExt)

	baseTime := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	// Add Item 1 (oldest)
	item1 := RecordingItem{ID: "1001", StartTime: baseTime, EndTime: baseTime.Add(30 * time.Second), DurationSeconds: 30}
	_, err := cache.Add(context.Background(), item1, bytes.NewReader([]byte("video-1")))
	require.NoError(t, err)

	// Add Item 2
	item2 := RecordingItem{ID: "1002", StartTime: baseTime.Add(time.Minute), EndTime: baseTime.Add(time.Minute + 30*time.Second), DurationSeconds: 30}
	_, err = cache.Add(context.Background(), item2, bytes.NewReader([]byte("video-2")))
	require.NoError(t, err)

	assert.Equal(t, 2, cache.Count())
	assert.True(t, cache.Has("1001"))
	assert.True(t, cache.Has("1002"))

	// Add Item 3 -> Item 1 should be evicted (FIFO / oldest)
	item3 := RecordingItem{ID: "1003", StartTime: baseTime.Add(2 * time.Minute), EndTime: baseTime.Add(2*time.Minute + 30*time.Second), DurationSeconds: 30}
	_, err = cache.Add(context.Background(), item3, bytes.NewReader([]byte("video-3")))
	require.NoError(t, err)

	assert.Equal(t, 2, cache.Count())
	assert.False(t, cache.Has("1001"), "Item 1001 should have been evicted")
	assert.True(t, cache.Has("1002"))
	assert.True(t, cache.Has("1003"))

	// Verify disk files for item 1001 are deleted
	assert.NoFileExists(t, filepath.Join(tempDir, "1001.mp4"))
	assert.NoFileExists(t, filepath.Join(tempDir, "1001.jpg"))
	assert.NoFileExists(t, filepath.Join(tempDir, "1001.json"))

	// Verify disk files for item 1003 exist
	assert.FileExists(t, filepath.Join(tempDir, "1003.mp4"))
	assert.FileExists(t, filepath.Join(tempDir, "1003.jpg"))
	assert.FileExists(t, filepath.Join(tempDir, "1003.json"))
}

func TestRecordingCacheLoadExisting(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := NewMockExtractor()

	// 1. Create cache and add 2 items
	cache1 := NewRecordingCache(tempDir, 5, mockExt)
	baseTime := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	_, _ = cache1.Add(context.Background(), RecordingItem{ID: "2001", StartTime: baseTime}, bytes.NewReader([]byte("vid-1")))
	_, _ = cache1.Add(context.Background(), RecordingItem{ID: "2002", StartTime: baseTime.Add(time.Minute)}, bytes.NewReader([]byte("vid-2")))

	// Add a dummy .tmp file that should be cleaned up
	tmpFile := filepath.Join(tempDir, "abandoned.mp4.tmp")
	require.NoError(t, os.WriteFile(tmpFile, []byte("garbage"), 0644))

	// 2. Instantiate new cache instance pointing to same directory
	cache2 := NewRecordingCache(tempDir, 5, mockExt)

	assert.Equal(t, 2, cache2.Count())
	assert.True(t, cache2.Has("2001"))
	assert.True(t, cache2.Has("2002"))
	assert.NoFileExists(t, tmpFile, ".tmp file should be cleaned up on startup")
}

func TestRecordingCacheListAndPagination(t *testing.T) {
	tempDir := t.TempDir()
	cache := NewRecordingCache(tempDir, 10, NewMockExtractor())

	baseTime := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		st := baseTime.Add(time.Duration(i) * time.Minute)
		id := string(rune('0' + i))
		_, err := cache.Add(context.Background(), RecordingItem{
			ID:              id,
			StartTime:       st,
			EndTime:         st.Add(30 * time.Second),
			DurationSeconds: 30,
		}, bytes.NewReader([]byte("video")))
		require.NoError(t, err)
	}

	assert.Equal(t, 5, cache.Count())

	// List all
	respAll := cache.List(time.Time{}, time.Time{}, 0, 10)
	assert.Equal(t, 5, respAll.Count)
	assert.Equal(t, 5, respAll.Total)
	// Newest first
	assert.Equal(t, "5", respAll.List[0].ID)
	assert.Equal(t, "1", respAll.List[4].ID)

	// Pagination: Page 0, Limit 2
	respP0 := cache.List(time.Time{}, time.Time{}, 0, 2)
	assert.Equal(t, 2, respP0.Count)
	assert.Equal(t, "5", respP0.List[0].ID)
	assert.Equal(t, "4", respP0.List[1].ID)

	// Pagination: Page 1, Limit 2
	respP1 := cache.List(time.Time{}, time.Time{}, 1, 2)
	assert.Equal(t, 2, respP1.Count)
	assert.Equal(t, "3", respP1.List[0].ID)
	assert.Equal(t, "2", respP1.List[1].ID)

	// Range filter: only items between 10:02 and 10:04
	respFiltered := cache.List(baseTime.Add(2*time.Minute), baseTime.Add(4*time.Minute), 0, 10)
	assert.Equal(t, 3, respFiltered.Count)
	assert.Equal(t, "4", respFiltered.List[0].ID)
	assert.Equal(t, "2", respFiltered.List[2].ID)
}
