package rtsp

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSPS720  = "6742c01fd9005005bb0110000003001000000301e0f1832480"
	testSPS1080 = "6742c028d900780227e5c044000003000400000300783c60c920"
	testPPS     = "68cb8cb2"
)

// fakeCamera produces a simple H.264 RTP stream: every gop-th frame is a
// keyframe (SPS, PPS, IDR in two FU-A fragments), all others are one P slice.
type fakeCamera struct {
	seq  uint16
	ts   uint32
	ssrc uint32
	sps  []byte
	pps  []byte
}

func newFakeCamera(t *testing.T, spsHex string, seq uint16, ts uint32) *fakeCamera {
	t.Helper()
	sps, err := hex.DecodeString(spsHex)
	require.NoError(t, err)
	pps, err := hex.DecodeString(testPPS)
	require.NoError(t, err)
	return &fakeCamera{seq: seq, ts: ts, ssrc: 1, sps: sps, pps: pps}
}

func (c *fakeCamera) pkt(payload []byte, marker bool) *rtp.Packet {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    102,
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

func (c *fakeCamera) keyframe() []*rtp.Packet {
	out := []*rtp.Packet{
		c.pkt(c.sps, false),
		c.pkt(c.pps, false),
		c.pkt([]byte{0x7C, 0x85, 0x01, 0x02, 0x03}, false), // FU-A start, IDR
		c.pkt([]byte{0x7C, 0x45, 0x04, 0x05, 0x06}, true),  // FU-A end, IDR
	}
	c.ts += 6000
	return out
}

func (c *fakeCamera) pframe() []*rtp.Packet {
	out := []*rtp.Packet{c.pkt([]byte{0x41, 0x9a, 0x10, 0x20}, true)}
	c.ts += 6000
	return out
}

func feed(p *videoPipe, pkts []*rtp.Packet, now time.Time) []*rtp.Packet {
	var out []*rtp.Packet
	for _, pkt := range pkts {
		if o := p.ingest(pkt, now); o != nil {
			out = append(out, o)
		}
	}
	return out
}

func TestVideoPipeGatesUntilFirstKeyframe(t *testing.T) {
	p := newVideoPipe()
	cam := newFakeCamera(t, testSPS720, 100, 9000)
	now := time.Now()

	assert.Empty(t, feed(p, cam.pframe(), now), "P frames before the first keyframe must be dropped")
	assert.Empty(t, feed(p, cam.pframe(), now.Add(66*time.Millisecond)))

	out := feed(p, cam.keyframe(), now.Add(132*time.Millisecond))
	require.Len(t, out, 4)
	assert.Equal(t, uint32(videoOutSSRC), out[0].SSRC)
	assert.Equal(t, uint8(96), out[0].PayloadType)

	assert.Equal(t, 1280, p.width)
	assert.Equal(t, 720, p.height)
	sps, pps, ok := p.takeParams()
	assert.True(t, ok)
	assert.NotEmpty(t, sps)
	assert.NotEmpty(t, pps)
	_, _, ok = p.takeParams()
	assert.False(t, ok, "parameter sets are reported once per change")
}

func TestVideoPipeGateTimeoutPassesStream(t *testing.T) {
	p := newVideoPipe()
	cam := newFakeCamera(t, testSPS720, 0, 0)
	now := time.Now()

	assert.Empty(t, feed(p, cam.pframe(), now))
	assert.Empty(t, feed(p, cam.pframe(), now.Add(keyframeGateTimeout-time.Second)))
	out := feed(p, cam.pframe(), now.Add(keyframeGateTimeout+time.Second/2))
	assert.Len(t, out, 1, "the gate must open after its timeout even without a keyframe")
	assert.Nil(t, p.joinBurst(now.Add(keyframeGateTimeout+time.Second/2)), "nothing decodable is cached without a keyframe")
}

func TestVideoPipeJoinBurstHoldsCurrentGOP(t *testing.T) {
	p := newVideoPipe()
	cam := newFakeCamera(t, testSPS720, 500, 1000)
	now := time.Now()

	assert.Nil(t, p.joinBurst(now), "nothing cached yet")

	feed(p, cam.keyframe(), now)
	feed(p, cam.pframe(), now)
	feed(p, cam.pframe(), now)

	burst := p.joinBurst(now)
	require.Len(t, burst, 6)
	assert.True(t, burst[0].Payload[0]&0x1F == 7, "burst starts with the SPS")
	for i := 1; i < len(burst); i++ {
		assert.Equal(t, burst[i-1].SequenceNumber+1, burst[i].SequenceNumber)
	}
	assert.Equal(t, p.lastOutSeq, burst[len(burst)-1].SequenceNumber, "burst ends where the live stream continues")

	// The next keyframe replaces the cache.
	feed(p, cam.keyframe(), now)
	feed(p, cam.pframe(), now)
	assert.Len(t, p.joinBurst(now), 5)

	// Not live any more: no burst (the viewer gets hold frames or waits).
	assert.Nil(t, p.joinBurst(now.Add(2*time.Second)))

	p.instantStart = false
	assert.Nil(t, p.joinBurst(now))
}

func TestVideoPipeContinuityAcrossCameraRestart(t *testing.T) {
	p := newVideoPipe()
	now := time.Now()

	cam := newFakeCamera(t, testSPS720, 60000, 4_000_000_000)
	var out []*rtp.Packet
	out = append(out, feed(p, cam.keyframe(), now)...)
	for i := 0; i < 20; i++ {
		now = now.Add(66 * time.Millisecond)
		out = append(out, feed(p, cam.pframe(), now)...)
	}
	lastSeq, lastTS := p.lastOutSeq, p.lastOutTS

	// Camera drops for 90 s and comes back with fresh RTP numbering.
	now = now.Add(90 * time.Second)
	cam2 := newFakeCamera(t, testSPS1080, 7, 123)
	assert.Empty(t, feed(p, cam2.pframe(), now), "new session is gated until its first keyframe")
	now = now.Add(66 * time.Millisecond)
	resumed := feed(p, cam2.keyframe(), now)
	require.Len(t, resumed, 4)

	assert.Equal(t, lastSeq+1, resumed[0].SequenceNumber, "sequence numbers continue without a jump")
	gap := resumed[0].Timestamp - lastTS
	assert.InDelta(t, 90.066*videoClockRate, float64(gap), videoClockRate/2, "timestamps advance by the real outage duration")
	assert.Equal(t, uint64(1), p.restarts)
	assert.Equal(t, 1920, p.width)
	assert.Equal(t, 1080, p.height)

	// Sequence wrap-around inside one session is not a restart.
	now = now.Add(66 * time.Millisecond)
	before := p.restarts
	for i := 0; i < 40; i++ {
		feed(p, cam2.pframe(), now)
		now = now.Add(66 * time.Millisecond)
	}
	assert.Equal(t, before, p.restarts)
}

func TestVideoPipeHoldLastFrame(t *testing.T) {
	p := newVideoPipe()
	p.holdMax = 5 * time.Second
	var events []bool
	p.onHoldChange = func(active bool) { events = append(events, active) }

	cam := newFakeCamera(t, testSPS720, 10, 90000)
	now := time.Now()
	feed(p, cam.keyframe(), now)
	feed(p, cam.pframe(), now)
	liveSeq, liveTS := p.lastOutSeq, p.lastOutTS

	assert.Nil(t, p.holdFrame(now.Add(500*time.Millisecond)), "no hold frame while the stream is still considered live")

	t1 := now.Add(1200 * time.Millisecond)
	hold := p.holdFrame(t1)
	require.Len(t, hold, 4, "the hold frame is the complete key unit (SPS, PPS, IDR)")
	assert.Equal(t, liveSeq+1, hold[0].SequenceNumber)
	assert.Equal(t, liveSeq+4, hold[3].SequenceNumber)
	assert.True(t, hold[3].Marker)
	assert.InDelta(t, 1.2*videoClockRate, float64(hold[0].Timestamp-liveTS), 100)
	assert.Equal(t, []bool{true}, events)

	assert.Nil(t, p.holdFrame(t1.Add(300*time.Millisecond)), "at most one hold frame per interval")
	require.Len(t, p.holdFrame(t1.Add(time.Second)), 4)
	assert.Nil(t, p.joinBurst(t1.Add(time.Second)), "no instant-start burst while holding")

	// Same camera session continues: must be re-based behind the hold frames
	// and wait for a keyframe.
	t2 := t1.Add(1500 * time.Millisecond)
	assert.Empty(t, feed(p, cam.pframe(), t2))
	resumed := feed(p, cam.keyframe(), t2.Add(66*time.Millisecond))
	require.Len(t, resumed, 4)
	assert.Equal(t, liveSeq+8+1, resumed[0].SequenceNumber)
	assert.Equal(t, []bool{true, false}, events)

	// Holding stops after holdMax so a real outage becomes visible to clients.
	t3 := t2.Add(2 * time.Second)
	require.NotNil(t, p.holdFrame(t3))
	assert.Nil(t, p.holdFrame(t3.Add(p.holdMax+time.Second)))

	// Disabled by default.
	q := newVideoPipe()
	feed(q, newFakeCamera(t, testSPS720, 0, 0).keyframe(), now)
	assert.Nil(t, q.holdFrame(now.Add(10*time.Second)))
}

func TestVideoPipeBoundsCacheWithoutKeyframes(t *testing.T) {
	p := newVideoPipe()
	cam := newFakeCamera(t, testSPS720, 0, 0)
	now := time.Now()
	feed(p, cam.keyframe(), now)
	for i := 0; i < maxGOPPackets+50; i++ {
		feed(p, cam.pframe(), now)
	}
	assert.LessOrEqual(t, len(p.gop), maxGOPPackets)
	assert.Nil(t, p.joinBurst(now), "an over-long GOP is not replayed")
	assert.Equal(t, 4, p.keyUnitLen, "the key unit is kept for hold frames")
}

// --- end-to-end: a real RTSP client joining a running stream ---

type testReader struct {
	mu   sync.Mutex
	pkts []*rtp.Packet
}

func (r *testReader) add(p *rtp.Packet) {
	r.mu.Lock()
	r.pkts = append(r.pkts, p)
	r.mu.Unlock()
}

func (r *testReader) snapshot() []*rtp.Packet {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*rtp.Packet(nil), r.pkts...)
}

func startTestClient(t *testing.T, url string) (*gortsplib.Client, *testReader, *description.Session) {
	t.Helper()
	u, err := base.ParseURL(url)
	require.NoError(t, err)

	tcp := gortsplib.TransportTCP
	c := &gortsplib.Client{Transport: &tcp}
	require.NoError(t, c.Start(u.Scheme, u.Host))

	desc, _, err := c.Describe(u)
	require.NoError(t, err)

	var h264 *format.H264
	medi := desc.FindFormat(&h264)
	require.NotNil(t, medi)
	_, err = c.Setup(desc.BaseURL, medi, 0, 0)
	require.NoError(t, err)

	r := &testReader{}
	c.OnPacketRTP(medi, h264, func(pkt *rtp.Packet) { r.add(pkt) })
	_, err = c.Play(nil)
	require.NoError(t, err)
	return c, r, desc
}

func TestInstantStartEndToEnd(t *testing.T) {
	srv, err := NewServer(8571, "cam", "pcmu", "", "")
	require.NoError(t, err)
	require.NoError(t, srv.Start())
	defer srv.Close()

	st := srv.PrimaryStream()
	cam := newFakeCamera(t, testSPS720, 30000, 555)

	// The camera has been running for a while: keyframe, then P frames.
	for _, p := range cam.keyframe() {
		st.WriteVideoPacket(p)
	}
	for i := 0; i < 9; i++ {
		for _, p := range cam.pframe() {
			st.WriteVideoPacket(p)
		}
	}

	c, r, desc := startTestClient(t, "rtsp://127.0.0.1:8571/cam")
	defer c.Close()

	var h264 *format.H264
	desc.FindFormat(&h264)
	sps, pps := h264.SafeParams()
	assert.NotEmpty(t, sps, "SDP carries the SPS")
	assert.NotEmpty(t, pps, "SDP carries the PPS")

	// Without any new camera packet the client must already hold a full,
	// decodable group of pictures.
	require.Eventually(t, func() bool { return len(r.snapshot()) >= 13 }, 2*time.Second, 10*time.Millisecond)
	got := r.snapshot()
	assert.Equal(t, byte(7), got[0].Payload[0]&0x1F, "first packet is the SPS")

	// Live packets continue seamlessly behind the burst.
	for i := 0; i < 5; i++ {
		for _, p := range cam.pframe() {
			st.WriteVideoPacket(p)
		}
	}
	require.Eventually(t, func() bool { return len(r.snapshot()) >= 18 }, 2*time.Second, 10*time.Millisecond)
	got = r.snapshot()
	for i := 1; i < len(got); i++ {
		assert.Equal(t, got[i-1].SequenceNumber+1, got[i].SequenceNumber, "packet %d", i)
	}
	assert.Equal(t, uint64(1), st.VideoStats().JoinBursts)
}

func TestHoldLastFrameEndToEnd(t *testing.T) {
	srv, err := NewServer(8572, "cam", "pcmu", "", "")
	require.NoError(t, err)
	require.NoError(t, srv.Start())
	defer srv.Close()

	st := srv.PrimaryStream()
	st.SetVideoOptions(true, 30*time.Second)
	cam := newFakeCamera(t, testSPS720, 1, 1)
	for _, p := range cam.keyframe() {
		st.WriteVideoPacket(p)
	}

	c, r, _ := startTestClient(t, "rtsp://127.0.0.1:8572/cam")
	defer c.Close()
	require.Eventually(t, func() bool { return len(r.snapshot()) >= 4 }, 2*time.Second, 10*time.Millisecond)

	// Camera goes silent: the client keeps receiving the last picture.
	require.Eventually(t, func() bool { return len(r.snapshot()) >= 12 }, 5*time.Second, 50*time.Millisecond)
	assert.True(t, st.VideoStats().Holding)

	// Camera returns with a new session; the stream stays continuous.
	cam2 := newFakeCamera(t, testSPS720, 40000, 99)
	for _, p := range cam2.keyframe() {
		st.WriteVideoPacket(p)
	}
	for _, p := range cam2.pframe() {
		st.WriteVideoPacket(p)
	}
	before := len(r.snapshot())
	require.Eventually(t, func() bool { return len(r.snapshot()) >= before+1 }, 2*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	got := r.snapshot()
	for i := 1; i < len(got); i++ {
		assert.Equal(t, got[i-1].SequenceNumber+1, got[i].SequenceNumber, "packet %d", i)
		assert.False(t, int32(got[i].Timestamp-got[i-1].Timestamp) < 0, "timestamps never run backwards (packet %d)", i)
	}
	assert.False(t, st.VideoStats().Holding)
}
