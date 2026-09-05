package storage

import (
	"context"
	"log"
	"sync"
	"time"
)

// RecordingSyncer manages initial and periodic synchronization of SD card recordings
// and pushes new recording events to Home Assistant / MQTT.
type RecordingSyncer struct {
	providerFunc   func() RecordingProvider
	onNewRecording func(RecordingItem)
	pollInterval   time.Duration
	lastSeenID     string
	lastSeenTime   time.Time
	triggerChan    chan struct{}
	mu             sync.Mutex
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

// TriggerSync requests an immediate sync check (with debouncing delay).
func (s *RecordingSyncer) TriggerSync() {
	select {
	case s.triggerChan <- struct{}{}:
	default:
	}
}

// Start runs the periodic and event-driven sync loops until ctx is cancelled.
func (s *RecordingSyncer) Start(ctx context.Context) {
	log.Printf("[Recording Sync] 🚀 Background sync engine started (Interval: %v)", s.pollInterval)

	// Step 1: Initial Sync after 3 seconds startup delay
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
		s.syncOnce(ctx, true)
	}

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			s.syncOnce(ctx, false)

		case <-s.triggerChan:
			// Wait 5 seconds so camera can finalize writing the MP4 file to SD card
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
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

	// Query latest recordings (epoch 0 on initial sync, or since lastSeenTime on periodic polls)
	resp, err := provider.ListRecordings(reqCtx, startTime, time.Time{}, 0, 5, "")
	if err != nil {
		if isInitial {
			log.Printf("[Recording Sync] ⚠️ Initial sync query returned error: %v", err)
		}
		return
	}

	if resp == nil || len(resp.List) == 0 {
		return
	}

	latest := resp.List[0]

	s.mu.Lock()
	lastID := s.lastSeenID
	if lastID == "" || latest.ID != lastID {
		isFirst := (lastID == "")
		s.lastSeenID = latest.ID
		s.lastSeenTime = latest.StartTime
		s.mu.Unlock()

		if isFirst {
			log.Printf("[Recording Sync] 📌 Initial sync: Found latest recording %s (%s)", latest.ID, latest.FileName)
		} else {
			log.Printf("[Recording Sync] 🆕 New recording detected on SD card: %s (%s)", latest.ID, latest.FileName)
		}

		if s.onNewRecording != nil {
			s.onNewRecording(latest)
		}
	} else {
		s.mu.Unlock()
	}
}
