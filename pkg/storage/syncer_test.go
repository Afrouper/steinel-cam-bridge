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

func TestSyncer_SkipsEmptyItem(t *testing.T) {
	prov := &mockProvider{
		items: []RecordingItem{
			{
				ID:        "", // Empty ID!
				FileName:  "",
				StartTime: time.Time{},
			},
		},
	}

	var published []RecordingItem
	onNew := func(item RecordingItem) {
		published = append(published, item)
	}

	syncer := NewRecordingSyncer(
		func() RecordingProvider { return prov },
		onNew,
		100*time.Millisecond,
	)

	ctx := context.Background()
	syncer.syncOnce(ctx, true)

	assert.Empty(t, published, "Empty recording item should never be published")
}

type mockCachedProvider struct {
	mockProvider
	syncLatestCalls int
	listCalls       int
}

func (m *mockCachedProvider) SyncLatest(ctx context.Context) ([]RecordingItem, error) {
	m.syncLatestCalls++
	return nil, nil // No new items to download
}

func (m *mockCachedProvider) ListRecordings(ctx context.Context, start, end time.Time, page, limit int, eventType string) (*RecordingListResponse, error) {
	m.listCalls++
	return m.mockProvider.ListRecordings(ctx, start, end, page, limit, eventType)
}

func TestSyncer_NoDuplicateUpstreamQueryOnCached(t *testing.T) {
	cachedProv := &mockCachedProvider{}
	syncer := NewRecordingSyncer(
		func() RecordingProvider { return cachedProv },
		func(RecordingItem) {},
		100*time.Millisecond,
	)

	ctx := context.Background()
	syncer.syncOnce(ctx, false)

	assert.Equal(t, 1, cachedProv.syncLatestCalls, "SyncLatest should be called once")
	assert.Equal(t, 0, cachedProv.listCalls, "ListRecordings should NOT be called when SyncLatest handled the provider")
}

func TestSyncer_ZeroIntervalDisablesPeriodicSync(t *testing.T) {
	syncer := NewRecordingSyncer(
		func() RecordingProvider { return &mockProvider{} },
		func(RecordingItem) {},
		0,
	)
	assert.Equal(t, time.Duration(0), syncer.pollInterval, "pollInterval 0 should be retained to disable periodic polling")

	negSyncer := NewRecordingSyncer(
		func() RecordingProvider { return &mockProvider{} },
		func(RecordingItem) {},
		-5*time.Second,
	)
	assert.Equal(t, time.Duration(0), negSyncer.pollInterval, "negative pollInterval should normalize to 0")
}
