package onvif

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

type MediaHandler struct {
	rtspPort      int
	rtspPath      string
	audioCodec    string
	onvifPort     int
	changeResFunc func(res string) error
	eventBus      *events.Bus

	channelsMu sync.RWMutex
	channels   []CameraChannel
}

func NewMediaHandler(rtspPort int, rtspPath string, audioCodec string, onvifPort int, changeResFunc func(res string) error, eventBus *events.Bus) *MediaHandler {
	audioCodec = strings.ToLower(strings.TrimSpace(audioCodec))
	if audioCodec == "" {
		audioCodec = "aac"
	}
	if eventBus == nil {
		eventBus = events.GlobalBus
	}
	h := &MediaHandler{
		rtspPort:      rtspPort,
		rtspPath:      rtspPath,
		audioCodec:    audioCodec,
		onvifPort:     onvifPort,
		changeResFunc: changeResFunc,
		eventBus:      eventBus,
	}
	h.channels = []CameraChannel{
		{
			Index:         1,
			Name:          "MainStream",
			Path:          rtspPath,
			AudioCodec:    audioCodec,
			Resolution:    "1080p",
			ChangeResFunc: changeResFunc,
			EventBus:      eventBus,
		},
	}
	return h
}

func (h *MediaHandler) SetChannels(channels []CameraChannel) {
	h.channelsMu.Lock()
	defer h.channelsMu.Unlock()
	h.channels = make([]CameraChannel, len(channels))
	copy(h.channels, channels)
}

func (h *MediaHandler) AddChannel(ch CameraChannel) {
	h.channelsMu.Lock()
	defer h.channelsMu.Unlock()
	h.channels = append(h.channels, ch)
}

func (h *MediaHandler) Channels() []CameraChannel {
	h.channelsMu.RLock()
	defer h.channelsMu.RUnlock()
	result := make([]CameraChannel, len(h.channels))
	copy(result, h.channels)
	return result
}

func extractXMLTag(xmlStr, tag string) string {
	startIdx := strings.Index(xmlStr, "<"+tag+">")
	endTag := "</" + tag + ">"
	if startIdx != -1 {
		startIdx += len(tag) + 2
		endIdx := strings.Index(xmlStr[startIdx:], endTag)
		if endIdx != -1 {
			return strings.TrimSpace(xmlStr[startIdx : startIdx+endIdx])
		}
	}
	for _, prefix := range []string{"trt:", "tt:", "tns1:"} {
		pTag := "<" + prefix + tag + ">"
		startIdx = strings.Index(xmlStr, pTag)
		if startIdx != -1 {
			startIdx += len(pTag)
			endIdx := strings.Index(xmlStr[startIdx:], "</"+prefix+tag+">")
			if endIdx != -1 {
				return strings.TrimSpace(xmlStr[startIdx : startIdx+endIdx])
			}
		}
	}
	return ""
}

func extractXMLAttr(xmlStr, tag, attr string) string {
	idx := strings.Index(xmlStr, "<"+tag)
	if idx == -1 {
		idx = strings.Index(xmlStr, "<trt:"+tag)
	}
	if idx == -1 {
		return ""
	}
	sub := xmlStr[idx:]
	endClose := strings.Index(sub, ">")
	if endClose != -1 {
		sub = sub[:endClose]
	}
	attrKey := attr + `="`
	aIdx := strings.Index(sub, attrKey)
	if aIdx != -1 {
		aIdx += len(attrKey)
		valEnd := strings.Index(sub[aIdx:], `"`)
		if valEnd != -1 {
			return sub[aIdx : aIdx+valEnd]
		}
	}
	return ""
}

func (h *MediaHandler) Handle(action string, reqXML string, host string) (string, error) {
	if strings.Contains(action, "GetProfiles") || strings.Contains(reqXML, "GetProfiles") {
		return h.getProfiles(), nil
	}
	if strings.Contains(action, "GetProfile") || strings.Contains(reqXML, "GetProfile") {
		return h.getProfile(reqXML), nil
	}
	if strings.Contains(action, "GetStreamUri") || strings.Contains(reqXML, "GetStreamUri") {
		return h.getStreamUri(host, reqXML), nil
	}
	if strings.Contains(action, "GetSnapshotUri") || strings.Contains(reqXML, "GetSnapshotUri") {
		return h.getSnapshotUri(host, reqXML), nil
	}
	if strings.Contains(action, "GetVideoSourceConfigurations") || strings.Contains(reqXML, "GetVideoSourceConfigurations") ||
		strings.Contains(action, "GetVideoSourceConfiguration") || strings.Contains(reqXML, "GetVideoSourceConfiguration") {
		return h.getVideoSourceConfigurations(), nil
	}
	if strings.Contains(action, "GetAudioSourceConfigurations") || strings.Contains(reqXML, "GetAudioSourceConfigurations") ||
		strings.Contains(action, "GetAudioSourceConfiguration") || strings.Contains(reqXML, "GetAudioSourceConfiguration") {
		return h.getAudioSourceConfigurations(), nil
	}
	if strings.Contains(action, "GetAudioOutputConfigurations") || strings.Contains(reqXML, "GetAudioOutputConfigurations") ||
		strings.Contains(action, "GetAudioOutputConfiguration") || strings.Contains(reqXML, "GetAudioOutputConfiguration") {
		return h.getAudioOutputConfigurations(), nil
	}
	if strings.Contains(action, "GetVideoEncoderConfigurationOptions") || strings.Contains(reqXML, "GetVideoEncoderConfigurationOptions") {
		return h.getVideoEncoderConfigurationOptions(), nil
	}
	if strings.Contains(action, "SetVideoEncoderConfiguration") || strings.Contains(reqXML, "SetVideoEncoderConfiguration") {
		return h.setVideoEncoderConfiguration(reqXML), nil
	}
	if strings.Contains(action, "GetAudioSources") || strings.Contains(reqXML, "GetAudioSources") {
		return h.getAudioSources(), nil
	}
	if strings.Contains(action, "GetAudioOutputs") || strings.Contains(reqXML, "GetAudioOutputs") {
		return h.getAudioOutputs(), nil
	}
	if strings.Contains(action, "GetAudioEncoderConfigurationOptions") || strings.Contains(reqXML, "GetAudioEncoderConfigurationOptions") {
		return h.getAudioEncoderConfigurationOptions(), nil
	}
	if strings.Contains(action, "GetVideoSources") || strings.Contains(reqXML, "GetVideoSources") {
		return h.getVideoSources(), nil
	}
	return `<trt:UnknownActionResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl"/>`, nil
}

func (h *MediaHandler) formatProfileXML(token, name string, chIndex int, path string, isMain bool, audioCodec string) string {
	audioEncoding := "AAC"
	sampleRate := 16
	if strings.ToLower(audioCodec) == "pcmu" {
		audioEncoding = "G711"
		sampleRate = 8
	}

	width, height, quality, bitrate, fps := 1920, 1080, "5.0", 2048, 15
	h264Profile := "High"
	encSuffix := "Main"
	if !isMain {
		width, height, quality, bitrate, fps = 640, 360, "3.0", 512, 10
		h264Profile = "Baseline"
		encSuffix = "Sub"
	}

	encToken := fmt.Sprintf("VideoEncoderConfig_%s", encSuffix)
	if chIndex > 1 {
		encToken = fmt.Sprintf("VideoEncoderConfig_%s_%s", path, encSuffix)
	}

	return fmt.Sprintf(`  <trt:Profiles token="%s" fixed="true">
    <tt:Name>%s</tt:Name>
    <tt:VideoSourceConfiguration token="VideoSourceConfig_%d">
      <tt:Name>VideoSourceConfig_%d</tt:Name>
      <tt:UseCount>2</tt:UseCount>
      <tt:SourceToken>VideoSource_%d</tt:SourceToken>
      <tt:Bounds x="0" y="0" width="%d" height="%d"/>
    </tt:VideoSourceConfiguration>
    <tt:AudioSourceConfiguration token="AudioSourceConfig_%d">
      <tt:Name>AudioSourceConfig_%d</tt:Name>
      <tt:UseCount>2</tt:UseCount>
      <tt:SourceToken>AudioSource_%d</tt:SourceToken>
    </tt:AudioSourceConfiguration>
    <tt:VideoEncoderConfiguration token="%s">
      <tt:Name>%s</tt:Name>
      <tt:UseCount>1</tt:UseCount>
      <tt:Encoding>H264</tt:Encoding>
      <tt:Resolution>
        <tt:Width>%d</tt:Width>
        <tt:Height>%d</tt:Height>
      </tt:Resolution>
      <tt:Quality>%s</tt:Quality>
      <tt:RateControl>
        <tt:FrameRateLimit>%d</tt:FrameRateLimit>
        <tt:EncodingInterval>1</tt:EncodingInterval>
        <tt:BitrateLimit>%d</tt:BitrateLimit>
      </tt:RateControl>
      <tt:H264>
        <tt:GovLength>%d</tt:GovLength>
        <tt:H264Profile>%s</tt:H264Profile>
      </tt:H264>
      <tt:Multicast>
        <tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address>
        <tt:Port>0</tt:Port>
        <tt:TTL>1</tt:TTL>
        <tt:AutoStart>false</tt:AutoStart>
      </tt:Multicast>
      <tt:SessionTimeout>PT60S</tt:SessionTimeout>
    </tt:VideoEncoderConfiguration>
    <tt:AudioEncoderConfiguration token="AudioEncoderConfig_%d">
      <tt:Name>AudioEncoderConfig_%d</tt:Name>
      <tt:UseCount>2</tt:UseCount>
      <tt:Encoding>%s</tt:Encoding>
      <tt:Bitrate>64</tt:Bitrate>
      <tt:SampleRate>%d</tt:SampleRate>
      <tt:SessionTimeout>PT60S</tt:SessionTimeout>
    </tt:AudioEncoderConfiguration>
    <tt:AudioOutputConfiguration token="AudioOutputConfig_%d">
      <tt:Name>AudioOutputConfig_%d</tt:Name>
      <tt:UseCount>1</tt:UseCount>
      <tt:OutputToken>AudioOutput_%d</tt:OutputToken>
      <tt:SendPrimacy>www.onvif.org/ver20/HalfDuplex/Server</tt:SendPrimacy>
      <tt:OutputLevel>80</tt:OutputLevel>
    </tt:AudioOutputConfiguration>
  </trt:Profiles>`,
		token, name,
		chIndex, chIndex, chIndex, width, height,
		chIndex, chIndex, chIndex,
		encToken, encToken,
		width, height, quality, fps, bitrate,
		fps*3, h264Profile,
		chIndex, chIndex, audioEncoding, sampleRate,
		chIndex, chIndex, chIndex)
}

func (h *MediaHandler) getProfiles() string {
	channels := h.Channels()
	if len(channels) <= 1 {
		codec := h.audioCodec
		if len(channels) == 1 && channels[0].AudioCodec != "" {
			codec = channels[0].AudioCodec
		}
		pMain := h.formatProfileXML("Profile_Main", "MainStream 1080p", 1, h.rtspPath, true, codec)
		pSub := h.formatProfileXML("Profile_Sub", "SubStream 360p", 1, h.rtspPath, false, codec)

		return fmt.Sprintf(`<trt:GetProfilesResponse xmlns:trt="%s" xmlns:tt="%s">
%s
%s
</trt:GetProfilesResponse>`, NS_TRT, NS_TT, pMain, pSub)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetProfilesResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for _, ch := range channels {
		mainToken := fmt.Sprintf("Profile_%s_Main", ch.Path)
		subToken := fmt.Sprintf("Profile_%s_Sub", ch.Path)
		mainName := fmt.Sprintf("%s MainStream 1080p", ch.Name)
		subName := fmt.Sprintf("%s SubStream 360p", ch.Name)

		sb.WriteString("\n")
		sb.WriteString(h.formatProfileXML(mainToken, mainName, ch.Index, ch.Path, true, ch.AudioCodec))
		sb.WriteString("\n")
		sb.WriteString(h.formatProfileXML(subToken, subName, ch.Index, ch.Path, false, ch.AudioCodec))
	}
	sb.WriteString("\n</trt:GetProfilesResponse>")
	return sb.String()
}

func (h *MediaHandler) getProfile(reqXML string) string {
	return h.getProfiles()
}

func (h *MediaHandler) getStreamUri(host string, reqXML string) string {
	ip := extractHostIP(host)
	channels := h.Channels()

	reqToken := extractXMLTag(reqXML, "ProfileToken")

	selectedPath := h.rtspPath
	if len(channels) > 0 {
		selectedPath = channels[0].Path
	}

	for _, ch := range channels {
		if reqToken != "" {
			if reqToken == fmt.Sprintf("Profile_%s_Main", ch.Path) ||
				reqToken == fmt.Sprintf("Profile_%s_Sub", ch.Path) ||
				strings.Contains(reqToken, ch.Path) {
				selectedPath = ch.Path
				break
			}
		}
	}

	rtspURL := fmt.Sprintf("rtsp://%s:%d/%s", ip, h.rtspPort, selectedPath)

	return fmt.Sprintf(`<trt:GetStreamUriResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:MediaUri>
    <tt:Uri>%s</tt:Uri>
    <tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>
    <tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>
    <tt:Timeout>PT60S</tt:Timeout>
  </trt:MediaUri>
</trt:GetStreamUriResponse>`, NS_TRT, NS_TT, rtspURL)
}

func (h *MediaHandler) getSnapshotUri(host string, reqXML string) string {
	ip := extractHostIP(host)
	channels := h.Channels()

	reqToken := extractXMLTag(reqXML, "ProfileToken")
	selectedPath := h.rtspPath
	if len(channels) > 0 {
		selectedPath = channels[0].Path
	}

	for _, ch := range channels {
		if reqToken != "" {
			if reqToken == fmt.Sprintf("Profile_%s_Main", ch.Path) ||
				reqToken == fmt.Sprintf("Profile_%s_Sub", ch.Path) ||
				strings.Contains(reqToken, ch.Path) {
				selectedPath = ch.Path
				break
			}
		}
	}

	snapshotURL := fmt.Sprintf("http://%s:%d/api/sdcard/events/latest/snapshot.jpg?camera=%s", ip, h.onvifPort, selectedPath)

	return fmt.Sprintf(`<trt:GetSnapshotUriResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:MediaUri>
    <tt:Uri>%s</tt:Uri>
    <tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>
    <tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>
    <tt:Timeout>PT60S</tt:Timeout>
  </trt:MediaUri>
</trt:GetSnapshotUriResponse>`, NS_TRT, NS_TT, snapshotURL)
}

func (h *MediaHandler) getVideoEncoderConfigurationOptions() string {
	return fmt.Sprintf(`<trt:GetVideoEncoderConfigurationOptionsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:Options>
    <tt:QualityRange><tt:Min>1</tt:Min><tt:Max>5</tt:Max></tt:QualityRange>
    <tt:H264>
      <tt:ResolutionsAvailable><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:ResolutionsAvailable>
      <tt:ResolutionsAvailable><tt:Width>1280</tt:Width><tt:Height>720</tt:Height></tt:ResolutionsAvailable>
      <tt:ResolutionsAvailable><tt:Width>640</tt:Width><tt:Height>360</tt:Height></tt:ResolutionsAvailable>
      <tt:GovLengthRange><tt:Min>15</tt:Min><tt:Max>60</tt:Max></tt:GovLengthRange>
      <tt:FrameRateRange><tt:Min>1</tt:Min><tt:Max>15</tt:Max></tt:FrameRateRange>
      <tt:H264ProfilesSupported>High</tt:H264ProfilesSupported>
      <tt:H264ProfilesSupported>Main</tt:H264ProfilesSupported>
      <tt:H264ProfilesSupported>Baseline</tt:H264ProfilesSupported>
    </tt:H264>
  </trt:Options>
</trt:GetVideoEncoderConfigurationOptionsResponse>`, NS_TRT, NS_TT)
}

func (h *MediaHandler) setVideoEncoderConfiguration(reqXML string) string {
	targetRes := "1080p"
	if strings.Contains(reqXML, "<Width>1280</Width>") || strings.Contains(reqXML, "<Width>720</Width>") {
		targetRes = "720p"
	} else if strings.Contains(reqXML, "<Width>640</Width>") || strings.Contains(reqXML, "<Width>360</Width>") {
		targetRes = "360p"
	}

	token := extractXMLAttr(reqXML, "Configuration", "token")
	logger.Info("ONVIF", "🔄 Received SetVideoEncoderConfiguration -> applying %s (token: %s)", targetRes, token)

	channels := h.Channels()
	var targetCh *CameraChannel
	for i := range channels {
		ch := &channels[i]
		if token != "" && (strings.Contains(token, ch.Path) || token == fmt.Sprintf("VideoEncoderConfig_%d", ch.Index)) {
			targetCh = ch
			break
		}
	}
	if targetCh == nil && len(channels) > 0 {
		targetCh = &channels[0]
	}

	if targetCh != nil {
		if targetCh.ChangeResFunc != nil {
			_ = targetCh.ChangeResFunc(targetRes)
		}
		if targetCh.EventBus != nil {
			st := targetCh.EventBus.GetStatus()
			st.Resolution = targetRes
			targetCh.EventBus.UpdateStatus(st)
		}
	} else {
		if h.changeResFunc != nil {
			_ = h.changeResFunc(targetRes)
		}
		if h.eventBus != nil {
			st := h.eventBus.GetStatus()
			st.Resolution = targetRes
			h.eventBus.UpdateStatus(st)
		}
	}

	return fmt.Sprintf(`<trt:SetVideoEncoderConfigurationResponse xmlns:trt="%s"/>`, NS_TRT)
}

func (h *MediaHandler) getAudioSources() string {
	channels := h.Channels()
	if len(channels) <= 1 {
		return fmt.Sprintf(`<trt:GetAudioSourcesResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:AudioSources token="AudioSource_1">
    <tt:Channels>1</tt:Channels>
  </trt:AudioSources>
</trt:GetAudioSourcesResponse>`, NS_TRT, NS_TT)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetAudioSourcesResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for _, ch := range channels {
		fmt.Fprintf(&sb, `
  <trt:AudioSources token="AudioSource_%d">
    <tt:Channels>1</tt:Channels>
  </trt:AudioSources>`, ch.Index)
	}
	sb.WriteString("\n</trt:GetAudioSourcesResponse>")
	return sb.String()
}

func (h *MediaHandler) getAudioOutputs() string {
	channels := h.Channels()
	if len(channels) <= 1 {
		return fmt.Sprintf(`<trt:GetAudioOutputsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:AudioOutputs token="AudioOutput_1"/>
</trt:GetAudioOutputsResponse>`, NS_TRT, NS_TT)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetAudioOutputsResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for _, ch := range channels {
		fmt.Fprintf(&sb, `
  <trt:AudioOutputs token="AudioOutput_%d"/>`, ch.Index)
	}
	sb.WriteString("\n</trt:GetAudioOutputsResponse>")
	return sb.String()
}

func (h *MediaHandler) getAudioEncoderConfigurationOptions() string {
	return fmt.Sprintf(`<trt:GetAudioEncoderConfigurationOptionsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:Options>
    <tt:Options>
      <tt:Encoding>AAC</tt:Encoding>
      <tt:BitrateList><tt:Items>64</tt:Items></tt:BitrateList>
      <tt:SampleRateList><tt:Items>16</tt:Items></tt:SampleRateList>
    </tt:Options>
    <tt:Options>
      <tt:Encoding>G711</tt:Encoding>
      <tt:BitrateList><tt:Items>64</tt:Items></tt:BitrateList>
      <tt:SampleRateList><tt:Items>8</tt:Items></tt:SampleRateList>
    </tt:Options>
  </trt:Options>
</trt:GetAudioEncoderConfigurationOptionsResponse>`, NS_TRT, NS_TT)
}

func (h *MediaHandler) getVideoSources() string {
	channels := h.Channels()
	if len(channels) <= 1 {
		return fmt.Sprintf(`<trt:GetVideoSourcesResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:VideoSources token="VideoSource_1">
    <tt:Framerate>15.0</tt:Framerate>
    <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
  </trt:VideoSources>
</trt:GetVideoSourcesResponse>`, NS_TRT, NS_TT)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetVideoSourcesResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for _, ch := range channels {
		fmt.Fprintf(&sb, `
  <trt:VideoSources token="VideoSource_%d">
    <tt:Framerate>15.0</tt:Framerate>
    <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
  </trt:VideoSources>`, ch.Index)
	}
	sb.WriteString("\n</trt:GetVideoSourcesResponse>")
	return sb.String()
}

func (h *MediaHandler) getVideoSourceConfigurations() string {
	channels := h.Channels()
	if len(channels) <= 1 {
		return fmt.Sprintf(`<trt:GetVideoSourceConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:Configurations token="VideoSourceConfig_1">
    <tt:Name>VideoSourceConfig_1</tt:Name>
    <tt:UseCount>2</tt:UseCount>
    <tt:SourceToken>VideoSource_1</tt:SourceToken>
    <tt:Bounds x="0" y="0" width="1920" height="1080"/>
  </trt:Configurations>
</trt:GetVideoSourceConfigurationsResponse>`, NS_TRT, NS_TT)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetVideoSourceConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for _, ch := range channels {
		fmt.Fprintf(&sb, `
  <trt:Configurations token="VideoSourceConfig_%d">
    <tt:Name>VideoSourceConfig_%d</tt:Name>
    <tt:UseCount>2</tt:UseCount>
    <tt:SourceToken>VideoSource_%d</tt:SourceToken>
    <tt:Bounds x="0" y="0" width="1920" height="1080"/>
  </trt:Configurations>`, ch.Index, ch.Index, ch.Index)
	}
	sb.WriteString("\n</trt:GetVideoSourceConfigurationsResponse>")
	return sb.String()
}

func (h *MediaHandler) getAudioSourceConfigurations() string {
	channels := h.Channels()
	if len(channels) <= 1 {
		return fmt.Sprintf(`<trt:GetAudioSourceConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:Configurations token="AudioSourceConfig_1">
    <tt:Name>AudioSourceConfig_1</tt:Name>
    <tt:UseCount>2</tt:UseCount>
    <tt:SourceToken>AudioSource_1</tt:SourceToken>
  </trt:Configurations>
</trt:GetAudioSourceConfigurationsResponse>`, NS_TRT, NS_TT)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetAudioSourceConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for _, ch := range channels {
		fmt.Fprintf(&sb, `
  <trt:Configurations token="AudioSourceConfig_%d">
    <tt:Name>AudioSourceConfig_%d</tt:Name>
    <tt:UseCount>2</tt:UseCount>
    <tt:SourceToken>AudioSource_%d</tt:SourceToken>
  </trt:Configurations>`, ch.Index, ch.Index, ch.Index)
	}
	sb.WriteString("\n</trt:GetAudioSourceConfigurationsResponse>")
	return sb.String()
}

func (h *MediaHandler) getAudioOutputConfigurations() string {
	channels := h.Channels()
	if len(channels) <= 1 {
		return fmt.Sprintf(`<trt:GetAudioOutputConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:Configurations token="AudioOutputConfig_1">
    <tt:Name>AudioOutputConfig_1</tt:Name>
    <tt:UseCount>1</tt:UseCount>
    <tt:OutputToken>AudioOutput_1</tt:OutputToken>
    <tt:SendPrimacy>www.onvif.org/ver20/HalfDuplex/Server</tt:SendPrimacy>
    <tt:OutputLevel>80</tt:OutputLevel>
  </trt:Configurations>
</trt:GetAudioOutputConfigurationsResponse>`, NS_TRT, NS_TT)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<trt:GetAudioOutputConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">`, NS_TRT, NS_TT)
	for _, ch := range channels {
		fmt.Fprintf(&sb, `
  <trt:Configurations token="AudioOutputConfig_%d">
    <tt:Name>AudioOutputConfig_%d</tt:Name>
    <tt:UseCount>1</tt:UseCount>
    <tt:OutputToken>AudioOutput_%d</tt:OutputToken>
    <tt:SendPrimacy>www.onvif.org/ver20/HalfDuplex/Server</tt:SendPrimacy>
    <tt:OutputLevel>80</tt:OutputLevel>
  </trt:Configurations>`, ch.Index, ch.Index, ch.Index)
	}
	sb.WriteString("\n</trt:GetAudioOutputConfigurationsResponse>")
	return sb.String()
}
