package audio

import (
	"encoding/binary"

	"github.com/zaf/g711"
)

// DecodePCMU converts a slice of G.711 mu-law (PCMU) bytes into 16-bit signed PCM samples.
func DecodePCMU(src []byte) []int16 {
	if len(src) == 0 {
		return nil
	}
	raw := g711.DecodeUlaw(src)
	dst := make([]int16, len(raw)/2)
	for i := 0; i < len(dst); i++ {
		dst[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return dst
}
