package driver

import (
	"github.com/Afrouper/steinel-cam-bridge/pkg/config"
	"github.com/Afrouper/steinel-cam-bridge/pkg/events"
	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
)

// New instantiates the appropriate CameraDriver implementation based on the detected camera model.
func New(
	cfg *config.Config,
	isL620 bool,
	rtspServer *rtsp.Server,
	eventBus *events.Bus,
	onDeviceDiscovered func(deviceID, productID string),
) (CameraDriver, error) {
	if isL620 {
		return NewL620Driver(cfg, rtspServer, eventBus), nil
	}
	return NewL625Driver(cfg, rtspServer, onDeviceDiscovered), nil
}
