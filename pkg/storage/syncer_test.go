package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	var publishedInitials []bool
	var pubMu sync.Mutex

	onNew := func(item RecordingItem, isInitial bool) {
		pubMu.Lock()
		published = append(published, item)
		publishedInitials = append(publishedInitials, isInitial)
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
	assert.True(t, publishedInitials[0], "First recording during initial sync must have isInitial=true")
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
	assert.False(t, publishedInitials[1], "New recording discovered during runtime must have isInitial=false")
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
	onNew := func(item RecordingItem, _ bool) {
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
		func(RecordingItem, bool) {},
		100*time.Millisecond,
	)

	ctx := context.Background()
	syncer.syncOnce(ctx, false)

	assert.Equal(t, 1, cachedProv.syncLatestCalls, "SyncLatest should be called once")
	assert.Equal(t, 0, cachedProv.listCalls, "ListRecordings should NOT be called when SyncLatest handled the provider")
}

func TestSyncer_RestoresFromCacheOnInitialSync(t *testing.T) {
	now := time.Now().UTC()
	cachedProv := &mockCachedProvider{
		mockProvider: mockProvider{
			items: []RecordingItem{
				{
					ID:              "cached_100",
					FileName:        "event_100.mp4",
					StartTime:       now.Add(-10 * time.Minute),
					EndTime:         now.Add(-9 * time.Minute),
					DurationSeconds: 60,
					EventType:       "motion",
				},
			},
		},
	}

	var published []RecordingItem
	syncer := NewRecordingSyncer(
		func() RecordingProvider { return cachedProv },
		func(item RecordingItem, _ bool) {
			published = append(published, item)
		},
		100*time.Millisecond,
	)

	// Initial sync when SyncLatest downloads 0 new items (already cached)
	ctx := context.Background()
	syncer.syncOnce(ctx, true)

	assert.Equal(t, 1, cachedProv.syncLatestCalls, "SyncLatest should be called once")
	assert.Equal(t, 1, cachedProv.listCalls, "ListRecordings should be called on initial sync to restore latest cached recording")
	assert.Len(t, published, 1, "Should publish 1 item restored from cache")
	assert.Equal(t, "cached_100", published[0].ID)

	// Second periodic sync -> no duplicate restore
	syncer.syncOnce(ctx, false)
	assert.Len(t, published, 1, "Should not publish again")
}

func TestSyncer_ZeroIntervalDisablesPeriodicSync(t *testing.T) {
	syncer := NewRecordingSyncer(
		func() RecordingProvider { return &mockProvider{} },
		func(RecordingItem, bool) {},
		0,
	)
	assert.Equal(t, time.Duration(0), syncer.pollInterval, "pollInterval 0 should be retained to disable periodic polling")

	negSyncer := NewRecordingSyncer(
		func() RecordingProvider { return &mockProvider{} },
		func(RecordingItem, bool) {},
		-5*time.Second,
	)
	assert.Equal(t, time.Duration(0), negSyncer.pollInterval, "negative pollInterval should normalize to 0")
}

type featureDisabledCachedProvider struct {
	mockProvider
	syncLatestCalls int
	listCalls       int
}

func (m *featureDisabledCachedProvider) SyncLatest(ctx context.Context) ([]RecordingItem, error) {
	m.syncLatestCalls++
	return nil, ErrFeatureDisabled
}

func (m *featureDisabledCachedProvider) ListRecordings(ctx context.Context, start, end time.Time, page, limit int, eventType string) (*RecordingListResponse, error) {
	m.listCalls++
	return m.mockProvider.ListRecordings(ctx, start, end, page, limit, eventType)
}

func TestSyncer_FallsThroughOnFeatureDisabled(t *testing.T) {
	now := time.Now().UTC()
	prov := &featureDisabledCachedProvider{
		mockProvider: mockProvider{
			items: []RecordingItem{
				{
					ID:              "1794211717",
					FileName:        "rec_1.h264",
					StartTime:       now.Add(-10 * time.Minute),
					EndTime:         now.Add(-9 * time.Minute),
					DurationSeconds: 60,
					EventType:       "motion",
				},
			},
		},
	}

	var published []RecordingItem
	syncer := NewRecordingSyncer(
		func() RecordingProvider { return prov },
		func(item RecordingItem, _ bool) { published = append(published, item) },
		100*time.Millisecond,
	)

	ctx := context.Background()

	// Initial sync: falls through to ListRecordings and publishes the initial recording
	syncer.syncOnce(ctx, true)
	assert.Equal(t, 1, prov.syncLatestCalls, "SyncLatest should be attempted")
	assert.Equal(t, 1, prov.listCalls, "ListRecordings must be called as fallback when video caching is disabled")
	require.Len(t, published, 1)
	assert.Equal(t, "1794211717", published[0].ID)

	// Second poll with same recording: no duplicate publish
	syncer.syncOnce(ctx, false)
	assert.Len(t, published, 1, "No duplicate publication when no new recording appeared")

	// Third poll with new motion recording: published!
	prov.items = []RecordingItem{
		{
			ID:              "1794212320",
			FileName:        "rec_2.h264",
			StartTime:       now.Add(-2 * time.Minute),
			EndTime:         now.Add(-1 * time.Minute),
			DurationSeconds: 60,
			EventType:       "motion",
		},
		{
			ID:              "1794211717",
			FileName:        "rec_1.h264",
			StartTime:       now.Add(-10 * time.Minute),
			EndTime:         now.Add(-9 * time.Minute),
			DurationSeconds: 60,
			EventType:       "motion",
		},
	}

	syncer.syncOnce(ctx, false)
	require.Len(t, published, 2, "New recording must be published when detected")
	assert.Equal(t, "1794212320", published[1].ID)
}
