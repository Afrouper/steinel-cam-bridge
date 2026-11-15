package events

import (
	"sync"
	"testing"
	"time"
)

func TestEventBus(t *testing.T) {
	bus := NewBus()

	var wg sync.WaitGroup
	wg.Add(1)

	var receivedMotion bool
	bus.Subscribe(func(evt EventType, data interface{}) {
		if evt == EventMotion {
			if m, ok := data.(MotionEvent); ok {
				receivedMotion = m.IsMotion
				wg.Done()
			}
		}
	})

	bus.SetMotion(true)

	if waitTimeout(&wg, 1*time.Second) {
		t.Fatalf("Timed out waiting for event")
	}

	if !receivedMotion {
		t.Errorf("Expected receivedMotion true")
	}

	st := bus.GetStatus()
	if !st.IsMotion {
		t.Errorf("Expected bus status IsMotion true")
	}
}

func TestEventBus_MotionSource(t *testing.T) {
	bus := NewBus()

	var eventsReceived []MotionEvent
	bus.Subscribe(func(evt EventType, data interface{}) {
		if evt == EventMotion {
			if m, ok := data.(MotionEvent); ok {
				eventsReceived = append(eventsReceived, m)
			}
		}
	})

	bus.SetMotion(true)
	bus.SetMotionWithSource(true, "recording")

	if len(eventsReceived) != 2 {
		t.Fatalf("Expected 2 events, got %d", len(eventsReceived))
	}
	if eventsReceived[0].Source != "hardware" {
		t.Errorf("Expected first event source 'hardware', got '%s'", eventsReceived[0].Source)
	}
	if eventsReceived[1].Source != "recording" {
		t.Errorf("Expected second event source 'recording', got '%s'", eventsReceived[1].Source)
	}
}

func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false
	case <-time.After(timeout):
		return true
	}
}
