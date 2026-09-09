package onvif

import (
	"fmt"
	"strings"

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
}

func NewMediaHandler(rtspPort int, rtspPath string, audioCodec string, onvifPort int, changeResFunc func(res string) error, eventBus *events.Bus) *MediaHandler {
	audioCodec = strings.ToLower(strings.TrimSpace(audioCodec))
	if audioCodec == "" {
		audioCodec = "aac"
	}
	if eventBus == nil {
		eventBus = events.GlobalBus
	}
	return &MediaHandler{
		rtspPort:      rtspPort,
		rtspPath:      rtspPath,
		audioCodec:    audioCodec,
		onvifPort:     onvifPort,
		changeResFunc: changeResFunc,
		eventBus:      eventBus,
	}
}

func (h *MediaHandler) Handle(action string, reqXML string, host string) (string, error) {
	if strings.Contains(action, "GetProfiles") || strings.Contains(reqXML, "GetProfiles") {
		return h.getProfiles(), nil
	}
	if strings.Contains(action, "GetProfile") || strings.Contains(reqXML, "GetProfile") {
		return h.getProfile(reqXML), nil
	}
	if strings.Contains(action, "GetStreamUri") || strings.Contains(reqXML, "GetStreamUri") {
		return h.getStreamUri(host), nil
	}
	if strings.Contains(action, "GetSnapshotUri") || strings.Contains(reqXML, "GetSnapshotUri") {
		return h.getSnapshotUri(host), nil
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

func (h *MediaHandler) getProfiles() string {
	audioEncoding := "AAC"
	sampleRate := 16
	if h.audioCodec == "pcmu" {
		audioEncoding = "G711"
		sampleRate = 8
	}

	return fmt.Sprintf(`<trt:GetProfilesResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:Profiles token="Profile_Main" fixed="true">
    <tt:Name>MainStream 1080p</tt:Name>
    <tt:VideoSourceConfiguration token="VideoSourceConfig_1">
      <tt:Name>VideoSourceConfig_1</tt:Name>
      <tt:UseCount>2</tt:UseCount>
      <tt:SourceToken>VideoSource_1</tt:SourceToken>
      <tt:Bounds x="0" y="0" width="1920" height="1080"/>
    </tt:VideoSourceConfiguration>
    <tt:AudioSourceConfiguration token="AudioSourceConfig_1">
      <tt:Name>AudioSourceConfig_1</tt:Name>
      <tt:UseCount>2</tt:UseCount>
      <tt:SourceToken>AudioSource_1</tt:SourceToken>
    </tt:AudioSourceConfiguration>
    <tt:VideoEncoderConfiguration token="VideoEncoderConfig_Main">
      <tt:Name>VideoEncoderConfig_Main</tt:Name>
      <tt:UseCount>1</tt:UseCount>
      <tt:Encoding>H264</tt:Encoding>
      <tt:Resolution>
        <tt:Width>1920</tt:Width>
        <tt:Height>1080</tt:Height>
      </tt:Resolution>
      <tt:Quality>5.0</tt:Quality>
      <tt:RateControl>
        <tt:FrameRateLimit>15</tt:FrameRateLimit>
        <tt:EncodingInterval>1</tt:EncodingInterval>
        <tt:BitrateLimit>2048</tt:BitrateLimit>
      </tt:RateControl>
      <tt:H264>
        <tt:GovLength>45</tt:GovLength>
        <tt:H264Profile>High</tt:H264Profile>
      </tt:H264>
      <tt:Multicast>
        <tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address>
        <tt:Port>0</tt:Port>
        <tt:TTL>1</tt:TTL>
        <tt:AutoStart>false</tt:AutoStart>
      </tt:Multicast>
      <tt:SessionTimeout>PT60S</tt:SessionTimeout>
    </tt:VideoEncoderConfiguration>
    <tt:AudioEncoderConfiguration token="AudioEncoderConfig_1">
      <tt:Name>AudioEncoderConfig_1</tt:Name>
      <tt:UseCount>2</tt:UseCount>
      <tt:Encoding>%s</tt:Encoding>
      <tt:Bitrate>64</tt:Bitrate>
      <tt:SampleRate>%d</tt:SampleRate>
      <tt:SessionTimeout>PT60S</tt:SessionTimeout>
    </tt:AudioEncoderConfiguration>
    <tt:AudioOutputConfiguration token="AudioOutputConfig_1">
      <tt:Name>AudioOutputConfig_1</tt:Name>
      <tt:UseCount>1</tt:UseCount>
      <tt:OutputToken>AudioOutput_1</tt:OutputToken>
      <tt:SendPrimacy>www.onvif.org/ver20/HalfDuplex/Server</tt:SendPrimacy>
      <tt:OutputLevel>80</tt:OutputLevel>
    </tt:AudioOutputConfiguration>
  </trt:Profiles>
  <trt:Profiles token="Profile_Sub" fixed="true">
    <tt:Name>SubStream 360p</tt:Name>
    <tt:VideoSourceConfiguration token="VideoSourceConfig_1">
      <tt:Name>VideoSourceConfig_1</tt:Name>
      <tt:UseCount>2</tt:UseCount>
      <tt:SourceToken>VideoSource_1</tt:SourceToken>
      <tt:Bounds x="0" y="0" width="640" height="360"/>
    </tt:VideoSourceConfiguration>
    <tt:AudioSourceConfiguration token="AudioSourceConfig_1">
      <tt:Name>AudioSourceConfig_1</tt:Name>
      <tt:UseCount>2</tt:UseCount>
      <tt:SourceToken>AudioSource_1</tt:SourceToken>
    </tt:AudioSourceConfiguration>
    <tt:VideoEncoderConfiguration token="VideoEncoderConfig_Sub">
      <tt:Name>VideoEncoderConfig_Sub</tt:Name>
      <tt:UseCount>1</tt:UseCount>
      <tt:Encoding>H264</tt:Encoding>
      <tt:Resolution>
        <tt:Width>640</tt:Width>
        <tt:Height>360</tt:Height>
      </tt:Resolution>
      <tt:Quality>3.0</tt:Quality>
      <tt:RateControl>
        <tt:FrameRateLimit>10</tt:FrameRateLimit>
        <tt:EncodingInterval>1</tt:EncodingInterval>
        <tt:BitrateLimit>512</tt:BitrateLimit>
      </tt:RateControl>
      <tt:H264>
        <tt:GovLength>30</tt:GovLength>
        <tt:H264Profile>Baseline</tt:H264Profile>
      </tt:H264>
      <tt:Multicast>
        <tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address>
        <tt:Port>0</tt:Port>
        <tt:TTL>1</tt:TTL>
        <tt:AutoStart>false</tt:AutoStart>
      </tt:Multicast>
      <tt:SessionTimeout>PT60S</tt:SessionTimeout>
    </tt:VideoEncoderConfiguration>
    <tt:AudioEncoderConfiguration token="AudioEncoderConfig_1">
      <tt:Name>AudioEncoderConfig_1</tt:Name>
      <tt:UseCount>2</tt:UseCount>
      <tt:Encoding>%s</tt:Encoding>
      <tt:Bitrate>64</tt:Bitrate>
      <tt:SampleRate>%d</tt:SampleRate>
      <tt:SessionTimeout>PT60S</tt:SessionTimeout>
    </tt:AudioEncoderConfiguration>
  </trt:Profiles>
</trt:GetProfilesResponse>`, NS_TRT, NS_TT, audioEncoding, sampleRate, audioEncoding, sampleRate)
}

func (h *MediaHandler) getProfile(reqXML string) string {
	return h.getProfiles()
}

func (h *MediaHandler) getStreamUri(host string) string {
	ip := extractHostIP(host)
	rtspURL := fmt.Sprintf("rtsp://%s:%d/%s", ip, h.rtspPort, h.rtspPath)

	return fmt.Sprintf(`<trt:GetStreamUriResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:MediaUri>
    <tt:Uri>%s</tt:Uri>
    <tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>
    <tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>
    <tt:Timeout>PT60S</tt:Timeout>
  </trt:MediaUri>
</trt:GetStreamUriResponse>`, NS_TRT, NS_TT, rtspURL)
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

	logger.Info("ONVIF", "🔄 Received SetVideoEncoderConfiguration -> applying %s", targetRes)
	if h.changeResFunc != nil {
		_ = h.changeResFunc(targetRes)
	}

	st := h.eventBus.GetStatus()
	st.Resolution = targetRes
	h.eventBus.UpdateStatus(st)

	return fmt.Sprintf(`<trt:SetVideoEncoderConfigurationResponse xmlns:trt="%s"/>`, NS_TRT)
}

func (h *MediaHandler) getAudioSources() string {
	return fmt.Sprintf(`<trt:GetAudioSourcesResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:AudioSources token="AudioSource_1">
    <tt:Channels>1</tt:Channels>
  </trt:AudioSources>
</trt:GetAudioSourcesResponse>`, NS_TRT, NS_TT)
}

func (h *MediaHandler) getAudioOutputs() string {
	return fmt.Sprintf(`<trt:GetAudioOutputsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:AudioOutputs token="AudioOutput_1"/>
</trt:GetAudioOutputsResponse>`, NS_TRT, NS_TT)
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
	return fmt.Sprintf(`<trt:GetVideoSourcesResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:VideoSources token="VideoSource_1">
    <tt:Framerate>15.0</tt:Framerate>
    <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
  </trt:VideoSources>
</trt:GetVideoSourcesResponse>`, NS_TRT, NS_TT)
}

func (h *MediaHandler) getSnapshotUri(host string) string {
	ip := extractHostIP(host)
	snapshotURL := fmt.Sprintf("http://%s:%d/api/snapshot.jpg", ip, h.onvifPort)

	return fmt.Sprintf(`<trt:GetSnapshotUriResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:MediaUri>
    <tt:Uri>%s</tt:Uri>
    <tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>
    <tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>
    <tt:Timeout>PT30S</tt:Timeout>
  </trt:MediaUri>
</trt:GetSnapshotUriResponse>`, NS_TRT, NS_TT, snapshotURL)
}

func (h *MediaHandler) getVideoSourceConfigurations() string {
	return fmt.Sprintf(`<trt:GetVideoSourceConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:Configurations token="VideoSourceConfig_1">
    <tt:Name>VideoSourceConfig_1</tt:Name>
    <tt:UseCount>2</tt:UseCount>
    <tt:SourceToken>VideoSource_1</tt:SourceToken>
    <tt:Bounds x="0" y="0" width="1920" height="1080"/>
  </trt:Configurations>
</trt:GetVideoSourceConfigurationsResponse>`, NS_TRT, NS_TT)
}

func (h *MediaHandler) getAudioSourceConfigurations() string {
	return fmt.Sprintf(`<trt:GetAudioSourceConfigurationsResponse xmlns:trt="%s" xmlns:tt="%s">
  <trt:Configurations token="AudioSourceConfig_1">
    <tt:Name>AudioSourceConfig_1</tt:Name>
    <tt:UseCount>2</tt:UseCount>
    <tt:SourceToken>AudioSource_1</tt:SourceToken>
  </trt:Configurations>
</trt:GetAudioSourceConfigurationsResponse>`, NS_TRT, NS_TT)
}

func (h *MediaHandler) getAudioOutputConfigurations() string {
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
