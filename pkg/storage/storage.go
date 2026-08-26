package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrStorageBusy     = errors.New("storage device is currently busy with another transfer")
	ErrStorageTimeout  = errors.New("storage operation timed out waiting for camera response")
	ErrStorageNotFound = errors.New("recording not found")
	ErrTransferAborted = errors.New("transfer was aborted by client")
	ErrFeatureDisabled = errors.New("sdcard recording feature not configured or available")
)

// RecordingItem represents a unified metadata entry for a local SD card video recording
type RecordingItem struct {
	ID              string    `json:"id"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	DurationSeconds int       `json:"duration_seconds"`
	EventType       string    `json:"event_type"` // e.g. "motion", "manual", "alarm", "all"
	FileSizeBytes   int64     `json:"file_size_bytes,omitempty"`
	FileName        string    `json:"file_name,omitempty"`
	ThumbnailURL    string    `json:"thumbnail_url,omitempty"`
	VideoURL        string    `json:"video_url"`
}

// RecordingListResponse represents a paginated list of recording items
type RecordingListResponse struct {
	Count int             `json:"count"`
	Total int             `json:"total"`
	List  []RecordingItem `json:"list"`
}

// RecordingProvider defines the unified interface that hardware drivers (Nabto, Xiongmai) implement
type RecordingProvider interface {
	// ListRecordings queries recordings within the given time range
	ListRecordings(ctx context.Context, start, end time.Time, page, limit int, eventType string) (*RecordingListResponse, error)

	// GetRecording fetches metadata for a specific recording by its ID
	GetRecording(ctx context.Context, id string) (*RecordingItem, error)

	// StreamThumbnail streams the JPEG snapshot associated with the recording to the writer
	StreamThumbnail(ctx context.Context, id string, w io.Writer) error

	// StreamVideo streams the MP4 video associated with the recording to the writer
	StreamVideo(ctx context.Context, id string, w io.Writer, onStart func(name string, size int64)) error
}
