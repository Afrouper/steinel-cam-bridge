package rtsp

import (
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/h264x"
	"github.com/pion/rtp"
)

const (
	videoClockRate = 90000

	// A camera session restart is assumed when packets pause this long or the
	// RTP sequence/timestamp jumps implausibly.
	discontinuityGap      = 2 * time.Second
	discontinuitySeqJump  = 3000
	discontinuityTSJump   = 10 * videoClockRate
	keyframeGateTimeout   = 5 * time.Second
	maxGOPPackets         = 2500
	maxGOPBytes           = 16 << 20
	maxKeyUnitPackets     = 1200
	liveWindow            = time.Second
	holdFrameInterval     = time.Second
	holdTickInterval      = 250 * time.Millisecond
	joinBurstSettleWindow = 3 * time.Millisecond
)

// videoPipe sits between the camera ingest and the RTSP clients. It
//   - rewrites SSRC / sequence numbers / timestamps so the outgoing stream stays
//     continuous across camera session restarts,
//   - remembers the current group of pictures so a new viewer can be served an
//     instantly decodable picture (instant start),
//   - can repeat the last keyframe while the camera is away (hold last frame),
//   - extracts SPS / PPS and the real picture size.
//
// All methods must be called with mu held unless stated otherwise.
type videoPipe struct {
	mu sync.Mutex

	instantStart bool
	holdMax      time.Duration

	// output continuity
	haveOut    bool
	lastOutSeq uint16
	lastOutTS  uint32
	lastOutAt  time.Time

	// source tracking
	haveSrc    bool
	lastSrcSeq uint16
	lastSrcTS  uint32
	lastSrcAt  time.Time
	seqOff     uint16
	tsOff      uint32

	// keyframe gate after a discontinuity
	gated     bool
	gateSince time.Time

	// group-of-pictures cache (packets already rewritten for output)
	gop          []*rtp.Packet
	gopBytes     int
	gopValid     bool
	keyUnitLen   int
	keyUnitDone  bool
	keyUnitIDR   bool
	holdStarted  time.Time
	holdActive   bool
	holdFrames   uint64
	joinBursts   uint64
	restarts     uint64
	sps, pps     []byte
	paramsDirty  bool
	width        int
	height       int
	sizeSince    time.Time
	lastLiveAt   time.Time
	onHoldChange func(active bool)
}

func newVideoPipe() *videoPipe {
	return &videoPipe{instantStart: true}
}

// ingest takes a packet from the camera and returns the packet to forward to
// the clients, or nil if the packet must be dropped (keyframe gate).
func (p *videoPipe) ingest(pkt *rtp.Packet, now time.Time) *rtp.Packet {
	info := h264x.Inspect(pkt.Payload)
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
	p.lastSrcSeq = pkt.SequenceNumber
	p.lastSrcTS = pkt.Timestamp
	p.lastSrcAt = now

	if p.gated {
		switch {
		case info.KeyStart():
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
	if p.holdActive {
		p.holdActive = false
		if p.onHoldChange != nil {
			p.onHoldChange(false)
		}
	}

	p.remember(out, info)
	return out
}

// videoOutSSRC is the constant SSRC of the outgoing video stream.
const videoOutSSRC = 0x53544C31 // "STL1"

func (p *videoPipe) isDiscontinuity(pkt *rtp.Packet, now time.Time) bool {
	if !p.haveSrc || p.holdActive {
		// First packet ever, or hold frames have advanced the outgoing stream.
		return true
	}
	if now.Sub(p.lastSrcAt) > discontinuityGap {
		return true
	}
	if d := pkt.SequenceNumber - p.lastSrcSeq; d > discontinuitySeqJump && d < 0xFFFF-discontinuitySeqJump {
		return true
	}
	d := int32(pkt.Timestamp - p.lastSrcTS)
	if d > discontinuityTSJump || d < -discontinuityTSJump {
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

func (p *videoPipe) noteParams(info h264x.PacketInfo, now time.Time) {
	if info.SPS != nil && !bytesEqual(info.SPS, p.sps) {
		p.sps = info.SPS
		p.paramsDirty = true
		if w, h, err := h264x.ParseSize(info.SPS); err == nil && (w != p.width || h != p.height) {
			p.width, p.height = w, h
			p.sizeSince = now
		}
	}
	if info.PPS != nil && !bytesEqual(info.PPS, p.pps) {
		p.pps = info.PPS
		p.paramsDirty = true
	}
}

// takeParams returns SPS/PPS once after they changed.
func (p *videoPipe) takeParams() (sps, pps []byte, ok bool) {
	if !p.paramsDirty || p.sps == nil || p.pps == nil {
		return nil, nil, false
	}
	p.paramsDirty = false
	return p.sps, p.pps, true
}

func (p *videoPipe) dropGOP() {
	p.gop = p.gop[:0]
	p.gopBytes = 0
	p.gopValid = false
	p.keyUnitLen = 0
	p.keyUnitDone = false
	p.keyUnitIDR = false
}

// remember appends an outgoing packet to the GOP cache, starting a new cache
// at every keyframe.
func (p *videoPipe) remember(out *rtp.Packet, info h264x.PacketInfo) {
	if info.KeyStart() && (!p.gopValid || p.keyUnitDone) {
		p.dropGOP()
		p.gopValid = true
	}
	if !p.gopValid {
		return
	}

	p.gop = append(p.gop, out)
	p.gopBytes += len(out.Payload)

	if !p.keyUnitDone {
		if info.IDR {
			p.keyUnitIDR = true
		}
		if p.keyUnitIDR && out.Marker {
			p.keyUnitDone = true
			p.keyUnitLen = len(p.gop)
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
func (p *videoPipe) joinBurst(now time.Time) []*rtp.Packet {
	if !p.instantStart || !p.gopValid || !p.keyUnitDone || len(p.gop) == 0 {
		return nil
	}
	if now.Sub(p.lastLiveAt) > liveWindow {
		return nil
	}
	p.joinBursts++
	return p.gop
}

// holdFrame returns a re-stamped copy of the last keyframe if the camera has
// been silent long enough, or nil.
func (p *videoPipe) holdFrame(now time.Time) []*rtp.Packet {
	if p.holdMax <= 0 || !p.haveOut || !p.keyUnitDone || p.keyUnitLen == 0 {
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
