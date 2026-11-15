// Package h264x inspects H.264 RTP payloads (RFC 6184) without decoding them.
// It tells the RTSP layer where a keyframe starts and which picture size the
// camera really delivers, so the bridge can serve late joiners instantly and
// verify the requested resolution.
package h264x

import (
	"fmt"

	"github.com/bluenviron/mediacommon/pkg/codecs/h264"
)

const (
	nalTypeIDR   = 5
	nalTypeSPS   = 7
	nalTypePPS   = 8
	nalTypeSTAPA = 24
	nalTypeFUA   = 28
)

// PacketInfo describes what a single H.264 RTP payload carries.
type PacketInfo struct {
	// ParamSet is true if the payload carries an SPS or PPS.
	ParamSet bool
	// IDR is true if the payload carries (a fragment of) an IDR slice.
	IDR bool
	// IDRStart is true if the payload carries the beginning of an IDR slice.
	IDRStart bool
	// SPS and PPS hold complete parameter sets found in the payload (copies), if any.
	SPS []byte
	PPS []byte
}

// KeyStart reports whether the payload can open a decodable access unit
// (parameter sets or the first packet of an IDR slice).
func (i PacketInfo) KeyStart() bool {
	return i.ParamSet || i.IDRStart
}

// Inspect classifies an H.264 RTP payload (single NAL unit, STAP-A or FU-A).
func Inspect(payload []byte) PacketInfo {
	var info PacketInfo
	if len(payload) < 1 {
		return info
	}

	switch typ := payload[0] & 0x1F; {
	case typ >= 1 && typ <= 23:
		info.addNAL(payload)

	case typ == nalTypeSTAPA:
		rest := payload[1:]
		for len(rest) >= 2 {
			size := int(rest[0])<<8 | int(rest[1])
			rest = rest[2:]
			if size == 0 || size > len(rest) {
				break
			}
			info.addNAL(rest[:size])
			rest = rest[size:]
		}

	case typ == nalTypeFUA:
		if len(payload) < 2 {
			return info
		}
		start := payload[1]&0x80 != 0
		switch payload[1] & 0x1F {
		case nalTypeIDR:
			info.IDR = true
			info.IDRStart = start
		case nalTypeSPS, nalTypePPS:
			// Fragmented parameter sets are unusual; flag them without copying.
			info.ParamSet = true
		}
	}
	return info
}

func (i *PacketInfo) addNAL(nal []byte) {
	if len(nal) < 1 {
		return
	}
	switch nal[0] & 0x1F {
	case nalTypeIDR:
		i.IDR = true
		i.IDRStart = true
	case nalTypeSPS:
		i.ParamSet = true
		i.SPS = append([]byte(nil), nal...)
	case nalTypePPS:
		i.ParamSet = true
		i.PPS = append([]byte(nil), nal...)
	}
}

// ParseSize returns the displayed picture size encoded in an SPS NAL unit.
func ParseSize(sps []byte) (width int, height int, err error) {
	var s h264.SPS
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
