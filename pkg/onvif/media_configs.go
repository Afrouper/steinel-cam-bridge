package onvif

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

func (h *MediaHandler) getServiceCapabilities() string {
	return fmt.Sprintf(`<trt:GetServiceCapabilitiesResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:Capabilities SnapshotUri="true" OSD="true">
    <trt:ProfileCapabilities MaximumNumberOfProfiles="4"/>
    <trt:StreamingCapabilities RTPMulticast="false" RTP_TCP="true" RTP_RTSP_TCP="true"/>
  </trt:Capabilities>
</trt:GetServiceCapabilitiesResponse>`, NS_TRT, NS_TT)
}

func (h *MediaHandler) formatVideoEncoderConfigElement(elemTag string, ch *CameraChannel, isMain bool) string {
	width, height, _ := h.getChannelResolution(ch, isMain)
	quality, bitrate, fps := "5.0", 2048, 15
	h264Profile := "High"
	encSuffix := "Main"
	if !isMain {
		quality, bitrate, fps = "3.0", 512, 10
		h264Profile = "Baseline"
		encSuffix = "Sub"
	}

	videoCodec := resolveChannelVideoCodec(ch)
	videoEncoding := "H264"
	codecParamsBlock := fmt.Sprintf(`
    <tt:H264>
      <tt:GovLength>%d</tt:GovLength>
      <tt:H264Profile>%s</tt:H264Profile>
    </tt:H264>`, fps*3, h264Profile)
	if videoCodec == videoCodecH265 {
		videoEncoding = "H265"
		codecParamsBlock = ""
	}

	chIndex := 1
	path := h.rtspPath
	if ch != nil {
		chIndex = ch.Index
		path = ch.Path
	}

	encToken := fmt.Sprintf("VideoEncoderConfig_%s", encSuffix)
	if chIndex > 1 {
		encToken = fmt.Sprintf("VideoEncoderConfig_%s_%s", path, encSuffix)
	}

	return fmt.Sprintf(`  <trt:%s token="%s">
    <tt:Name>%s</tt:Name>
    <tt:UseCount>1</tt:UseCount>
    <tt:Encoding>%s</tt:Encoding>
    <tt:Resolution>
      <tt:Width>%d</tt:Width>
      <tt:Height>%d</tt:Height>
    </tt:Resolution>
    <tt:Quality>%s</tt:Quality>
    <tt:RateControl>
      <tt:FrameRateLimit>%d</tt:FrameRateLimit>
      <tt:EncodingInterval>1</tt:EncodingInterval>
      <tt:BitrateLimit>%d</tt:BitrateLimit>
    </tt:RateControl>%s
    <tt:Multicast>
      <tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address>
      <tt:Port>0</tt:Port>
      <tt:TTL>1</tt:TTL>
      <tt:AutoStart>false</tt:AutoStart>
    </tt:Multicast>
    <tt:SessionTimeout>PT60S</tt:SessionTimeout>
  </trt:%s>`,
		elemTag, encToken, encToken, videoEncoding,
		width, height, quality, fps, bitrate, codecParamsBlock, elemTag)
}

func (h *MediaHandler) getVideoEncoderConfigurations() string {
	channels := h.Channels()
	if len(channels) <= 1 {
		var ch0 *CameraChannel
		if len(channels) == 1 {
			ch0 = &channels[0]
		}
		cMain := h.formatVideoEncoderConfigElement("Configurations", ch0, true)
		cSub := h.formatVideoEncoderConfigElement("Configurations", ch0, false)
		return fmt.Sprintf(`<trt:GetVideoEncoderConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">
%s
%s
</trt:GetVideoEncoderConfigurationsResponse>`, NS_TRT, NS_TT, cMain, cSub)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetVideoEncoderConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for i := range channels {
		ch := &channels[i]
		sb.WriteString("\n")
		sb.WriteString(h.formatVideoEncoderConfigElement("Configurations", ch, true))
		sb.WriteString("\n")
		sb.WriteString(h.formatVideoEncoderConfigElement("Configurations", ch, false))
	}
	sb.WriteString("\n</trt:GetVideoEncoderConfigurationsResponse>")
	return sb.String()
}

func (h *MediaHandler) getVideoEncoderConfiguration(reqXML string) string {
	configToken := extractXMLTag(reqXML, "ConfigurationToken")
	isMain := !isSubProfileOrConfigToken(configToken)
	targetCh := h.resolveChannelForTokens("", configToken, reqXML)
	cfgXML := h.formatVideoEncoderConfigElement("Configuration", targetCh, isMain)
	return fmt.Sprintf(`<trt:GetVideoEncoderConfigurationResponse xmlns:trt="%s" xmlns:tt="%s">
%s
</trt:GetVideoEncoderConfigurationResponse>`, NS_TRT, NS_TT, cfgXML)
}

func formatAudioEncoderConfigElement(elemTag string, chIndex int, audioCodec string) string {
	audioEncoding := "AAC"
	sampleRate := 16
	if strings.EqualFold(strings.TrimSpace(audioCodec), "pcmu") {
		audioEncoding = "G711"
		sampleRate = 8
	}
	return fmt.Sprintf(`  <trt:%s token="AudioEncoderConfig_%d">
    <tt:Name>AudioEncoderConfig_%d</tt:Name>
    <tt:UseCount>2</tt:UseCount>
    <tt:Encoding>%s</tt:Encoding>
    <tt:Bitrate>64</tt:Bitrate>
    <tt:SampleRate>%d</tt:SampleRate>
    <tt:SessionTimeout>PT60S</tt:SessionTimeout>
  </trt:%s>`, elemTag, chIndex, chIndex, audioEncoding, sampleRate, elemTag)
}

func (h *MediaHandler) getAudioEncoderConfigurations() string {
	channels := h.Channels()
	if len(channels) <= 1 {
		codec := h.audioCodec
		if len(channels) == 1 && channels[0].AudioCodec != "" {
			codec = channels[0].AudioCodec
		}
		if isAudioDisabled(codec) {
			return fmt.Sprintf(`<trt:GetAudioEncoderConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s"/>`, NS_TRT, NS_TT)
		}
		cfg := formatAudioEncoderConfigElement("Configurations", 1, codec)
		return fmt.Sprintf(`<trt:GetAudioEncoderConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">
%s
</trt:GetAudioEncoderConfigurationsResponse>`, NS_TRT, NS_TT, cfg)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetAudioEncoderConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for _, ch := range channels {
		if isAudioDisabled(ch.AudioCodec) {
			continue
		}
		sb.WriteString("\n")
		sb.WriteString(formatAudioEncoderConfigElement("Configurations", ch.Index, ch.AudioCodec))
	}
	sb.WriteString("\n</trt:GetAudioEncoderConfigurationsResponse>")
	return sb.String()
}

func (h *MediaHandler) getAudioEncoderConfiguration(reqXML string) string {
	configToken := extractXMLTag(reqXML, "ConfigurationToken")
	channels := h.Channels()
	chIndex := 1
	codec := h.audioCodec
	if len(channels) > 0 {
		if channels[0].AudioCodec != "" {
			codec = channels[0].AudioCodec
		}
		if after, ok := strings.CutPrefix(configToken, "AudioEncoderConfig_"); ok {
			if idx, err := strconv.Atoi(strings.TrimSpace(after)); err == nil {
				for _, ch := range channels {
					if ch.Index == idx {
						chIndex = ch.Index
						if ch.AudioCodec != "" {
							codec = ch.AudioCodec
						}
						break
					}
				}
			}
		}
	}
	cfg := formatAudioEncoderConfigElement("Configuration", chIndex, codec)
	return fmt.Sprintf(`<trt:GetAudioEncoderConfigurationResponse xmlns:trt="%s" xmlns:tt="%s">
%s
</trt:GetAudioEncoderConfigurationResponse>`, NS_TRT, NS_TT, cfg)
}

func (h *MediaHandler) setAudioEncoderConfiguration(reqXML string) string {
	token := extractXMLAttr(reqXML, "Configuration", "token")
	logger.Debug("ONVIF", "🔊 Received SetAudioEncoderConfiguration (token: %s) -> retaining bridge audio configuration", token)
	return fmt.Sprintf(`<trt:SetAudioEncoderConfigurationResponse xmlns:trt="%s"/>`, NS_TRT)
}

func (h *MediaHandler) getOSDs(reqXML string) string {
	configToken := extractXMLTag(reqXML, "ConfigurationToken")
	channels := h.Channels()
	if len(channels) <= 1 {
		return fmt.Sprintf(`<trt:GetOSDsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:OSDs token="OSD_1">
    <tt:VideoSourceConfigurationToken>VideoSourceConfig_1</tt:VideoSourceConfigurationToken>
    <tt:Type>Text</tt:Type>
    <tt:Position><tt:Type>UpperLeft</tt:Type></tt:Position>
    <tt:TextString><tt:Type>DateAndTime</tt:Type></tt:TextString>
  </trt:OSDs>
</trt:GetOSDsResponse>`, NS_TRT, NS_TT)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetOSDsResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for _, ch := range channels {
		vsToken := fmt.Sprintf("VideoSourceConfig_%d", ch.Index)
		if configToken != "" && !strings.EqualFold(configToken, vsToken) {
			continue
		}
		fmt.Fprintf(&sb, `
  <trt:OSDs token="OSD_%d">
    <tt:VideoSourceConfigurationToken>%s</tt:VideoSourceConfigurationToken>
    <tt:Type>Text</tt:Type>
    <tt:Position><tt:Type>UpperLeft</tt:Type></tt:Position>
    <tt:TextString><tt:Type>DateAndTime</tt:Type></tt:TextString>
  </trt:OSDs>`, ch.Index, vsToken)
	}
	sb.WriteString("\n</trt:GetOSDsResponse>")
	return sb.String()
}

func isSubProfileOrConfigToken(token string) bool {
	lower := strings.ToLower(strings.TrimSpace(token))
	return lower == "profile_sub" || lower == "videoencoderconfig_sub" || strings.HasSuffix(lower, "_sub")
}
