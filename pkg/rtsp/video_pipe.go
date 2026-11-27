package rtsp

import (
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/h264x"
	"github.com/Afrouper/steinel-cam-bridge/pkg/h265x"
	"github.com/pion/rtp"
)

const (
	videoClockRate = 90000

	// A camera session restart is assumed when packets pause this long or the
	// RTP sequence/timestamp jumps implausibly.
	discontinuityGap       = 2 * time.Second
	discontinuitySeqJump   = 3000
	discontinuityTSJump    = 10 * videoClockRate
	keyframeGateTimeout    = 5 * time.Second
	maxGOPPackets          = 2500
	maxGOPBytes            = 16 << 20
	maxKeyUnitPackets      = 1200
	liveWindow             = time.Second
	codecSwitchGraceWindow = 3 * time.Second
	holdFrameInterval      = time.Second
	holdTickInterval       = 250 * time.Millisecond
	joinBurstSettleWindow  = 3 * time.Millisecond
)

type pipePacketInfo struct {
	keyStart bool
	keyFrame bool // IDR (H.264) or IRAP (H.265)
	vps      []byte
	sps      []byte
	pps      []byte
}

// videoPipe sits between the camera ingest and the RTSP clients. It
//   - rewrites SSRC / sequence numbers / timestamps so the outgoing stream stays
//     continuous across camera session restarts (both H.264 and H.265),
//   - remembers the current group of pictures so a new viewer can be served an
//     instantly decodable picture (instant start),
//   - can repeat the last keyframe while the camera is away (hold last frame),
//   - extracts VPS / SPS / PPS and the real picture size.
//
// All methods must be called with mu held unless stated otherwise.
type videoPipe struct {
	mu sync.Mutex

	codec        string
	instantStart bool
	holdMax      time.Duration

	// output continuity
	haveOut    bool
	lastOutSeq uint16
	lastOutTS  uint32
	lastOutAt  time.Time

	// source tracking
	haveSrc     bool
	lastSrcSSRC uint32
	lastSrcSeq  uint16
	lastSrcTS   uint32
	lastSrcAt   time.Time
	seqOff      uint16
	tsOff       uint32

	// keyframe gate after a discontinuity
	gated     bool
	gateSince time.Time

	// group-of-pictures cache (packets already rewritten for output)
	gop           []*rtp.Packet
	gopBytes      int
	gopValid      bool
	keyUnitLen    int
	keyUnitDone   bool
	keyUnitIDR    bool
	holdStarted   time.Time
	holdActive    bool
	holdTimedOut  bool // holdMax is over, wait for live video before holding again
	holdFrames    uint64
	joinBursts    uint64
	restarts      uint64
	vps           []byte
	sps, pps      []byte
	paramsDirty   bool
	width         int
	height        int
	sizeSince     time.Time
	lastLiveAt    time.Time
	codecSwitchAt time.Time
	onHoldChange  func(active bool)
	// onHoldTimeout is called once when holding ends because holdMax is over.
	onHoldTimeout func()
}

func newVideoPipe() *videoPipe {
	return &videoPipe{
		codec:        VideoCodecH264,
		instantStart: true,
	}
}

func (p *videoPipe) setCodec(codec string, now time.Time) {
	if codec != VideoCodecH265 {
		codec = VideoCodecH264
	}
	p.codec = codec
	p.codecSwitchAt = now
	p.dropGOP()
	p.haveSrc = false
	p.gated = false
	p.gateSince = time.Time{}
	p.holdActive = false
	p.holdTimedOut = false
	p.vps = nil
	p.sps = nil
	p.pps = nil
	p.paramsDirty = false
	p.width = 0
	p.height = 0
	p.sizeSince = time.Time{}
}

func (p *videoPipe) inspectPacket(payload []byte) pipePacketInfo {
	if p.codec == VideoCodecH265 {
		info := h265x.Inspect(payload)
		return pipePacketInfo{
			keyStart: info.KeyStart(),
			keyFrame: info.IRAP,
			vps:      info.VPS,
			sps:      info.SPS,
			pps:      info.PPS,
		}
	}
	info := h264x.Inspect(payload)
	return pipePacketInfo{
		keyStart: info.KeyStart(),
		keyFrame: info.IDR,
		sps:      info.SPS,
		pps:      info.PPS,
	}
}

// ingest takes a packet from the camera and returns the packet to forward to
// the clients, or nil if the packet must be dropped (keyframe gate).
func (p *videoPipe) ingest(pkt *rtp.Packet, now time.Time) *rtp.Packet {
	info := p.inspectPacket(pkt.Payload)
	p.noteParams(info, now)

	if !p.gated && p.isDiscontinuity(pkt, now) {
		// New camera session (or first packet ever): the decoder state of every
		// client is tied to the old session, so wait for a keyframe before
		// forwarding anything from the new one.
		if p.haveSrc {
			p.restarts++
		}
		p.gated = true
		p.gateSince = now
	}

	p.haveSrc = true
	p.lastSrcSSRC = pkt.SSRC
	p.lastSrcSeq = pkt.SequenceNumber
	p.lastSrcTS = pkt.Timestamp
	p.lastSrcAt = now

	if p.gated {
		switch {
		case info.keyStart:
		case now.Sub(p.gateSince) > keyframeGateTimeout:
			// Never starve the clients: give up waiting and pass the stream through.
		default:
			return nil
		}
		p.gated = false
		p.dropGOP()
		p.rebase(pkt, now)
	}

	c := *pkt
	out := &c
	out.SequenceNumber = pkt.SequenceNumber + p.seqOff
	out.Timestamp = pkt.Timestamp + p.tsOff
	out.SSRC = videoOutSSRC
	out.PayloadType = 96

	p.haveOut = true
	p.lastOutSeq = out.SequenceNumber
	p.lastOutTS = out.Timestamp
	p.lastOutAt = now
	p.lastLiveAt = now
	if p.holdActive || p.holdTimedOut {
		p.holdActive = false
		p.holdTimedOut = false
		if p.onHoldChange != nil {
			p.onHoldChange(false)
		}
	}

	p.remember(out, info, now)
	return out
}

// videoOutSSRC is the constant SSRC of the outgoing video stream.
const videoOutSSRC = 0x53544C31 // "STL1"

func (p *videoPipe) isDiscontinuity(pkt *rtp.Packet, now time.Time) bool {
	if !p.haveSrc || p.holdActive || p.holdTimedOut {
		// First packet ever, or hold frames have advanced the outgoing stream.
		return true
	}
	if pkt.SSRC != p.lastSrcSSRC {
		return true
	}
	if d := pkt.SequenceNumber - p.lastSrcSeq; d > discontinuitySeqJump && d < 0xFFFF-discontinuitySeqJump {
		return true
	}
	d := int32(pkt.Timestamp - p.lastSrcTS)
	if d > discontinuityTSJump || d < -discontinuityTSJump {
		return true
	}
	if now.Sub(p.lastSrcAt) > discontinuityGap {
		// Over lossless TCP (Sofia OPMonitor / RTSP interleaved), a large 1080p keyframe
		// on a low-bitrate CBR encoder (e.g. 130 KB at 460 kbps) or brief Wi-Fi buffering
		// can pause frame delivery for >2s without losing a single RTP packet. When SSRC,
		// contiguous SequenceNumber, and bounded forward timestamp delta all confirm the
		// exact same unbroken stream, keep forwarding P-frames instead of gating for 5s.
		if pkt.SequenceNumber == p.lastSrcSeq+1 && d >= 0 {
			return false
		}
		return true
	}
	return false
}

// rebase computes new offsets so that pkt continues the outgoing stream.
func (p *videoPipe) rebase(pkt *rtp.Packet, now time.Time) {
	if !p.haveOut {
		p.seqOff = 0
		p.tsOff = 0
		return
	}
	elapsed := now.Sub(p.lastOutAt)
	if elapsed < time.Second/15 {
		elapsed = time.Second / 15
	}
	nextTS := p.lastOutTS + uint32(elapsed.Seconds()*videoClockRate)
	p.seqOff = p.lastOutSeq + 1 - pkt.SequenceNumber
	p.tsOff = nextTS - pkt.Timestamp
}

func (p *videoPipe) noteParams(info pipePacketInfo, now time.Time) {
	if p.codec == VideoCodecH265 && info.vps != nil && !bytesEqual(info.vps, p.vps) {
		p.vps = info.vps
		p.paramsDirty = true
	}
	if info.sps != nil && !bytesEqual(info.sps, p.sps) {
		p.sps = info.sps
		p.paramsDirty = true
		var (
			w, h int
			err  error
		)
		if p.codec == VideoCodecH265 {
			w, h, err = h265x.ParseSize(info.sps)
		} else {
			w, h, err = h264x.ParseSize(info.sps)
		}
		if err == nil && (w != p.width || h != p.height) {
			p.width, p.height = w, h
			p.sizeSince = now
		}
	}
	if info.pps != nil && !bytesEqual(info.pps, p.pps) {
		p.pps = info.pps
		p.paramsDirty = true
	}
}

// takeParams returns H.264 SPS/PPS once after they changed.
func (p *videoPipe) takeParams() (sps, pps []byte, ok bool) {
	if !p.paramsDirty || p.sps == nil || p.pps == nil {
		return nil, nil, false
	}
	p.paramsDirty = false
	return p.sps, p.pps, true
}

// takeH265Params returns H.265 VPS/SPS/PPS once after they changed.
func (p *videoPipe) takeH265Params() (vps, sps, pps []byte, ok bool) {
	if !p.paramsDirty || p.vps == nil || p.sps == nil || p.pps == nil {
		return nil, nil, nil, false
	}
	p.paramsDirty = false
	return p.vps, p.sps, p.pps, true
}

func (p *videoPipe) dropGOP() {
	clear(p.gop)
	p.gop = p.gop[:0]
	p.gopBytes = 0
	p.gopValid = false
	p.keyUnitLen = 0
	p.keyUnitDone = false
	p.keyUnitIDR = false
}

// remember appends an outgoing packet to the GOP cache when instantStart,
// post-codec-switch grace, or hold-last-frame requires it.
func (p *videoPipe) remember(out *rtp.Packet, info pipePacketInfo, now time.Time) {
	inCodecSwitchGrace := !p.codecSwitchAt.IsZero() && now.Sub(p.codecSwitchAt) <= codecSwitchGraceWindow
	needFullGOP := p.instantStart || inCodecSwitchGrace
	needKeyUnitOnly := !needFullGOP && p.holdMax > 0

	if !needFullGOP && !needKeyUnitOnly {
		if len(p.gop) > 0 {
			p.dropGOP()
			p.gop = nil
		}
		return
	}

	if info.keyStart && (!p.gopValid || p.keyUnitDone) {
		p.dropGOP()
		p.gopValid = true
	}
	if !p.gopValid {
		return
	}
	if needKeyUnitOnly && p.keyUnitDone {
		// Hold-last-frame only replays the key unit; skip trailing P-frames and mark
		// the cache non-contiguous so only holdFrame uses it.
		return
	}

	p.gop = append(p.gop, out)
	p.gopBytes += len(out.Payload)

	if !p.keyUnitDone {
		if info.keyFrame {
			p.keyUnitIDR = true
		}
		if p.keyUnitIDR && out.Marker {
			p.keyUnitDone = true
			p.keyUnitLen = len(p.gop)
			if needKeyUnitOnly {
				p.gopValid = false
			}
		} else if len(p.gop) > maxKeyUnitPackets {
			p.dropGOP()
			return
		}
	}

	if len(p.gop) > maxGOPPackets || p.gopBytes > maxGOPBytes {
		// Camera sends no regular keyframes: keep only the key unit for the hold frame.
		if p.keyUnitDone {
			p.gop = p.gop[:p.keyUnitLen]
			p.gopBytes = 0
			for _, k := range p.gop {
				p.gopBytes += len(k.Payload)
			}
			p.gopValid = false // no longer a contiguous run up to "now"
		} else {
			p.dropGOP()
		}
	}
}

// joinBurst returns the packets a freshly joined viewer needs to decode a
// picture immediately, or nil if the stream is not live right now.
// Even when instantStart is disabled, a one-shot grace burst is granted right
// after SetVideoCodec replaced the RTSP ServerStream so the client forced to
// reconnect does not wait a full GOP for the camera's second keyframe.
func (p *videoPipe) joinBurst(now time.Time) []*rtp.Packet {
	inCodecSwitchGrace := !p.codecSwitchAt.IsZero() && now.Sub(p.codecSwitchAt) <= codecSwitchGraceWindow
	if (!p.instantStart && !inCodecSwitchGrace) || !p.gopValid || !p.keyUnitDone || len(p.gop) == 0 {
		return nil
	}
	if now.Sub(p.lastLiveAt) > liveWindow {
		return nil
	}
	p.codecSwitchAt = time.Time{}
	p.joinBursts++
	// A copy of the slice, so the caller does not depend on mu while it sends.
	return append([]*rtp.Packet(nil), p.gop...)
}

// holdFrame returns a re-stamped copy of the last keyframe if the camera has
// been silent long enough, or nil.
func (p *videoPipe) holdFrame(now time.Time) []*rtp.Packet {
	if p.holdMax <= 0 || !p.haveOut || !p.keyUnitDone || p.keyUnitLen == 0 {
		return nil
	}
	if p.holdTimedOut {
		// Given up on this outage; only live video starts a new hold.
		return nil
	}
	if now.Sub(p.lastOutAt) < holdFrameInterval {
		return nil
	}
	if !p.holdActive {
		if now.Sub(p.lastLiveAt) < holdFrameInterval {
			return nil
		}
		p.holdActive = true
		p.holdStarted = now
		if p.onHoldChange != nil {
			p.onHoldChange(true)
		}
	}
	if now.Sub(p.holdStarted) > p.holdMax {
		// Stop holding so a real outage becomes visible to the clients. The
		// flag keeps the next tick from starting a fresh hold period.
		p.holdActive = false
		p.holdTimedOut = true
		if p.onHoldTimeout != nil {
			p.onHoldTimeout()
		}
		return nil
	}

	ts := p.lastOutTS + uint32(now.Sub(p.lastOutAt).Seconds()*videoClockRate)
	frames := make([]*rtp.Packet, 0, p.keyUnitLen)
	for _, k := range p.gop[:p.keyUnitLen] {
		p.lastOutSeq++
		cp := *k
		c := &cp
		c.SequenceNumber = p.lastOutSeq
		c.Timestamp = ts
		frames = append(frames, c)
	}
	p.lastOutTS = ts
	p.lastOutAt = now
	p.holdFrames++
	// The cached run is no longer contiguous with the outgoing stream.
	p.gopValid = false
	return frames
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
