package rtsp

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testH265VPS = []byte{
		0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00,
		0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xac, 0x09,
	}
	testH265SPS1080 = []byte{
		0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03,
		0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03,
		0x00, 0x78, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5,
		0x96, 0x66, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00,
		0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01,
		0xe0, 0x80,
	}
	testH265PPS = []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}
)

type fakeH265Camera struct {
	seq  uint16
	ts   uint32
	ssrc uint32
}

func newFakeH265Camera(seq uint16, ts, ssrc uint32) *fakeH265Camera {
	return &fakeH265Camera{seq: seq, ts: ts, ssrc: ssrc}
}

func (c *fakeH265Camera) pkt(payload []byte, marker bool) *rtp.Packet {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: c.seq,
			Timestamp:      c.ts,
			SSRC:           c.ssrc,
			Marker:         marker,
		},
		Payload: payload,
	}
	c.seq++
	return p
}

func (c *fakeH265Camera) keyframe() []*rtp.Packet {
	out := []*rtp.Packet{
		c.pkt(testH265VPS, false),
		c.pkt(testH265SPS1080, false),
		c.pkt(testH265PPS, false),
		c.pkt([]byte{0x62, 0x01, 0x80 | 19, 0x01, 0x02, 0x03}, false), // FU start, IDR_W_RADL
		c.pkt([]byte{0x62, 0x01, 0x40 | 19, 0x04, 0x05, 0x06}, true),  // FU end, IDR_W_RADL
	}
	c.ts += 15000 // 6 FPS @ 90 kHz
	return out
}

func (c *fakeH265Camera) pframe() []*rtp.Packet {
	out := []*rtp.Packet{c.pkt([]byte{0x02, 0x01, 0xd0, 0x10, 0x20}, true)} // TRAIL_R
	c.ts += 15000
	return out
}

func TestVideoPipeH265ContinuityAndInstantStartEndToEnd(t *testing.T) {
	srv, err := NewServer(8584, "cam265", "none", "", "")
	require.NoError(t, err)
	require.NoError(t, srv.Start())
	defer srv.Close()

	st := srv.PrimaryStream()
	require.True(t, st.SetVideoCodec(VideoCodecH265))

	var onPlayCalls atomic.Int32
	st.SetOnPlayHandler(func() {
		onPlayCalls.Add(1)
	})

	cam1 := newFakeH265Camera(50000, 900000, 0x1111)
	// P-frame before first keyframe must be dropped by the keyframe gate.
	for _, p := range cam1.pframe() {
		st.WriteVideoPacket(p)
	}
	assert.Zero(t, st.VideoStats().CachedPkts)

	// Feed H.265 keyframe + 4 P-frames (5 + 4 = 9 RTP packets).
	for _, p := range cam1.keyframe() {
		st.WriteVideoPacket(p)
	}
	for range 4 {
		for _, p := range cam1.pframe() {
			st.WriteVideoPacket(p)
		}
	}

	w, h, _ := st.VideoSize()
	assert.Equal(t, 1920, w)
	assert.Equal(t, 1080, h)

	// Late joiner connects over RTSP TCP: must immediately receive the cached H.265 GOP
	// without waiting for the camera's next 10s keyframe and without triggering AwaitNextKeyframe.
	u, err := base.ParseURL("rtsp://127.0.0.1:8584/cam265")
	require.NoError(t, err)
	tcp := gortsplib.TransportTCP
	c := &gortsplib.Client{Transport: &tcp}
	require.NoError(t, c.Start(u.Scheme, u.Host))
	defer c.Close()

	desc, _, err := c.Describe(u)
	require.NoError(t, err)
	var h265Fmt *format.H265
	medi := desc.FindFormat(&h265Fmt)
	require.NotNil(t, medi)
	gotVPS, gotSPS, gotPPS := h265Fmt.SafeParams()
	assert.Equal(t, testH265VPS, gotVPS)
	assert.Equal(t, testH265SPS1080, gotSPS)
	assert.Equal(t, testH265PPS, gotPPS)

	_, err = c.Setup(desc.BaseURL, medi, 0, 0)
	require.NoError(t, err)
	r := &testReader{}
	c.OnPacketRTP(medi, h265Fmt, func(pkt *rtp.Packet) { r.add(pkt) })
	_, err = c.Play(nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(r.snapshot()) >= 9 }, 2*time.Second, 10*time.Millisecond)
	assert.Zero(t, onPlayCalls.Load(), "instant-start GOP delivery must not trigger RequestKeyframe/AwaitNextKeyframe")

	// Simulate a camera session restart with completely new SSRC, sequence numbers, and timestamps:
	// outgoing RTP sequence numbers and timestamps must remain continuous!
	time.Sleep(20 * time.Millisecond)
	cam2 := newFakeH265Camera(3, 100, 0x9999)
	for _, p := range cam2.pframe() {
		st.WriteVideoPacket(p) // dropped until cam2's first keyframe
	}
	for _, p := range cam2.keyframe() {
		st.WriteVideoPacket(p)
	}
	for _, p := range cam2.pframe() {
		st.WriteVideoPacket(p)
	}

	require.Eventually(t, func() bool { return len(r.snapshot()) >= 15 }, 2*time.Second, 10*time.Millisecond)
	got := r.snapshot()
	for i := 1; i < len(got); i++ {
		assert.Equal(t, got[i-1].SequenceNumber+1, got[i].SequenceNumber, "continuous sequence at packet %d", i)
		assert.GreaterOrEqual(t, int32(got[i].Timestamp-got[i-1].Timestamp), int32(0), "monotonic timestamp at packet %d", i)
		assert.Equal(t, got[0].SSRC, got[i].SSRC, "constant SSRC across H.265 camera restarts")
	}
	assert.Equal(t, uint64(1), st.VideoStats().Restarts)
}

func TestVideoPipeH265_CodecSwitchDeliversInitialKeyframeWhenInstantStartDisabled(t *testing.T) {
	srv, err := NewServer(8586, "cam265_default", "none", "", "")
	require.NoError(t, err)
	require.NoError(t, srv.Start())
	defer srv.Close()

	st := srv.PrimaryStream()
	// Production default: INSTANT_START=false.
	st.SetVideoOptions(false, 0)

	var onPlayCalls atomic.Int32
	st.SetOnPlayHandler(func() {
		onPlayCalls.Add(1)
	})

	// Simulate SofiaStream detecting H.265 on the first I-frame: SetVideoCodec("h265") runs
	// and immediately writes that first H.265 keyframe before the disconnected client reconnects.
	st.SetH265Parameters(testH265VPS, testH265SPS1080, testH265PPS)
	require.True(t, st.SetVideoCodec(VideoCodecH265))

	cam := newFakeH265Camera(100, 90000, 0x2222)
	for _, p := range cam.keyframe() {
		st.WriteVideoPacket(p)
	}

	// Client reconnects right after the forced codec switch: must receive the cached initial
	// keyframe immediately instead of waiting a full GOP (up to 10s on L 620) for the second I-frame.
	u, err := base.ParseURL("rtsp://127.0.0.1:8586/cam265_default")
	require.NoError(t, err)
	tcp := gortsplib.TransportTCP
	c1 := &gortsplib.Client{Transport: &tcp}
	require.NoError(t, c1.Start(u.Scheme, u.Host))
	defer c1.Close()

	desc, _, err := c1.Describe(u)
	require.NoError(t, err)
	var h265Fmt *format.H265
	medi := desc.FindFormat(&h265Fmt)
	require.NotNil(t, medi)

	_, err = c1.Setup(desc.BaseURL, medi, 0, 0)
	require.NoError(t, err)
	r1 := &testReader{}
	c1.OnPacketRTP(medi, h265Fmt, func(pkt *rtp.Packet) { r1.add(pkt) })
	_, err = c1.Play(nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(r1.snapshot()) >= 5 }, time.Second, 10*time.Millisecond)
	assert.Zero(t, onPlayCalls.Load(), "post-codec-switch reconnect must receive cached keyframe without calling RequestKeyframe")

	// Subsequent second viewer with INSTANT_START=false must NOT receive joinBurst once the one-shot grace token was consumed.
	beforeCalls := onPlayCalls.Load()

	// Subsequent second viewer with INSTANT_START=false must NOT receive joinBurst once the one-shot grace token was consumed.
	for _, p := range cam.pframe() {
		st.WriteVideoPacket(p)
	}
	c2 := &gortsplib.Client{Transport: &tcp}
	require.NoError(t, c2.Start(u.Scheme, u.Host))
	defer c2.Close()
	desc2, _, err := c2.Describe(u)
	require.NoError(t, err)
	var h265Fmt2 *format.H265
	medi2 := desc2.FindFormat(&h265Fmt2)
	require.NotNil(t, medi2)
	_, err = c2.Setup(desc2.BaseURL, medi2, 0, 0)
	require.NoError(t, err)
	_, err = c2.Play(nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return onPlayCalls.Load() == beforeCalls+1 }, time.Second, 10*time.Millisecond)

	// Re-sending PLAY on the same active ServerSession (e.g. VLC range/keepalive PLAY) must not trigger onPlayHandler again.
	dupSess := &gortsplib.ServerSession{}
	st.handlePlay("cam265_default", dupSess)
	require.Eventually(t, func() bool { return onPlayCalls.Load() == beforeCalls+2 }, time.Second, 10*time.Millisecond)
	st.handlePlay("cam265_default", dupSess)
	time.Sleep(40 * time.Millisecond)
	assert.Equal(t, beforeCalls+2, onPlayCalls.Load(), "duplicate PLAY on an active session must not re-trigger onPlayHandler")
}

func TestVideoPipe_LosslessTCPPauseDoesNotDropSubsequentPFrames(t *testing.T) {
	p := newVideoPipe()
	now := time.Now()
	p.setCodec(VideoCodecH265, now)
	cam := newFakeH265Camera(10, 90000, 0x7777)

	require.Len(t, feed(p, cam.keyframe(), now), 5)

	// Simulate a 2.5s VBV / Wi-Fi buffer pause after a large 1080p H.265 keyframe on a 460 kbps CBR encoder:
	// SSRC is unchanged, SequenceNumber is strictly contiguous (lastSrcSeq + 1), and RTP timestamp delta is valid.
	now = now.Add(2500 * time.Millisecond)
	cam.ts += uint32(2500 * 90)
	out := feed(p, cam.pframe(), now)
	require.Len(t, out, 1, "contiguous same-session TCP P-frame after 2.5s VBV pause must not be gated/dropped")
	assert.Zero(t, p.restarts, "lossless VBV/Wi-Fi pause on same SSRC and sequence must not count as a session restart")
}

func TestVideoPipe_ConditionalGOPCaching(t *testing.T) {
	now := time.Now()

	// 1. Default daemon config on L 620 (instantStart=false, holdMax=0, outside codecSwitchGrace):
	// zero packets retained in GOP cache.
	pNone := newVideoPipe()
	pNone.instantStart = false
	pNone.holdMax = 0
	cam1 := newFakeH265Camera(1, 90000, 0xAAAA)
	pNone.setCodec(VideoCodecH265, now.Add(-10*time.Second))
	require.Len(t, feed(pNone, cam1.keyframe(), now), 5)
	for range 20 {
		require.Len(t, feed(pNone, cam1.pframe(), now), 1)
	}
	assert.Empty(t, pNone.gop, "GOP cache must remain empty when instantStart and holdLastFrame are both disabled")
	assert.Zero(t, pNone.gopBytes)

	// 2. Default daemon config on L 625 (instantStart=false, holdMax=30s):
	// retains ONLY the keyframe unit (5 packets) and skips trailing P-frames, while holdFrame still works.
	pHold := newVideoPipe()
	pHold.instantStart = false
	pHold.holdMax = 30 * time.Second
	cam2 := newFakeH265Camera(1, 90000, 0xBBBB)
	pHold.setCodec(VideoCodecH265, now.Add(-10*time.Second))
	require.Len(t, feed(pHold, cam2.keyframe(), now), 5)
	for range 25 {
		require.Len(t, feed(pHold, cam2.pframe(), now), 1)
	}
	assert.Len(t, pHold.gop, 5, "hold-only mode must retain only the keyframe unit without buffering trailing P-frames")
	holdPkts := pHold.holdFrame(now.Add(1500 * time.Millisecond))
	assert.Len(t, holdPkts, 5, "holdFrame must still replay the complete keyframe unit")
}

func BenchmarkVideoPipeIngest_H265PFrame_NoCache(b *testing.B) {
	p := newVideoPipe()
	now := time.Now()
	p.setCodec(VideoCodecH265, now.Add(-10*time.Second))
	p.instantStart = false
	p.holdMax = 0
	cam := newFakeH265Camera(1, 90000, 0x9999)
	feed(p, cam.keyframe(), now)

	pkt := cam.pframe()[0]
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		pkt.SequenceNumber++
		pkt.Timestamp += 6000
		_ = p.ingest(pkt, now)
	}
}

// TestStream_CodecRoundTripH264ToH265AndBackToH264 verifies that H.264 remains a first-class
// citizen before AND after switching to H.265 and back to H.264 (SPS/PPS SDP parameter extraction,
// VideoSize resolution tracking, InstantStart GOP joinBurst, and HoldLastFrame keyframe replay).
func TestStream_CodecRoundTripH264ToH265AndBackToH264(t *testing.T) {
	srv, err := NewServer(18586, "roundtrip", "aac", "", "")
	require.NoError(t, err)
	require.NoError(t, srv.Start())
	defer srv.Close()

	st := srv.PrimaryStream()
	st.video.mu.Lock()
	st.video.instantStart = true
	st.video.holdMax = 10 * time.Second
	st.video.mu.Unlock()

	// Phase 1: Native H.264 (720p) — default on boot (L 625 WebRTC & L 620 H.264 mode).
	assert.Equal(t, VideoCodecH264, st.VideoCodec())
	cam264a := newFakeCamera(t, testSPS720, 10, 9000)
	for _, p := range cam264a.keyframe() {
		st.WriteVideoPacket(p)
	}
	for _, p := range cam264a.pframe() {
		st.WriteVideoPacket(p)
	}
	w, h, _ := st.VideoSize()
	assert.Equal(t, 1280, w)
	assert.Equal(t, 720, h)

	// Phase 2: Switch to H.265 (1080p).
	require.True(t, st.SetVideoCodec(VideoCodecH265))
	assert.Equal(t, VideoCodecH265, st.VideoCodec())
	w, h, _ = st.VideoSize()
	assert.Zero(t, w, "switching codec must clear stale picture width until new SPS arrives")
	assert.Zero(t, h, "switching codec must clear stale picture height until new SPS arrives")

	cam265 := newFakeH265Camera(500, 90000, 0x2650)
	for _, p := range cam265.keyframe() {
		st.WriteVideoPacket(p)
	}
	w, h, _ = st.VideoSize()
	assert.Equal(t, 1920, w)
	assert.Equal(t, 1080, h)

	// Phase 3: Switch BACK to H.264 (1080p) and verify an RTSP client (Scrypted / HomeKit / VLC)
	// receives H.264 SDP sprop-parameter-sets AND an immediate InstantStart H.264 GOP burst.
	require.True(t, st.SetVideoCodec(VideoCodecH264))
	assert.Equal(t, VideoCodecH264, st.VideoCodec())

	cam264b := newFakeCamera(t, testSPS1080, 900, 180000)
	for _, p := range cam264b.keyframe() {
		st.WriteVideoPacket(p)
	}
	for _, p := range cam264b.pframe() {
		st.WriteVideoPacket(p)
	}
	w, h, _ = st.VideoSize()
	assert.Equal(t, 1920, w)
	assert.Equal(t, 1080, h)

	u, err := base.ParseURL("rtsp://127.0.0.1:18586/roundtrip")
	require.NoError(t, err)
	tcp := gortsplib.TransportTCP
	c := &gortsplib.Client{Transport: &tcp}
	require.NoError(t, c.Start(u.Scheme, u.Host))
	defer c.Close()

	desc, _, err := c.Describe(u)
	require.NoError(t, err)
	var h264Fmt *format.H264
	medi := desc.FindFormat(&h264Fmt)
	require.NotNil(t, medi, "stream must advertise H.264 format in SDP after switching back to H.264")
	sps, pps := h264Fmt.SafeParams()
	assert.NotEmpty(t, sps, "H.264 SPS must be advertised in SDP sprop-parameter-sets")
	assert.NotEmpty(t, pps, "H.264 PPS must be advertised in SDP sprop-parameter-sets")

	_, err = c.Setup(desc.BaseURL, medi, 0, 0)
	require.NoError(t, err)
	reader := &testReader{}
	c.OnPacketRTP(medi, h264Fmt, func(pkt *rtp.Packet) { reader.add(pkt) })
	_, err = c.Play(nil)
	require.NoError(t, err)

	// 4 keyframe packets + 1 P-frame = 5 packets delivered immediately via joinBurst!
	require.Eventually(t, func() bool { return len(reader.snapshot()) >= 5 }, time.Second, 10*time.Millisecond)
}
