package driver

import (
	"context"
	"time"

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
func NewL620Driver(cfg *config.Config, rtspServer rtsp.MediaStream, eventBus *events.Bus) *L620Driver {
	xm := xiongmai.NewDriver(
		cfg.NabtoConfig.CameraIP,
		cfg.CameraUser,
		cfg.CameraPassword,
		cfg.Resolution,
		rtspServer,
		eventBus,
	)
	if cfg.ForceRTSPIngest {
		xm.SetForceRTSPIngest(true)
	}
	if cfg.SDCardDownloadRateLimitKBps >= 0 {
		xm.SetSDCardRateLimit(cfg.SDCardDownloadRateLimitKBps)
	}

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

// ReconnectSession triggers an immediate reconnect of the camera session.
func (d *L620Driver) ReconnectSession() error {
	return d.xmDriver.ReconnectSession()
}

// SetResolution updates the video stream resolution (720p or 360p).
func (d *L620Driver) SetResolution(res string) error {
	return d.xmDriver.SetResolution(res)
}

// RequestKeyframe requests an immediate keyframe or triggers on-demand stream startup.
func (d *L620Driver) RequestKeyframe() {
	d.xmDriver.RequestKeyframe()
}

// SetLampState switches the primary lamp mode ("on"/"dauerlicht", "off"/"auto"/"sensor", or "aus").
func (d *L620Driver) SetLampState(mode string) error {
	return d.xmDriver.SetLampMode(mode)
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

// SetLowlightTime sets the nightlight duration (hours 0-12, or legacy minutes/seconds).
func (d *L620Driver) SetLowlightTime(timeVal int) error {
	return d.xmDriver.SetLowlightTime(timeVal)
}

// SetPIRSensitivity sets the motion detector sensitivity (mapped to 3m, 6m, 10m).
func (d *L620Driver) SetPIRSensitivity(percent int) error {
	var dist int
	if percent >= 67 {
		dist = 10 // Maximum ('u')
	} else if percent >= 34 {
		dist = 6 // Medium ('p')
	} else {
		dist = 3 // Minimum ('k')
	}
	return d.xmDriver.SetDistance(dist)
}

// SetLuxThreshold sets the twilight threshold in lux (2-1000 lx).
func (d *L620Driver) SetLuxThreshold(lux int) error {
	return d.xmDriver.SetTwilight(lux)
}

// SetSiren enables or disables the camera's motion alarm voice/buzzer output.
func (d *L620Driver) SetSiren(on bool) error {
	return d.xmDriver.SetSiren(on)
}

// SetSpeakerVolume sets the volume of the camera's speaker (0-100).
func (d *L620Driver) SetSpeakerVolume(percent int) error {
	return d.xmDriver.SetSpeakerVolume(percent)
}

// SetTimestampPosition switches the timestamp overlay off (0) or moves it to a corner (1-4).
func (d *L620Driver) SetTimestampPosition(position int) error {
	return d.xmDriver.SetTimestampPosition(position)
}

// SetDetectMode enables ("ai") or disables ("off") camera-side motion detection.
func (d *L620Driver) SetDetectMode(mode string) error {
	return d.xmDriver.SetDetectMode(mode)
}

// SetRecordMode chooses what makes the camera record to its SD card (2, 3 or 4).
func (d *L620Driver) SetRecordMode(mode int) error {
	return d.xmDriver.SetRecordMode(mode)
}

// Reboot triggers a controlled camera reboot.
func (d *L620Driver) Reboot() error {
	return d.xmDriver.Reboot()
}

// SetSystemDateAndTime synchronizes the system clock on the Xiongmai L 620 camera.
func (d *L620Driver) SetSystemDateAndTime(t time.Time, tz string) error {
	return d.xmDriver.SetTime(t)
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
