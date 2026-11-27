package onvif

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	bus := events.NewBus()
	server := NewServer(
		8000,
		8554,
		"steinel",
		"aac",
		"de-xxxxxxx",
		"pr-xxxxx",
		"L 625 CAM SC",
		"",
		"",
		"",
		changeRes,
		nil,
		setLamp,
		nil,
		nil,
		bus,
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
	bus.SetMotion(true)

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

	// 4b. Test SetSynchronizationPoint
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <SetSynchronizationPoint xmlns="http://www.onvif.org/ver10/events/wsdl"/>
  </s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/event_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	server.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "SetSynchronizationPointResponse") {
		t.Fatalf("SetSynchronizationPoint failed: %s", string(body))
	}

	// 4c. Test SubscribeRequest
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <Subscribe xmlns="http://docs.oasis-open.org/wsn/b-2"/>
  </s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/event_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	server.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "CreatePullPointSubscriptionResponse") {
		t.Fatalf("Subscribe failed: %s", string(body))
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

func TestDeviceHandler_NewActions(t *testing.T) {
	dh := NewDeviceHandler("device-uuid-test", "product-id-test", "L 625 CAM SC", 8000, 8554, "admin", "", nil, events.NewBus())

	// 1. GetServiceCapabilities
	resp, err := dh.Handle("GetServiceCapabilities", "", "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetServiceCapabilities failed: %v", err)
	}
	if !strings.Contains(resp, "GetServiceCapabilitiesResponse") ||
		!strings.Contains(resp, `UsernameToken="true"`) ||
		!strings.Contains(resp, `HttpDigest="true"`) {
		t.Errorf("Unexpected GetServiceCapabilities response: %s", resp)
	}

	// 2. GetEndpointReference
	resp, err = dh.Handle("GetEndpointReference", "", "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetEndpointReference failed: %v", err)
	}
	if !strings.Contains(resp, "GetEndpointReferenceResponse") ||
		!strings.Contains(resp, "urn:uuid:device-uuid-test") {
		t.Errorf("Unexpected GetEndpointReference response: %s", resp)
	}

	// 3. GetWsdlUrl
	resp, err = dh.Handle("GetWsdlUrl", "", "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetWsdlUrl failed: %v", err)
	}
	if !strings.Contains(resp, "GetWsdlUrlResponse") ||
		!strings.Contains(resp, "http://www.onvif.org/ver10/device/wsdl") {
		t.Errorf("Unexpected GetWsdlUrl response: %s", resp)
	}

	// 4. GetNetworkDefaultGateway
	resp, err = dh.Handle("GetNetworkDefaultGateway", "", "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetNetworkDefaultGateway failed: %v", err)
	}
	if !strings.Contains(resp, "GetNetworkDefaultGatewayResponse") ||
		!strings.Contains(resp, "192.168.1.50") {
		t.Errorf("Unexpected GetNetworkDefaultGateway response: %s", resp)
	}
}

func TestDeviceHandler_GetServicesCapabilities(t *testing.T) {
	dh := NewDeviceHandler("device-uuid-test", "product-id-test", "L 625 CAM SC", 8000, 8554, "admin", "", nil, events.NewBus())

	// Without IncludeCapability
	respNoCap, err := dh.Handle("GetServices", `<GetServices xmlns="http://www.onvif.org/ver10/device/wsdl"/>`, "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetServices without caps failed: %v", err)
	}
	if strings.Contains(respNoCap, "trt:Capabilities") || strings.Contains(respNoCap, "tev:Capabilities") {
		t.Errorf("GetServices without IncludeCapability should not include embedded capabilities: %s", respNoCap)
	}

	// With IncludeCapability = true
	respWithCap, err := dh.Handle("GetServices", `<GetServices xmlns="http://www.onvif.org/ver10/device/wsdl"><IncludeCapability>true</IncludeCapability></GetServices>`, "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetServices with caps failed: %v", err)
	}
	if !strings.Contains(respWithCap, "trt:Capabilities") ||
		!strings.Contains(respWithCap, "tev:Capabilities") ||
		!strings.Contains(respWithCap, "tmd:Capabilities") {
		t.Errorf("GetServices with IncludeCapability=true missing service capabilities: %s", respWithCap)
	}
}

func TestDeviceHandler_ModelReporting(t *testing.T) {
	dh620 := NewDeviceHandler("device-620", "product-620", "L 620 CAM", 8000, 8554, "admin", "", nil, events.NewBus())
	resp620, err := dh620.Handle("GetDeviceInformation", "", "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetDeviceInformation failed: %v", err)
	}
	if !strings.Contains(resp620, "<tds:Model>L 620 CAM</tds:Model>") {
		t.Errorf("expected model 'L 620 CAM', got: %s", resp620)
	}

	dh625 := NewDeviceHandler("device-625", "product-625", "L 625 CAM SC", 8000, 8554, "admin", "", nil, events.NewBus())
	resp625, err := dh625.Handle("GetDeviceInformation", "", "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetDeviceInformation failed: %v", err)
	}
	if !strings.Contains(resp625, "<tds:Model>L 625 CAM SC</tds:Model>") {
		t.Errorf("expected model 'L 625 CAM SC', got: %s", resp625)
	}
}

func TestDeviceHandler_MACAddress(t *testing.T) {
	// Default fallback MAC
	dhDefault := NewDeviceHandler("device-uuid", "product-id", "L 625 CAM SC", 8000, 8554, "admin", "", nil, events.NewBus())
	respDefault, err := dhDefault.Handle("GetNetworkInterfaces", "", "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetNetworkInterfaces failed: %v", err)
	}
	if !strings.Contains(respDefault, "<tt:HwAddress>00:0C:43:00:01:02</tt:HwAddress>") {
		t.Errorf("expected default MAC 00:0C:43:00:01:02, got: %s", respDefault)
	}

	// Custom MAC
	customMAC := "00:0C:43:AB:CD:EF"
	dhCustom := NewDeviceHandler("device-uuid", "product-id", "L 625 CAM SC", 8000, 8554, "admin", customMAC, nil, events.NewBus())
	respCustom, err := dhCustom.Handle("GetNetworkInterfaces", "", "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("GetNetworkInterfaces failed: %v", err)
	}
	if !strings.Contains(respCustom, "<tt:HwAddress>"+customMAC+"</tt:HwAddress>") {
		t.Errorf("expected custom MAC %s, got: %s", customMAC, respCustom)
	}
}

func TestMultiCameraONVIF(t *testing.T) {
	bus1 := events.NewBus()
	bus2 := events.NewBus()

	srv := NewServer(
		8000, 8554, "einfahrt", "aac",
		"dev-1", "prod-1", "L 625 CAM SC", "00:0C:43:00:01:02",
		"", "", nil, nil, nil, nil, nil, bus1,
	)

	// Configure multiple channels
	srv.SetChannels([]CameraChannel{
		{
			Index:      1,
			Name:       "Einfahrt",
			Path:       "einfahrt",
			AudioCodec: "aac",
			EventBus:   bus1,
		},
		{
			Index:      2,
			Name:       "Garten",
			Path:       "garten",
			AudioCodec: "pcmu",
			EventBus:   bus2,
		},
	})

	// 1. Check GetProfiles
	reqBody := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><GetProfiles xmlns="http://www.onvif.org/ver10/media/wsdl"/></s:Body>
</s:Envelope>`
	req := httptest.NewRequest("POST", "/onvif/media_service", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()
	srv.handleSOAP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	respStr := string(body)

	if !strings.Contains(respStr, "Profile_einfahrt_Main") || !strings.Contains(respStr, "Profile_garten_Main") {
		t.Fatalf("expected both camera profiles in GetProfiles response: %s", respStr)
	}
	if !strings.Contains(respStr, "VideoSource_1") || !strings.Contains(respStr, "VideoSource_2") {
		t.Fatalf("expected VideoSource_1 and VideoSource_2 in GetProfiles: %s", respStr)
	}

	// 2. Check GetVideoSources
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><GetVideoSources xmlns="http://www.onvif.org/ver10/media/wsdl"/></s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/media_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	respStr = string(body)

	if !strings.Contains(respStr, `token="VideoSource_1"`) || !strings.Contains(respStr, `token="VideoSource_2"`) {
		t.Fatalf("expected both VideoSource_1 and VideoSource_2: %s", respStr)
	}

	// 3. Check GetStreamUri for each profile
	for _, tc := range []struct {
		profileToken string
		expectedPath string
	}{
		{"Profile_einfahrt_Main", "einfahrt"},
		{"Profile_garten_Main", "garten"},
		{"Profile_garten_Sub", "garten"},
		{"Profile_Main", "einfahrt"}, // fallback to primary
	} {
		reqBody = fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><GetStreamUri xmlns="http://www.onvif.org/ver10/media/wsdl"><ProfileToken>%s</ProfileToken></GetStreamUri></s:Body>
</s:Envelope>`, tc.profileToken)
		req = httptest.NewRequest("POST", "/onvif/media_service", bytes.NewBufferString(reqBody))
		w = httptest.NewRecorder()
		srv.handleSOAP(w, req)
		body, _ = io.ReadAll(w.Result().Body)
		expectedURI := fmt.Sprintf("rtsp://example.com:8554/%s", tc.expectedPath)
		if !strings.Contains(string(body), expectedURI) {
			t.Errorf("GetStreamUri for %s did not contain %s: %s", tc.profileToken, expectedURI, string(body))
		}
	}

	// 4. Check REST API /api/cameras
	req = httptest.NewRequest("GET", "/api/cameras", nil)
	w = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), `"einfahrt"`) || !strings.Contains(string(body), `"garten"`) {
		t.Errorf("/api/cameras response missing cameras: %s", string(body))
	}

	// 5. Check PullMessages motion routing for Camera 2
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><CreatePullPointSubscription xmlns="http://www.onvif.org/ver10/events/wsdl"/></s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/event_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.handleSOAP(w, req)

	// Trigger motion on camera 2
	bus2.SetMotion(true)

	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><PullMessages xmlns="http://www.onvif.org/ver10/events/wsdl"/></s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/event_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	respStr = string(body)

	if !strings.Contains(respStr, `Value="VideoSourceConfig_2"`) || !strings.Contains(respStr, `Value="true"`) {
		t.Fatalf("expected motion event for VideoSourceConfig_2: %s", respStr)
	}

	// 6. Check GetCapabilities returns dynamic VideoSources/AudioSources = 2
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><GetCapabilities xmlns="http://www.onvif.org/ver10/device/wsdl"/></s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/device_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	respStr = string(body)
	if !strings.Contains(respStr, "<tt:VideoSources>2</tt:VideoSources>") ||
		!strings.Contains(respStr, "<tt:AudioSources>2</tt:AudioSources>") {
		t.Errorf("GetCapabilities should report 2 VideoSources and 2 AudioSources for 2 cameras: %s", respStr)
	}

	// 7. Check GetServices with IncludeCapability=true reports dynamic VideoSources = 2
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body><GetServices xmlns="http://www.onvif.org/ver10/device/wsdl"><IncludeCapability>true</IncludeCapability></GetServices></s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/device_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	respStr = string(body)
	if !strings.Contains(respStr, `VideoSources="2"`) || !strings.Contains(respStr, `AudioSources="2"`) {
		t.Errorf("GetServices should report VideoSources=2 and AudioSources=2: %s", respStr)
	}
}

func TestExtractXMLTag_NamespacesAndAttributes(t *testing.T) {
	cases := []struct {
		name     string
		xml      string
		tag      string
		expected string
	}{
		{
			name:     "plain tag",
			xml:      `<ProfileToken>Profile_Main</ProfileToken>`,
			tag:      "ProfileToken",
			expected: "Profile_Main",
		},
		{
			name:     "zeep ns0 namespace prefix",
			xml:      `<ns0:GetStreamUri><ns0:ProfileToken>Profile_garten_Main</ns0:ProfileToken></ns0:GetStreamUri>`,
			tag:      "ProfileToken",
			expected: "Profile_garten_Main",
		},
		{
			name:     "onvif trt namespace prefix with attributes",
			xml:      `<trt:ProfileToken xmlns:trt="http://www.onvif.org/ver10/media/wsdl">Profile_einfahrt_Sub</trt:ProfileToken>`,
			tag:      "ProfileToken",
			expected: "Profile_einfahrt_Sub",
		},
		{
			name:     "custom namespace prefix",
			xml:      `<wsdl:ProfileToken>Profile_custom</wsdl:ProfileToken>`,
			tag:      "ProfileToken",
			expected: "Profile_custom",
		},
		{
			name:     "self-closing tag with token attribute",
			xml:      `<ProfileToken token="Profile_token_attr"/>`,
			tag:      "ProfileToken",
			expected: "Profile_token_attr",
		},
		{
			name:     "namespaced self-closing tag with token attribute",
			xml:      `<ns0:ProfileToken token="Profile_ns_token_attr"/>`,
			tag:      "ProfileToken",
			expected: "Profile_ns_token_attr",
		},
		{
			name:     "tag with Value attribute fallback",
			xml:      `<ProfileToken Value="Profile_value_attr"/>`,
			tag:      "ProfileToken",
			expected: "Profile_value_attr",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractXMLTag(tc.xml, tc.tag)
			if got != tc.expected {
				t.Errorf("extractXMLTag(%q, %q) = %q, want %q", tc.xml, tc.tag, got, tc.expected)
			}
		})
	}
}

func TestMediaHandler_GetProfile_SingleAndMulti(t *testing.T) {
	// 1. Single-camera mode
	mhSingle := NewMediaHandler(8554, "steinel", "aac", 8000, nil, nil)
	mhSingle.SetChannels([]CameraChannel{
		{
			Index:      1,
			Name:       "Einfahrt",
			Path:       "steinel",
			AudioCodec: "aac",
		},
	})

	// Request Profile_Main
	reqMain := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><ns0:GetProfile xmlns:ns0="http://www.onvif.org/ver10/media/wsdl"><ns0:ProfileToken>Profile_Main</ns0:ProfileToken></ns0:GetProfile></s:Body></s:Envelope>`
	resp, err := mhSingle.Handle("GetProfile", reqMain, "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("Handle GetProfile failed: %v", err)
	}
	if !strings.Contains(resp, "<trt:GetProfileResponse") || !strings.Contains(resp, `token="Profile_Main"`) {
		t.Errorf("GetProfile Profile_Main response malformed: %s", resp)
	}
	if strings.Contains(resp, "<trt:Profiles") || strings.Contains(resp, "</trt:Profiles>") {
		t.Errorf("GetProfile response must use singular <trt:Profile>, not <trt:Profiles>: %s", resp)
	}

	// Request Profile_Sub
	reqSub := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><ns0:GetProfile xmlns:ns0="http://www.onvif.org/ver10/media/wsdl"><ns0:ProfileToken>Profile_Sub</ns0:ProfileToken></ns0:GetProfile></s:Body></s:Envelope>`
	resp, err = mhSingle.Handle("GetProfile", reqSub, "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("Handle GetProfile failed: %v", err)
	}
	if !strings.Contains(resp, "<trt:GetProfileResponse") || !strings.Contains(resp, `token="Profile_Sub"`) {
		t.Errorf("GetProfile Profile_Sub response malformed: %s", resp)
	}

	// 2. Multi-camera mode
	mhMulti := NewMediaHandler(8554, "einfahrt", "aac", 8000, nil, nil)
	mhMulti.SetChannels([]CameraChannel{
		{
			Index:      1,
			Name:       "Einfahrt",
			Path:       "einfahrt",
			AudioCodec: "aac",
		},
		{
			Index:      2,
			Name:       "Garten",
			Path:       "garten",
			AudioCodec: "pcmu",
		},
	})

	// Request Profile_garten_Main
	reqGarten := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><ns0:GetProfile xmlns:ns0="http://www.onvif.org/ver10/media/wsdl"><ns0:ProfileToken>Profile_garten_Main</ns0:ProfileToken></ns0:GetProfile></s:Body></s:Envelope>`
	resp, err = mhMulti.Handle("GetProfile", reqGarten, "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("Handle GetProfile failed: %v", err)
	}
	if !strings.Contains(resp, "<trt:GetProfileResponse") || !strings.Contains(resp, `token="Profile_garten_Main"`) {
		t.Errorf("GetProfile Profile_garten_Main response malformed: %s", resp)
	}
	if strings.Contains(resp, "VideoSourceConfig_1") {
		t.Errorf("GetProfile Profile_garten_Main should use VideoSourceConfig_2, but got VideoSourceConfig_1: %s", resp)
	}

	// Request Profile_einfahrt_Sub
	reqEinfahrtSub := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><ns0:GetProfile xmlns:ns0="http://www.onvif.org/ver10/media/wsdl"><ns0:ProfileToken>Profile_einfahrt_Sub</ns0:ProfileToken></ns0:GetProfile></s:Body></s:Envelope>`
	resp, err = mhMulti.Handle("GetProfile", reqEinfahrtSub, "192.168.1.50:8000")
	if err != nil {
		t.Fatalf("Handle GetProfile failed: %v", err)
	}
	if !strings.Contains(resp, "<trt:GetProfileResponse") || !strings.Contains(resp, `token="Profile_einfahrt_Sub"`) {
		t.Errorf("GetProfile Profile_einfahrt_Sub response malformed: %s", resp)
	}
}

func TestMultiCameraONVIF_ZeepGetStreamUriAndSnapshot(t *testing.T) {
	bus1 := events.NewBus()
	bus2 := events.NewBus()

	srv := NewServer(
		8000, 8554, "einfahrt", "aac",
		"dev-1", "prod-1", "L 625 CAM SC", "00:0C:43:00:01:02",
		"", "", nil, nil, nil, nil, nil, bus1,
	)

	srv.SetChannels([]CameraChannel{
		{
			Index:      1,
			Name:       "Einfahrt",
			Path:       "einfahrt",
			AudioCodec: "aac",
			EventBus:   bus1,
		},
		{
			Index:      2,
			Name:       "Garten",
			Path:       "garten",
			AudioCodec: "pcmu",
			EventBus:   bus2,
		},
	})

	// Test 1: GetStreamUri with Home Assistant onvif-zeep dynamic prefix <ns0:ProfileToken>
	reqBody := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <ns0:GetStreamUri xmlns:ns0="http://www.onvif.org/ver10/media/wsdl">
      <ns0:ProfileToken>Profile_garten_Main</ns0:ProfileToken>
    </ns0:GetStreamUri>
  </s:Body>
</s:Envelope>`
	req := httptest.NewRequest("POST", "/onvif/media_service", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()
	srv.handleSOAP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "rtsp://example.com:8554/garten") {
		t.Fatalf("GetStreamUri for Profile_garten_Main failed to route to /garten: %s", string(body))
	}

	// Test 2: GetStreamUri for Einfahrt with <trt:ProfileToken>
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <trt:GetStreamUri xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
      <trt:ProfileToken>Profile_einfahrt_Main</trt:ProfileToken>
    </trt:GetStreamUri>
  </s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/media_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "rtsp://example.com:8554/einfahrt") {
		t.Fatalf("GetStreamUri for Profile_einfahrt_Main failed to route to /einfahrt: %s", string(body))
	}

	// Test 3: GetSnapshotUri for Garten with <ns0:ProfileToken>
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <ns0:GetSnapshotUri xmlns:ns0="http://www.onvif.org/ver10/media/wsdl">
      <ns0:ProfileToken>Profile_garten_Main</ns0:ProfileToken>
    </ns0:GetSnapshotUri>
  </s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/media_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "camera=garten") {
		t.Fatalf("GetSnapshotUri for Profile_garten_Main failed to include ?camera=garten: %s", string(body))
	}

	// Test 4: GetSnapshotUri for Einfahrt with <ns0:ProfileToken>
	reqBody = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <ns0:GetSnapshotUri xmlns:ns0="http://www.onvif.org/ver10/media/wsdl">
      <ns0:ProfileToken>Profile_einfahrt_Main</ns0:ProfileToken>
    </ns0:GetSnapshotUri>
  </s:Body>
</s:Envelope>`
	req = httptest.NewRequest("POST", "/onvif/media_service", bytes.NewBufferString(reqBody))
	w = httptest.NewRecorder()
	srv.handleSOAP(w, req)
	body, _ = io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "camera=einfahrt") {
		t.Fatalf("GetSnapshotUri for Profile_einfahrt_Main failed to include ?camera=einfahrt: %s", string(body))
	}
}

func TestDeviceHandler_SetSystemDateAndTime(t *testing.T) {
	var calledTime time.Time
	var calledTZ string
	var callCount int

	srv := NewServer(
		8000,
		8554,
		"steinel",
		"aac",
		"de-xxxxxxx",
		"pr-xxxxx",
		"L 620 CAM",
		"",
		"",
		"",
		nil,
		nil,
		nil,
		nil,
		nil,
		events.NewBus(),
	)

	srv.SetTimeFunc(func(tm time.Time, tz string) error {
		callCount++
		calledTime = tm
		calledTZ = tz
		return nil
	})

	soapReq := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <tds:SetSystemDateAndTime>
      <tds:DateTimeType>Manual</tds:DateTimeType>
      <tds:DaylightSavings>false</tds:DaylightSavings>
      <tds:TimeZone>
        <tt:TZ>CET-1CEST,M3.5.0,M10.5.0/3</tt:TZ>
      </tds:TimeZone>
      <tds:UTCDateTime>
        <tt:Time>
          <tt:Hour>14</tt:Hour>
          <tt:Minute>30</tt:Minute>
          <tt:Second>45</tt:Second>
        </tt:Time>
        <tt:Date>
          <tt:Year>2026</tt:Year>
          <tt:Month>11</tt:Month>
          <tt:Day>9</tt:Day>
        </tt:Date>
      </tds:UTCDateTime>
    </tds:SetSystemDateAndTime>
  </s:Body>
</s:Envelope>`

	req := httptest.NewRequest("POST", "/onvif/device_service", bytes.NewBufferString(soapReq))
	req.Header.Set("Content-Type", "application/soap+xml")
	w := httptest.NewRecorder()

	srv.handleSOAP(w, req)
	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "SetSystemDateAndTimeResponse") {
		t.Fatalf("expected SetSystemDateAndTimeResponse, got: %s", string(body))
	}

	if callCount != 1 {
		t.Fatalf("expected SetTimeFunc to be called once, called %d times", callCount)
	}

	if calledTime.Year() != 2026 || calledTime.Month() != 11 || calledTime.Day() != 9 ||
		calledTime.Hour() != 14 || calledTime.Minute() != 30 || calledTime.Second() != 45 {
		t.Fatalf("unexpected parsed time: %v", calledTime)
	}

	if calledTZ != "CET-1CEST,M3.5.0,M10.5.0/3" {
		t.Fatalf("unexpected parsed timezone: %q", calledTZ)
	}
}

func TestONVIF_SnapshotLatestFallback(t *testing.T) {
	tempDir := t.TempDir()
	mockExt := storage.NewMockExtractor()
	cache := storage.NewRecordingCache(tempDir, 5, mockExt)

	// Create CachedRecordingProvider wrapping the empty cache
	provider := storage.NewCachedRecordingProvider(cache, nil)

	srv := &Server{
		recordingProvider: func() storage.RecordingProvider {
			return provider
		},
	}

	// 1. When no recordings exist, requesting latest snapshot.jpg must return 200 OK with fallback JPEG
	req := httptest.NewRequest("GET", "/api/sdcard/events/latest/snapshot.jpg", nil)
	w := httptest.NewRecorder()
	srv.handleAPISDCardItem(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))
	assert.Equal(t, fallbackSnapshotJPEG, body)

	// 2. Add a recording with a thumbnail to the cache
	now := time.Now().UTC()
	item := storage.RecordingItem{ID: "event_999", StartTime: now}
	_, err := cache.Add(context.Background(), item, bytes.NewReader([]byte("video-content-999")))
	require.NoError(t, err)

	// Now requesting latest snapshot.jpg must return the real thumbnail from the cache
	w2 := httptest.NewRecorder()
	srv.handleAPISDCardItem(w2, req)

	resp2 := w2.Result()
	body2, _ := io.ReadAll(resp2.Body)
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	assert.Equal(t, "image/jpeg", resp2.Header.Get("Content-Type"))
	assert.NotEmpty(t, body2)
	assert.NotEqual(t, fallbackSnapshotJPEG, body2)
}

func TestONVIF_DynamicResolution720p(t *testing.T) {
	bus := events.NewBus()
	st := bus.GetStatus()
	st.Resolution = "720p"
	bus.UpdateStatus(st)

	handler := NewMediaHandler(8554, "cam", "aac", 8000, nil, bus)
	handler.SetChannels([]CameraChannel{
		{
			Index:      1,
			Name:       "Einfahrt",
			Path:       "cam",
			AudioCodec: "aac",
			Resolution: "720p",
			EventBus:   bus,
		},
	})

	// 1. GetProfiles should report 1280x720 for Main profile
	profiles := handler.getProfiles()
	assert.Contains(t, profiles, "<tt:Width>1280</tt:Width>")
	assert.Contains(t, profiles, "<tt:Height>720</tt:Height>")
	assert.Contains(t, profiles, "MainStream 720p")

	// 2. GetVideoSources should report 1280x720
	sources := handler.getVideoSources()
	assert.Contains(t, sources, "<tt:Width>1280</tt:Width>")
	assert.Contains(t, sources, "<tt:Height>720</tt:Height>")

	// 3. GetVideoSourceConfigurations should report width="1280" height="720"
	sourceConfigs := handler.getVideoSourceConfigurations()
	assert.Contains(t, sourceConfigs, `width="1280" height="720"`)
}

func TestEventHandler_NoDuplicateSubscription(t *testing.T) {
	bus := events.NewBus()
	h := NewEventHandler(8000, bus)
	// Re-registering the same source token & bus (as SetChannels does for primary cam) must be a no-op
	h.AddSource("VideoSourceConfig_1", bus)

	subResp := h.createPullPointSubscription("127.0.0.1:8000")
	// Extract subID from ?sub=<id>
	idx := strings.Index(subResp, "?sub=")
	require.Greater(t, idx, 0)
	subID := subResp[idx+5:]
	subID = subID[:strings.Index(subID, "</wsa:Address>")]

	bus.SetMotion(true)

	h.mu.Lock()
	sub := h.subscriptions[subID]
	h.mu.Unlock()
	require.NotNil(t, sub)
	assert.Equal(t, 1, len(sub.EventChan), "motion event must only be queued once")
}

func TestEventHandler_ConcurrentPullMessagesAndRenew(t *testing.T) {
	bus := events.NewBus()
	h := NewEventHandler(8000, bus)

	subResp := h.createPullPointSubscription("127.0.0.1:8000")
	idx := strings.Index(subResp, "?sub=")
	require.Greater(t, idx, 0)
	subID := subResp[idx+5:]
	subID = subID[:strings.Index(subID, "</wsa:Address>")]

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			bus.SetMotion(n%2 == 0)
			_ = h.pullMessages(subID, "")
			_ = h.renew(subID)
		}(i)
	}
	wg.Wait()
}

// With audio_codec "none" the stream has no audio track, so the media profiles
// must not announce audio source, encoder or output either.
func TestMediaHandler_AudioNoneRemovesAudioBlocks(t *testing.T) {
	audioTags := []string{"AudioSourceConfiguration", "AudioEncoderConfiguration", "AudioOutputConfiguration"}
	reqAll := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetProfiles xmlns:trt="http://www.onvif.org/ver10/media/wsdl"/></s:Body></s:Envelope>`
	reqOne := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><ns0:GetProfile xmlns:ns0="http://www.onvif.org/ver10/media/wsdl"><ns0:ProfileToken>%s</ns0:ProfileToken></ns0:GetProfile></s:Body></s:Envelope>`

	responses := func(codec string) []string {
		mh := NewMediaHandler(8554, "steinel", codec, 8000, nil, nil)
		mh.SetChannels([]CameraChannel{{Index: 1, Name: "Einfahrt", Path: "steinel", AudioCodec: codec}})
		var out []string
		all, err := mh.Handle("GetProfiles", reqAll, "192.168.1.50:8000")
		require.NoError(t, err)
		out = append(out, all)
		for _, token := range []string{"Profile_Main", "Profile_Sub"} {
			one, err := mh.Handle("GetProfile", fmt.Sprintf(reqOne, token), "192.168.1.50:8000")
			require.NoError(t, err)
			out = append(out, one)
		}
		return out
	}

	// With audio the blocks are there (otherwise the check below proves nothing).
	for _, resp := range responses("aac") {
		for _, tag := range audioTags {
			assert.Contains(t, resp, "<tt:"+tag, "aac")
		}
	}

	for _, codec := range []string{"none", "None", "off"} {
		for _, resp := range responses(codec) {
			for _, tag := range audioTags {
				assert.NotContains(t, resp, tag, "audio_codec %q", codec)
			}
			// The video part stays complete and the XML closes properly.
			assert.Contains(t, resp, "<tt:VideoSourceConfiguration", "audio_codec %q", codec)
			assert.Contains(t, resp, "<tt:VideoEncoderConfiguration", "audio_codec %q", codec)
			assert.Equal(t, strings.Count(resp, "<tt:VideoEncoderConfiguration"), strings.Count(resp, "</tt:VideoEncoderConfiguration>"))
		}
	}
}

func assertValidXML(t *testing.T, payload string) {
	t.Helper()
	var sink any
	require.NoError(t, xml.Unmarshal([]byte(payload), &sink), "expected well-formed XML: %s", payload)
}

func TestMediaHandler_DynamicVideoCodec(t *testing.T) {
	t.Parallel()

	reqProfiles := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetProfiles xmlns:trt="http://www.onvif.org/ver10/media/wsdl"/></s:Body></s:Envelope>`
	reqProfileMain := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetProfile xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><trt:ProfileToken>Profile_Main</trt:ProfileToken></trt:GetProfile></s:Body></s:Envelope>`
	reqOptions := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetVideoEncoderConfigurationOptions xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><trt:ProfileToken>Profile_Main</trt:ProfileToken></trt:GetVideoEncoderConfigurationOptions></s:Body></s:Envelope>`

	t.Run("switches dynamically via VideoCodecFunc without reconfiguring handler", func(t *testing.T) {
		t.Parallel()
		is := assert.New(t)
		must := require.New(t)

		var activeCodec atomic.Value
		activeCodec.Store("h264")

		mh := NewMediaHandler(8554, "steinel", "aac", 8000, nil, nil)
		mh.SetChannels([]CameraChannel{
			{
				Index:      1,
				Name:       "Einfahrt",
				Path:       "steinel",
				AudioCodec: "aac",
				VideoCodecFunc: func() string {
					return activeCodec.Load().(string)
				},
			},
		})

		// Initially H.264
		profiles, err := mh.Handle("GetProfiles", reqProfiles, "192.168.1.50:8000")
		must.NoError(err)
		assertValidXML(t, profiles)
		is.Contains(profiles, "<tt:Encoding>H264</tt:Encoding>")
		is.Contains(profiles, "<tt:H264>")
		is.NotContains(profiles, "<tt:Encoding>H265</tt:Encoding>")

		opts, err := mh.Handle("GetVideoEncoderConfigurationOptions", reqOptions, "192.168.1.50:8000")
		must.NoError(err)
		assertValidXML(t, opts)
		is.Contains(opts, "<tt:H264>")
		is.NotContains(opts, "<tt:H265>")

		// Live switch to H.265
		activeCodec.Store("h265")

		profiles, err = mh.Handle("GetProfiles", reqProfiles, "192.168.1.50:8000")
		must.NoError(err)
		assertValidXML(t, profiles)
		is.Contains(profiles, "<tt:Encoding>H265</tt:Encoding>")
		is.NotContains(profiles, "<tt:Encoding>H264</tt:Encoding>")
		is.NotContains(profiles, "<tt:H264>")
		is.Contains(profiles, "<tt:Multicast>")

		single, err := mh.Handle("GetProfile", reqProfileMain, "192.168.1.50:8000")
		must.NoError(err)
		assertValidXML(t, single)
		is.Contains(single, "<tt:Encoding>H265</tt:Encoding>")
		is.NotContains(single, "<tt:H264>")

		opts, err = mh.Handle("GetVideoEncoderConfigurationOptions", reqOptions, "192.168.1.50:8000")
		must.NoError(err)
		assertValidXML(t, opts)
		is.Contains(opts, "<tt:H264>", "H.264 options must always remain advertised for NVR / Scrypted compatibility")
		is.Contains(opts, "<tt:H265>")
		is.Contains(opts, "<tt:H265ProfilesSupported>Main</tt:H265ProfilesSupported>")
	})

	t.Run("multi-camera channels report independent codecs including prefix-similar paths", func(t *testing.T) {
		t.Parallel()
		is := assert.New(t)
		must := require.New(t)

		mh := NewMediaHandler(8554, "cam", "aac", 8000, nil, nil)
		mh.SetChannels([]CameraChannel{
			{
				Index:      1,
				Name:       "Einfahrt",
				Path:       "cam",
				AudioCodec: "aac",
				VideoCodecFunc: func() string {
					return "h264"
				},
			},
			{
				Index:      2,
				Name:       "Garten",
				Path:       "cam2",
				AudioCodec: "aac",
				VideoCodecFunc: func() string {
					return "hevc"
				},
			},
		})

		profiles, err := mh.Handle("GetProfiles", reqProfiles, "192.168.1.50:8000")
		must.NoError(err)
		assertValidXML(t, profiles)
		is.Equal(2, strings.Count(profiles, "<tt:Encoding>H264</tt:Encoding>"))
		is.Equal(2, strings.Count(profiles, "<tt:Encoding>H265</tt:Encoding>"))

		// Verify Channels() reports resolved live VideoCodec as well.
		chs := mh.Channels()
		must.Len(chs, 2)
		is.Equal("h264", chs[0].VideoCodec)
		is.Equal("h265", chs[1].VideoCodec)

		// Verify GetProfile, GetStreamUri, and GetSnapshotUri resolve Profile_cam2_Main without prefix collision with "cam".
		profileCam2Req := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetProfile xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><trt:ProfileToken>Profile_cam2_Main</trt:ProfileToken></trt:GetProfile></s:Body></s:Envelope>`
		profileCam2, err := mh.Handle("GetProfile", profileCam2Req, "192.168.1.50:8000")
		must.NoError(err)
		assertValidXML(t, profileCam2)
		is.Contains(profileCam2, `token="Profile_cam2_Main"`)
		is.Contains(profileCam2, "Garten MainStream")
		is.Contains(profileCam2, "<tt:Encoding>H265</tt:Encoding>")

		streamURICam2Req := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetStreamUri xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><trt:ProfileToken>Profile_cam2_Main</trt:ProfileToken></trt:GetStreamUri></s:Body></s:Envelope>`
		streamURICam2, err := mh.Handle("GetStreamUri", streamURICam2Req, "192.168.1.50:8000")
		must.NoError(err)
		assertValidXML(t, streamURICam2)
		is.Contains(streamURICam2, "<tt:Uri>rtsp://192.168.1.50:8554/cam2</tt:Uri>")

		snapURICam2Req := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetSnapshotUri xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><trt:ProfileToken>Profile_cam2_Main</trt:ProfileToken></trt:GetSnapshotUri></s:Body></s:Envelope>`
		snapURICam2, err := mh.Handle("GetSnapshotUri", snapURICam2Req, "192.168.1.50:8000")
		must.NoError(err)
		assertValidXML(t, snapURICam2)
		is.Contains(snapURICam2, "camera=cam2</tt:Uri>")

		optsTests := []struct {
			name        string
			reqXML      string
			wantBlocks  []string
			forbidBlock string
		}{
			{
				name:        "profile token cam selects h264",
				reqXML:      `<trt:GetVideoEncoderConfigurationOptions><trt:ProfileToken>Profile_cam_Main</trt:ProfileToken></trt:GetVideoEncoderConfigurationOptions>`,
				wantBlocks:  []string{"<tt:H264>"},
				forbidBlock: "<tt:H265>",
			},
			{
				name:       "profile token cam2 advertises both h264 and h265 despite cam prefix",
				reqXML:     `<trt:GetVideoEncoderConfigurationOptions><trt:ProfileToken>Profile_cam2_Main</trt:ProfileToken></trt:GetVideoEncoderConfigurationOptions>`,
				wantBlocks: []string{"<tt:H264>", "<tt:H265>"},
			},
			{
				name:       "config token cam2 advertises both h264 and h265",
				reqXML:     `<trt:GetVideoEncoderConfigurationOptions><trt:ConfigurationToken>VideoEncoderConfig_cam2_Main</trt:ConfigurationToken></trt:GetVideoEncoderConfigurationOptions>`,
				wantBlocks: []string{"<tt:H264>", "<tt:H265>"},
			},
			{
				name:        "config token VideoEncoderConfig_Main falls back to primary h264",
				reqXML:      `<trt:GetVideoEncoderConfigurationOptions><trt:ConfigurationToken>VideoEncoderConfig_Main</trt:ConfigurationToken></trt:GetVideoEncoderConfigurationOptions>`,
				wantBlocks:  []string{"<tt:H264>"},
				forbidBlock: "<tt:H265>",
			},
		}

		for _, tt := range optsTests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				subIs := assert.New(t)
				subMust := require.New(t)

				resp, err := mh.Handle("GetVideoEncoderConfigurationOptions", tt.reqXML, "192.168.1.50:8000")
				subMust.NoError(err)
				assertValidXML(t, resp)
				for _, want := range tt.wantBlocks {
					subIs.Contains(resp, want)
				}
				if tt.forbidBlock != "" {
					subIs.NotContains(resp, tt.forbidBlock)
				}
			})
		}
	})
}

func TestMediaHandler_ScryptedAutoconfigureAndOSDs(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	bus := events.NewBus()
	var appliedResolutions []string
	changeRes := func(res string) error {
		appliedResolutions = append(appliedResolutions, res)
		return nil
	}

	mh := NewMediaHandler(8554, "steinel", "aac", 8000, changeRes, bus)
	mh.SetChannels([]CameraChannel{
		{
			Index:          1,
			Name:           "Einfahrt",
			Path:           "steinel",
			AudioCodec:     "aac",
			Resolution:     "1080p",
			VideoCodecFunc: func() string { return "h265" },
			ChangeResFunc:  changeRes,
			EventBus:       bus,
		},
	})

	// 1. GetServiceCapabilities (called by Scrypted canConfigureEncoding)
	caps, err := mh.Handle("", `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetServiceCapabilities xmlns="http://www.onvif.org/ver10/media/wsdl" /></s:Body></s:Envelope>`, "127.0.0.1:8000")
	must.NoError(err)
	assertValidXML(t, caps)
	is.Contains(caps, "<trt:ProfileCapabilities")

	// 2. node-onvif SetVideoEncoderConfiguration uses spaces around '=' in token = "..."
	setMainXML := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><SetVideoEncoderConfiguration xmlns="http://www.onvif.org/ver10/media/wsdl"><Configuration token = "VideoEncoderConfig_Main"><Resolution xmlns="http://www.onvif.org/ver10/schema"><Width>1920</Width><Height>1080</Height></Resolution></Configuration><ForcePersistence>true</ForcePersistence></SetVideoEncoderConfiguration></s:Body></s:Envelope>`
	setMainResp, err := mh.Handle("", setMainXML, "127.0.0.1:8000")
	must.NoError(err)
	assertValidXML(t, setMainResp)
	is.Contains(setMainResp, "SetVideoEncoderConfigurationResponse")

	// 3. Subsequent Profile_Sub autoconfigure pass (640x360) must NOT clobber 1080p main stream
	setSubXML := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><SetVideoEncoderConfiguration xmlns="http://www.onvif.org/ver10/media/wsdl"><Configuration token = "VideoEncoderConfig_Sub"><Resolution xmlns="http://www.onvif.org/ver10/schema"><Width>640</Width><Height>360</Height></Resolution></Configuration><ForcePersistence>true</ForcePersistence></SetVideoEncoderConfiguration></s:Body></s:Envelope>`
	setSubResp, err := mh.Handle("", setSubXML, "127.0.0.1:8000")
	must.NoError(err)
	assertValidXML(t, setSubResp)
	is.Equal([]string{"1080p"}, appliedResolutions, "Profile_Sub SetVideoEncoderConfiguration must not downgrade main stream to 360p")
	is.Equal("1080p", bus.GetStatus().Resolution)

	// 4. GetVideoEncoderConfiguration (follow-up readback by node-onvif after SetVideoEncoderConfiguration)
	getVecMain, err := mh.Handle("", `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetVideoEncoderConfiguration xmlns="http://www.onvif.org/ver10/media/wsdl"><ConfigurationToken>VideoEncoderConfig_Main</ConfigurationToken></GetVideoEncoderConfiguration></s:Body></s:Envelope>`, "127.0.0.1:8000")
	must.NoError(err)
	assertValidXML(t, getVecMain)
	is.Contains(getVecMain, `<trt:Configuration token="VideoEncoderConfig_Main">`)
	is.Contains(getVecMain, "<tt:Width>1920</tt:Width>")

	getVecSub, err := mh.Handle("", `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetVideoEncoderConfiguration xmlns="http://www.onvif.org/ver10/media/wsdl"><ConfigurationToken>VideoEncoderConfig_Sub</ConfigurationToken></GetVideoEncoderConfiguration></s:Body></s:Envelope>`, "127.0.0.1:8000")
	must.NoError(err)
	assertValidXML(t, getVecSub)
	is.Contains(getVecSub, `<trt:Configuration token="VideoEncoderConfig_Sub">`)
	is.Contains(getVecSub, "<tt:Width>640</tt:Width>")

	// 5. SetAudioEncoderConfiguration + GetAudioEncoderConfiguration (called by Scrypted autoconfigureCodecs)
	setAecResp, err := mh.Handle("", `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><SetAudioEncoderConfiguration xmlns="http://www.onvif.org/ver10/media/wsdl"><Configuration token = "AudioEncoderConfig_1"><Encoding xmlns="http://www.onvif.org/ver10/schema">G711</Encoding></Configuration></SetAudioEncoderConfiguration></s:Body></s:Envelope>`, "127.0.0.1:8000")
	must.NoError(err)
	assertValidXML(t, setAecResp)
	is.Contains(setAecResp, "SetAudioEncoderConfigurationResponse")

	getAecResp, err := mh.Handle("", `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetAudioEncoderConfiguration xmlns="http://www.onvif.org/ver10/media/wsdl"><ConfigurationToken>AudioEncoderConfig_1</ConfigurationToken></GetAudioEncoderConfiguration></s:Body></s:Envelope>`, "127.0.0.1:8000")
	must.NoError(err)
	assertValidXML(t, getAecResp)
	is.Contains(getAecResp, `<trt:Configuration token="AudioEncoderConfig_1">`)

	// 6. GetOSDs (called by Scrypted getVideoTextOverlays)
	osdsResp, err := mh.Handle("", `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetOSDs xmlns="http://www.onvif.org/ver10/media/wsdl" ></GetOSDs></s:Body></s:Envelope>`, "127.0.0.1:8000")
	must.NoError(err)
	assertValidXML(t, osdsResp)
	is.Contains(osdsResp, `<trt:OSDs token="OSD_1">`)
	is.Contains(osdsResp, "<tt:Type>DateAndTime</tt:Type>")
}
