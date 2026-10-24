package xiongmai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

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

func TestRTSPIngestDialShortCircuit(t *testing.T) {
	// Find an unused local port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // Close port so connection is refused

	ing := NewRTSPIngest("127.0.0.1", port, "admin", "secret", 0, nil, "fallback")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err = ing.runSession(ctx)
	elapsed := time.Since(start)

	require.Error(t, err)

	var dErr *dialError
	assert.True(t, errors.As(err, &dErr), "Expected dialError when port is closed")
	assert.Contains(t, dErr.host, fmt.Sprintf("127.0.0.1:%d", port))

	// Short-circuit must return quickly (single attempt) instead of trying 10+ candidates
	assert.Less(t, elapsed, 500*time.Millisecond, "Dial to closed port should short-circuit immediately")
}
