package onvif

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"steinel-cam-bridge/pkg/events"
	"steinel-cam-bridge/pkg/webrtc"

	"github.com/google/uuid"
)

type Server struct {
	port           int
	httpServer     *http.Server
	deviceHandler  *DeviceHandler
	mediaHandler   *MediaHandler
	eventHandler   *EventHandler
	deviceIO       *DeviceIOHandler
	discovery      *DiscoveryServer
	sdcardProvider func() *webrtc.SDCardManager
}

func NewServer(
	port int,
	rtspPort int,
	rtspPath string,
	audioCodec string,
	deviceID string,
	productID string,
	changeResFunc func(res string) error,
	rebootFunc func() error,
	setLampFunc func(mode string) error,
	setSirenFunc func(on bool) error,
	sdcardProvider func() *webrtc.SDCardManager,
) *Server {
	if port == 0 {
		port = 8000
	}

	devHandler := NewDeviceHandler(deviceID, productID, port, rtspPort, rebootFunc)
	medHandler := NewMediaHandler(rtspPort, rtspPath, audioCodec, port, changeResFunc)
	evtHandler := NewEventHandler(port)
	ioHandler := NewDeviceIOHandler(setLampFunc, setSirenFunc)
	discServer := NewDiscoveryServer(port, deviceID)

	s := &Server{
		port:           port,
		deviceHandler:  devHandler,
		mediaHandler:   medHandler,
		eventHandler:   evtHandler,
		deviceIO:       ioHandler,
		discovery:      discServer,
		sdcardProvider: sdcardProvider,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/device_service", s.handleSOAP)
	mux.HandleFunc("/onvif/media_service", s.handleSOAP)
	mux.HandleFunc("/onvif/event_service", s.handleSOAP)
	mux.HandleFunc("/onvif/deviceio_service", s.handleSOAP)
	mux.HandleFunc("/api/status", s.handleAPIStatus)
	mux.HandleFunc("/api/light", s.handleAPILight)
	mux.HandleFunc("/api/sdcard/events", s.handleAPISDCardEvents)
	mux.HandleFunc("/api/sdcard/events/", s.handleAPISDCardItem)

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return s
}

func (s *Server) Start(ctx context.Context) error {
	log.Printf("[ONVIF] 🚀 ONVIF Profile S/T Server listening at http://0.0.0.0:%d/onvif/device_service", s.port)

	// Start WS-Discovery in background
	go func() {
		if err := s.discovery.Start(ctx); err != nil {
			log.Printf("[WS-Discovery] ⚠️ Discovery error: %v", err)
		}
	}()

	// Start HTTP Server in background
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[ONVIF] ⚠️ HTTP server error: %v", err)
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

	var innerResp string
	var handleErr error

	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "device_service"):
		innerResp, handleErr = s.deviceHandler.Handle(action, reqStr, host)
	case strings.HasSuffix(path, "media_service"):
		innerResp, handleErr = s.mediaHandler.Handle(action, reqStr, host)
	case strings.HasSuffix(path, "event_service"):
		innerResp, handleErr = s.eventHandler.Handle(action, reqStr, host, subID)
	case strings.HasSuffix(path, "deviceio_service"):
		innerResp, handleErr = s.deviceIO.Handle(action, reqStr)
	default:
		// Fallback detection by content
		if strings.Contains(reqStr, "GetDeviceInformation") || strings.Contains(reqStr, "GetCapabilities") || strings.Contains(reqStr, "GetServices") {
			innerResp, handleErr = s.deviceHandler.Handle(action, reqStr, host)
		} else if strings.Contains(reqStr, "GetProfiles") || strings.Contains(reqStr, "GetStreamUri") {
			innerResp, handleErr = s.mediaHandler.Handle(action, reqStr, host)
		} else if strings.Contains(reqStr, "PullMessages") || strings.Contains(reqStr, "CreatePullPointSubscription") {
			innerResp, handleErr = s.eventHandler.Handle(action, reqStr, host, subID)
		} else {
			innerResp, handleErr = s.deviceHandler.Handle(action, reqStr, host)
		}
	}

	if handleErr != nil || innerResp == "" {
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
	st := events.GlobalBus.GetStatus()
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
	if s.sdcardProvider == nil {
		http.Error(w, `{"error":"sdcard feature not configured"}`, http.StatusNotImplemented)
		return
	}
	sdm := s.sdcardProvider()
	if sdm == nil {
		http.Error(w, `{"error":"camera offline"}`, http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))

	resp, err := sdm.GetEventList(r.Context(), start, end, page, limit)
	if err != nil {
		if errors.Is(err, webrtc.ErrSDCardBusy) {
			http.Error(w, `{"error":"sdcard busy"}`, http.StatusTooManyRequests)
			return
		}
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleAPISDCardItem(w http.ResponseWriter, r *http.Request) {
	if s.sdcardProvider == nil {
		http.Error(w, `{"error":"sdcard feature not configured"}`, http.StatusNotImplemented)
		return
	}
	sdm := s.sdcardProvider()
	if sdm == nil {
		http.Error(w, `{"error":"camera offline"}`, http.StatusServiceUnavailable)
		return
	}

	// Path format: /api/sdcard/events/<timestamp>/<action>
	subPath := strings.TrimPrefix(r.URL.Path, "/api/sdcard/events/")
	parts := strings.Split(subPath, "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid path format. Expected /api/sdcard/events/<timestamp>/<action>", http.StatusBadRequest)
		return
	}

	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "Invalid timestamp", http.StatusBadRequest)
		return
	}

	action := parts[1]
	switch action {
	case "snapshot.jpg", "thumbnail.jpg", "snapshot":
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if err := sdm.StreamSnapshot(r.Context(), ts, w); err != nil {
			if errors.Is(err, webrtc.ErrSDCardBusy) {
				http.Error(w, "SD card busy", http.StatusTooManyRequests)
				return
			}
			log.Printf("[SDCard] Snapshot streaming error: %v", err)
		}

	case "video.mp4", "download.mp4", "video":
		err := sdm.StreamVideo(r.Context(), ts, w, func(name string, size int64) {
			if name == "" {
				name = fmt.Sprintf("event_%d.mp4", ts)
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name))
			if size > 0 {
				w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			}
			w.WriteHeader(http.StatusOK)
		})
		if err != nil {
			if errors.Is(err, webrtc.ErrSDCardBusy) {
				http.Error(w, "SD card busy", http.StatusTooManyRequests)
				return
			}
			if !errors.Is(err, webrtc.ErrTransferAborted) {
				log.Printf("[SDCard] Video streaming error: %v", err)
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
