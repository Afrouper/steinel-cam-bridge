package webrtc

import (
	"context"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/h264x"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

const (
	resolutionGuardGrace    = 10 * time.Second
	resolutionGuardInterval = 5 * time.Second
)

// videoSizeSource is implemented by RTSP streams that know the picture size
// found in the camera's H.264 stream.
type videoSizeSource interface {
	VideoSize() (width int, height int, since time.Time)
}

// runResolutionGuard verifies that the camera really delivers the requested
// resolution. The camera acknowledges nothing and has been seen to fall back
// to another quality on its own, so the request is repeated when needed and
// the reported state always reflects the real picture size.
func (b *Bridge) runResolutionGuard(ctx context.Context) {
	src, ok := b.rtspServer.(videoSizeSource)
	if !ok {
		return
	}

	// Picture size seen before this session started may be stale: wait until
	// the new session has delivered its first keyframes.
	select {
	case <-ctx.Done():
		return
	case <-time.After(resolutionGuardGrace):
	}

	ticker := time.NewTicker(resolutionGuardInterval)
	defer ticker.Stop()

	for {
		width, height, _ := src.VideoSize()
		now := time.Now()

		b.mu.Lock()
		requested := b.resolution
		v := b.resGuard.Observe(width, height, now)
		if v.Retry {
			b.resGuard.Retried(now)
		}
		b.mu.Unlock()

		if v.Changed {
			logger.Info("Video", "📐 Camera delivers %dx%d (%s), requested: %s", width, height, v.Actual, requested)
			b.publishResolution()
		}
		switch {
		case v.Recovered:
			logger.Info("Video", "✅ Camera now delivers the requested resolution %s", requested)
		case v.Retry:
			logger.Warn("Video", "⚠️ Camera delivers %s although %s was requested. Requesting %s again (attempt %d)...", v.Actual, requested, requested, v.Attempt)
			if err := b.sendResolutionRequest(requested); err != nil {
				logger.Warn("Video", "⚠️ Repeating the resolution request failed: %v", err)
			}
		case v.GaveUp:
			logger.Warn("Video", "⚠️ Camera keeps delivering %s instead of %s. No further requests in this session.", v.Actual, requested)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reportedResolution returns the resolution to show to the user: the one the
// camera really delivers if known, otherwise the requested one.
func (b *Bridge) reportedResolution() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if actual := b.resGuard.Actual(); h264x.IsStandardLabel(actual) {
		return actual
	}
	return b.resolution
}

// publishResolution pushes the reported resolution to the event bus (MQTT / ONVIF state).
func (b *Bridge) publishResolution() {
	res := b.reportedResolution()
	status := b.eventBus.GetStatus()
	if status.Resolution == res {
		return
	}
	status.Resolution = res
	b.eventBus.UpdateStatus(status)
}
