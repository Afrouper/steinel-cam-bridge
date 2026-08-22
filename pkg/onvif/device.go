package onvif

import (
	"fmt"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
)

type DeviceHandler struct {
	deviceID   string
	productID  string
	onvifPort  int
	rtspPort   int
	rebootFunc func() error
}

func NewDeviceHandler(deviceID, productID string, onvifPort, rtspPort int, rebootFunc func() error) *DeviceHandler {
	return &DeviceHandler{
		deviceID:   deviceID,
		productID:  productID,
		onvifPort:  onvifPort,
		rtspPort:   rtspPort,
		rebootFunc: rebootFunc,
	}
}

func (h *DeviceHandler) Handle(action string, reqXML string, host string) (string, error) {
	if strings.Contains(action, "GetDeviceInformation") || strings.Contains(reqXML, "GetDeviceInformation") {
		return h.getDeviceInformation(), nil
	}
	if strings.Contains(action, "GetCapabilities") || strings.Contains(reqXML, "GetCapabilities") {
		return h.getCapabilities(host), nil
	}
	if strings.Contains(action, "GetServices") || strings.Contains(reqXML, "GetServices") {
		return h.getServices(host), nil
	}
	if strings.Contains(action, "GetSystemDateAndTime") || strings.Contains(reqXML, "GetSystemDateAndTime") {
		return h.getSystemDateAndTime(), nil
	}
	if strings.Contains(action, "GetNetworkInterfaces") || strings.Contains(reqXML, "GetNetworkInterfaces") {
		return h.getNetworkInterfaces(), nil
	}
	if strings.Contains(action, "SystemReboot") || strings.Contains(reqXML, "SystemReboot") {
		if h.rebootFunc != nil {
			_ = h.rebootFunc()
		}
		return `<tds:SystemRebootResponse><tds:Message>Rebooting Steinel Camera</tds:Message></tds:SystemRebootResponse>`, nil
	}

	return "", fmt.Errorf("unhandled device action: %s", action)
}

func (h *DeviceHandler) getDeviceInformation() string {
	st := events.GlobalBus.GetStatus()
	fw := "2.0.0"
	if st.FirmwareVer != "" {
		fw = st.FirmwareVer
	}

	return fmt.Sprintf(`<tds:GetDeviceInformationResponse xmlns:tds="%s">
  <tds:Manufacturer>STEINEL</tds:Manufacturer>
  <tds:Model>L 625 CAM SC</tds:Model>
  <tds:FirmwareVersion>%s</tds:FirmwareVersion>
  <tds:SerialNumber>%s</tds:SerialNumber>
  <tds:HardwareId>%s</tds:HardwareId>
</tds:GetDeviceInformationResponse>`, NS_TDS, fw, h.deviceID, h.productID)
}

func (h *DeviceHandler) getCapabilities(host string) string {
	ip := extractHostIP(host)
	deviceURL := fmt.Sprintf("http://%s:%d/onvif/device_service", ip, h.onvifPort)
	mediaURL := fmt.Sprintf("http://%s:%d/onvif/media_service", ip, h.onvifPort)
	eventURL := fmt.Sprintf("http://%s:%d/onvif/event_service", ip, h.onvifPort)
	deviceIOURL := fmt.Sprintf("http://%s:%d/onvif/deviceio_service", ip, h.onvifPort)

	return fmt.Sprintf(`<tds:GetCapabilitiesResponse xmlns:tds="%s" xmlns:tt="%s">
  <tds:Capabilities>
    <tt:Device>
      <tt:XAddr>%s</tt:XAddr>
      <tt:Network>
        <tt:IPFilter>false</tt:IPFilter>
        <tt:ZeroConfiguration>false</tt:ZeroConfiguration>
        <tt:IPVersion6>false</tt:IPVersion6>
        <tt:DynDNS>false</tt:DynDNS>
      </tt:Network>
      <tt:System>
        <tt:DiscoveryResolve>false</tt:DiscoveryResolve>
        <tt:DiscoveryBye>true</tt:DiscoveryBye>
        <tt:RemoteDiscovery>true</tt:RemoteDiscovery>
        <tt:SystemBackup>false</tt:SystemBackup>
        <tt:SystemLogging>false</tt:SystemLogging>
        <tt:FirmwareUpgrade>false</tt:FirmwareUpgrade>
        <tt:SupportedVersions>
          <tt:Major>2</tt:Major>
          <tt:Minor>0</tt:Minor>
        </tt:SupportedVersions>
      </tt:System>
    </tt:Device>
    <tt:Events>
      <tt:XAddr>%s</tt:XAddr>
      <tt:WSSubscriptionPolicySupport>true</tt:WSSubscriptionPolicySupport>
      <tt:WSPullPointSupport>true</tt:WSPullPointSupport>
      <tt:WSPausableSubscriptionManagerInterfaceSupport>false</tt:WSPausableSubscriptionManagerInterfaceSupport>
    </tt:Events>
    <tt:Media>
      <tt:XAddr>%s</tt:XAddr>
      <tt:StreamingCapabilities>
        <tt:RTPMulticast>false</tt:RTPMulticast>
        <tt:RTP_TCP>true</tt:RTP_TCP>
        <tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP>
      </tt:StreamingCapabilities>
    </tt:Media>
    <tt:Extension>
      <tt:DeviceIO>
        <tt:XAddr>%s</tt:XAddr>
        <tt:VideoSources>1</tt:VideoSources>
        <tt:VideoOutputs>0</tt:VideoOutputs>
        <tt:AudioSources>1</tt:AudioSources>
        <tt:AudioOutputs>1</tt:AudioOutputs>
        <tt:RelayOutputs>1</tt:RelayOutputs>
      </tt:DeviceIO>
    </tt:Extension>
  </tds:Capabilities>
</tds:GetCapabilitiesResponse>`, NS_TDS, NS_TT, deviceURL, eventURL, mediaURL, deviceIOURL)
}

func (h *DeviceHandler) getServices(host string) string {
	ip := extractHostIP(host)
	deviceURL := fmt.Sprintf("http://%s:%d/onvif/device_service", ip, h.onvifPort)
	mediaURL := fmt.Sprintf("http://%s:%d/onvif/media_service", ip, h.onvifPort)
	eventURL := fmt.Sprintf("http://%s:%d/onvif/event_service", ip, h.onvifPort)
	deviceIOURL := fmt.Sprintf("http://%s:%d/onvif/deviceio_service", ip, h.onvifPort)

	return fmt.Sprintf(`<tds:GetServicesResponse xmlns:tds="%s" xmlns:tt="%s">
  <tds:Service>
    <tds:Namespace>%s</tds:Namespace>
    <tds:XAddr>%s</tds:XAddr>
    <tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version>
  </tds:Service>
  <tds:Service>
    <tds:Namespace>%s</tds:Namespace>
    <tds:XAddr>%s</tds:XAddr>
    <tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version>
  </tds:Service>
  <tds:Service>
    <tds:Namespace>%s</tds:Namespace>
    <tds:XAddr>%s</tds:XAddr>
    <tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version>
  </tds:Service>
  <tds:Service>
    <tds:Namespace>%s</tds:Namespace>
    <tds:XAddr>%s</tds:XAddr>
    <tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version>
  </tds:Service>
</tds:GetServicesResponse>`, NS_TDS, NS_TT, NS_TDS, deviceURL, NS_TRT, mediaURL, NS_TEV, eventURL, NS_TIO, deviceIOURL)
}

func (h *DeviceHandler) getSystemDateAndTime() string {
	now := time.Now().UTC()
	return fmt.Sprintf(`<tds:GetSystemDateAndTimeResponse xmlns:tds="%s" xmlns:tt="%s">
  <tds:SystemDateAndTime>
    <tt:DateTimeType>NTP</tt:DateTimeType>
    <tt:DaylightSavings>false</tt:DaylightSavings>
    <tt:TimeZone><tt:TZ>UTC</tt:TZ></tt:TimeZone>
    <tt:UTCDateTime>
      <tt:Time>
        <tt:Hour>%d</tt:Hour>
        <tt:Minute>%d</tt:Minute>
        <tt:Second>%d</tt:Second>
      </tt:Time>
      <tt:Date>
        <tt:Year>%d</tt:Year>
        <tt:Month>%d</tt:Month>
        <tt:Day>%d</tt:Day>
      </tt:Date>
    </tt:UTCDateTime>
  </tds:SystemDateAndTime>
</tds:GetSystemDateAndTimeResponse>`, NS_TDS, NS_TT, now.Hour(), now.Minute(), now.Second(), now.Year(), int(now.Month()), now.Day())
}

func (h *DeviceHandler) getNetworkInterfaces() string {
	return fmt.Sprintf(`<tds:GetNetworkInterfacesResponse xmlns:tds="%s" xmlns:tt="%s">
  <tds:NetworkInterfaces token="eth0">
    <tt:Enabled>true</tt:Enabled>
    <tt:Info>
      <tt:Name>eth0</tt:Name>
      <tt:HwAddress>00:0C:43:00:01:02</tt:HwAddress>
      <tt:MTU>1500</tt:MTU>
    </tt:Info>
    <tt:IPv4>
      <tt:Enabled>true</tt:Enabled>
      <tt:Config>
        <tt:Manual>
          <tt:Address>127.0.0.1</tt:Address>
          <tt:PrefixLength>24</tt:PrefixLength>
        </tt:Manual>
        <tt:DHCP>true</tt:DHCP>
      </tt:Config>
    </tt:IPv4>
  </tds:NetworkInterfaces>
</tds:GetNetworkInterfacesResponse>`, NS_TDS, NS_TT)
}

func extractHostIP(host string) string {
	if host == "" {
		return getOutboundIP()
	}
	if idx := strings.Index(host, ":"); idx >= 0 {
		return host[:idx]
	}
	return host
}
