package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

// RecordingCache manages persistent on-disk storage, metadata indexing, and automated
// housekeeping (FIFO eviction) for camera recordings and snapshot thumbnails.
type RecordingCache struct {
	dir       string
	maxCount  int
	extractor FrameExtractor

	mu        sync.RWMutex
	items     map[string]RecordingItem
	sortedIDs []string // Sorted by StartTime descending (newest first)
}

// NewRecordingCache initializes a new RecordingCache instance.
// If maxCount <= 0, caching is disabled (pass-through).
func NewRecordingCache(dir string, maxCount int, extractor FrameExtractor) *RecordingCache {
	if dir == "" {
		dir = "data/recordings"
	}
	if maxCount < 0 {
		maxCount = 0
	}

	c := &RecordingCache{
		dir:       dir,
		maxCount:  maxCount,
		extractor: extractor,
		items:     make(map[string]RecordingItem),
		sortedIDs: make([]string, 0),
	}

	if maxCount > 0 {
		if err := os.MkdirAll(dir, 0755); err != nil {
			logger.Warn("Cache", "⚠️ Warning: Failed to create cache directory %s: %v", dir, err)
		} else {
			if err := c.LoadExisting(); err != nil {
				logger.Warn("Cache", "⚠️ Warning: Error loading existing cached recordings: %v", err)
			}
		}
	}

	return c
}

// LoadExisting scans the cache directory on startup, removes temporary files,
// indexes existing recordings, and prunes excess files if needed.
func (c *RecordingCache) LoadExisting() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	c.items = make(map[string]RecordingItem)
	c.sortedIDs = make([]string, 0)

	for _, entry := range entries {
		name := entry.Name()

		// Clean up leftover temporary files from aborted transfers or thumbnail extractions
		if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".tmp_") {
			_ = os.Remove(filepath.Join(c.dir, name))
			continue
		}

		if !entry.IsDir() && strings.HasSuffix(name, ".json") {
			id := strings.TrimSuffix(name, ".json")
			jsonPath := filepath.Join(c.dir, name)
			videoPath := filepath.Join(c.dir, id+".mp4")

			// Validate that the corresponding MP4 video exists and is non-empty
			vStat, err := os.Stat(videoPath)
			if err != nil || vStat.Size() == 0 {
				logger.Trace("Cache", "Orphaned or empty metadata without video: %s, removing", name)
				_ = os.Remove(jsonPath)
				continue
			}

			data, err := os.ReadFile(jsonPath)
			if err != nil {
				continue
			}

			var item RecordingItem
			if err := json.Unmarshal(data, &item); err != nil {
				continue
			}

			// Validate if file was incompletely downloaded / corrupted (e.g. from earlier dropped chunks)
			if item.FileSizeBytes > 0 && vStat.Size() < item.FileSizeBytes {
				logger.Warn("Cache", "🗑️ Purging truncated/corrupted recording %s (disk: %d bytes, expected: %d bytes)",
					id, vStat.Size(), item.FileSizeBytes)
				_ = os.Remove(videoPath)
				_ = os.Remove(jsonPath)
				_ = os.Remove(filepath.Join(c.dir, id+".jpg"))
				continue
			}

			// Ensure valid paths and URLs
			item.FileSizeBytes = vStat.Size()
			item.VideoURL = fmt.Sprintf("/api/sdcard/events/%s/video.mp4", id)

			thumbPath := filepath.Join(c.dir, id+".jpg")
			if tStat, err := os.Stat(thumbPath); err == nil && tStat.Size() > 0 {
				item.ThumbnailURL = fmt.Sprintf("/api/sdcard/events/%s/thumbnail.jpg", id)
			} else {
				// Thumbnail missing or empty: Attempt self-healing if extractor is available
				if c.extractor != nil && c.extractor.IsAvailable() {
					offset := 5 * time.Second
					if item.DurationSeconds > 0 && item.DurationSeconds < 5 {
						offset = time.Duration(item.DurationSeconds/2) * time.Second
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					extractErr := c.extractor.ExtractFrame(ctx, videoPath, offset, thumbPath)
					cancel()
					if extractErr == nil {
						item.ThumbnailURL = fmt.Sprintf("/api/sdcard/events/%s/thumbnail.jpg", id)
						logger.Info("Cache", "🩹 Self-healed missing thumbnail for recording %s", id)
						// Update metadata JSON on disk so it is up-to-date
						if metaBytes, err := json.MarshalIndent(item, "", "  "); err == nil {
							_ = os.WriteFile(jsonPath, metaBytes, 0644)
						}
					} else {
						logger.Debug("Cache", "Self-healing thumbnail failed for %s: %v", id, extractErr)
						item.ThumbnailURL = ""
					}
				} else {
					item.ThumbnailURL = ""
				}
			}

			c.items[id] = item
			c.sortedIDs = append(c.sortedIDs, id)
		}
	}

	c.sortIDsLocked()
	c.pruneLocked()

	logger.Info("Cache", "💾 Loaded %d valid recordings in local cache (%s, Max: %d)",
		len(c.sortedIDs), c.dir, c.maxCount)

	return nil
}

// Add saves a recording's video stream to disk, extracts a thumbnail snapshot,
// persists metadata, and performs automated housekeeping.
func (c *RecordingCache) Add(ctx context.Context, item RecordingItem, videoReader io.Reader) (*RecordingItem, error) {
	if c.maxCount <= 0 {
		return &item, nil
	}

	id := item.ID
	if id == "" {
		return nil, fmt.Errorf("recording item ID cannot be empty")
	}

	// 1. Stream video to temporary file
	tmpVideoPath := filepath.Join(c.dir, id+".mp4.tmp")
	finalVideoPath := filepath.Join(c.dir, id+".mp4")

	f, err := os.OpenFile(tmpVideoPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary video file: %w", err)
	}

	written, err := io.Copy(f, videoReader)
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(tmpVideoPath)
		return nil, fmt.Errorf("failed to write video data: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(tmpVideoPath)
		return nil, fmt.Errorf("failed to close video file: %w", closeErr)
	}
	if written == 0 {
		_ = os.Remove(tmpVideoPath)
		return nil, fmt.Errorf("received 0 bytes of video data")
	}

	// Validate against reported file size (if known): prevent saving truncated video
	if item.FileSizeBytes > 0 && written < item.FileSizeBytes {
		_ = os.Remove(tmpVideoPath)
		return nil, fmt.Errorf("incomplete video download: received %d of %d bytes", written, item.FileSizeBytes)
	}

	// Atomic rename to final video path
	if err := os.Rename(tmpVideoPath, finalVideoPath); err != nil {
		_ = os.Remove(tmpVideoPath)
		return nil, fmt.Errorf("failed to finalize video file: %w", err)
	}

	item.FileSizeBytes = written
	item.VideoURL = fmt.Sprintf("/api/sdcard/events/%s/video.mp4", id)
	if item.FileName == "" {
		item.FileName = fmt.Sprintf("event_%s.mp4", id)
	}

	// 2. Extract snapshot thumbnail (5s frame)
	thumbPath := filepath.Join(c.dir, id+".jpg")
	if c.extractor != nil && c.extractor.IsAvailable() {
		offset := 5 * time.Second
		if item.DurationSeconds > 0 && item.DurationSeconds < 5 {
			offset = time.Duration(item.DurationSeconds/2) * time.Second
		}
		if err := c.extractor.ExtractFrame(ctx, finalVideoPath, offset, thumbPath); err != nil {
			logger.Warn("Cache", "⚠️ Thumbnail extraction failed for %s: %v", id, err)
			item.ThumbnailURL = ""
		} else {
			item.ThumbnailURL = fmt.Sprintf("/api/sdcard/events/%s/thumbnail.jpg", id)
			logger.Trace("Cache", "Extracted snapshot for recording %s", id)
		}
	} else {
		item.ThumbnailURL = ""
	}

	// 3. Persist metadata JSON
	jsonPath := filepath.Join(c.dir, id+".json")
	tmpJsonPath := jsonPath + ".tmp"
	metaBytes, err := json.MarshalIndent(item, "", "  ")
	if err == nil {
		if errWrite := os.WriteFile(tmpJsonPath, metaBytes, 0644); errWrite == nil {
			_ = os.Rename(tmpJsonPath, jsonPath)
		}
	}

	// 4. Update in-memory index & prune
	c.mu.Lock()
	c.items[id] = item

	// Check if ID is already in sortedIDs
	found := false
	for _, existingID := range c.sortedIDs {
		if existingID == id {
			found = true
			break
		}
	}
	if !found {
		c.sortedIDs = append(c.sortedIDs, id)
	}

	c.sortIDsLocked()
	c.pruneLocked()
	c.mu.Unlock()

	logger.Debug("Cache", "💾 Cached recording %s (%s, %.1f MB, Thumb: %t)",
		id, item.FileName, float64(written)/(1024*1024), item.ThumbnailURL != "")

	return &item, nil
}

// Has returns true if the recording ID is present in the cache.
func (c *RecordingCache) Has(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.items[id]
	return ok
}

// Get returns the cached recording metadata by ID.
func (c *RecordingCache) Get(id string) (RecordingItem, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, ok := c.items[id]
	return item, ok
}

// GetVideoPath returns the absolute path to the cached video file, if it exists.
func (c *RecordingCache) GetVideoPath(id string) (string, bool) {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.items[id]; !ok {
		return "", false
	}
	absDir, err := filepath.Abs(c.dir)
	if err != nil {
		return "", false
	}
	if !strings.HasSuffix(absDir, string(filepath.Separator)) {
		absDir += string(filepath.Separator)
	}
	absPath, err := filepath.Abs(filepath.Join(absDir, id+".mp4"))
	if err != nil || !strings.HasPrefix(absPath, absDir) {
		return "", false
	}
	if fi, err := os.Stat(absPath); err == nil && fi.Size() > 0 {
		return absPath, true
	}
	return "", false
}

// GetThumbnailPath returns the absolute path to the cached thumbnail JPEG, if it exists.
func (c *RecordingCache) GetThumbnailPath(id string) (string, bool) {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if item, ok := c.items[id]; !ok || item.ThumbnailURL == "" {
		return "", false
	}
	absDir, err := filepath.Abs(c.dir)
	if err != nil {
		return "", false
	}
	if !strings.HasSuffix(absDir, string(filepath.Separator)) {
		absDir += string(filepath.Separator)
	}
	absPath, err := filepath.Abs(filepath.Join(absDir, id+".jpg"))
	if err != nil || !strings.HasPrefix(absPath, absDir) {
		return "", false
	}
	if fi, err := os.Stat(absPath); err == nil && fi.Size() > 0 {
		return absPath, true
	}
	return "", false
}

// List returns cached recordings filtered by time range and paginated.
func (c *RecordingCache) List(start, end time.Time, page, limit int) *RecordingListResponse {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var matched []RecordingItem
	for _, id := range c.sortedIDs {
		item := c.items[id]

		if !start.IsZero() && item.EndTime.Before(start) {
			continue
		}
		if !end.IsZero() && item.StartTime.After(end) {
			continue
		}

		matched = append(matched, item)
	}

	total := len(matched)
	if limit <= 0 {
		limit = total
	}
	if page < 0 {
		page = 0
	}

	offset := page * limit
	if offset >= total {
		return &RecordingListResponse{
			Count: 0,
			Total: total,
			List:  []RecordingItem{},
		}
	}

	endIdx := offset + limit
	if endIdx > total {
		endIdx = total
	}

	result := matched[offset:endIdx]
	return &RecordingListResponse{
		Count: len(result),
		Total: total,
		List:  result,
	}
}

// Count returns the number of currently cached recordings.
func (c *RecordingCache) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.sortedIDs)
}

// MaxCount returns the configured capacity of the cache.
func (c *RecordingCache) MaxCount() int {
	return c.maxCount
}

// Latest returns the most recent cached recording item, if available.
func (c *RecordingCache) Latest() (*RecordingItem, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.sortedIDs) == 0 {
		return nil, false
	}
	item := c.items[c.sortedIDs[0]]
	return &item, true
}

// OldestStartTime returns the StartTime of the oldest cached recording.
func (c *RecordingCache) OldestStartTime() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.sortedIDs) == 0 {
		return time.Time{}
	}
	oldestID := c.sortedIDs[len(c.sortedIDs)-1]
	return c.items[oldestID].StartTime
}

// NewestStartTime returns the StartTime of the newest cached recording.
func (c *RecordingCache) NewestStartTime() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.sortedIDs) == 0 {
		return time.Time{}
	}
	newestID := c.sortedIDs[0]
	return c.items[newestID].StartTime
}

// Prune manually triggers housekeeping to adhere to maxCount.
func (c *RecordingCache) Prune() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked()
}

func (c *RecordingCache) pruneLocked() {
	if c.maxCount <= 0 {
		return
	}

	for len(c.sortedIDs) > c.maxCount {
		// Evict oldest (at the tail of sortedIDs)
		evictIdx := len(c.sortedIDs) - 1
		evictID := c.sortedIDs[evictIdx]

		c.sortedIDs = c.sortedIDs[:evictIdx]
		delete(c.items, evictID)

		// Delete disk files
		_ = os.Remove(filepath.Join(c.dir, evictID+".mp4"))
		_ = os.Remove(filepath.Join(c.dir, evictID+".jpg"))
		_ = os.Remove(filepath.Join(c.dir, evictID+".json"))
		_ = os.Remove(filepath.Join(c.dir, evictID+".mp4.tmp"))

		logger.Debug("Cache", "🧹 Housekeeping: Evicted oldest cached recording %s", evictID)
	}
}

func (c *RecordingCache) sortIDsLocked() {
	sort.Slice(c.sortedIDs, func(i, j int) bool {
		return c.items[c.sortedIDs[i]].StartTime.After(c.items[c.sortedIDs[j]].StartTime)
	})
}
