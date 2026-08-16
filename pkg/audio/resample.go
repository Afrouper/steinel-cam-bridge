package audio

// Resample8kTo16k upsamples a mono 8000 Hz 16-bit PCM slice to 16000 Hz (2x upsampling) using linear interpolation.
func Resample8kTo16k(src []int16) []int16 {
	if len(src) == 0 {
		return nil
	}
	dst := make([]int16, len(src)*2)
	for i := 0; i < len(src); i++ {
		dst[i*2] = src[i]
		if i+1 < len(src) {
			dst[i*2+1] = int16((int32(src[i]) + int32(src[i+1])) / 2)
		} else {
			dst[i*2+1] = src[i]
		}
	}
	return dst
}

// Resample8kTo32k upsamples a mono 8000 Hz 16-bit PCM slice to 32000 Hz (4x upsampling) using linear interpolation.
func Resample8kTo32k(src []int16) []int16 {
	if len(src) == 0 {
		return nil
	}
	dst := make([]int16, len(src)*4)
	for i := 0; i < len(src); i++ {
		s0 := int32(src[i])
		s1 := s0
		if i+1 < len(src) {
			s1 = int32(src[i+1])
		}
		dst[i*4] = src[i]
		dst[i*4+1] = int16(s0 + (s1-s0)*1/4)
		dst[i*4+2] = int16(s0 + (s1-s0)*2/4)
		dst[i*4+3] = int16(s0 + (s1-s0)*3/4)
	}
	return dst
}
