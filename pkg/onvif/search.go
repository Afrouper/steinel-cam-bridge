package onvif

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/google/uuid"
)

type SearchHandler struct {
	providerFunc func() storage.RecordingProvider
	onvifPort    int
}

func NewSearchHandler(providerFunc func() storage.RecordingProvider, onvifPort int) *SearchHandler {
	return &SearchHandler{
		providerFunc: providerFunc,
		onvifPort:    onvifPort,
	}
}

func (h *SearchHandler) Handle(action, reqXML, host string) (string, error) {
	if strings.Contains(action, "GetRecordingSummary") || strings.Contains(reqXML, "GetRecordingSummary") {
		return h.getRecordingSummary(), nil
	}
	if strings.Contains(action, "FindRecordings") || strings.Contains(reqXML, "FindRecordings") {
		return h.findRecordings(), nil
	}
	if strings.Contains(action, "GetRecordingSearchResults") || strings.Contains(reqXML, "GetRecordingSearchResults") {
		return h.getRecordingSearchResults(host), nil
	}
	if strings.Contains(action, "FindEvents") || strings.Contains(reqXML, "FindEvents") {
		return h.findEvents(), nil
	}
	if strings.Contains(action, "GetEventSearchResults") || strings.Contains(reqXML, "GetEventSearchResults") {
		return h.getEventSearchResults(), nil
	}
	if strings.Contains(action, "GetRecordingInformation") || strings.Contains(reqXML, "GetRecordingInformation") {
		return h.getRecordingInformation(), nil
	}

	return "", fmt.Errorf("unhandled search action: %s", action)
}

func (h *SearchHandler) getRecordingSummary() string {
	now := time.Now().UTC()
	start := now.Add(-7 * 24 * time.Hour)
	count := 0

	if h.providerFunc != nil {
		if p := h.providerFunc(); p != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if resp, err := p.ListRecordings(ctx, start, now, 0, 1000, ""); err == nil && resp != nil {
				count = resp.Total
				if len(resp.List) > 0 {
					start = resp.List[len(resp.List)-1].StartTime
					now = resp.List[0].EndTime
				}
			}
		}
	}

	return fmt.Sprintf(`<tse:GetRecordingSummaryResponse xmlns:tse="%s" xmlns:tt="%s">
  <tse:Summary>
    <tt:DataFromStore>
      <tt:TotalRecordings>%d</tt:TotalRecordings>
      <tt:EarliestRecording>%s</tt:EarliestRecording>
      <tt:LatestRecording>%s</tt:LatestRecording>
    </tt:DataFromStore>
  </tse:Summary>
</tse:GetRecordingSummaryResponse>`, NS_TSE, NS_TT, count, start.Format(time.RFC3339), now.Format(time.RFC3339))
}

func (h *SearchHandler) findRecordings() string {
	token := "SearchToken_" + uuid.New().String()[:8]
	return fmt.Sprintf(`<tse:FindRecordingsResponse xmlns:tse="%s">
  <tse:SearchToken>%s</tse:SearchToken>
</tse:FindRecordingsResponse>`, NS_TSE, token)
}

func (h *SearchHandler) getRecordingSearchResults(host string) string {
	var items []storage.RecordingItem
	if h.providerFunc != nil {
		if p := h.providerFunc(); p != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if resp, err := p.ListRecordings(ctx, time.Now().Add(-24*time.Hour), time.Now(), 0, 50, ""); err == nil && resp != nil {
				items = resp.List
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<tse:GetRecordingSearchResultsResponse xmlns:tse="%s" xmlns:tt="%s"><tse:ResultList>`, NS_TSE, NS_TT)

	for _, item := range items {
		fmt.Fprintf(&sb, `
    <tt:RecordingInformation>
      <tt:RecordingToken>%s</tt:RecordingToken>
      <tt:Source>
        <tt:SourceId>SteinelCamera0</tt:SourceId>
        <tt:Name>%s</tt:Name>
        <tt:Location>Main</tt:Location>
        <tt:Description>%s</tt:Description>
        <tt:Address>http://%s/api/sdcard/events/%s/video.mp4</tt:Address>
      </tt:Source>
      <tt:EarliestRecording>%s</tt:EarliestRecording>
      <tt:LatestRecording>%s</tt:LatestRecording>
      <tt:Content>Motion Event Recording</tt:Content>
      <tt:Track>
        <tt:TrackToken>Track_Video</tt:TrackToken>
        <tt:TrackInformation>
          <tt:TrackType>Video</tt:TrackType>
          <tt:Description>H264 Main Video Track</tt:Description>
          <tt:DataFrom>%s</tt:DataFrom>
          <tt:DataTo>%s</tt:DataTo>
        </tt:TrackInformation>
      </tt:Track>
    </tt:RecordingInformation>`, item.ID, item.FileName, item.EventType, host, item.ID, item.StartTime.Format(time.RFC3339), item.EndTime.Format(time.RFC3339), item.StartTime.Format(time.RFC3339), item.EndTime.Format(time.RFC3339))
	}

	sb.WriteString(`</tse:ResultList></tse:GetRecordingSearchResultsResponse>`)
	return sb.String()
}

func (h *SearchHandler) findEvents() string {
	token := "EventSearchToken_" + uuid.New().String()[:8]
	return fmt.Sprintf(`<tse:FindEventsResponse xmlns:tse="%s">
  <tse:SearchToken>%s</tse:SearchToken>
</tse:FindEventsResponse>`, NS_TSE, token)
}

func (h *SearchHandler) getEventSearchResults() string {
	return fmt.Sprintf(`<tse:GetEventSearchResultsResponse xmlns:tse="%s" xmlns:tt="%s">
  <tse:ResultList></tse:ResultList>
</tse:GetEventSearchResultsResponse>`, NS_TSE, NS_TT)
}

func (h *SearchHandler) getRecordingInformation() string {
	now := time.Now().UTC()
	return fmt.Sprintf(`<tse:GetRecordingInformationResponse xmlns:tse="%s" xmlns:tt="%s">
  <tse:RecordingInformation>
    <tt:RecordingToken>Recording_Main</tt:RecordingToken>
    <tt:Source>
      <tt:SourceId>SteinelCamera0</tt:SourceId>
      <tt:Name>Steinel Cam</tt:Name>
      <tt:Location>Main</tt:Location>
      <tt:Description>Local MicroSD Storage</tt:Description>
    </tt:Source>
    <tt:EarliestRecording>%s</tt:EarliestRecording>
    <tt:LatestRecording>%s</tt:LatestRecording>
    <tt:Content>Motion Recordings</tt:Content>
  </tse:RecordingInformation>
</tse:GetRecordingInformationResponse>`, NS_TSE, NS_TT, now.Add(-24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
}
