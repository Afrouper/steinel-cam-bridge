package events

import (
	"sync"
	"time"
)

type EventType string

const (
	EventMotion EventType = "motion"
	EventLight  EventType = "light"
	EventLux    EventType = "lux"
	EventDevice EventType = "device"
)

type MotionEvent struct {
	IsMotion  bool      `json:"is_motion"`
	Timestamp time.Time `json:"timestamp"`
}

type DeviceStatus struct {
	IsMotion     bool      `json:"is_motion"`
	MotionLastAt time.Time `json:"motion_last_at"`
	LampMode     int       `json:"lamp_mode"`
	Lux          int       `json:"lux"`
	PIRActive    bool      `json:"pir_active"`
	PIRThreshold int       `json:"pir_threshold"`
	FirmwareVer  string    `json:"firmware_ver"`
	Resolution   string    `json:"resolution"`
	LastSeen     time.Time `json:"last_seen"`
}

type Listener func(evt EventType, data interface{})

type Bus struct {
	mu        sync.RWMutex
	listeners []Listener
	status    DeviceStatus
}

var GlobalBus = NewBus()

func NewBus() *Bus {
	return &Bus{
		status: DeviceStatus{
			Resolution: "1080p",
			LastSeen:   time.Now(),
		},
	}
}

func (b *Bus) Subscribe(fn Listener) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners = append(b.listeners, fn)
}

func (b *Bus) Publish(evt EventType, data interface{}) {
	b.mu.Lock()
	if evt == EventMotion {
		if m, ok := data.(MotionEvent); ok {
			b.status.IsMotion = m.IsMotion
			if m.IsMotion {
				b.status.MotionLastAt = m.Timestamp
			}
		}
	}
	b.status.LastSeen = time.Now()
	listeners := make([]Listener, len(b.listeners))
	copy(listeners, b.listeners)
	b.mu.Unlock()

	for _, l := range listeners {
		l(evt, data)
	}
}

func (b *Bus) SetMotion(isMotion bool) {
	b.Publish(EventMotion, MotionEvent{
		IsMotion:  isMotion,
		Timestamp: time.Now(),
	})
}

func (b *Bus) UpdateStatus(status DeviceStatus) {
	b.mu.Lock()
	b.status = status
	b.mu.Unlock()
	b.Publish(EventDevice, status)
}

func (b *Bus) GetStatus() DeviceStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.status
}
