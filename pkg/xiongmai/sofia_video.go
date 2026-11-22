package xiongmai

import (
	"bytes"
	"fmt"

	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph265"
	"github.com/bluenviron/mediacommon/pkg/codecs/h264"
	"github.com/pion/rtp"
)

// This file implements codec detection, NAL unit filtering, out-of-band SDP parameter
// publication, and RTP packetization for native Xiongmai Sofia video streams (MsgID 1412).
//
// Some Steinel / Xiongmai camera firmwares deliver an H.265 (HEVC) elementary stream on
// the Sofia media channel even when Simplify.Encode reports H.264. By inspecting the
// Annex-B NAL unit headers of each I-frame and encapsulating codec-specific RTP encoding
// behind the videoPacketizer interface, SofiaStream can transparently switch the active
// packetizer and the announced RTSP video track between H.264 (RFC 6184) and H.265 (RFC 7798).

// Video codecs detected on the native Sofia media stream.
const (
	videoCodecH264 = "h264"
	videoCodecH265 = "h265"
)

// Xiongmai video codec identifiers (byte 4 of the 16-byte I-frame record header).
const (
	sofiaCodecH264 byte = 0x02
	sofiaCodecH265 byte = 0x03
)

// Optional RTSP writer capabilities discovered via small consumer-side interfaces.
type videoCodecSwitcher interface {
	SetVideoCodec(codec string) bool
}

type h264ParamsWriter interface {
	SetH264Parameters(sps, pps []byte)
}

type h265ParamsWriter interface {
	SetH265Parameters(vps, sps, pps []byte)
}

// videoPacketizer filters Annex-B NAL units for a single video codec, publishes out-of-band
// parameter sets to the RTSP writer when present, and packetizes the frame into RTP packets.
type videoPacketizer interface {
	codec() string
	packetize(nalus [][]byte, writer VideoWriter) ([]*rtp.Packet, error)
}

var (
	_ videoPacketizer = (*h264Packetizer)(nil)
	_ videoPacketizer = (*h265Packetizer)(nil)
)

func newVideoPacketizer(codec string) (videoPacketizer, error) {
	switch codec {
	case videoCodecH265:
		enc := &rtph265.Encoder{PayloadType: 96}
		if err := enc.Init(); err != nil {
			return nil, fmt.Errorf("init rtp h265 encoder: %w", err)
		}
		return &h265Packetizer{enc: enc}, nil
	default:
		enc := &rtph264.Encoder{
			PayloadType:       96,
			PacketizationMode: 1,
		}
		if err := enc.Init(); err != nil {
			return nil, fmt.Errorf("init rtp h264 encoder: %w", err)
		}
		return &h264Packetizer{enc: enc}, nil
	}
}

type h264Packetizer struct {
	enc *rtph264.Encoder
}

func (p *h264Packetizer) codec() string { return videoCodecH264 }

func (p *h264Packetizer) packetize(nalus [][]byte, writer VideoWriter) ([]*rtp.Packet, error) {
	var sps, pps []byte
	filtered := make([][]byte, 0, len(nalus))

	for _, nalu := range nalus {
		if len(nalu) == 0 || nalu[0]&0x80 != 0 {
			continue
		}
		switch h264.NALUType(nalu[0] & 0x1F) {
		case h264.NALUTypeSPS:
			if sps == nil {
				sps = nalu
			}
			filtered = append(filtered, nalu)
		case h264.NALUTypePPS:
			if pps == nil {
				pps = nalu
			}
			filtered = append(filtered, nalu)
		case h264.NALUTypeNonIDR, h264.NALUTypeIDR, h264.NALUTypeSEI:
			filtered = append(filtered, nalu)
		}
	}
	if len(filtered) == 0 {
		return nil, nil
	}

	if len(sps) > 0 && len(pps) > 0 {
		if pw, ok := writer.(h264ParamsWriter); ok {
			pw.SetH264Parameters(sps, pps)
		}
	}

	return p.enc.Encode(filtered)
}

// HEVC (RFC 7798) NAL unit types used across Sofia and RTSP video handling.
const (
	hevcNALUVPS               byte = 32
	hevcNALUSPS               byte = 33
	hevcNALUPPS               byte = 34
	hevcNALUAggregationPacket byte = 48
)

// hevcNALUType extracts the 6-bit NAL unit type from the first byte of a 2-byte HEVC NAL header.
func hevcNALUType(hdr byte) byte {
	return (hdr >> 1) & 0x3F
}

// needsHEVCAggregationTemporalIDFix reports whether payload is an RFC 7798 Aggregation Packet
// (NALU type 48) whose header violates Section 1.1.4/4.4.2 by carrying nuh_temporal_id_plus1 == 0.
func needsHEVCAggregationTemporalIDFix(payload []byte) bool {
	return len(payload) >= 2 && hevcNALUType(payload[0]) == hevcNALUAggregationPacket && payload[1]&0x07 == 0
}

type h265Packetizer struct {
	enc *rtph265.Encoder
}

func (p *h265Packetizer) codec() string { return videoCodecH265 }

func (p *h265Packetizer) packetize(nalus [][]byte, writer VideoWriter) ([]*rtp.Packet, error) {
	var vps, sps, pps []byte
	filtered := make([][]byte, 0, len(nalus))

	for _, nalu := range nalus {
		// Reject truncated headers, forbidden_zero_bit != 0, and illegal temporal_id_plus1 == 0 (RFC 7798 Section 1.1.4).
		hasValidHeader := len(nalu) >= 2 && nalu[0]&0x80 == 0 && nalu[1]&0x07 != 0
		if !hasValidHeader {
			continue
		}
		hevcType := hevcNALUType(nalu[0])
		switch {
		case hevcType == hevcNALUVPS:
			if vps == nil {
				vps = nalu
			}
			filtered = append(filtered, nalu)
		case hevcType == hevcNALUSPS:
			if sps == nil {
				sps = nalu
			}
			filtered = append(filtered, nalu)
		case hevcType == hevcNALUPPS:
			if pps == nil {
				pps = nalu
			}
			filtered = append(filtered, nalu)
		case hevcType <= 31, hevcType == 39, hevcType == 40:
			// Retain VCL slices (0..31) and prefix/suffix SEI (39, 40); drop AUD (35), EOS (36),
			// EOB (37), filler data (38), and reserved or packetization types (>40).
			filtered = append(filtered, nalu)
		}
	}
	if len(filtered) == 0 {
		return nil, nil
	}

	if len(vps) > 0 && len(sps) > 0 && len(pps) > 0 {
		if pw, ok := writer.(h265ParamsWriter); ok {
			pw.SetH265Parameters(vps, sps, pps)
		}
	}

	pkts, err := p.enc.Encode(filtered)
	if err != nil {
		return nil, err
	}
	for _, pkt := range pkts {
		// RFC 7798 Section 1.1.4 and Section 4.4.2 require nuh_temporal_id_plus1 (low 3 bits of byte 1)
		// of an Aggregation Packet header (NALU type 48) to be at least 1. Ensure the header carries a
		// valid temporal ID even when the underlying RTP encoder leaves those bits cleared.
		if needsHEVCAggregationTemporalIDFix(pkt.Payload) {
			pkt.Payload[1] |= 0x01
		}
	}
	return pkts, nil
}

// firstAnnexBNALU returns the first NAL unit (without start code) of an Annex-B buffer.
func firstAnnexBNALU(data []byte) []byte {
	var first []byte
	scanAnnexBNALUs(data, 1, func(nal []byte) bool {
		first = nal
		return true
	})
	return first
}

// scanAnnexBNALUs invokes fn for up to maxNALUs Annex-B NAL units in data without heap allocations.
// Iteration stops early when fn returns true.
func scanAnnexBNALUs(data []byte, maxNALUs int, fn func(nal []byte) bool) {
	rem := data
	for count := 0; count < maxNALUs && len(rem) > 0; count++ {
		idx := bytes.Index(rem, []byte{0x00, 0x00, 0x01})
		if idx < 0 {
			return
		}
		nal := rem[idx+3:]
		if next := bytes.Index(nal, []byte{0x00, 0x00, 0x01}); next >= 0 {
			end := next
			if end > 0 && nal[end-1] == 0x00 {
				end--
			}
			if fn(nal[:end]) {
				return
			}
			rem = nal[next:]
		} else {
			fn(nal)
			return
		}
	}
}

// isHEVCLayerZeroHeader reports whether a 2-byte NAL header belongs to HEVC layer 0
// (nuh_layer_id == 0 across bit 0 of byte 0 and the upper 5 bits of byte 1) with a
// valid non-zero nuh_temporal_id_plus1 (lower 3 bits of byte 1, RFC 7798 Section 1.1.4).
func isHEVCLayerZeroHeader(nal []byte) bool {
	if len(nal) < 2 {
		return false
	}
	layerIDZero := nal[0]&0x01 == 0 && (nal[1]&0xF8) == 0
	temporalIDValid := (nal[1] & 0x07) != 0
	return layerIDZero && temporalIDValid
}

// detectSofiaVideoCodec determines the codec of a Sofia I-frame. Annex-B NAL headers in the
// access unit are authoritative (HEVC VPS/SPS/PPS/IRAP vs. H.264 SPS/PPS/IDR), skipping any
// leading AUD/SEI units; the Xiongmai header codec byte is used as fallback.
// Returns "" if the codec cannot be determined.
func detectSofiaVideoCodec(codecByte byte, payload []byte) string {
	var detected string

	// Pass 1: Match unambiguous parameter sets and H.264 IDR across the initial NAL units.
	// H.264 SPS (7) and IDR (5) have bit 0 set in nal[0] and cannot collide with single-layer HEVC (bit 0 == 0).
	// HEVC VPS/SPS/PPS (32..34 -> 0x40/0x42/0x44) have nal[0]&0x1F in {0, 2, 4} and cannot collide with H.264 SPS/PPS/IDR/SEI/AUD.
	scanAnnexBNALUs(payload, 8, func(nal []byte) bool {
		if len(nal) < 2 || nal[0]&0x80 != 0 {
			return false
		}
		hevcType := hevcNALUType(nal[0])
		isHEVCParam := hevcType >= hevcNALUVPS && hevcType <= hevcNALUPPS
		if isHEVCLayerZeroHeader(nal) && isHEVCParam {
			detected = videoCodecH265
			return true
		}
		switch h264.NALUType(nal[0] & 0x1F) {
		case h264.NALUTypeSPS, h264.NALUTypeIDR:
			detected = videoCodecH264
			return true
		}
		return false
	})
	if detected != "" {
		return detected
	}

	// Pass 2: Match bare HEVC IRAP slices (16..21) or H.264 PPS (8) when no SPS/VPS preceded them.
	scanAnnexBNALUs(payload, 8, func(nal []byte) bool {
		if len(nal) < 2 || nal[0]&0x80 != 0 {
			return false
		}
		hevcType := hevcNALUType(nal[0])
		isHEVCIRAP := hevcType >= 16 && hevcType <= 21
		if isHEVCLayerZeroHeader(nal) && isHEVCIRAP {
			detected = videoCodecH265
			return true
		}
		if h264.NALUType(nal[0]&0x1F) == h264.NALUTypePPS {
			detected = videoCodecH264
			return true
		}
		return false
	})
	if detected != "" {
		return detected
	}

	switch codecByte & 0x0F {
	case sofiaCodecH265:
		return videoCodecH265
	case sofiaCodecH264:
		return videoCodecH264
	}
	return ""
}
