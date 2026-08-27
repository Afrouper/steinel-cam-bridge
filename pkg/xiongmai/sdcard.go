package xiongmai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
)

// SofiaFileItem represents a single file match in OPFileQuery
type SofiaFileItem struct {
	BeginTime  string `json:"BeginTime"`
	EndTime    string `json:"EndTime"`
	FileLength string `json:"FileLength"`
	FileName   string `json:"FileName"`
}

// SDCardManager manages querying and streaming SD card recordings for Xiongmai Sofia cameras
type SDCardManager struct {
	client     *Client
	cameraIP   string
	cameraUser string
	cameraPwd  string
}

// NewSDCardManager creates a new Xiongmai SD-Card recording manager
func NewSDCardManager(client *Client, cameraIP, user, password string) *SDCardManager {
	return &SDCardManager{
		client:     client,
		cameraIP:   cameraIP,
		cameraUser: user,
		cameraPwd:  password,
	}
}

// ListRecordings implements storage.RecordingProvider
func (m *SDCardManager) ListRecordings(ctx context.Context, start, end time.Time, page, limit int, eventType string) (*storage.RecordingListResponse, error) {
	if m.client == nil || !m.client.IsLoggedIn() {
		return nil, storage.ErrStorageBusy
	}

	if start.IsZero() {
		start = time.Now().Add(-24 * time.Hour)
	}
	if end.IsZero() {
		end = time.Now()
	}

	beginStr := start.Format("2006-01-02 15:04:05")
	endStr := end.Format("2006-01-02 15:04:05")

	reqPayload := map[string]interface{}{
		"Name": "OPFileQuery",
		"OPFileQuery": map[string]interface{}{
			"BeginTime":        beginStr,
			"EndTime":          endStr,
			"Channel":          0,
			"DriverTypeFilter": "0x00000000",
			"Event":            "*",
			"StreamType":       "0x00000000",
			"Type":             "h264",
		},
		"SessionID": fmt.Sprintf("0x%08x", m.client.sessionID),
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	respBytes, err := m.client.SendPacket(MsgFileSearchReq, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("OPFileQuery failed: %w", err)
	}

	var parsed struct {
		Name        string          `json:"Name"`
		OPFileQuery []SofiaFileItem `json:"OPFileQuery"`
		Ret         int             `json:"Ret"`
	}

	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse OPFileQuery response: %w", err)
	}

	recordings := make([]storage.RecordingItem, 0, len(parsed.OPFileQuery))
	for _, f := range parsed.OPFileQuery {
		st, err1 := time.ParseInLocation("2006-01-02 15:04:05", f.BeginTime, time.Local)
		et, err2 := time.ParseInLocation("2006-01-02 15:04:05", f.EndTime, time.Local)
		if err1 != nil {
			st = start
		}
		if err2 != nil {
			et = end
		}

		dur := int(et.Sub(st).Seconds())
		if dur <= 0 {
			dur = 30
		}

		id := fmt.Sprintf("%d", st.Unix())
		recordings = append(recordings, storage.RecordingItem{
			ID:              id,
			StartTime:       st.UTC(),
			EndTime:         et.UTC(),
			DurationSeconds: dur,
			EventType:       "motion",
			FileName:        f.FileName,
			ThumbnailURL:    fmt.Sprintf("/api/sdcard/events/%s/thumbnail.jpg", id),
			VideoURL:        fmt.Sprintf("/api/sdcard/events/%s/video.mp4", id),
		})
	}

	return &storage.RecordingListResponse{
		Count: len(recordings),
		Total: len(recordings),
		List:  recordings,
	}, nil
}

// GetRecording implements storage.RecordingProvider
func (m *SDCardManager) GetRecording(ctx context.Context, id string) (*storage.RecordingItem, error) {
	resp, err := m.ListRecordings(ctx, time.Time{}, time.Time{}, 0, 100, "")
	if err != nil {
		return nil, err
	}

	for _, r := range resp.List {
		if r.ID == id {
			return &r, nil
		}
	}

	return nil, storage.ErrStorageNotFound
}

// StreamThumbnail implements storage.RecordingProvider
func (m *SDCardManager) StreamThumbnail(ctx context.Context, id string, w io.Writer) error {
	// For Sofia cameras without direct HTTP snapshot API for historical recordings,
	// return empty or generate frame
	return storage.ErrFeatureDisabled
}

// StreamVideo implements storage.RecordingProvider
func (m *SDCardManager) StreamVideo(ctx context.Context, id string, w io.Writer, onStart func(name string, size int64)) error {
	rec, err := m.GetRecording(ctx, id)
	if err != nil {
		return err
	}

	if onStart != nil {
		onStart(rec.FileName, rec.FileSizeBytes)
	}

	// For Xiongmai RTSP playback:
	// rtsp://admin:pwd@cameraIP:554/playback?channel=1&starttime=...
	playbackURL := fmt.Sprintf("rtsp://%s:%d/user=%s_password=%s_channel=1_stream=0.sdp?playback&start=%s",
		m.cameraIP, RTSPPort, m.cameraUser, m.cameraPwd, rec.StartTime.Format("20060102150405"))

	log.Printf("[Xiongmai SDCard] Replay requested for recording ID %s (%s)", id, SanitizeRTSPURL(playbackURL))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	return nil
}
