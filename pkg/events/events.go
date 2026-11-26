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
	Source    string    `json:"source,omitempty"`
}

type DeviceStatus struct {
	IsMotion       bool      `json:"is_motion"`
	MotionLastAt   time.Time `json:"motion_last_at"`
	LampMode       int       `json:"lamp_mode"` // 0=Off, 1=On (Dauerlicht), 2=Sensor (Auto)
	Lux            int       `json:"lux"`
	PIRActive      bool      `json:"pir_active"`
	PIRSensitivity int       `json:"pir_sensitivity"`
	Highlight      int       `json:"highlight"`      // 10 - 100%
	HighlightTime  int       `json:"highlight_time"` // 5 - 900s
	Lowlight       int       `json:"lowlight"`       // 0 - 50%
	LowlightTime   int       `json:"lowlight_time"`
	ColorTemp      int       `json:"color_temp"`
	FirmwareVer    string    `json:"firmware_ver"`
	Resolution     string    `json:"resolution"`
	LastSeen       time.Time `json:"last_seen"`

	// Camera diagnostics (L 625, from get_device_info).
	WifiSignal int    `json:"wifi_signal"` // 0 - 100 %, 0 = unknown
	SDFreeMB   int    `json:"sd_free_mb"`  // free space on the SD card as reported (MiB)
	SDTotalMB  int    `json:"sd_total_mb"` // card size as reported (MiB); 0 = unknown / no card
	SDStatus   string `json:"sd_status"`   // e.g. "normal"

	// Camera-side settings (L 625, from get_device_info).
	OSDPosition   int    `json:"osd_position"`   // timestamp overlay: 0 = off, 1-4 = corner, -1 = unknown
	SpeakerVolume int    `json:"speaker_volume"` // 0 - 100 %, -1 = unknown
	DetectMode    string `json:"detect_mode"`    // e.g. "ai"
	SirenEnabled  int    `json:"siren_enabled"`  // Motion alarm siren/voice state: 1 = on, 0 = off, -1 = unknown.

	// Lamp setting from the MCU status frame: 1 = "Pixel Change Detection" on,
	// 0 = off, -1 = unknown.
	PixelChangeDetection int `json:"pixel_change_detection"`

	// Camera-side detection and recording settings (L 625, from
	// get_detect_data / get_record_setting). -1 = unknown.
	DrawFrame  int `json:"draw_frame"`  // 1 = detection boxes are burnt into the picture
	AIPerson   int `json:"ai_person"`   // person detection sensitivity (app: 2 / 5 / 7)
	RecordMode int `json:"record_mode"` // SD recording: 4 = none, 2 = camera, 3 = camera or PIR

	// Firmware update offer reported by the camera itself (get_device_info).
	UpdateInfoKnown bool   `json:"update_info_known"`
	FirmwareRemote  string `json:"firmware_remote"`
	FirmwareUpdate  bool   `json:"firmware_update"`
	MCUVersion      string `json:"mcu_version"`
	MCURemote       string `json:"mcu_remote"`
	MCUUpdate       bool   `json:"mcu_update"`
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
			Highlight:      100,
			HighlightTime:  60,
			PIRSensitivity: 50,
			Resolution:     "1080p",
			LastSeen:       time.Now(),
			OSDPosition:    -1,
			SpeakerVolume:  -1,
			SirenEnabled:   -1,

			PixelChangeDetection: -1,
			DrawFrame:            -1,
			AIPerson:             -1,
			RecordMode:           -1,
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
	b.SetMotionWithSource(isMotion, "hardware")
}

func (b *Bus) SetMotionWithSource(isMotion bool, source string) {
	b.Publish(EventMotion, MotionEvent{
		IsMotion:  isMotion,
		Timestamp: time.Now(),
		Source:    source,
	})
}

func (b *Bus) UpdateStatus(status DeviceStatus) {
	b.mu.Lock()
	b.status = status
	b.mu.Unlock()
	b.Publish(EventDevice, status)
}

// UpdateStatusFunc atomically modifies the stored DeviceStatus under the bus lock
// and publishes the updated snapshot.
func (b *Bus) UpdateStatusFunc(fn func(*DeviceStatus)) {
	b.mu.Lock()
	fn(&b.status)
	b.status.LastSeen = time.Now()
	st := b.status
	listeners := make([]Listener, len(b.listeners))
	copy(listeners, b.listeners)
	b.mu.Unlock()

	for _, l := range listeners {
		l(EventDevice, st)
	}
}

func (b *Bus) GetStatus() DeviceStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.status
}

func (b *Bus) SubscribeMotion(fn func(isMotion bool)) {
	b.Subscribe(func(evt EventType, data interface{}) {
		if evt == EventMotion {
			if m, ok := data.(MotionEvent); ok {
				fn(m.IsMotion)
			}
		}
	})
}
