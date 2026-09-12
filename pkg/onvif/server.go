package onvif

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
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

// authenticateSOAP validates incoming SOAP requests against configured credentials.
// Returns true if authenticated or exempt, false if authentication failed (401 response sent).
func (s *Server) authenticateSOAP(w http.ResponseWriter, r *http.Request, action, reqStr string) bool {
	if s.authUser == "" {
		return true
	}

	// Exempt discovery and time sync from authentication (mandated by ONVIF Core Spec)
	if strings.Contains(action, "GetSystemDateAndTime") ||
		strings.Contains(reqStr, "GetSystemDateAndTime") ||
		strings.Contains(action, "GetCapabilities") ||
		strings.Contains(reqStr, "GetCapabilities") {
		return true
	}

	authHeader := r.Header.Get("Authorization")
	var isStale bool

	// 1. Try HTTP Digest Auth Header (RFC 2617, mandated by ONVIF Core Spec 5.1.2)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(authHeader)), "digest ") {
		status := ValidateDigestAuth(r.Method, r.URL.Path, authHeader, s.authUser, s.authPass, ONVIFAuthRealm, s.nonceManager)
		if status == AuthStatusSuccess {
			return true
		}
		if status == AuthStatusStale {
			isStale = true
		}
	}

	// 2. Try HTTP Basic Auth Header (used by simple HTTP clients)
	if user, pass, ok := r.BasicAuth(); ok {
		if subtle.ConstantTimeCompare([]byte(user), []byte(s.authUser)) == 1 &&
			subtle.ConstantTimeCompare([]byte(pass), []byte(s.authPass)) == 1 {
			return true
		}
	}

	// 3. Try WS-Security UsernameToken (used by ODM & ONVIF SOAP clients)
	tok, _ := ExtractUsernameToken(reqStr)
	if ValidateWSSecurity(tok, s.authUser, s.authPass) {
		return true
	}

	logger.Warn("ONVIF", "🔒 Authentication failure from %s for action '%s' on %s (Auth: %s)",
		r.RemoteAddr, action, r.URL.Path, RedactAuthHeader(authHeader))

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
	return false
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

	if !s.authenticateSOAP(w, r, action, reqStr) {
		return
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
