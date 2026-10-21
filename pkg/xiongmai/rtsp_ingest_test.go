package xiongmai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetCandidateURLs(t *testing.T) {
	urls := getCandidateURLs("192.168.1.100", 554, "admin", "secret123", 0)
	require.NotEmpty(t, urls)

	// Candidate #1 MUST be the clean canonical URL WITHOUT ?real_stream to avoid camera buffer overflow
	assert.Equal(t, "rtsp://192.168.1.100:554/user=admin_password=secret123_channel=1_stream=0.sdp", urls[0])

	// Candidate #2 MUST be the legacy query-parameter URL WITH ?real_stream
	assert.Equal(t, "rtsp://192.168.1.100:554/user=admin_password=secret123_channel=1_stream=0.sdp?real_stream", urls[1])

	// Fallback Xiongmai standard path
	assert.Contains(t, urls[2], "rtsp://admin:secret123@192.168.1.100:554/stream=0")

	// Verify fallback empty password URL is included at the end
	var emptyPwdFound bool
	for _, u := range urls {
		if strings.Contains(u, "password=_") {
			emptyPwdFound = true
			break
		}
	}
	assert.True(t, emptyPwdFound, "Fallback empty password candidate should be generated")
}
