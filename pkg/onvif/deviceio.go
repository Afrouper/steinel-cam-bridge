package onvif

import (
	"fmt"
	"strings"
)

type DeviceIOHandler struct {
	setLampFunc  func(mode string) error
	setSirenFunc func(on bool) error
}

func NewDeviceIOHandler(setLampFunc func(mode string) error, setSirenFunc func(on bool) error) *DeviceIOHandler {
	return &DeviceIOHandler{
		setLampFunc:  setLampFunc,
		setSirenFunc: setSirenFunc,
	}
}

func (h *DeviceIOHandler) Handle(action string, reqXML string) (string, error) {
	if strings.Contains(action, "GetRelayOutputs") || strings.Contains(reqXML, "GetRelayOutputs") {
		return h.getRelayOutputs(), nil
	}
	if strings.Contains(action, "SetRelayOutputState") || strings.Contains(reqXML, "SetRelayOutputState") {
		return h.setRelayOutputState(reqXML), nil
	}
	if strings.Contains(action, "SendAuxiliaryCommand") || strings.Contains(reqXML, "SendAuxiliaryCommand") {
		return h.sendAuxiliaryCommand(reqXML), nil
	}

	return "", fmt.Errorf("unhandled deviceio action: %s", action)
}

func (h *DeviceIOHandler) getRelayOutputs() string {
	return fmt.Sprintf(`<tio:GetRelayOutputsResponse xmlns:tio="%s" xmlns:tt="%s">
  <tio:RelayOutputs token="RelayOutput_1">
    <tt:Properties>
      <tt:Mode>Bistable</tt:Mode>
      <tt:DelayTime>PT0S</tt:DelayTime>
      <tt:IdleState>open</tt:IdleState>
    </tt:Properties>
  </tio:RelayOutputs>
</tio:GetRelayOutputsResponse>`, NS_TIO, NS_TT)
}

func (h *DeviceIOHandler) setRelayOutputState(reqXML string) string {
	if strings.Contains(reqXML, "active") || strings.Contains(reqXML, "closed") {
		if h.setLampFunc != nil {
			_ = h.setLampFunc("on")
		}
	} else {
		if h.setLampFunc != nil {
			_ = h.setLampFunc("off")
		}
	}
	return fmt.Sprintf(`<tio:SetRelayOutputStateResponse xmlns:tio="%s"/>`, NS_TIO)
}

func (h *DeviceIOHandler) sendAuxiliaryCommand(reqXML string) string {
	if strings.Contains(reqXML, "Light:On") || strings.Contains(reqXML, "light:on") {
		if h.setLampFunc != nil {
			_ = h.setLampFunc("on")
		}
	} else if strings.Contains(reqXML, "Light:Off") || strings.Contains(reqXML, "light:off") {
		if h.setLampFunc != nil {
			_ = h.setLampFunc("off")
		}
	} else if strings.Contains(reqXML, "Light:Auto") || strings.Contains(reqXML, "light:auto") {
		if h.setLampFunc != nil {
			_ = h.setLampFunc("auto")
		}
	} else if strings.Contains(reqXML, "Siren:On") || strings.Contains(reqXML, "siren:on") {
		if h.setSirenFunc != nil {
			_ = h.setSirenFunc(true)
		}
	} else if strings.Contains(reqXML, "Siren:Off") || strings.Contains(reqXML, "siren:off") {
		if h.setSirenFunc != nil {
			_ = h.setSirenFunc(false)
		}
	}

	return `<tptz:SendAuxiliaryCommandResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:AuxiliaryResponse>OK</tptz:AuxiliaryResponse></tptz:SendAuxiliaryCommandResponse>`
}
