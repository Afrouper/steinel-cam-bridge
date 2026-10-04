package netprobe

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculateChecksum(t *testing.T) {
	data := []byte{0x08, 0x00, 0x00, 0x00, 0x12, 0x34, 0x00, 0x01}
	cs := CalculateChecksum(data)
	assert.NotZero(t, cs)

	// Appending checksum should verify to 0 or 0xffff
	data[2] = byte(cs >> 8)
	data[3] = byte(cs & 0xff)
	verify := CalculateChecksum(data)
	assert.True(t, verify == 0 || verify == 0xffff)
}

func TestPing_EmptyTarget(t *testing.T) {
	ctx := context.Background()
	res, err := Ping(ctx, "", 500*time.Millisecond)
	assert.Error(t, err)
	assert.False(t, res.Reachable)
}

func TestPing_Localhost(t *testing.T) {
	ctx := context.Background()
	res, err := Ping(ctx, "127.0.0.1", 1*time.Second)
	require.NoError(t, err)
	assert.True(t, res.Reachable)
	assert.NotEmpty(t, res.Method)
}

func TestPing_UnreachableTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// 192.0.2.1 is TEST-NET-1 (RFC 5737), guaranteed to not respond
	res, err := Ping(ctx, "192.0.2.1", 200*time.Millisecond)
	assert.Error(t, err)
	assert.False(t, res.Reachable)
}

type mockProber struct {
	reachable bool
	rtt       time.Duration
}

func (m *mockProber) Ping(ctx context.Context, target string, timeout time.Duration) (Result, error) {
	return Result{Reachable: m.reachable, RTT: m.rtt, Method: "mock"}, nil
}

func TestCustomProber(t *testing.T) {
	orig := DefaultProber
	defer func() { DefaultProber = orig }()

	DefaultProber = &mockProber{reachable: true, rtt: 5 * time.Millisecond}
	res, err := Ping(context.Background(), "10.0.0.1", time.Second)
	require.NoError(t, err)
	assert.True(t, res.Reachable)
	assert.Equal(t, "mock", res.Method)
	assert.Equal(t, 5*time.Millisecond, res.RTT)
}
