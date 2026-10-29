package onvif

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"

	"github.com/google/uuid"
)

type motionEventItem struct {
	sourceToken string
	isMotion    bool
}

type Subscription struct {
	ID         string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastMotion map[string]bool
	EventChan  chan motionEventItem
}

type EventHandler struct {
	onvifPort     int
	eventBus      *events.Bus
	subscriptions map[string]*Subscription
	mu            sync.Mutex
	sourcesMu     sync.RWMutex
	sources       map[string]*events.Bus
}

func NewEventHandler(onvifPort int, eventBus *events.Bus) *EventHandler {
	if eventBus == nil {
		eventBus = events.GlobalBus
	}
	h := &EventHandler{
		onvifPort:     onvifPort,
		eventBus:      eventBus,
		subscriptions: make(map[string]*Subscription),
		sources:       make(map[string]*events.Bus),
	}

	h.AddSource("VideoSourceConfig_1", eventBus)
	return h
}

func (h *EventHandler) AddSource(sourceToken string, bus *events.Bus) {
	if bus == nil {
		return
	}
	h.sourcesMu.Lock()
	h.sources[sourceToken] = bus
	h.sourcesMu.Unlock()

	bus.Subscribe(func(evt events.EventType, data interface{}) {
		if evt == events.EventMotion {
			if m, ok := data.(events.MotionEvent); ok {
				h.broadcastMotionEvent(sourceToken, m.IsMotion)
			}
		}
	})
}

func (h *EventHandler) broadcastMotionEvent(sourceToken string, isMotion bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	item := motionEventItem{
		sourceToken: sourceToken,
		isMotion:    isMotion,
	}

	for _, sub := range h.subscriptions {
		select {
		case sub.EventChan <- item:
		default:
			// Queue full, drain and replace
			select {
			case <-sub.EventChan:
			default:
			}
			sub.EventChan <- item
		}
	}
}

func (h *EventHandler) Handle(action string, reqXML string, host string, subID string) (string, error) {
	if strings.Contains(action, "GetServiceCapabilities") || strings.Contains(reqXML, "GetServiceCapabilities") {
		return h.getServiceCapabilities(), nil
	}
	if strings.Contains(action, "GetEventProperties") || strings.Contains(reqXML, "GetEventProperties") {
		return h.getEventProperties(), nil
	}
	if strings.Contains(action, "CreatePullPointSubscription") || strings.Contains(reqXML, "CreatePullPointSubscription") {
		return h.createPullPointSubscription(host), nil
	}
	if strings.Contains(action, "PullMessages") || strings.Contains(reqXML, "PullMessages") {
		return h.pullMessages(subID, reqXML), nil
	}
	if strings.Contains(action, "Renew") || strings.Contains(reqXML, "Renew") {
		return h.renew(subID), nil
	}
	if strings.Contains(action, "Unsubscribe") || strings.Contains(reqXML, "Unsubscribe") {
		return h.unsubscribe(subID), nil
	}
	if strings.Contains(action, "SetSynchronizationPoint") || strings.Contains(reqXML, "SetSynchronizationPoint") {
		return h.setSynchronizationPoint(), nil
	}
	if strings.Contains(action, "Subscribe") || strings.Contains(reqXML, "Subscribe") {
		return h.createPullPointSubscription(host), nil
	}

	return "", fmt.Errorf("unhandled event action: %s", action)
}

func (h *EventHandler) setSynchronizationPoint() string {
	h.sourcesMu.RLock()
	defer h.sourcesMu.RUnlock()
	for token, bus := range h.sources {
		h.broadcastMotionEvent(token, bus.GetStatus().IsMotion)
	}
	return fmt.Sprintf(`<tev:SetSynchronizationPointResponse xmlns:tev="%s"/>`, NS_TEV)
}

func (h *EventHandler) getServiceCapabilities() string {
	return fmt.Sprintf(`<tev:GetServiceCapabilitiesResponse xmlns:tev="%s" xmlns:tt="%s">
  <tev:Capabilities WSSubscriptionPolicySupport="true" WSPullPointSupport="true" WSPausableSubscriptionManagerInterfaceSupport="false" MaxNotificationProducers="10" MaxPullPoints="10"/>
</tev:GetServiceCapabilitiesResponse>`, NS_TEV, NS_TT)
}

func (h *EventHandler) getEventProperties() string {
	return fmt.Sprintf(`<tev:GetEventPropertiesResponse xmlns:tev="%s" xmlns:tt="%s" xmlns:wsnt="%s">
  <tev:TopicNamespaceLocation>http://www.onvif.org/onvif/ver10/topics/topicns.xml</tev:TopicNamespaceLocation>
  <wsnt:FixedTopicSet>true</wsnt:FixedTopicSet>
  <tev:MessageContentFilterDialect>http://www.onvif.org/ver10/tev/messageContentFilter/ItemFilter</tev:MessageContentFilterDialect>
  <tev:ProducerPropertiesFilterDialect>http://www.onvif.org/ver10/tev/messageContentFilter/ItemFilter</tev:ProducerPropertiesFilterDialect>
  <tev:MessageContentSchemaLocation>http://www.onvif.org/onvif/ver10/schema/onvif.xsd</tev:MessageContentSchemaLocation>
</tev:GetEventPropertiesResponse>`, NS_TEV, NS_TT, NS_WSNT)
}

func (h *EventHandler) createPullPointSubscription(host string) string {
	ip := extractHostIP(host)
	subID := uuid.New().String()
	now := time.Now().UTC()
	expires := now.Add(10 * time.Minute)

	lastMotion := make(map[string]bool)
	h.sourcesMu.RLock()
	for token, bus := range h.sources {
		lastMotion[token] = bus.GetStatus().IsMotion
	}
	h.sourcesMu.RUnlock()

	sub := &Subscription{
		ID:         subID,
		CreatedAt:  now,
		ExpiresAt:  expires,
		EventChan:  make(chan motionEventItem, 20),
		LastMotion: lastMotion,
	}

	h.mu.Lock()
	h.subscriptions[subID] = sub
	h.mu.Unlock()

	subURL := fmt.Sprintf("http://%s:%d/onvif/event_service?sub=%s", ip, h.onvifPort, subID)

	return fmt.Sprintf(`<tev:CreatePullPointSubscriptionResponse xmlns:tev="%s" xmlns:wsnt="%s" xmlns:wsa="%s">
  <tev:SubscriptionReference>
    <wsa:Address>%s</wsa:Address>
  </tev:SubscriptionReference>
  <wsnt:CurrentTime>%s</wsnt:CurrentTime>
  <wsnt:TerminationTime>%s</wsnt:TerminationTime>
</tev:CreatePullPointSubscriptionResponse>`, NS_TEV, NS_WSNT, NS_WSA, subURL, now.Format(time.RFC3339), expires.Format(time.RFC3339))
}

func (h *EventHandler) pullMessages(subID string, reqXML string) string {
	h.mu.Lock()
	sub, exists := h.subscriptions[subID]
	if !exists {
		// If only one active subscription exists, bind to it (e.g. client using generic subID)
		if len(h.subscriptions) == 1 {
			for _, s := range h.subscriptions {
				sub = s
				exists = true
				break
			}
		} else {
			subID = "default"
			if s, ok := h.subscriptions[subID]; ok {
				sub = s
			} else {
				sub = &Subscription{
					ID:         subID,
					CreatedAt:  time.Now().UTC(),
					ExpiresAt:  time.Now().UTC().Add(10 * time.Minute),
					EventChan:  make(chan motionEventItem, 20),
					LastMotion: make(map[string]bool),
				}
				h.subscriptions[subID] = sub
			}
		}
	}
	h.mu.Unlock()

	now := time.Now().UTC()

	var item motionEventItem
	hasNewEvent := false

	select {
	case it := <-sub.EventChan:
		item = it
		hasNewEvent = true
		sub.LastMotion[it.sourceToken] = it.isMotion
	case <-time.After(3 * time.Second):
		h.sourcesMu.RLock()
		for token, bus := range h.sources {
			curr := bus.GetStatus().IsMotion
			if sub.LastMotion[token] != curr {
				item = motionEventItem{sourceToken: token, isMotion: curr}
				hasNewEvent = true
				sub.LastMotion[token] = curr
				break
			}
		}
		h.sourcesMu.RUnlock()
	}

	if !hasNewEvent {
		return fmt.Sprintf(`<tev:PullMessagesResponse xmlns:tev="%s" xmlns:wsnt="%s">
  <tev:CurrentTime>%s</tev:CurrentTime>
  <tev:TerminationTime>%s</tev:TerminationTime>
</tev:PullMessagesResponse>`, NS_TEV, NS_WSNT, now.Format(time.RFC3339), sub.ExpiresAt.Format(time.RFC3339))
	}

	motionStr := "false"
	if item.isMotion {
		motionStr = "true"
	}
	sourceToken := item.sourceToken
	if sourceToken == "" {
		sourceToken = "VideoSourceConfig_1"
	}

	return fmt.Sprintf(`<tev:PullMessagesResponse xmlns:tev="%s" xmlns:wsnt="%s" xmlns:tt="%s">
  <tev:CurrentTime>%s</tev:CurrentTime>
  <tev:TerminationTime>%s</tev:TerminationTime>
  <wsnt:NotificationMessage>
    <wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet">tns1:RuleEngine/CellMotionDetector/Motion</wsnt:Topic>
    <wsnt:Message>
      <tt:Message UtcTime="%s" PropertyOperation="Changed">
        <tt:Source>
          <tt:SimpleItem Name="VideoSourceConfigurationToken" Value="%s"/>
        </tt:Source>
        <tt:Data>
          <tt:SimpleItem Name="IsMotion" Value="%s"/>
        </tt:Data>
      </tt:Message>
    </wsnt:Message>
  </wsnt:NotificationMessage>
</tev:PullMessagesResponse>`, NS_TEV, NS_WSNT, NS_TT, now.Format(time.RFC3339), sub.ExpiresAt.Format(time.RFC3339), now.Format(time.RFC3339), sourceToken, motionStr)
}

func (h *EventHandler) renew(subID string) string {
	now := time.Now().UTC()
	expires := now.Add(10 * time.Minute)

	h.mu.Lock()
	if sub, exists := h.subscriptions[subID]; exists {
		sub.ExpiresAt = expires
	}
	h.mu.Unlock()

	return fmt.Sprintf(`<wsnt:RenewResponse xmlns:wsnt="%s">
  <wsnt:TerminationTime>%s</wsnt:TerminationTime>
  <wsnt:CurrentTime>%s</wsnt:CurrentTime>
</wsnt:RenewResponse>`, NS_WSNT, expires.Format(time.RFC3339), now.Format(time.RFC3339))
}

func (h *EventHandler) unsubscribe(subID string) string {
	h.mu.Lock()
	delete(h.subscriptions, subID)
	h.mu.Unlock()

	return fmt.Sprintf(`<wsnt:UnsubscribeResponse xmlns:wsnt="%s"/>`, NS_WSNT)
}
