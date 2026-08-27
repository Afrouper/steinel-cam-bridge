package onvif

import (
	"fmt"
	"strings"
)

type ReplayHandler struct {
	rtspPort  int
	onvifPort int
}

func NewReplayHandler(rtspPort, onvifPort int) *ReplayHandler {
	return &ReplayHandler{
		rtspPort:  rtspPort,
		onvifPort: onvifPort,
	}
}

func (h *ReplayHandler) Handle(action, reqXML, host string) (string, error) {
	if strings.Contains(action, "GetReplayUri") || strings.Contains(reqXML, "GetReplayUri") {
		return h.getReplayUri(host), nil
	}
	if strings.Contains(action, "GetReplayConfiguration") || strings.Contains(reqXML, "GetReplayConfiguration") {
		return h.getReplayConfiguration(), nil
	}
	if strings.Contains(action, "SetReplayConfiguration") || strings.Contains(reqXML, "SetReplayConfiguration") {
		return `<trp:SetReplayConfigurationResponse xmlns:trp="http://www.onvif.org/ver10/replay/wsdl"/>`, nil
	}

	return "", fmt.Errorf("unhandled replay action: %s", action)
}

func (h *ReplayHandler) getReplayUri(host string) string {
	ip := extractHostIP(host)
	replayURI := fmt.Sprintf("rtsp://%s:%d/live", ip, h.rtspPort)

	return fmt.Sprintf(`<trp:GetReplayUriResponse xmlns:trp="%s">
  <trp:Uri>%s</trp:Uri>
</trp:GetReplayUriResponse>`, NS_TRP, replayURI)
}

func (h *ReplayHandler) getReplayConfiguration() string {
	return fmt.Sprintf(`<trp:GetReplayConfigurationResponse xmlns:trp="%s" xmlns:tt="%s">
  <trp:Configuration>
    <tt:SessionTimeout>PT60S</tt:SessionTimeout>
  </trp:Configuration>
</trp:GetReplayConfigurationResponse>`, NS_TRP, NS_TT)
}
