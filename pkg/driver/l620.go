package driver

import (
	"context"
	"fmt"
	"strings"

	"github.com/pion/rtp"

	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/Afrouper/steinel-cam-bridge/pkg/xiongmai"
)

var _ CameraDriver = (*L620Driver)(nil)

// L620Driver implements CameraDriver for Steinel L 620 CAM and XLED CAM 1 (Generation 1, Xiongmai Sofia architecture).
type L620Driver struct {
	cfg      *config.Config
	xmDriver *xiongmai.Driver
}

// NewL620Driver creates a new Driver instance for Steinel L 620 CAM.
func NewL620Driver(cfg *config.Config, rtspServer *rtsp.Server, eventBus *events.Bus) *L620Driver {
	xm := xiongmai.NewDriver(
		cfg.NabtoConfig.CameraIP,
		cfg.CameraUser,
		cfg.CameraPassword,
		cfg.Resolution,
		rtspServer,
		eventBus,
	)

	return &L620Driver{
		cfg:      cfg,
		xmDriver: xm,
	}
}

// Run starts the Xiongmai Sofia connection, RTSP ingest, state sync and keepalive loops.
func (d *L620Driver) Run(ctx context.Context) error {
	logger.Info("Bridge", "🚀 [ONLINE] Steinel L 620 CAM stream ready at rtsp://0.0.0.0:%d/%s", d.cfg.RTSPPort, d.cfg.RTSPPath)
	logger.Info("Bridge", "🛰️ [ONVIF] Endpoints active at http://0.0.0.0:%d/onvif/device_service", d.cfg.ONVIFPort)

	if err := d.xmDriver.Start(ctx); err != nil {
		logger.Warn("Xiongmai", "⚠️ Driver initialization warning: %v", err)
	}
	defer func() { _ = d.xmDriver.Close() }()

	<-ctx.Done()
	return nil
}

// Close releases the driver resources.
func (d *L620Driver) Close() error {
	return d.xmDriver.Close()
}

// SetResolution updates the video stream resolution (720p or 360p).
func (d *L620Driver) SetResolution(res string) error {
	return d.xmDriver.SetResolution(res)
}

// RequestKeyframe requests a keyframe (handled automatically by RTSP Ingest).
func (d *L620Driver) RequestKeyframe() {}

// SetLampState switches the primary lamp on or off.
func (d *L620Driver) SetLampState(mode string) error {
	modeLower := strings.ToLower(mode)
	on := modeLower == "on" || modeLower == "1" || modeLower == "dauerlicht"
	return d.xmDriver.SetLamp(on)
}

// SetHighlight sets the primary light dimming level (10-100%).
func (d *L620Driver) SetHighlight(percent int) error {
	return d.xmDriver.SetDim(percent)
}

// SetHighlightTime sets the primary light duration in seconds.
func (d *L620Driver) SetHighlightTime(seconds int) error {
	return d.xmDriver.SetDuration(seconds)
}

// SetLowlight sets the nightlight percentage (0-50%).
func (d *L620Driver) SetLowlight(percent int) error {
	return d.xmDriver.SetNightlight(percent)
}

// SetLowlightTime sets the nightlight duration.
func (d *L620Driver) SetLowlightTime(timeVal int) error {
	return d.xmDriver.SetNightlightDuration(fmt.Sprintf("%dh", timeVal/60))
}

// SetPIRSensitivity sets the motion detector sensitivity (mapped to 1-10m).
func (d *L620Driver) SetPIRSensitivity(percent int) error {
	dist := percent / 10
	if dist <= 0 {
		dist = 1
	}
	return d.xmDriver.SetDistance(dist)
}

// SetLuxThreshold sets the twilight threshold in lux (2-1000 lx).
func (d *L620Driver) SetLuxThreshold(lux int) error {
	return d.xmDriver.SetTwilight(lux)
}

// SetSiren is a no-op for L 620 CAM as the hardware has no built-in siren.
func (d *L620Driver) SetSiren(on bool) error {
	return nil
}

// GetRecordingProvider returns the storage manager for SD card queries.
func (d *L620Driver) GetRecordingProvider() storage.RecordingProvider {
	return d.xmDriver.GetSDCardManager()
}

// WriteAudioBackchannel forwards two-way audio RTP packets to the camera speaker.
func (d *L620Driver) WriteAudioBackchannel(pkt *rtp.Packet) error {
	d.xmDriver.OnAudioBackchannelPacket(pkt)
	return nil
}
