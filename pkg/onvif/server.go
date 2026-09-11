package onvif

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"

	"github.com/google/uuid"
)

type Server struct {
	port              int
	authUser          string
	authPass          string
	nonceManager      *NonceManager
	httpServer        *http.Server
	eventBus          *events.Bus
	deviceHandler     *DeviceHandler
	mediaHandler      *MediaHandler
	eventHandler      *EventHandler
	deviceIO          *DeviceIOHandler
	searchHandler     *SearchHandler
	replayHandler     *ReplayHandler
	recordingHandler  *RecordingHandler
	discovery         *DiscoveryServer
	recordingProvider func() storage.RecordingProvider
}

func NewServer(
	port int,
	rtspPort int,
	rtspPath string,
	audioCodec string,
	deviceID string,
	productID string,
	authUser string,
	authPass string,
	changeResFunc func(res string) error,
	rebootFunc func() error,
	setLampFunc func(mode string) error,
	setSirenFunc func(on bool) error,
	recordingProvider func() storage.RecordingProvider,
	eventBus *events.Bus,
) *Server {
	if port == 0 {
		port = 8000
	}
	if eventBus == nil {
		eventBus = events.GlobalBus
	}

	devHandler := NewDeviceHandler(deviceID, productID, port, rtspPort, authUser, rebootFunc, eventBus)
	medHandler := NewMediaHandler(rtspPort, rtspPath, audioCodec, port, changeResFunc, eventBus)
	evtHandler := NewEventHandler(port, eventBus)
	ioHandler := NewDeviceIOHandler(setLampFunc, setSirenFunc)
	searchH := NewSearchHandler(recordingProvider, port)
	replayH := NewReplayHandler(rtspPort, port)
	recH := NewRecordingHandler(port)
	discServer := NewDiscoveryServer(port, deviceID)

	s := &Server{
		port:              port,
		authUser:          authUser,
		authPass:          authPass,
		nonceManager:      NewNonceManager(5 * time.Minute),
		eventBus:          eventBus,
		deviceHandler:     devHandler,
		mediaHandler:      medHandler,
		eventHandler:      evtHandler,
		deviceIO:          ioHandler,
		searchHandler:     searchH,
		replayHandler:     replayH,
		recordingHandler:  recH,
		discovery:         discServer,
		recordingProvider: recordingProvider,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/device_service", s.handleSOAP)
	mux.HandleFunc("/onvif/media_service", s.handleSOAP)
	mux.HandleFunc("/onvif/event_service", s.handleSOAP)
	mux.HandleFunc("/onvif/deviceio_service", s.handleSOAP)
	mux.HandleFunc("/onvif/search_service", s.handleSOAP)
	mux.HandleFunc("/onvif/replay_service", s.handleSOAP)
	mux.HandleFunc("/onvif/recording_service", s.handleSOAP)
	mux.HandleFunc("/api/status", s.withBasicAuth(s.handleAPIStatus))
	mux.HandleFunc("/api/light", s.withBasicAuth(s.handleAPILight))
	mux.HandleFunc("/api/sdcard/events", s.withBasicAuth(s.handleAPISDCardEvents))
	mux.HandleFunc("/api/sdcard/events/", s.withBasicAuth(s.handleAPISDCardItem))

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return s
}

func (s *Server) Start(ctx context.Context) error {
	if s.authUser != "" {
		logger.Info("ONVIF", "🚀 ONVIF Profile S/T Server listening at http://0.0.0.0:%d/onvif/device_service (Auth: user '%s')", s.port, s.authUser)
	} else {
		logger.Info("ONVIF", "🚀 ONVIF Profile S/T Server listening at http://0.0.0.0:%d/onvif/device_service", s.port)
	}

	// Start WS-Discovery in background
	go func() {
		if err := s.discovery.Start(ctx); err != nil {
			logger.Warn("WS-Discovery", "⚠️ Discovery error: %v", err)
		}
	}()

	// Start HTTP Server in background
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warn("ONVIF", "⚠️ HTTP server error: %v", err)
		}
	}()

	return nil
}

func (s *Server) Close() {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}
}

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

func (s *Server) handleSOAP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	reqStr := string(bodyBytes)
	action := r.Header.Get("SOAPAction")
	subID := r.URL.Query().Get("sub")
	host := r.Host

	path := r.URL.Path
	logger.Trace("ONVIF", "SOAP request path=%s action=%s", path, action)

	// Check authentication when auth is enabled
	if s.authUser != "" {
		isExempt := strings.Contains(action, "GetSystemDateAndTime") ||
			strings.Contains(reqStr, "GetSystemDateAndTime") ||
			strings.Contains(action, "GetCapabilities") ||
			strings.Contains(reqStr, "GetCapabilities")

		if !isExempt {
			authenticated := false
			var isStale bool
			authHeader := r.Header.Get("Authorization")

			// 1. Try HTTP Digest Auth Header (RFC 2617, mandated by ONVIF Core Spec 5.1.2, used by Synology)
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(authHeader)), "digest ") {
				var valid bool
				valid, isStale = ValidateDigestAuth(r.Method, r.URL.Path, authHeader, s.authUser, s.authPass, ONVIFAuthRealm, s.nonceManager)
				if valid {
					authenticated = true
				}
			}

			// 2. Try HTTP Basic Auth Header (used by simple HTTP clients)
			if !authenticated {
				if user, pass, ok := r.BasicAuth(); ok {
					if subtle.ConstantTimeCompare([]byte(user), []byte(s.authUser)) == 1 &&
						subtle.ConstantTimeCompare([]byte(pass), []byte(s.authPass)) == 1 {
						authenticated = true
					}
				}
			}

			// 3. Try WS-Security UsernameToken (used by ODM & ONVIF SOAP clients)
			if !authenticated {
				tok, _ := ExtractUsernameToken(reqStr)
				if ValidateWSSecurity(tok, s.authUser, s.authPass) {
					authenticated = true
				}
			}

			if !authenticated {
				logger.Warn("ONVIF", "🔒 Authentication failure from %s for action '%s' on %s (Auth: %s)", r.RemoteAddr, action, path, RedactAuthHeader(authHeader))
				nonce := s.nonceManager.Generate()
				staleAttr := ""
				if isStale {
					staleAttr = `, stale=true`
				}
				w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", nonce="%s", qop="auth", algorithm=MD5%s`, ONVIFAuthRealm, nonce, staleAttr))
				w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, ONVIFAuthRealm))
				w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(FormatSOAPNotAuthorizedFault()))
				return
			}
		}
	}

	var innerResp string
	var handleErr error

	switch {
	case strings.HasSuffix(path, "device_service"):
		innerResp, handleErr = s.deviceHandler.Handle(action, reqStr, host)
	case strings.HasSuffix(path, "media_service"):
		innerResp, handleErr = s.mediaHandler.Handle(action, reqStr, host)
	case strings.HasSuffix(path, "event_service"):
		innerResp, handleErr = s.eventHandler.Handle(action, reqStr, host, subID)
	case strings.HasSuffix(path, "deviceio_service"):
		innerResp, handleErr = s.deviceIO.Handle(action, reqStr)
	case strings.HasSuffix(path, "search_service"):
		innerResp, handleErr = s.searchHandler.Handle(action, reqStr, host)
	case strings.HasSuffix(path, "replay_service"):
		innerResp, handleErr = s.replayHandler.Handle(action, reqStr, host)
	case strings.HasSuffix(path, "recording_service"):
		innerResp, handleErr = s.recordingHandler.Handle(action, reqStr, host)
	default:
		// Fallback detection by content
		if strings.Contains(reqStr, "GetDeviceInformation") || strings.Contains(reqStr, "GetCapabilities") || strings.Contains(reqStr, "GetServices") {
			innerResp, handleErr = s.deviceHandler.Handle(action, reqStr, host)
		} else if strings.Contains(reqStr, "GetProfiles") || strings.Contains(reqStr, "GetStreamUri") {
			innerResp, handleErr = s.mediaHandler.Handle(action, reqStr, host)
		} else if strings.Contains(reqStr, "PullMessages") || strings.Contains(reqStr, "CreatePullPointSubscription") {
			innerResp, handleErr = s.eventHandler.Handle(action, reqStr, host, subID)
		} else if strings.Contains(reqStr, "FindRecordings") || strings.Contains(reqStr, "GetRecordingSummary") || strings.Contains(reqStr, "GetRecordingSearchResults") {
			innerResp, handleErr = s.searchHandler.Handle(action, reqStr, host)
		} else if strings.Contains(reqStr, "GetReplayUri") || strings.Contains(reqStr, "GetReplayConfiguration") {
			innerResp, handleErr = s.replayHandler.Handle(action, reqStr, host)
		} else if strings.Contains(reqStr, "GetRecordings") || strings.Contains(reqStr, "GetRecordingConfiguration") {
			innerResp, handleErr = s.recordingHandler.Handle(action, reqStr, host)
		} else {
			innerResp, handleErr = s.deviceHandler.Handle(action, reqStr, host)
		}
	}

	if handleErr != nil || innerResp == "" {
		logger.Warn("ONVIF", "⚠️ Unhandled SOAP action '%s' on %s (error: %v)", action, path, handleErr)
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(wrapSOAPFault(action, handleErr)))
		return
	}

	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(wrapSOAPResponse(innerResp)))
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	st := s.eventBus.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

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

func wrapSOAPResponse(innerXML string) string {
	msgID := "urn:uuid:" + uuid.New().String()
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://www.w3.org/2005/08/addressing" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tev="http://www.onvif.org/ver10/events/wsdl" xmlns:tio="http://www.onvif.org/ver10/deviceIO/wsdl" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">
  <s:Header>
    <a:MessageID>%s</a:MessageID>
    <a:To>http://www.w3.org/2005/08/addressing/anonymous</a:To>
  </s:Header>
  <s:Body>
    %s
  </s:Body>
</s:Envelope>`, msgID, innerXML)
}

func wrapSOAPFault(action string, err error) string {
	errMsg := "Action not supported"
	if err != nil {
		errMsg = err.Error()
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://www.w3.org/2005/08/addressing">
  <s:Body>
    <s:Fault>
      <s:Code><s:Value>s:Receiver</s:Value></s:Code>
      <s:Reason><s:Text xml:lang="en">%s: %s</s:Text></s:Reason>
    </s:Fault>
  </s:Body>
</s:Envelope>`, action, errMsg)
}
