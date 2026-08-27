package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRecordingSyncer(t *testing.T) {
	now := time.Now().UTC()
	var mu sync.Mutex
	items := []RecordingItem{
		{
			ID:              "rec_100",
			FileName:        "event_100.mp4",
			StartTime:       now.Add(-2 * time.Minute),
			EndTime:         now.Add(-1 * time.Minute),
			DurationSeconds: 60,
			EventType:       "motion",
		},
	}

	prov := &mockProvider{items: items}

	var published []RecordingItem
	var pubMu sync.Mutex

	onNew := func(item RecordingItem) {
		pubMu.Lock()
		published = append(published, item)
		pubMu.Unlock()
	}

	syncer := NewRecordingSyncer(
		func() RecordingProvider {
			mu.Lock()
			defer mu.Unlock()
			return prov
		},
		onNew,
		100*time.Millisecond,
	)

	// Test Initial Sync via direct call
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	syncer.syncOnce(ctx, true)

	pubMu.Lock()
	assert.Len(t, published, 1)
	assert.Equal(t, "rec_100", published[0].ID)
	pubMu.Unlock()

	// Calling again with same items -> no duplicate publication
	syncer.syncOnce(ctx, false)
	pubMu.Lock()
	assert.Len(t, published, 1)
	pubMu.Unlock()

	// Add new recording
	mu.Lock()
	prov.items = append([]RecordingItem{
		{
			ID:              "rec_101",
			FileName:        "event_101.mp4",
			StartTime:       now,
			EndTime:         now.Add(30 * time.Second),
			DurationSeconds: 30,
			EventType:       "motion",
		},
	}, prov.items...)
	mu.Unlock()

	// Sync again -> should publish new item
	syncer.syncOnce(ctx, false)
	pubMu.Lock()
	assert.Len(t, published, 2)
	assert.Equal(t, "rec_101", published[1].ID)
	pubMu.Unlock()
}
