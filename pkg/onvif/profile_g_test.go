package onvif

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/stretchr/testify/assert"
)

type testRecordingProvider struct {
	items []storage.RecordingItem
}

func (m *testRecordingProvider) ListRecordings(ctx context.Context, start, end time.Time, page, limit int, eventType string) (*storage.RecordingListResponse, error) {
	return &storage.RecordingListResponse{
		Count: len(m.items),
		Total: len(m.items),
		List:  m.items,
	}, nil
}

func (m *testRecordingProvider) GetRecording(ctx context.Context, id string) (*storage.RecordingItem, error) {
	for _, it := range m.items {
		if it.ID == id {
			return &it, nil
		}
	}
	return nil, storage.ErrStorageNotFound
}

func (m *testRecordingProvider) StreamThumbnail(ctx context.Context, id string, w io.Writer) error {
	_, err := w.Write([]byte("fake-jpeg-data"))
	return err
}

func (m *testRecordingProvider) StreamVideo(ctx context.Context, id string, w io.Writer, onStart func(name string, size int64)) error {
	if onStart != nil {
		onStart("event_test.mp4", 1024)
	}
	_, err := w.Write([]byte("fake-mp4-video-data"))
	return err
}

func TestProfileGServicesAndREST(t *testing.T) {
	now := time.Now().UTC()
	fakeItems := []storage.RecordingItem{
		{
			ID:              "1724528700",
			StartTime:       now.Add(-5 * time.Minute),
			EndTime:         now.Add(-4 * time.Minute),
			DurationSeconds: 60,
			EventType:       "motion",
			FileSizeBytes:   1024,
			FileName:        "event_1724528700.mp4",
			ThumbnailURL:    "/api/sdcard/events/1724528700/thumbnail.jpg",
			VideoURL:        "/api/sdcard/events/1724528700/video.mp4",
		},
	}
	mockProv := &testRecordingProvider{items: fakeItems}

	srv := NewServer(
		8000, 8554, "live", "aac", "de-test", "pr-test",
		nil, nil, nil, nil,
		func() storage.RecordingProvider { return mockProv },
	)

	// 1. Test Profile G in GetCapabilities
	capResp := srv.deviceHandler.getCapabilities("127.0.0.1:8000")
	assert.Contains(t, capResp, "http://127.0.0.1:8000/onvif/search_service")
	assert.Contains(t, capResp, "http://127.0.0.1:8000/onvif/replay_service")
	assert.Contains(t, capResp, "http://127.0.0.1:8000/onvif/recording_service")

	// 2. Test Profile G in GetServices
	svcResp := srv.deviceHandler.getServices("127.0.0.1:8000")
	assert.Contains(t, svcResp, NS_TSE)
	assert.Contains(t, svcResp, NS_TRP)
	assert.Contains(t, svcResp, NS_TRC)

	// 3. Test Search Service: FindRecordings & GetRecordingSearchResults
	findResp, err := srv.searchHandler.Handle("FindRecordings", "", "127.0.0.1:8000")
	assert.NoError(t, err)
	assert.Contains(t, findResp, "SearchToken_")

	searchRes, err := srv.searchHandler.Handle("GetRecordingSearchResults", "", "127.0.0.1:8000")
	assert.NoError(t, err)
	assert.Contains(t, searchRes, "1724528700")
	assert.Contains(t, searchRes, "Track_Video")

	// 4. Test Replay Service: GetReplayUri
	replayResp, err := srv.replayHandler.Handle("GetReplayUri", "", "127.0.0.1:8000")
	assert.NoError(t, err)
	assert.Contains(t, replayResp, "rtsp://127.0.0.1:8554/live")

	// 5. Test Recording Service: GetRecordings
	recResp, err := srv.recordingHandler.Handle("GetRecordings", "", "127.0.0.1:8000")
	assert.NoError(t, err)
	assert.Contains(t, recResp, "Recording_Main")

	// 6. Test REST API: GET /api/sdcard/events
	req := httptest.NewRequest(http.MethodGet, "/api/sdcard/events?limit=10", nil)
	w := httptest.NewRecorder()
	srv.handleAPISDCardEvents(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var list []storage.RecordingItem
	err = json.Unmarshal(w.Body.Bytes(), &list)
	assert.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "1724528700", list[0].ID)

	// 7. Test REST API: GET /api/sdcard/events/1724528700 (Metadata)
	reqMeta := httptest.NewRequest(http.MethodGet, "/api/sdcard/events/1724528700", nil)
	wMeta := httptest.NewRecorder()
	srv.handleAPISDCardItem(wMeta, reqMeta)
	assert.Equal(t, http.StatusOK, wMeta.Code)
	var single storage.RecordingItem
	assert.NoError(t, json.Unmarshal(wMeta.Body.Bytes(), &single))
	assert.Equal(t, "1724528700", single.ID)

	// 8. Test REST API: GET /api/sdcard/events/1724528700/thumbnail.jpg
	reqThumb := httptest.NewRequest(http.MethodGet, "/api/sdcard/events/1724528700/thumbnail.jpg", nil)
	wThumb := httptest.NewRecorder()
	srv.handleAPISDCardItem(wThumb, reqThumb)
	assert.Equal(t, http.StatusOK, wThumb.Code)
	assert.Equal(t, "image/jpeg", wThumb.Header().Get("Content-Type"))
	assert.Equal(t, "fake-jpeg-data", wThumb.Body.String())

	// 9. Test REST API: GET /api/sdcard/events/1724528700/video.mp4
	reqVid := httptest.NewRequest(http.MethodGet, "/api/sdcard/events/1724528700/video.mp4", nil)
	wVid := httptest.NewRecorder()
	srv.handleAPISDCardItem(wVid, reqVid)
	assert.Equal(t, http.StatusOK, wVid.Code)
	assert.Equal(t, "video/mp4", wVid.Header().Get("Content-Type"))
	assert.Contains(t, wVid.Header().Get("Content-Disposition"), "event_test.mp4")
	assert.Equal(t, "fake-mp4-video-data", wVid.Body.String())

	// 10. Test SOAP routing in handleSOAP
	soapReq := httptest.NewRequest(http.MethodPost, "/onvif/search_service", strings.NewReader(`<FindRecordings xmlns="http://www.onvif.org/ver10/search/wsdl"/>`))
	soapW := httptest.NewRecorder()
	srv.handleSOAP(soapW, soapReq)
	assert.Equal(t, http.StatusOK, soapW.Code)
	assert.Contains(t, soapW.Body.String(), "FindRecordingsResponse")
}
