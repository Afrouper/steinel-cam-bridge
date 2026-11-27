// Package h265x inspects H.265 / HEVC RTP payloads (RFC 7798) without decoding
// them. It tells the RTSP layer where a keyframe starts, extracts in-band
// VPS/SPS/PPS parameter sets, and reads the picture size from the SPS so the
// video pipeline can maintain RTP continuity and instant-start GOP caching.
package h265x

import (
	"bytes"
	"fmt"

	"github.com/bluenviron/mediacommon/pkg/codecs/h265"
)

const (
	nalTypeBLAWLP = 16
	nalTypeCRANUT = 21
	nalTypeVPS    = 32
	nalTypeSPS    = 33
	nalTypePPS    = 34
	nalTypeAP     = 48
	nalTypeFU     = 49
)

// PacketInfo describes what a single H.265 RTP payload carries.
type PacketInfo struct {
	// ParamSet is true if the payload carries a VPS, SPS, or PPS.
	ParamSet bool
	// IRAP is true if the payload carries (a fragment of) an IRAP keyframe slice (types 16..21).
	IRAP bool
	// IRAPStart is true if the payload carries the beginning of an IRAP keyframe slice.
	IRAPStart bool
	// VPS, SPS, and PPS hold complete parameter sets found in the payload (copies), if any.
	VPS []byte
	SPS []byte
	PPS []byte
}

// KeyStart reports whether the payload can open a decodable access unit
// (parameter sets or the first packet of an IRAP slice).
func (i PacketInfo) KeyStart() bool {
	return i.ParamSet || i.IRAPStart
}

// Inspect classifies an H.265 RTP payload (single NAL unit, Aggregation Packet, or Fragmentation Unit).
func Inspect(payload []byte) PacketInfo {
	var info PacketInfo
	if len(payload) < 2 || payload[0]&0x80 != 0 || payload[1]&0x07 == 0 {
		return info
	}

	switch typ := (payload[0] >> 1) & 0x3F; {
	case typ <= 40:
		info.addNAL(payload)

	case typ == nalTypeAP:
		// RFC 7798 Section 4.4.2 Aggregation Packet (without DONL, sprop-max-don-diff == 0).
		rest := payload[2:]
		for len(rest) >= 2 {
			size := int(rest[0])<<8 | int(rest[1])
			rest = rest[2:]
			if size == 0 || size > len(rest) {
				break
			}
			info.addNAL(rest[:size])
			rest = rest[size:]
		}

	case typ == nalTypeFU:
		// RFC 7798 Section 4.4.3 Fragmentation Unit: 2-byte PayloadHdr + 1-byte FU header.
		if len(payload) < 3 {
			return info
		}
		fuHdr := payload[2]
		start := fuHdr&0x80 != 0
		fuType := fuHdr & 0x3F
		switch {
		case fuType >= nalTypeBLAWLP && fuType <= nalTypeCRANUT:
			info.IRAP = true
			if start {
				info.IRAPStart = true
			}
		case fuType == nalTypeVPS || fuType == nalTypeSPS || fuType == nalTypePPS:
			info.ParamSet = true
		}
	}
	return info
}

func isBaseLayerHeader(nal []byte) bool {
	return len(nal) >= 2 && nal[0]&0x01 == 0 && (nal[1]&0xF8) == 0 && (nal[1]&0x07) != 0
}

func (i *PacketInfo) addNAL(nal []byte) {
	if len(nal) < 2 || nal[0]&0x80 != 0 || nal[1]&0x07 == 0 {
		return
	}
	typ := (nal[0] >> 1) & 0x3F
	switch {
	case typ >= nalTypeBLAWLP && typ <= nalTypeCRANUT:
		i.IRAP = true
		i.IRAPStart = true
	case typ == nalTypeVPS && isBaseLayerHeader(nal):
		i.ParamSet = true
		i.VPS = bytes.Clone(nal)
	case typ == nalTypeSPS && isBaseLayerHeader(nal):
		i.ParamSet = true
		i.SPS = bytes.Clone(nal)
	case typ == nalTypePPS && isBaseLayerHeader(nal):
		i.ParamSet = true
		i.PPS = bytes.Clone(nal)
	}
}

// ParseSize returns the displayed picture size encoded in an H.265 SPS NAL unit.
func ParseSize(sps []byte) (width int, height int, err error) {
	var s h265.SPS
	if err := s.Unmarshal(sps); err != nil {
		return 0, 0, err
	}
	return s.Width(), s.Height(), nil
}

// ResolutionLabel maps a picture size to the bridge's resolution names
// ("1080p", "720p", "360p"). Unknown sizes are returned as "WxH".
func ResolutionLabel(width, height int) string {
	switch {
	case width <= 0 || height <= 0:
		return ""
	case height >= 1080 && height <= 1088:
		return "1080p"
	case height == 720:
		return "720p"
	case height >= 360 && height <= 368:
		return "360p"
	default:
		return fmt.Sprintf("%dx%d", width, height)
	}
}
