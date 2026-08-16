package onvif

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"steinel-cam-bridge/pkg/events"
)

func TestONVIFServices(t *testing.T) {
	var requestedRes string
	var lampMode string

	changeRes := func(res string) error {
		requestedRes = res
		return nil
	}
	setLamp := func(mode string) error {
		lampMode = mode
		return nil
	}

	server := NewServer(
		8000,
		8554,
		"steinel",
		"aac",
		"de-xxxxxxx",
		"pr-xxxxx",
		changeRes,
		nil,
		setLamp,
		nil,
	)

	// 1. Test GetDeviceInformation
	reqBody := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>
  </s:Body>
</s:Envelope>`

	req := httptest.NewRequest("POST", "/onvif/device_service", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/soap+xml")
	w := httptest.NewRecorder()

	server.handleSOAP(w, req)
	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "STEINEL") || !strings.Contains(string(body), "L 625 CAM SC") {
		t.Fatalf("GetDeviceInformation response missing device info: %s", string(body))
	}

	// 2. Test GetProfiles (Media Service)
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <GetProfiles xmlns="http://www.onvif.org/ver10/media/wsdl"/>
  </s:Body>
</s:Envelope>`

	req = httptest.NewRequest("POST", "/onvif/media_service", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/soap+xml")
	w = httptest.NewRecorder()

	server.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)

	if !strings.Contains(string(body), "Profile_Main") || !strings.Contains(string(body), "1920") {
		t.Fatalf("GetProfiles response missing Profile_Main: %s", string(body))
	}

	// 3. Test SetVideoEncoderConfiguration (Dynamic Resolution)
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <SetVideoEncoderConfiguration xmlns="http://www.onvif.org/ver10/media/wsdl">
      <Configuration token="VideoEncoderConfig_1">
        <Resolution><Width>1280</Width><Height>720</Height></Resolution>
      </Configuration>
    </SetVideoEncoderConfiguration>
  </s:Body>
</s:Envelope>`

	req = httptest.NewRequest("POST", "/onvif/media_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	server.handleSOAP(w, req)

	if requestedRes != "720p" {
		t.Errorf("Expected requestedRes 720p, got %s", requestedRes)
	}

	// 4. Test Event Service PullPoint & Motion Event Trigger
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <CreatePullPointSubscription xmlns="http://www.onvif.org/ver10/events/wsdl"/>
  </s:Body>
</s:Envelope>`

	req = httptest.NewRequest("POST", "/onvif/event_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	server.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)

	if !strings.Contains(string(body), "CreatePullPointSubscriptionResponse") {
		t.Fatalf("CreatePullPointSubscription failed: %s", string(body))
	}

	// Trigger motion on EventBus
	events.GlobalBus.SetMotion(true)

	// Pull messages
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <PullMessages xmlns="http://www.onvif.org/ver10/events/wsdl"><Timeout>PT1S</Timeout><MessageLimit>10</MessageLimit></PullMessages>
  </s:Body>
</s:Envelope>`

	req = httptest.NewRequest("POST", "/onvif/event_service?sub=default", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	server.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)

	if !strings.Contains(string(body), "CellMotionDetector/Motion") || !strings.Contains(string(body), "true") {
		t.Fatalf("PullMessages did not return Motion=true: %s", string(body))
	}

	// 5. Test DeviceIO / Auxiliary Command (Light:On)
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <SendAuxiliaryCommand xmlns="http://www.onvif.org/ver20/ptz/wsdl"><AuxiliaryData>Light:On</AuxiliaryData></SendAuxiliaryCommand>
  </s:Body>
</s:Envelope>`

	req = httptest.NewRequest("POST", "/onvif/deviceio_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	server.handleSOAP(w, req)

	if lampMode != "on" {
		t.Errorf("Expected lampMode on, got %s", lampMode)
	}
}

func TestDiscoveryProbe(t *testing.T) {
	disc := NewDiscoveryServer(8000, "test-uuid-123")
	disc.localIP = "192.168.1.100"

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = disc.Start(ctx)
}
