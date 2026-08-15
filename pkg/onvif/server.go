package onvif

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"steinel-cam-bridge/pkg/events"

	"github.com/google/uuid"
)

type Server struct {
	port          int
	httpServer    *http.Server
	deviceHandler *DeviceHandler
	mediaHandler  *MediaHandler
	eventHandler  *EventHandler
	deviceIO      *DeviceIOHandler
	discovery     *DiscoveryServer
	snapshotFunc  func() []byte
	mu            sync.RWMutex
}

func NewServer(
	port int,
	rtspPort int,
	rtspPath string,
	deviceID string,
	productID string,
	changeResFunc func(res string) error,
	rebootFunc func() error,
	setLampFunc func(mode string) error,
	setSirenFunc func(on bool) error,
	snapshotFunc func() []byte,
) *Server {
	if port == 0 {
		port = 8000
	}

	devHandler := NewDeviceHandler(deviceID, productID, port, rtspPort, rebootFunc)
	medHandler := NewMediaHandler(rtspPort, rtspPath, port, changeResFunc)
	evtHandler := NewEventHandler(port)
	ioHandler := NewDeviceIOHandler(setLampFunc, setSirenFunc)
	discServer := NewDiscoveryServer(port, deviceID)

	s := &Server{
		port:          port,
		deviceHandler: devHandler,
		mediaHandler:  medHandler,
		eventHandler:  evtHandler,
		deviceIO:      ioHandler,
		discovery:     discServer,
		snapshotFunc:  snapshotFunc,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/device_service", s.handleSOAP)
	mux.HandleFunc("/onvif/media_service", s.handleSOAP)
	mux.HandleFunc("/onvif/event_service", s.handleSOAP)
	mux.HandleFunc("/onvif/deviceio_service", s.handleSOAP)
	mux.HandleFunc("/snapshot.jpg", s.handleSnapshot)
	mux.HandleFunc("/api/status", s.handleAPIStatus)
	mux.HandleFunc("/api/light", s.handleAPILight)

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
		} else if strings.Contains(reqStr, "GetProfiles") || strings.Contains(reqStr, "GetStreamUri") || strings.Contains(reqStr, "GetSnapshotUri") {
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

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	var snap []byte
	if s.snapshotFunc != nil {
		snap = s.snapshotFunc()
	}

	if len(snap) == 0 {
		// Return 1x1 transparent JPEG placeholder if no keyframe captured yet
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		// Minimal valid 1x1 JPEG
		_, _ = w.Write([]byte{
			0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
			0x01, 0x01, 0x00, 0x48, 0x00, 0x48, 0x00, 0x00, 0xFF, 0xDB, 0x00, 0x43,
			0x00, 0x08, 0x06, 0x06, 0x07, 0x06, 0x05, 0x08, 0x07, 0x07, 0x07, 0x09,
			0x09, 0x08, 0x0A, 0x0C, 0x14, 0x0D, 0x0C, 0x0B, 0x0B, 0x0C, 0x19, 0x12,
			0x13, 0x0F, 0x14, 0x1D, 0x1A, 0x1F, 0x1E, 0x1D, 0x1A, 0x1C, 0x1C, 0x20,
			0x24, 0x2E, 0x27, 0x20, 0x22, 0x2C, 0x23, 0x1C, 0x1C, 0x28, 0x37, 0x29,
			0x2C, 0x30, 0x31, 0x34, 0x34, 0x34, 0x1F, 0x27, 0x39, 0x3D, 0x38, 0x32,
			0x3C, 0x2E, 0x33, 0x34, 0x32, 0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01,
			0x00, 0x01, 0x01, 0x01, 0x11, 0x00, 0xFF, 0xC4, 0x00, 0x1F, 0x00, 0x00,
			0x01, 0x05, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0A, 0x0B, 0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F,
			0x00, 0xBF, 0x80, 0xFF, 0xD9,
		})
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snap)
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
