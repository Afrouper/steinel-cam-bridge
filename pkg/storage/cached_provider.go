package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

var _ RecordingProvider = (*CachedRecordingProvider)(nil)

// CachedRecordingProvider wraps an upstream RecordingProvider with a local RecordingCache
// (Decorator Pattern). It serves queries, video streams, and snapshot thumbnails directly
// from disk when available, avoiding expensive camera SD-card operations.
type CachedRecordingProvider struct {
	cache        *RecordingCache
	upstreamFunc func() RecordingProvider
	syncMu       sync.Mutex
}

// NewCachedRecordingProvider creates a new CachedRecordingProvider.
func NewCachedRecordingProvider(cache *RecordingCache, upstreamFunc func() RecordingProvider) *CachedRecordingProvider {
	return &CachedRecordingProvider{
		cache:        cache,
		upstreamFunc: upstreamFunc,
	}
}

// Cache returns the underlying RecordingCache.
func (p *CachedRecordingProvider) Cache() *RecordingCache {
	return p.cache
}

// ListRecordings implements RecordingProvider.
// Standard requests (such as latest recordings, or ranges within the cache) are served
// directly from the local disk cache without contacting the camera.
func (p *CachedRecordingProvider) ListRecordings(ctx context.Context, start, end time.Time, page, limit int, eventType string) (*RecordingListResponse, error) {
	if p.cache != nil && p.cache.MaxCount() > 0 && p.cache.Count() > 0 {
		// 1. Check if the query is for the latest recordings within cache capacity
		if page == 0 && start.IsZero() && end.IsZero() {
			if limit <= 0 || limit <= p.cache.Count() {
				logger.Trace("CacheProvider", "Serving ListRecordings (latest, limit: %d) directly from cache", limit)
				return p.cache.List(start, end, page, limit), nil
			}
		}

		// 2. Check if the requested time range falls completely inside the cached window
		oldest := p.cache.OldestStartTime()
		if !start.IsZero() && !oldest.IsZero() && !start.Before(oldest) {
			logger.Trace("CacheProvider", "Serving ListRecordings (start: %v) directly from cache", start)
			return p.cache.List(start, end, page, limit), nil
		}
	}

	// 3. Fallback: Query upstream camera
	var upstream RecordingProvider
	if p.upstreamFunc != nil {
		upstream = p.upstreamFunc()
	}

	if upstream == nil {
		if p.cache != nil && p.cache.Count() > 0 {
			logger.Debug("CacheProvider", "Camera offline; falling back to cached recordings")
			return p.cache.List(start, end, page, limit), nil
		}
		return nil, ErrFeatureDisabled
	}

	resp, err := upstream.ListRecordings(ctx, start, end, page, limit, eventType)
	if err != nil {
		// Graceful fallback to cache if camera returns busy or timeout
		if p.cache != nil && p.cache.Count() > 0 {
			logger.Warn("CacheProvider", "⚠️ Upstream ListRecordings failed (%v); falling back to cache", err)
			return p.cache.List(start, end, page, limit), nil
		}
		return nil, err
	}

	if resp == nil {
		return &RecordingListResponse{}, nil
	}

	// Enrich upstream response with local thumbnail/video URLs if cached
	if p.cache != nil {
		for i := range resp.List {
			id := resp.List[i].ID
			if p.cache.Has(id) {
				if cachedItem, ok := p.cache.Get(id); ok {
					if cachedItem.ThumbnailURL != "" {
						resp.List[i].ThumbnailURL = cachedItem.ThumbnailURL
					}
					resp.List[i].VideoURL = cachedItem.VideoURL
				}
			}
		}
	}

	return resp, nil
}

// GetRecording implements RecordingProvider.
func (p *CachedRecordingProvider) GetRecording(ctx context.Context, id string) (*RecordingItem, error) {
	// 1. Check local cache first
	if p.cache != nil {
		if item, ok := p.cache.Get(id); ok {
			return &item, nil
		}
	}

	// 2. Fallback to upstream
	if p.upstreamFunc != nil {
		if upstream := p.upstreamFunc(); upstream != nil {
			return upstream.GetRecording(ctx, id)
		}
	}

	return nil, ErrStorageNotFound
}

// StreamThumbnail implements RecordingProvider.
func (p *CachedRecordingProvider) StreamThumbnail(ctx context.Context, id string, w io.Writer) error {
	baseID := filepath.Base(id)
	if baseID != id || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return ErrStorageNotFound
	}

	// 1. Stream from cache if available
	if p.cache != nil {
		if thumbPath, ok := p.cache.GetThumbnailPath(baseID); ok {
			cleanPath := filepath.Clean(thumbPath)
			f, err := os.Open(cleanPath)
			if err == nil {
				defer func() { _ = f.Close() }()
				_, copyErr := io.Copy(w, f)
				return copyErr
			}
		}
	}

	// 2. Fallback to upstream camera if supported
	if p.upstreamFunc != nil {
		if upstream := p.upstreamFunc(); upstream != nil {
			return upstream.StreamThumbnail(ctx, baseID, w)
		}
	}

	return ErrFeatureDisabled
}

// StreamVideo implements RecordingProvider.
func (p *CachedRecordingProvider) StreamVideo(ctx context.Context, id string, w io.Writer, onStart func(name string, size int64)) error {
	baseID := filepath.Base(id)
	if baseID != id || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return ErrStorageNotFound
	}

	// 1. Stream from cache if available
	if p.cache != nil {
		if videoPath, ok := p.cache.GetVideoPath(baseID); ok {
			cleanPath := filepath.Clean(videoPath)
			f, err := os.Open(cleanPath)
			if err == nil {
				defer func() { _ = f.Close() }()
				stat, statErr := f.Stat()
				if statErr == nil && onStart != nil {
					name := fmt.Sprintf("event_%s.mp4", baseID)
					onStart(name, stat.Size())
				}
				_, copyErr := io.Copy(w, f)
				return copyErr
			}
		}
	}

	// 2. Fallback to upstream camera
	if p.upstreamFunc != nil {
		if upstream := p.upstreamFunc(); upstream != nil {
			return upstream.StreamVideo(ctx, baseID, w, onStart)
		}
	}

	return ErrStorageNotFound
}

// GetCachedVideoPath returns the local path to the cached video file if present.
// This is used by the REST API to enable HTTP 206 Partial Content (seeking/scrubbing).
func (p *CachedRecordingProvider) GetCachedVideoPath(id string) (string, bool) {
	if p.cache == nil {
		return "", false
	}
	baseID := filepath.Base(id)
	if baseID != id || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", false
	}
	return p.cache.GetVideoPath(baseID)
}

// SyncLatest synchronizes the newest recordings from the camera into the local cache.
// It downloads missing video files, extracts thumbnails, and runs housekeeping.
// Strict concurrency lock ensures that only one sync/download is in progress at a time.
func (p *CachedRecordingProvider) SyncLatest(ctx context.Context) ([]RecordingItem, error) {
	if p.cache == nil || p.cache.MaxCount() <= 0 {
		return nil, nil
	}

	if !p.syncMu.TryLock() {
		logger.Trace("CacheProvider", "SyncLatest already in progress, skipping concurrent run")
		return nil, nil
	}
	defer p.syncMu.Unlock()

	if p.upstreamFunc == nil {
		return nil, nil
	}
	upstream := p.upstreamFunc()
	if upstream == nil {
		return nil, nil
	}

	// Query top N recordings from camera
	queryLimit := p.cache.MaxCount()
	if queryLimit <= 0 {
		queryLimit = 10
	}

	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := upstream.ListRecordings(queryCtx, time.Time{}, time.Time{}, 0, queryLimit, "")
	if err != nil {
		return nil, fmt.Errorf("failed to query camera recordings: %w", err)
	}

	if resp == nil || len(resp.List) == 0 {
		return nil, nil
	}

	var newlyCached []RecordingItem

	// Process recordings from newest to oldest
	for _, item := range resp.List {
		if ctx.Err() != nil {
			break
		}

		// Skip if already in local cache
		if p.cache.Has(item.ID) {
			continue
		}

		logger.Info("CacheProvider", "📥 Pre-loading recording %s (%s) into bridge cache...", item.ID, item.FileName)

		// Download video via streaming pipe into cache
		pr, pw := io.Pipe()
		downloadCtx, dCancel := context.WithTimeout(ctx, 60*time.Second)

		var streamErr error
		var wg sync.WaitGroup
		wg.Add(1)

		go func(recID string) {
			defer wg.Done()
			streamErr = upstream.StreamVideo(downloadCtx, recID, pw, nil)
			if streamErr != nil {
				_ = pw.CloseWithError(streamErr)
			} else {
				_ = pw.Close()
			}
		}(item.ID)

		savedItem, addErr := p.cache.Add(downloadCtx, item, pr)
		wg.Wait()
		dCancel()

		if addErr != nil {
			logger.Warn("CacheProvider", "⚠️ Failed to cache recording %s: %v (streamErr: %v)", item.ID, addErr, streamErr)
			continue
		}

		newlyCached = append(newlyCached, *savedItem)

		// Brief delay to let the camera's embedded CPU breathe between downloads
		select {
		case <-ctx.Done():
			return newlyCached, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	return newlyCached, nil
}
