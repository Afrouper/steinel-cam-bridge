package storage

import (
	"context"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

// RecordingSyncer manages initial and periodic synchronization of SD card recordings
// and pushes new recording events to Home Assistant / MQTT.
type RecordingSyncer struct {
	providerFunc     func() RecordingProvider
	onNewRecording   func(RecordingItem)
	pollInterval     time.Duration
	isStreamActive   func() bool
	lastPeriodicPoll time.Time
	lastSeenID       string
	lastSeenTime     time.Time
	triggerChan      chan struct{}
	mu               sync.Mutex
}

// NewRecordingSyncer creates a new RecordingSyncer instance.
func NewRecordingSyncer(
	providerFunc func() RecordingProvider,
	onNewRecording func(RecordingItem),
	pollInterval time.Duration,
) *RecordingSyncer {
	if pollInterval <= 0 {
		pollInterval = 60 * time.Second
	}
	return &RecordingSyncer{
		providerFunc:   providerFunc,
		onNewRecording: onNewRecording,
		pollInterval:   pollInterval,
		triggerChan:    make(chan struct{}, 1),
	}
}

// SetStreamActiveFunc registers a callback that checks if video streaming is active
// (e.g. RTSP clients connected).
func (s *RecordingSyncer) SetStreamActiveFunc(fn func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isStreamActive = fn
}

// TriggerSync requests an immediate sync check (with debouncing delay).
func (s *RecordingSyncer) TriggerSync() {
	select {
	case s.triggerChan <- struct{}{}:
	default:
	}
}

// Start runs the periodic and event-driven sync loops until ctx is cancelled.
func (s *RecordingSyncer) Start(ctx context.Context) {
	if s.pollInterval > 0 {
		logger.Info("Recording Sync", "🚀 Background sync engine started (Interval: %v)", s.pollInterval)
	} else {
		logger.Info("Recording Sync", "ℹ️ Periodic background sync is disabled (Interval <= 0). Syncing will only occur on startup and motion triggers.")
	}

	// Step 1: Initial Sync / Startup Publication after 3 seconds startup delay
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
		s.syncOnce(ctx, true)
	}

	var tickerChan <-chan time.Time
	if s.pollInterval > 0 {
		ticker := time.NewTicker(s.pollInterval)
		defer ticker.Stop()
		tickerChan = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return

		case now := <-tickerChan:
			s.mu.Lock()
			activeFn := s.isStreamActive
			lastPoll := s.lastPeriodicPoll
			s.mu.Unlock()

			if activeFn != nil && activeFn() {
				// During active 24/7 RTSP streaming, throttle background polling to at least 5 minutes
				// to avoid hammering the camera CPU while motion events are handled event-driven anyway.
				if !lastPoll.IsZero() && now.Sub(lastPoll) < 5*time.Minute {
					logger.Trace("Recording Sync", "Throttling periodic sync during active streaming (next in %v)", 5*time.Minute-now.Sub(lastPoll))
					continue
				}
			}

			s.mu.Lock()
			s.lastPeriodicPoll = now
			s.mu.Unlock()
			s.syncOnce(ctx, false)

		case <-s.triggerChan:
			logger.Info("Recording Sync", "⏳ Motion detected: waiting 35s to allow camera to finish writing MP4 clip before syncing...")
			// Wait 35 seconds so camera can finalize writing the MP4 file to SD card without concurrent I/O stress
			select {
			case <-ctx.Done():
				return
			case <-time.After(35 * time.Second):
			}
			s.syncOnce(ctx, false)
		}
	}
}

func (s *RecordingSyncer) syncOnce(ctx context.Context, isInitial bool) {
	if s.providerFunc == nil {
		return
	}
	provider := s.providerFunc()
	if provider == nil {
		return
	}

	// 1. Proactive cache sync if provider wraps a local RecordingCache
	if cacheSyncer, ok := provider.(interface {
		SyncLatest(ctx context.Context) ([]RecordingItem, error)
	}); ok {
		newlyCached, err := cacheSyncer.SyncLatest(ctx)
		if err != nil {
			if isInitial {
				logger.Warn("Recording Sync", "⚠️ Initial cache sync returned error: %v", err)
			} else {
				logger.Debug("Recording Sync", "Periodic cache sync error: %v", err)
			}
		}
		if len(newlyCached) > 0 {
			latest := newlyCached[0]
			if latest.ID == "" || latest.StartTime.IsZero() {
				return
			}
			s.mu.Lock()
			isFirst := (s.lastSeenID == "")
			s.lastSeenID = latest.ID
			s.lastSeenTime = latest.StartTime
			s.mu.Unlock()

			if isFirst {
				logger.Info("Recording Sync", "📌 Initial sync: Cached latest recording %s (%s, %.1f MB, Thumb: %t)",
					latest.ID, latest.FileName, float64(latest.FileSizeBytes)/(1024*1024), latest.ThumbnailURL != "")
			} else {
				logger.Info("Recording Sync", "🆕 New recording cached: %s (%s, %.1f MB, Thumb: %t)",
					latest.ID, latest.FileName, float64(latest.FileSizeBytes)/(1024*1024), latest.ThumbnailURL != "")
			}
			logger.Trace("Recording Sync", "Recording details: %+v", latest)

			if s.onNewRecording != nil {
				s.onNewRecording(latest)
			}
		}
		// If cacheSyncer is present and handled, never perform a redundant second query to upstream camera
		return
	}

	// 2. Query fallback (for non-cached providers or when cache already holds latest items)
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	s.mu.Lock()
	lastTime := s.lastSeenTime
	s.mu.Unlock()

	var startTime time.Time
	if !lastTime.IsZero() {
		// Only query recordings around the last seen time (-10s buffer) to avoid fetching the entire history
		startTime = lastTime.Add(-10 * time.Second)
	}

	logger.Trace("Recording Sync", "Querying recordings since: %v", startTime)

	// Query latest recordings (epoch 0 on initial sync, or since lastSeenTime on periodic polls)
	resp, err := provider.ListRecordings(reqCtx, startTime, time.Time{}, 0, 5, "")
	if err != nil {
		if isInitial {
			logger.Warn("Recording Sync", "⚠️ Initial sync query returned error: %v", err)
		} else {
			logger.Debug("Recording Sync", "Periodic sync query error: %v", err)
		}
		return
	}

	if resp == nil || len(resp.List) == 0 {
		return
	}

	latest := resp.List[0]
	if latest.ID == "" || latest.StartTime.IsZero() {
		return
	}

	s.mu.Lock()
	lastID := s.lastSeenID
	if lastID == "" || latest.ID != lastID {
		isFirst := (lastID == "")
		s.lastSeenID = latest.ID
		s.lastSeenTime = latest.StartTime
		s.mu.Unlock()

		if isFirst {
			logger.Info("Recording Sync", "📌 Initial sync: Found latest recording %s (%s)", latest.ID, latest.FileName)
		} else {
			logger.Info("Recording Sync", "🆕 New recording detected on SD card: %s (%s)", latest.ID, latest.FileName)
		}
		logger.Trace("Recording Sync", "Recording details: %+v", latest)

		if s.onNewRecording != nil {
			s.onNewRecording(latest)
		}
	} else {
		s.mu.Unlock()
	}
}
