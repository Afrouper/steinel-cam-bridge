package onvif

import (
	"fmt"
	"strings"
)

type RecordingHandler struct {
	onvifPort int
}

func NewRecordingHandler(onvifPort int) *RecordingHandler {
	return &RecordingHandler{
		onvifPort: onvifPort,
	}
}

func (h *RecordingHandler) Handle(action, reqXML, host string) (string, error) {
	if strings.Contains(action, "GetRecordings") || strings.Contains(reqXML, "GetRecordings") {
		return h.getRecordings(), nil
	}
	if strings.Contains(action, "GetRecordingConfiguration") || strings.Contains(reqXML, "GetRecordingConfiguration") {
		return h.getRecordingConfiguration(), nil
	}

	return "", fmt.Errorf("unhandled recording action: %s", action)
}

func (h *RecordingHandler) getRecordings() string {
	return fmt.Sprintf(`<trc:GetRecordingsResponse xmlns:trc="%s" xmlns:tt="%s">
  <trc:RecordingItem>
    <tt:RecordingToken>Recording_Main</tt:RecordingToken>
    <tt:Configuration>
      <tt:Source>
        <tt:SourceId>SteinelCamera0</tt:SourceId>
        <tt:Name>Steinel Cam Main</tt:Name>
        <tt:Location>Main</tt:Location>
        <tt:Description>MicroSD Storage</tt:Description>
        <tt:Address>http://127.0.0.1:%d/onvif/device_service</tt:Address>
      </tt:Source>
      <tt:Content>Motion Events</tt:Content>
      <tt:MaximumRetentionTime>P30D</tt:MaximumRetentionTime>
    </tt:Configuration>
  </trc:RecordingItem>
</trc:GetRecordingsResponse>`, NS_TRC, NS_TT, h.onvifPort)
}

func (h *RecordingHandler) getRecordingConfiguration() string {
	return fmt.Sprintf(`<trc:GetRecordingConfigurationResponse xmlns:trc="%s" xmlns:tt="%s">
  <trc:RecordingConfiguration>
    <tt:Source>
      <tt:SourceId>SteinelCamera0</tt:SourceId>
      <tt:Name>Steinel Cam Main</tt:Name>
      <tt:Location>Main</tt:Location>
      <tt:Description>MicroSD Storage</tt:Description>
      <tt:Address>http://127.0.0.1:%d/onvif/device_service</tt:Address>
    </tt:Source>
    <tt:Content>Motion Events</tt:Content>
    <tt:MaximumRetentionTime>P30D</tt:MaximumRetentionTime>
  </trc:RecordingConfiguration>
</trc:GetRecordingConfigurationResponse>`, NS_TRC, NS_TT, h.onvifPort)
}
