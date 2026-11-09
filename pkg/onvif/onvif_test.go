package onvif

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
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
