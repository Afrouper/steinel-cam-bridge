package storage

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type mockProvider struct {
	items []RecordingItem
}

func (m *mockProvider) ListRecordings(ctx context.Context, start, end time.Time, page, limit int, eventType string) (*RecordingListResponse, error) {
	return &RecordingListResponse{
		Count: len(m.items),
		Total: len(m.items),
		List:  m.items,
	}, nil
}

func (m *mockProvider) GetRecording(ctx context.Context, id string) (*RecordingItem, error) {
	for _, it := range m.items {
		if it.ID == id {
			return &it, nil
		}
	}
	return nil, ErrStorageNotFound
}

func (m *mockProvider) StreamThumbnail(ctx context.Context, id string, w io.Writer) error {
	_, err := w.Write([]byte("fake-jpeg"))
	return err
}

func (m *mockProvider) StreamVideo(ctx context.Context, id string, w io.Writer, onStart func(name string, size int64)) error {
	if onStart != nil {
		onStart("test.mp4", 100)
	}
	_, err := w.Write([]byte("fake-mp4"))
	return err
}

func TestRecordingProviderInterface(t *testing.T) {
	now := time.Now().UTC()
	prov := &mockProvider{
		items: []RecordingItem{
			{
				ID:              "12345",
				StartTime:       now.Add(-10 * time.Minute),
				EndTime:         now.Add(-9 * time.Minute),
				DurationSeconds: 60,
				EventType:       "motion",
				FileSizeBytes:   5000,
				FileName:        "event_12345.mp4",
				ThumbnailURL:    "/api/sdcard/events/12345/thumbnail.jpg",
				VideoURL:        "/api/sdcard/events/12345/video.mp4",
			},
		},
	}

	var p RecordingProvider = prov

	resp, err := p.ListRecordings(context.Background(), time.Time{}, time.Time{}, 0, 10, "")
	assert.NoError(t, err)
	assert.Equal(t, 1, resp.Count)
	assert.Equal(t, "12345", resp.List[0].ID)

	rec, err := p.GetRecording(context.Background(), "12345")
	assert.NoError(t, err)
	assert.Equal(t, "event_12345.mp4", rec.FileName)

	_, err = p.GetRecording(context.Background(), "99999")
	assert.ErrorIs(t, err, ErrStorageNotFound)
}
