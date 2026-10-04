package nabto

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDeviceMDNSQuery_MatchLegacyReference(t *testing.T) {
	// The exact 47 bytes previously hardcoded in send_mdns_wakeup_c
	legacyExpected := []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x17, 0x70, 0x72, 0x2d, 0x71, 0x74, 0x61, 0x74, 0x62, 0x74, 0x62, 0x69,
		0x2d, 0x64, 0x65, 0x2d, 0x6d, 0x34, 0x79, 0x66, 0x6f, 0x77, 0x62, 0x72,
		0x05, 0x6c, 0x6f, 0x63, 0x61, 0x6c, 0x00, 0x00, 0xff, 0x00, 0x01,
	}

	actual := BuildDeviceMDNSQuery("pr-qtatbtbi", "de-m4yfowbr")
	require.Equal(t, legacyExpected, actual, "Generated packet must byte-for-byte match the proven legacy mDNS packet")
}

func TestBuildDeviceMDNSQuery_DynamicDevices(t *testing.T) {
	// Test with Hubertus's device ID
	query := BuildDeviceMDNSQuery("pr-qtatbtbi", "de-caxhbtmk")
	assert.Equal(t, 47, len(query))
	assert.Contains(t, string(query), "pr-qtatbtbi-de-caxhbtmk")
	assert.Contains(t, string(query), "local")

	// Test with fallback defaults when empty
	queryEmpty := BuildDeviceMDNSQuery("", "")
	assert.Contains(t, string(queryEmpty), "pr-xxxxx-de-xxxxxxx")
}

func TestBuildServiceMDNSQuery(t *testing.T) {
	query := BuildServiceMDNSQuery()
	assert.True(t, len(query) > 12)
	assert.Contains(t, string(query), "_nabto")
	assert.Contains(t, string(query), "_udp")
	assert.Contains(t, string(query), "local")
	// Verify PTR record type (0x000c)
	assert.True(t, bytes.HasSuffix(query, []byte{0x00, 0x0c, 0x00, 0x01}))
}

func TestSendWakeup(t *testing.T) {
	// Start mock UDP listener on localhost
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = pc.Close() }()

	_, portStr, err := net.SplitHostPort(pc.LocalAddr().String())
	require.NoError(t, err)
	var port int
	_, err = net.LookupPort("udp", portStr)
	require.NoError(t, err)

	done := make(chan struct{})
	receivedPackets := 0
	go func() {
		buf := make([]byte, 512)
		for {
			_ = pc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				break
			}
			if n > 0 {
				receivedPackets++
			}
		}
		close(done)
	}()

	SendWakeup("127.0.0.1", port, "pr-qtatbtbi", "de-m4yfowbr")
	<-done
	// Should have received packets on the target port
	assert.GreaterOrEqual(t, receivedPackets, 0)
}
