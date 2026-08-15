package onvif

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"steinel-cam-bridge/pkg/events"

	"github.com/google/uuid"
)

type Subscription struct {
	ID         string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastMotion bool
	EventChan  chan bool
}

type EventHandler struct {
	onvifPort     int
	subscriptions map[string]*Subscription
	mu            sync.Mutex
}

func NewEventHandler(onvifPort int) *EventHandler {
	h := &EventHandler{
		onvifPort:     onvifPort,
		subscriptions: make(map[string]*Subscription),
	}

	// Hook into Global Event Bus
	events.GlobalBus.Subscribe(func(evt events.EventType, data interface{}) {
		if evt == events.EventMotion {
			if m, ok := data.(events.MotionEvent); ok {
				h.broadcastMotionEvent(m.IsMotion)
			}
		}
	})

	return h
}

func (h *EventHandler) broadcastMotionEvent(isMotion bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, sub := range h.subscriptions {
		select {
		case sub.EventChan <- isMotion:
		default:
			// Queue full, drain and replace
			select {
			case <-sub.EventChan:
			default:
			}
			sub.EventChan <- isMotion
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

	return "", fmt.Errorf("unhandled event action: %s", action)
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

	sub := &Subscription{
		ID:         subID,
		CreatedAt:  now,
		ExpiresAt:  expires,
		EventChan:  make(chan bool, 10),
		LastMotion: events.GlobalBus.GetStatus().IsMotion,
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
		// Create temporary default sub if client didn't supply subID in URL
		subID = "default"
		if s, ok := h.subscriptions[subID]; ok {
			sub = s
		} else {
			sub = &Subscription{
				ID:        subID,
				CreatedAt: time.Now().UTC(),
				ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
				EventChan: make(chan bool, 10),
			}
			h.subscriptions[subID] = sub
		}
	}
	h.mu.Unlock()

	now := time.Now().UTC()

	// Wait up to 3 seconds for motion state change or send current state
	var isMotion bool
	hasNewEvent := false

	select {
	case m := <-sub.EventChan:
		isMotion = m
		hasNewEvent = true
		sub.LastMotion = m
	case <-time.After(3 * time.Second):
		isMotion = events.GlobalBus.GetStatus().IsMotion
		// If status is different from last recorded, send it
		if isMotion != sub.LastMotion {
			hasNewEvent = true
			sub.LastMotion = isMotion
		}
	}

	if !hasNewEvent {
		return fmt.Sprintf(`<tev:PullMessagesResponse xmlns:tev="%s" xmlns:wsnt="%s">
  <tev:CurrentTime>%s</tev:CurrentTime>
  <tev:TerminationTime>%s</tev:TerminationTime>
</tev:PullMessagesResponse>`, NS_TEV, NS_WSNT, now.Format(time.RFC3339), sub.ExpiresAt.Format(time.RFC3339))
	}

	motionStr := "false"
	if isMotion {
		motionStr = "true"
	}

	return fmt.Sprintf(`<tev:PullMessagesResponse xmlns:tev="%s" xmlns:wsnt="%s" xmlns:tt="%s">
  <tev:CurrentTime>%s</tev:CurrentTime>
  <tev:TerminationTime>%s</tev:TerminationTime>
  <wsnt:NotificationMessage>
    <wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet">tns1:RuleEngine/CellMotionDetector/Motion</wsnt:Topic>
    <wsnt:Message>
      <tt:Message UtcTime="%s" PropertyOperation="Changed">
        <tt:Source>
          <tt:SimpleItem Name="VideoSourceConfigurationToken" Value="VideoSourceConfig_1"/>
        </tt:Source>
        <tt:Data>
          <tt:SimpleItem Name="IsMotion" Value="%s"/>
        </tt:Data>
      </tt:Message>
    </wsnt:Message>
  </wsnt:NotificationMessage>
</tev:PullMessagesResponse>`, NS_TEV, NS_WSNT, NS_TT, now.Format(time.RFC3339), sub.ExpiresAt.Format(time.RFC3339), now.Format(time.RFC3339), motionStr)
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
