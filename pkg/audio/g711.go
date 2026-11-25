package audio

import (
	"encoding/binary"

	"github.com/zaf/g711"
)

var (
	ulawToPCM  [256]int16
	alawToPCM  [256]int16
	ulawToAlaw [256]byte
	alawToUlaw [256]byte
)

func init() {
	var all [256]byte
	for i := range 256 {
		all[i] = byte(i)
	}
	rawU := g711.DecodeUlaw(all[:])
	rawA := g711.DecodeAlaw(all[:])
	encA := g711.EncodeAlaw(rawU)
	encU := g711.EncodeUlaw(rawA)
	for i := range 256 {
		ulawToPCM[i] = int16(binary.LittleEndian.Uint16(rawU[i*2:]))
		alawToPCM[i] = int16(binary.LittleEndian.Uint16(rawA[i*2:]))
		ulawToAlaw[i] = encA[i]
		alawToUlaw[i] = encU[i]
	}
}

// DecodePCMU converts a slice of G.711 mu-law (PCMU) bytes into 16-bit signed PCM samples.
func DecodePCMU(src []byte) []int16 {
	if len(src) == 0 {
		return nil
	}
	dst := make([]int16, len(src))
	for i, b := range src {
		dst[i] = ulawToPCM[b]
	}
	return dst
}

// DecodePCMA converts a slice of G.711 a-law (PCMA) bytes into 16-bit signed PCM samples.
func DecodePCMA(src []byte) []int16 {
	if len(src) == 0 {
		return nil
	}
	dst := make([]int16, len(src))
	for i, b := range src {
		dst[i] = alawToPCM[b]
	}
	return dst
}

// TranscodePCMUToPCMAInto converts G.711 mu-law (PCMU) bytes into dst as G.711 A-law (PCMA) without allocating.
func TranscodePCMUToPCMAInto(dst, src []byte) {
	_ = dst[len(src)-1]
	for i, b := range src {
		dst[i] = ulawToAlaw[b]
	}
}

// TranscodePCMAToPCMUInto converts G.711 A-law (PCMA) bytes into dst as G.711 mu-law (PCMU) without allocating.
func TranscodePCMAToPCMUInto(dst, src []byte) {
	_ = dst[len(src)-1]
	for i, b := range src {
		dst[i] = alawToUlaw[b]
	}
}

// TranscodePCMUToPCMA converts a slice of G.711 mu-law (PCMU) bytes into G.711 A-law (PCMA) bytes.
func TranscodePCMUToPCMA(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	TranscodePCMUToPCMAInto(dst, src)
	return dst
}

// TranscodePCMAToPCMU converts a slice of G.711 A-law (PCMA) bytes into G.711 mu-law (PCMU) bytes.
func TranscodePCMAToPCMU(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	TranscodePCMAToPCMUInto(dst, src)
	return dst
}
