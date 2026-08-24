package xiongmai

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/mqtt"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/pion/rtp"
)

// Driver implements the Steinel Camera Driver for Generation 1 devices (Steinel L 620 CAM / XLED CAM 1).
type Driver struct {
	cameraIP   string
	user       string
	password   string
	rtspServer *rtsp.Server
	eventBus   *events.Bus
	client     *Client
	ingest     *RTSPIngest
	talk       *TalkClient
	debug      bool
	mu         sync.Mutex
	running    bool
}

// NewDriver creates a new driver instance for a Steinel L 620 CAM.
func NewDriver(cameraIP string, user, password string, rtspServer *rtsp.Server, eventBus *events.Bus, debug bool) *Driver {
	if user == "" {
		user = "admin"
	}
	if eventBus == nil {
		eventBus = events.GlobalBus
	}
	client := NewClient(cameraIP, DefaultPort, user, password, debug)
	talk := NewTalkClient(client, debug)
	ingest := NewRTSPIngest(cameraIP, RTSPPort, user, password, 0, rtspServer, debug)

	return &Driver{
		cameraIP:   cameraIP,
		user:       user,
		password:   password,
		rtspServer: rtspServer,
		eventBus:   eventBus,
		client:     client,
		talk:       talk,
		ingest:     ingest,
		debug:      debug,
	}
}

// Start connects to the camera, activates RTSP, queries state, and starts streaming and keepalive loops.
func (d *Driver) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return nil
	}

	log.Printf("[Xiongmai Driver] 🚀 Connecting to Steinel L 620 CAM at %s (TCP Port %d)...", d.cameraIP, DefaultPort)

	// Step 1: Connect & Login on Sofia port 34567
	if err := d.client.Connect(ctx); err != nil {
		return fmt.Errorf("xiongmai connection error: %w", err)
	}

	// Step 2: Zero-Touch RTSP Enablement
	if err := d.client.EnableRTSP(); err != nil {
		log.Printf("[Xiongmai Driver] ⚠️ Could not enable RTSP automatically: %v", err)
	}

	// Step 3: Query initial light and MCU states
	d.syncInitialState()

	// Step 4: Start RTSP Ingest with effective password discovered during login
	effectivePwd := d.client.GetEffectivePassword()
	d.ingest = NewRTSPIngest(d.cameraIP, RTSPPort, d.user, effectivePwd, 0, d.rtspServer, d.debug)
	if err := d.ingest.Start(ctx); err != nil {
		log.Printf("[Xiongmai Driver] ⚠️ Failed to start RTSP Ingest: %v", err)
	}

	// Step 5: Start KeepAlive loop
	go d.keepAliveLoop(ctx)

	d.running = true
	log.Printf("[Xiongmai Driver] ✅ Steinel L 620 CAM driver fully initialized and running")
	return nil
}

func (d *Driver) syncInitialState() {
	if d.eventBus == nil {
		return
	}

	st := d.eventBus.GetStatus()
	st.Resolution = "1080p"
	st.FirmwareVer = "Xiongmai-Sofia"

	// Query Light Switch
	if lightOn, err := d.client.QueryLightState(); err == nil {
		if lightOn {
			st.LampMode = 1
		} else {
			st.LampMode = 0
		}
	}

	// Query MCU Config
	if mcuCfg, err := d.client.QueryMCUConfig(); err == nil && mcuCfg != nil {
		st.Highlight = mcuCfg.Highlight
		st.HighlightTime = mcuCfg.HighlightDelaySec
		st.Lux = mcuCfg.TwilightLux
		st.PIRSensitivity = mcuCfg.Distance * 10
		st.Lowlight = mcuCfg.Lowlight
		if dur, err := strconv.Atoi(strings.TrimSuffix(mcuCfg.LowlightDuration, "h")); err == nil {
			st.LowlightTime = dur * 60
		}

		if d.debug {
			log.Printf("[Xiongmai Driver] 💡 Synced MCU Config: Light=%d%%, Lux=%d, Dist=%dm, Delay=%ds, Lowlight=%d%% (%s)",
				mcuCfg.Highlight, mcuCfg.TwilightLux, mcuCfg.Distance, mcuCfg.HighlightDelaySec, mcuCfg.Lowlight, mcuCfg.LowlightDuration)
		}
	}

	d.eventBus.UpdateStatus(st)
}

func (d *Driver) keepAliveLoop(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.client.SendKeepAlive(); err != nil {
				if d.debug {
					log.Printf("[Xiongmai Driver] ⚠️ Heartbeat failed: %v", err)
				}
			}
		}
	}
}

// OnAudioBackchannelPacket forwards incoming audio from the RTSP server to the camera speaker.
func (d *Driver) OnAudioBackchannelPacket(pkt *rtp.Packet) {
	if d.talk != nil {
		_ = d.talk.SendAudioPacket(pkt)
	}
}

// GetMQTTCallbacks returns the Callbacks struct configured for this driver.
func (d *Driver) GetMQTTCallbacks() mqtt.Callbacks {
	return mqtt.Callbacks{
		SetLampMode: func(mode string) error {
			modeLower := strings.ToLower(mode)
			on := modeLower == "on" || modeLower == "1" || modeLower == "dauerlicht"
			return d.SetLamp(on)
		},
		SetHighlight: func(percent int) error {
			return d.SetDim(percent)
		},
		SetHighlightTime: func(seconds int) error {
			return d.SetDuration(seconds)
		},
		SetLowlight: func(percent int) error {
			return d.SetNightlight(percent)
		},
		SetLowlightTime: func(timeVal int) error {
			return d.SetNightlightDuration(fmt.Sprintf("%dh", timeVal/60))
		},
		SetPIRSensitivity: func(percent int) error {
			dist := percent / 10
			if dist <= 0 {
				dist = 1
			}
			return d.SetDistance(dist)
		},
		SetLuxThreshold: func(lux int) error {
			return d.SetTwilight(lux)
		},
	}
}

// SetLamp turns the main light on or off.
func (d *Driver) SetLamp(on bool) error {
	err := d.client.SetLightState(on)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		if on {
			st.LampMode = 1
		} else {
			st.LampMode = 0
		}
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetDim sets the main light dimming level (10-100%).
func (d *Driver) SetDim(val int) error {
	err := d.client.SetHighlight(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.Highlight = val
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetTwilight sets the twilight sensor threshold (2-1000 Lux).
func (d *Driver) SetTwilight(val int) error {
	err := d.client.SetLux(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.Lux = val
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetDistance sets the PIR detection distance (1-10 meters).
func (d *Driver) SetDistance(val int) error {
	err := d.client.SetDistance(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.PIRSensitivity = val * 10
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetDuration sets the main light on-time in seconds.
func (d *Driver) SetDuration(val int) error {
	err := d.client.SetHighlightDelay(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.HighlightTime = val
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetNightlight sets the nightlight brightness (0-50%).
func (d *Driver) SetNightlight(val int) error {
	err := d.client.SetLowlight(val)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		st.Lowlight = val
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// SetNightlightDuration sets the nightlight duration mode ("all_night", "4h", "off").
func (d *Driver) SetNightlightDuration(val string) error {
	var durInt int
	valLower := strings.ToLower(strings.TrimSpace(val))
	switch {
	case valLower == "all_night" || valLower == "ganze nacht":
		durInt = 20
	case valLower == "off" || valLower == "aus":
		durInt = 0
	case strings.HasSuffix(valLower, "h"):
		hStr := strings.TrimSuffix(valLower, "h")
		if hours, err := strconv.Atoi(hStr); err == nil {
			durInt = hours * 2
		}
	}

	err := d.client.SetLowlightDuration(durInt)
	if err == nil && d.eventBus != nil {
		st := d.eventBus.GetStatus()
		if dur, err := strconv.Atoi(strings.TrimSuffix(valLower, "h")); err == nil {
			st.LowlightTime = dur * 60
		}
		d.eventBus.UpdateStatus(st)
	}
	return err
}

// Close shuts down all driver connections and streams.
func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.running {
		return nil
	}
	d.running = false

	if d.talk != nil {
		_ = d.talk.StopTalk()
	}
	if d.ingest != nil {
		_ = d.ingest.Close()
	}
	if d.client != nil {
		_ = d.client.Close()
	}
	return nil
}
