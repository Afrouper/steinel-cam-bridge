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

func TestRecordingCacheSelfHealingAndLatest(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := NewMockExtractor()

	// 1. Manually prepare an MP4 and JSON metadata without thumbnail (simulating beta.2 failed extraction)
	now := time.Now().UTC().Truncate(time.Second)
	mp4Path := filepath.Join(tempDir, "3001.mp4")
	require.NoError(t, os.WriteFile(mp4Path, []byte("fake-mp4-data"), 0644))

	jsonPath := filepath.Join(tempDir, "3001.json")
	metaJSON := []byte(`{
		"id": "3001",
		"start_time": "` + now.Format(time.RFC3339) + `",
		"end_time": "` + now.Add(30*time.Second).Format(time.RFC3339) + `",
		"duration_sec": 30,
		"file_name": "event_3001.mp4",
		"video_url": "/api/sdcard/events/3001/video.mp4",
		"thumbnail_url": ""
	}`)
	require.NoError(t, os.WriteFile(jsonPath, metaJSON, 0644))

	// Leftover temporary files from earlier extraction or download
	tmpFile1 := filepath.Join(tempDir, "test.tmp")
	require.NoError(t, os.WriteFile(tmpFile1, []byte("garbage1"), 0644))
	tmpFile2 := filepath.Join(tempDir, ".tmp_3001_deadbeef.jpg")
	require.NoError(t, os.WriteFile(tmpFile2, []byte("garbage2"), 0644))

	// 2. Instantiate cache - LoadExisting should self-heal the missing thumbnail
	cache := NewRecordingCache(tempDir, 5, mockExt)

	assert.Equal(t, 1, cache.Count())
	assert.True(t, cache.Has("3001"))

	// Verify temporary files were removed
	assert.NoFileExists(t, tmpFile1)
	assert.NoFileExists(t, tmpFile2)

	// Verify thumbnail was created and self-healed
	thumbPath := filepath.Join(tempDir, "3001.jpg")
	assert.FileExists(t, thumbPath)

	item, ok := cache.Get("3001")
	assert.True(t, ok)
	assert.Equal(t, "/api/sdcard/events/3001/thumbnail.jpg", item.ThumbnailURL)

	// Verify Latest() method
	latest, ok := cache.Latest()
	assert.True(t, ok)
	assert.Equal(t, "3001", latest.ID)
}

func TestRecordingCachePurgeTruncatedAndAddValidation(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := NewMockExtractor()

	// 1. Prepare a truncated video (reported size 1000 bytes, but only 200 bytes on disk)
	mp4Path := filepath.Join(tempDir, "4001.mp4")
	require.NoError(t, os.WriteFile(mp4Path, []byte("short-corrupted-data"), 0644))

	jsonPath := filepath.Join(tempDir, "4001.json")
	metaJSON := []byte(`{
		"id": "4001",
		"file_name": "event_4001.mp4",
		"file_size_bytes": 1000000
	}`)
	require.NoError(t, os.WriteFile(jsonPath, metaJSON, 0644))

	// LoadExisting should purge the truncated recording
	cache := NewRecordingCache(tempDir, 5, mockExt)
	assert.Equal(t, 0, cache.Count())
	assert.False(t, cache.Has("4001"))
	assert.NoFileExists(t, mp4Path)
	assert.NoFileExists(t, jsonPath)

	// 2. Test Add validation: item reporting 500 bytes but stream only provides 100 bytes
	item := RecordingItem{
		ID:            "4002",
		FileSizeBytes: 500,
	}
	shortStream := bytes.NewReader([]byte("short-stream-100-bytes"))
	_, err := cache.Add(context.Background(), item, shortStream)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incomplete video download")
	assert.NoFileExists(t, filepath.Join(tempDir, "4002.mp4"))
	assert.NoFileExists(t, filepath.Join(tempDir, "4002.mp4.tmp"))
}

func TestRecordingCacheFailedMarker(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := NewMockExtractor()
	cache := NewRecordingCache(tempDir, 2, mockExt)

	// Attempt 1: Should not permanently fail yet
	permFailed := cache.RecordFailure("5001", os.ErrDeadlineExceeded)
	assert.False(t, permFailed)
	assert.False(t, cache.Has("5001"))
	assert.False(t, cache.HasFailed("5001"))
	assert.NoFileExists(t, filepath.Join(tempDir, "5001.failed"))

	// Attempt 2: Reaches threshold (2), should mark permanently failed
	permFailed = cache.RecordFailure("5001", os.ErrDeadlineExceeded)
	assert.True(t, permFailed)
	assert.True(t, cache.Has("5001"))
	assert.True(t, cache.HasFailed("5001"))
	assert.FileExists(t, filepath.Join(tempDir, "5001.failed"))

	// Verify file content
	content, err := os.ReadFile(filepath.Join(tempDir, "5001.failed"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "i/o timeout")

	// Test LoadExisting restores .failed marker
	cache2 := NewRecordingCache(tempDir, 2, mockExt)
	assert.True(t, cache2.Has("5001"))
	assert.True(t, cache2.HasFailed("5001"))

	// Test Pruning of .failed items:
	// Add 2 valid recordings with newer timestamps -> 5001 (oldest) should be pruned
	now := time.Now().UTC()
	item1 := RecordingItem{ID: "5002", StartTime: now.Add(time.Minute)}
	_, err = cache2.Add(context.Background(), item1, bytes.NewReader([]byte("video-data-1")))
	require.NoError(t, err)

	item2 := RecordingItem{ID: "5003", StartTime: now.Add(2 * time.Minute)}
	_, err = cache2.Add(context.Background(), item2, bytes.NewReader([]byte("video-data-2")))
	require.NoError(t, err)

	// Since maxCount is 2, 5001 should be evicted
	assert.False(t, cache2.Has("5001"))
	assert.False(t, cache2.HasFailed("5001"))
	assert.NoFileExists(t, filepath.Join(tempDir, "5001.failed"))
}
