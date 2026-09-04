package webrtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
)

var (
	ErrSDCardBusy      = errors.New("sdcard is currently busy with another transfer")
	ErrSDCardTimeout   = errors.New("sdcard transfer timed out waiting for camera response")
	ErrTransferAborted = errors.New("transfer was aborted by client")
)

// EventItem represents a single motion/manual recording on the camera's internal SD card
type EventItem struct {
	TimeTag    int64  `json:"time_tag"`
	RecordTime int    `json:"record_time"`
	Type       string `json:"type"`
	Name       string `json:"name,omitempty"`
	FileSize   int64  `json:"filesize,omitempty"`
}

// EventListResponse represents the list of recordings returned by the camera
type EventListResponse struct {
	Count int         `json:"count"`
	Total int         `json:"total"`
	List  []EventItem `json:"list"`
}

// activeTransfer tracks the current in-flight binary download (snapshot or video)
type activeTransfer struct {
	cmd       string
	timestamp int64
	fileName  string
	fileSize  int64
	chunkChan chan []byte
	doneChan  chan struct{}
	errChan   chan error
	lastData  time.Time
}

// SDCardManager manages querying recordings, snapshots, and streaming video files from the SD card
type SDCardManager struct {
	sendJSONCmd func(cmd string, info map[string]interface{}) error
	transferMu  sync.Mutex
	active      *activeTransfer
	mu          sync.Mutex
	debug       bool

	// Event list response synchronization
	eventListMu   sync.Mutex
	eventListChan chan *EventListResponse
}

// NewSDCardManager creates a new SD-Card manager instance
func NewSDCardManager(sendJSONCmd func(cmd string, info map[string]interface{}) error) *SDCardManager {
	return &SDCardManager{
		sendJSONCmd: sendJSONCmd,
	}
}

// SetDebug enables or disables verbose debug logging for SD card operations.
func (m *SDCardManager) SetDebug(debug bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.debug = debug
}

// GetEventList queries the list of recordings from the camera's SD card within a given time range
func (m *SDCardManager) GetEventList(ctx context.Context, startTime, endTime int64, page, limit int) (*EventListResponse, error) {
	if limit <= 0 {
		limit = 1000
	}
	if page < 0 {
		page = 0
	}
	if endTime == 0 {
		// Default to future to cover any camera clock skew
		endTime = 2147483647
	}

	m.mu.Lock()
	isDebug := m.debug
	m.mu.Unlock()

	if isDebug {
		log.Printf("[SDCard] 🔍 Requesting event list (start: %d, end: %d, page: %d, limit: %d)", startTime, endTime, page, limit)
	}

	m.eventListMu.Lock()
	respChan := make(chan *EventListResponse, 1)
	m.eventListChan = respChan
	m.eventListMu.Unlock()

	defer func() {
		m.eventListMu.Lock()
		m.eventListChan = nil
		m.eventListMu.Unlock()
	}()

	info := map[string]interface{}{
		"page":       page,
		"limit":      limit,
		"type":       "all",
		"start_time": startTime,
		"end_time":   endTime,
	}

	if err := m.sendJSONCmd("get_event_list", info); err != nil {
		return nil, fmt.Errorf("failed to send get_event_list command: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		log.Printf("[SDCard] ⚠️ Timed out waiting for get_event_list response from camera")
		return nil, ErrSDCardTimeout
	case resp := <-respChan:
		if resp == nil {
			return &EventListResponse{Count: 0, Total: 0, List: []EventItem{}}, nil
		}
		if isDebug {
			log.Printf("[SDCard] 📋 Received event list with %d items (Total: %d)", resp.Count, resp.Total)
		}
		return resp, nil
	}
}

// ListRecordings implements storage.RecordingProvider
func (m *SDCardManager) ListRecordings(ctx context.Context, start, end time.Time, page, limit int, eventType string) (*storage.RecordingListResponse, error) {
	var startUnix, endUnix int64
	if !start.IsZero() {
		startUnix = start.Unix()
	}
	if !end.IsZero() {
		endUnix = end.Unix()
	}

	rawResp, err := m.GetEventList(ctx, startUnix, endUnix, page, limit)
	if err != nil {
		if errors.Is(err, ErrSDCardBusy) {
			return nil, storage.ErrStorageBusy
		}
		if errors.Is(err, ErrSDCardTimeout) {
			return nil, storage.ErrStorageTimeout
		}
		return nil, err
	}

	items := make([]storage.RecordingItem, 0, len(rawResp.List))
	for _, raw := range rawResp.List {
		st := time.Unix(raw.TimeTag, 0).UTC()
		dur := raw.RecordTime
		if dur <= 0 {
			dur = 30 // Steinel CAM default recording clip duration is 30 seconds
		}
		et := st.Add(time.Duration(dur) * time.Second)
		id := strconv.FormatInt(raw.TimeTag, 10)
		name := raw.Name
		if name == "" {
			name = fmt.Sprintf("event_%s.mp4", id)
		}
		eType := raw.Type
		if eType == "" {
			eType = "motion"
		}
		items = append(items, storage.RecordingItem{
			ID:              id,
			StartTime:       st,
			EndTime:         et,
			DurationSeconds: dur,
			EventType:       eType,
			FileSizeBytes:   raw.FileSize,
			FileName:        name,
			ThumbnailURL:    "",
			VideoURL:        fmt.Sprintf("/api/sdcard/events/%s/video.mp4", id),
		})
	}

	return &storage.RecordingListResponse{
		Count: len(items),
		Total: rawResp.Total,
		List:  items,
	}, nil
}

// GetRecording implements storage.RecordingProvider
func (m *SDCardManager) GetRecording(_ context.Context, id string) (*storage.RecordingItem, error) {
	ts, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid recording ID: %w", err)
	}

	st := time.Unix(ts, 0).UTC()
	return &storage.RecordingItem{
		ID:              id,
		StartTime:       st,
		EndTime:         st.Add(30 * time.Second),
		DurationSeconds: 30,
		EventType:       "motion",
		FileName:        fmt.Sprintf("event_%s.mp4", id),
		ThumbnailURL:    "",
		VideoURL:        fmt.Sprintf("/api/sdcard/events/%s/video.mp4", id),
	}, nil
}

// StreamSnapshot streams a JPEG snapshot for a given event timestamp directly to an io.Writer
func (m *SDCardManager) StreamSnapshot(ctx context.Context, timestamp int64, w io.Writer) error {
	return m.streamFile(ctx, "get_snapshot", timestamp, w, nil)
}

// StreamThumbnail implements storage.RecordingProvider
func (m *SDCardManager) StreamThumbnail(_ context.Context, _ string, _ io.Writer) error {
	// L 625 CAM SC stores MP4 recordings on SD card without standalone JPEG thumbnails
	return storage.ErrFeatureDisabled
}

// StreamVideo implements storage.RecordingProvider
func (m *SDCardManager) StreamVideo(ctx context.Context, id string, w io.Writer, onStart func(name string, size int64)) error {
	ts, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid recording ID: %w", err)
	}
	err = m.streamFile(ctx, "get_event_video", ts, w, onStart)
	if errors.Is(err, ErrSDCardBusy) {
		return storage.ErrStorageBusy
	}
	if errors.Is(err, ErrSDCardTimeout) {
		return storage.ErrStorageTimeout
	}
	if errors.Is(err, ErrTransferAborted) {
		return storage.ErrTransferAborted
	}
	return err
}

// streamFile executes the binary download protocol with single-flight locking, streaming, and watchdog
func (m *SDCardManager) streamFile(ctx context.Context, cmd string, timestamp int64, w io.Writer, onStart func(name string, size int64)) error {
	// 1. Enforce strict single-flight concurrency (Concurrency = 1)
	if !m.transferMu.TryLock() {
		return ErrSDCardBusy
	}
	defer m.transferMu.Unlock()

	transfer := &activeTransfer{
		cmd:       cmd,
		timestamp: timestamp,
		chunkChan: make(chan []byte, 32),
		doneChan:  make(chan struct{}),
		errChan:   make(chan error, 1),
		lastData:  time.Now(),
	}

	m.mu.Lock()
	m.active = transfer
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.active = nil
		m.mu.Unlock()
	}()

	// 2. Request file transfer from camera
	action := "start"
	if cmd == "get_snapshot" {
		action = "event_start"
	}
	info := map[string]interface{}{
		"timestamp": timestamp,
		"action":    action,
	}

	if err := m.sendJSONCmd(cmd, info); err != nil {
		return fmt.Errorf("failed to initiate transfer: %w", err)
	}

	// 3. Pump chunks from camera to HTTP writer with cancellation & watchdog
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	headerReported := false

	for {
		select {
		case <-ctx.Done():
			// Client cancelled download (e.g. browser tab closed) -> send stop to camera
			m.sendStopCommand(cmd)
			return ErrTransferAborted

		case err := <-transfer.errChan:
			m.sendStopCommand(cmd)
			return err

		case <-transfer.doneChan:
			// Transfer completed successfully
			return nil

		case <-ticker.C:
			// Transfer watchdog: abort if camera goes silent for > 10s
			m.mu.Lock()
			last := transfer.lastData
			m.mu.Unlock()
			if time.Since(last) > 10*time.Second {
				log.Printf("[SDCard] ⚠️ Transfer stalled (>10s no data). Aborting transfer for timestamp %d...", timestamp)
				m.sendStopCommand(cmd)
				return ErrSDCardTimeout
			}

		case chunk := <-transfer.chunkChan:
			m.mu.Lock()
			transfer.lastData = time.Now()
			name := transfer.fileName
			size := transfer.fileSize
			m.mu.Unlock()

			if !headerReported && onStart != nil && size > 0 {
				onStart(name, size)
				headerReported = true
			}

			if len(chunk) > 0 {
				if _, err := w.Write(chunk); err != nil {
					// Writer error (e.g. broken pipe) -> cancel camera transfer
					m.sendStopCommand(cmd)
					return err
				}
				// Flush if writer supports http.Flusher
				if flusher, ok := w.(interface{ Flush() }); ok {
					flusher.Flush()
				}
			}
		}
	}
}

func (m *SDCardManager) sendStopCommand(cmd string) {
	info := map[string]interface{}{
		"action": "stop",
	}
	_ = m.sendJSONCmd(cmd, info)
}

// HandleJSONMessage processes incoming DataChannel JSON responses for SD card events
func (m *SDCardManager) HandleJSONMessage(msg map[string]interface{}) bool {
	resp, _ := msg["resp"].(string)
	if resp == "" {
		return false
	}

	switch resp {
	case "get_event_list":
		info, _ := msg["info"].(map[string]interface{})
		res := &EventListResponse{}
		if info != nil {
			if count, ok := info["count"].(float64); ok {
				res.Count = int(count)
			}
			if total, ok := info["total"].(float64); ok {
				res.Total = int(total)
			}
			if listRaw, ok := info["list"]; ok {
				listBytes, _ := json.Marshal(listRaw)
				_ = json.Unmarshal(listBytes, &res.List)
			}
		}
		m.eventListMu.Lock()
		if m.eventListChan != nil {
			select {
			case m.eventListChan <- res:
			default:
			}
		}
		m.eventListMu.Unlock()
		return true

	case "get_event_video", "get_snapshot":
		action, _ := msg["action"].(string)
		info, _ := msg["info"].(map[string]interface{})

		m.mu.Lock()
		t := m.active
		m.mu.Unlock()

		if t == nil {
			return true
		}

		switch action {
		case "start":
			if info != nil {
				if name, ok := info["name"].(string); ok {
					t.fileName = name
				}
				if size, ok := info["filesize"].(float64); ok {
					t.fileSize = int64(size)
				}
			}
			log.Printf("[SDCard] 📥 Transfer started: %s (Size: %d bytes)", t.fileName, t.fileSize)

		case "end":
			log.Printf("[SDCard] ✅ Transfer completed: %s", t.fileName)
			close(t.doneChan)

		case "state":
			// Progress update from camera

		default:
			log.Printf("[SDCard] ℹ️ Message for %s with action '%s': %v", resp, action, msg)
			if action == "fail" || action == "error" {
				select {
				case t.errChan <- fmt.Errorf("camera reported transfer error: %s", action):
				default:
				}
			}
		}

		if info != nil {
			if code, ok := info["code"].(float64); ok && code != 200 && code != 0 {
				errMsg, _ := info["msg"].(string)
				log.Printf("[SDCard] ⚠️ Camera reported error code %v: %s", code, errMsg)
				select {
				case t.errChan <- fmt.Errorf("camera error %v: %s", code, errMsg):
				default:
				}
			}
		}
		return true
	}

	return false
}

// HandleBinaryChunk processes incoming raw binary data from the camera DataChannel
func (m *SDCardManager) HandleBinaryChunk(data []byte) {
	m.mu.Lock()
	t := m.active
	m.mu.Unlock()

	if t == nil || len(data) == 0 {
		return
	}

	select {
	case t.chunkChan <- data:
	default:
		log.Printf("[SDCard] ⚠️ Chunk buffer full, dropping chunk (%d bytes)", len(data))
	}
}
