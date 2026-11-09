package webrtc

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

func TestAudioBackchannelChunking(t *testing.T) {
	track, err := pion.NewTrackLocalStaticRTP(
		pion.RTPCodecCapability{MimeType: pion.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		"audioLabel",
		"audioStream",
	)
	if err != nil {
		t.Fatalf("failed to create track: %v", err)
	}

	b := &Bridge{
		audioSendTrack: track,
	}

	// 1. Send small chunk (< 160 bytes)
	smallPkt := &rtp.Packet{
		Header:  rtp.Header{PayloadType: 0},
		Payload: make([]byte, 100),
	}
	if err := b.WriteAudioBackchannel(smallPkt); err != nil {
		t.Fatalf("unexpected error on small chunk: %v", err)
	}
	if len(b.backchannelBuf) != 100 {
		t.Fatalf("expected buffer len 100, got %d", len(b.backchannelBuf))
	}

	// 2. Send 944 bytes (e.g. Scrypted jumbo packet) -> total 1044 bytes
	// 1044 / 160 = 6 full frames (960 bytes), 84 bytes remaining
	jumboPkt := &rtp.Packet{
		Header:  rtp.Header{PayloadType: 0},
		Payload: make([]byte, 944),
	}
	if err := b.WriteAudioBackchannel(jumboPkt); err != nil {
		t.Fatalf("unexpected error on jumbo chunk: %v", err)
	}

	if b.backchannelSeq != 6 {
		t.Fatalf("expected 6 frames generated, got seq %d", b.backchannelSeq)
	}
	if b.backchannelTs != 6*160 {
		t.Fatalf("expected timestamp %d, got %d", 6*160, b.backchannelTs)
	}
	if len(b.backchannelBuf) != 84 {
		t.Fatalf("expected buffer remaining 84, got %d", len(b.backchannelBuf))
	}
}

func TestWatchdogConstants(t *testing.T) {
	if watchdogGracePeriod != 15*time.Second {
		t.Errorf("expected watchdogGracePeriod to be 15s, got %v", watchdogGracePeriod)
	}
	if watchdogSilenceThreshold != 15*time.Second {
		t.Errorf("expected watchdogSilenceThreshold to be 15s, got %v", watchdogSilenceThreshold)
	}
}

func TestICEStateManager_Recovery(t *testing.T) {
	var cancelled atomic.Bool
	cancelFunc := func() {
		cancelled.Store(true)
	}

	mgr := newICEStateManager(50*time.Millisecond, cancelFunc)
	defer mgr.Cancel()

	// 1. Transition to Disconnected
	mgr.OnStateChange(pion.ICEConnectionStateDisconnected)

	// 2. Recover to Connected before grace period expires
	time.Sleep(10 * time.Millisecond)
	mgr.OnStateChange(pion.ICEConnectionStateConnected)

	// Wait past initial grace period
	time.Sleep(60 * time.Millisecond)

	if cancelled.Load() {
		t.Fatal("expected cancelFunc NOT to be called after recovery, but it was called")
	}
}

func TestICEStateManager_Timeout(t *testing.T) {
	var cancelled atomic.Bool
	cancelFunc := func() {
		cancelled.Store(true)
	}

	mgr := newICEStateManager(30*time.Millisecond, cancelFunc)
	defer mgr.Cancel()

	mgr.OnStateChange(pion.ICEConnectionStateDisconnected)

	// Wait for grace period to expire
	time.Sleep(60 * time.Millisecond)

	if !cancelled.Load() {
		t.Fatal("expected cancelFunc to be called after grace period expired, but was not")
	}
}

func TestICEStateManager_ImmediateFail(t *testing.T) {
	var cancelled atomic.Bool
	cancelFunc := func() {
		cancelled.Store(true)
	}

	mgr := newICEStateManager(1*time.Second, cancelFunc)
	defer mgr.Cancel()

	mgr.OnStateChange(pion.ICEConnectionStateFailed)

	if !cancelled.Load() {
		t.Fatal("expected cancelFunc to be called immediately on Failed state")
	}
}

func TestResetReasonString(t *testing.T) {
	if ResetReasonVideoStalled.String() != "video_stalled" {
		t.Errorf("expected 'video_stalled', got %s", ResetReasonVideoStalled.String())
	}
	if ResetReasonControlPlaneRecovery.String() != "control_plane_recovery" {
		t.Errorf("expected 'control_plane_recovery', got %s", ResetReasonControlPlaneRecovery.String())
	}
	if ResetReasonManual.String() != "manual_reconnect" {
		t.Errorf("expected 'manual_reconnect', got %s", ResetReasonManual.String())
	}
	if ResetReasonNone.String() != "unknown" {
		t.Errorf("expected 'unknown', got %s", ResetReasonNone.String())
	}
}

func TestBridgeManualReset(t *testing.T) {
	b := &Bridge{}
	if b.ResetReason() != ResetReasonNone {
		t.Fatalf("expected initial ResetReasonNone, got %v", b.ResetReason())
	}

	cancelled := false
	b.sessCancel = func() {
		cancelled = true
	}

	b.TriggerManualReset()

	if !cancelled {
		t.Fatal("expected sessCancel to be called on TriggerManualReset")
	}
	if b.ResetReason() != ResetReasonManual {
		t.Fatalf("expected ResetReasonManual, got %v", b.ResetReason())
	}
}

func TestBridgeControlPlaneEscalationHandler(t *testing.T) {
	b := &Bridge{
		recoveryTimeout: 10 * time.Minute,
	}
	sdm := NewSDCardManager(func(_ string, _ map[string]interface{}) error { return nil })
	b.sdcardManager = sdm

	var cancelled bool
	sessCancel := func() { cancelled = true }

	handler := func(stallDuration time.Duration, timeouts int) bool {
		lastNano := b.lastVideoPacket.Load()
		videoHealthy := lastNano > 0 && time.Since(time.Unix(0, lastNano)) < 5*time.Second

		if !videoHealthy {
			b.resetReason.Store(uint32(ResetReasonVideoStalled))
			sessCancel()
			return true
		}

		if !b.sdcardManager.HasEverSucceeded() {
			return false
		}

		b.mu.Lock()
		recTimeout := b.recoveryTimeout
		b.mu.Unlock()

		if recTimeout > 0 && stallDuration >= recTimeout {
			b.resetReason.Store(uint32(ResetReasonControlPlaneRecovery))
			sessCancel()
			return true
		}

		return false
	}

	// 1. Video stalled -> immediate reset
	cancelled = false
	b.lastVideoPacket.Store(0)
	res := handler(1*time.Minute, 3)
	if !res || !cancelled || b.ResetReason() != ResetReasonVideoStalled {
		t.Fatalf("expected video stalled reset, got res=%v, cancelled=%v, reason=%v", res, cancelled, b.ResetReason())
	}

	// 2. Video healthy, but HasEverSucceeded is false (no SD card) -> preserve session, NO reset
	cancelled = false
	b.resetReason.Store(uint32(ResetReasonNone))
	b.lastVideoPacket.Store(time.Now().UnixNano())
	res = handler(15*time.Minute, 5)
	if res || cancelled || b.ResetReason() != ResetReasonNone {
		t.Fatalf("expected preserved session when HasEverSucceeded is false, got res=%v, cancelled=%v", res, cancelled)
	}

	// 3. Video healthy, HasEverSucceeded is true, but stallDuration < recoveryTimeout -> preserve session
	sdm.markSuccess()
	cancelled = false
	res = handler(5*time.Minute, 5)
	if res || cancelled || b.ResetReason() != ResetReasonNone {
		t.Fatalf("expected preserved session when stallDuration < recoveryTimeout, got res=%v, cancelled=%v", res, cancelled)
	}

	// 4. Video healthy, HasEverSucceeded is true, stallDuration >= recoveryTimeout -> soft recovery reset
	cancelled = false
	res = handler(11*time.Minute, 12)
	if !res || !cancelled || b.ResetReason() != ResetReasonControlPlaneRecovery {
		t.Fatalf("expected control plane recovery reset, got res=%v, cancelled=%v, reason=%v", res, cancelled, b.ResetReason())
	}
}
