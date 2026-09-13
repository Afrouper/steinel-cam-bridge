package onvif

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
)

// withBasicAuth provides HTTP Basic Authentication protection for the REST API endpoints.
func (s *Server) withBasicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.authUser != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(s.authUser)) != 1 ||
				subtle.ConstantTimeCompare([]byte(pass), []byte(s.authPass)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="Steinel CAM Bridge"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

// handleAPIStatus returns the current camera and bridge telemetry as JSON.
func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	st := s.eventBus.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

// handleAPILight controls the camera light mode (on, off, auto).
func (s *Server) handleAPILight(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "auto"
	}
	if s.deviceIO != nil && s.deviceIO.setLampFunc != nil {
		if err := s.deviceIO.setLampFunc(mode); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleAPISDCardEvents lists MicroSD card recording events with optional time range and pagination.
func (s *Server) handleAPISDCardEvents(w http.ResponseWriter, r *http.Request) {
	if s.recordingProvider == nil {
		http.Error(w, `{"error":"sdcard recording feature not configured"}`, http.StatusNotImplemented)
		return
	}
	provider := s.recordingProvider()
	if provider == nil {
		http.Error(w, `{"error":"camera offline"}`, http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	var startTime, endTime time.Time
	if startStr := q.Get("start"); startStr != "" {
		if ts, err := strconv.ParseInt(startStr, 10, 64); err == nil {
			startTime = time.Unix(ts, 0).UTC()
		} else if t, err := time.Parse(time.RFC3339, startStr); err == nil {
			startTime = t.UTC()
		}
	}
	if endStr := q.Get("end"); endStr != "" {
		if ts, err := strconv.ParseInt(endStr, 10, 64); err == nil {
			endTime = time.Unix(ts, 0).UTC()
		} else if t, err := time.Parse(time.RFC3339, endStr); err == nil {
			endTime = t.UTC()
		}
	}

	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	eventType := q.Get("type")

	resp, err := provider.ListRecordings(r.Context(), startTime, endTime, page, limit, eventType)
	if err != nil {
		if errors.Is(err, storage.ErrStorageBusy) {
			http.Error(w, `{"error":"sdcard busy"}`, http.StatusTooManyRequests)
			return
		}
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if resp == nil || len(resp.List) == 0 {
		_, _ = w.Write([]byte(`[]`))
		return
	}
	_ = json.NewEncoder(w).Encode(resp.List)
}

// handleAPISDCardItem handles single recording metadata queries or video/thumbnail streaming.
func (s *Server) handleAPISDCardItem(w http.ResponseWriter, r *http.Request) {
	if s.recordingProvider == nil {
		http.Error(w, `{"error":"sdcard recording feature not configured"}`, http.StatusNotImplemented)
		return
	}
	provider := s.recordingProvider()
	if provider == nil {
		http.Error(w, `{"error":"camera offline"}`, http.StatusServiceUnavailable)
		return
	}

	subPath := strings.TrimPrefix(r.URL.Path, "/api/sdcard/events/")
	parts := strings.Split(strings.Trim(subPath, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Invalid path format. Expected /api/sdcard/events/<id> or /api/sdcard/events/<id>/<action>", http.StatusBadRequest)
		return
	}

	id := parts[0]

	// 1. Single recording metadata query: GET /api/sdcard/events/{id}
	if len(parts) == 1 {
		rec, err := provider.GetRecording(r.Context(), id)
		if err != nil {
			if errors.Is(err, storage.ErrStorageNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rec)
		return
	}

	// 2. Action query: GET /api/sdcard/events/{id}/thumbnail.jpg or video.mp4
	action := parts[1]
	switch action {
	case "snapshot.jpg", "thumbnail.jpg", "snapshot":
		var buf bytes.Buffer
		if err := provider.StreamThumbnail(r.Context(), id, &buf); err != nil {
			if errors.Is(err, storage.ErrStorageBusy) {
				http.Error(w, "SD card busy", http.StatusTooManyRequests)
				return
			}
			if errors.Is(err, storage.ErrFeatureDisabled) {
				http.Error(w, "Thumbnail not supported on this model", http.StatusNotImplemented)
				return
			}
			logger.Warn("SDCard", "Snapshot streaming error: %v", err)
			http.Error(w, fmt.Sprintf("Failed to load snapshot: %v", err), http.StatusInternalServerError)
			return
		}
		if buf.Len() == 0 {
			http.Error(w, "Snapshot is empty", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())

	case "video.mp4", "download.mp4", "video", "stream.mp4":
		err := provider.StreamVideo(r.Context(), id, w, func(name string, size int64) {
			if name == "" {
				name = fmt.Sprintf("event_%s.mp4", id)
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name))
			if size > 0 {
				w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			}
			w.WriteHeader(http.StatusOK)
		})
		if err != nil {
			if errors.Is(err, storage.ErrStorageBusy) {
				http.Error(w, "SD card busy", http.StatusTooManyRequests)
				return
			}
			if !errors.Is(err, storage.ErrTransferAborted) {
				logger.Warn("SDCard", "Video streaming error: %v", err)
			}
		}

	default:
		http.NotFound(w, r)
	}
}
