package storage

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThrottledReader_Unthrottled(t *testing.T) {
	data := []byte("hello world unthrottled read")
	src := bytes.NewReader(data)
	tr := NewThrottledReader(context.Background(), src, 0)

	out, err := io.ReadAll(tr)
	require.NoError(t, err)
	assert.Equal(t, data, out)
}

func TestThrottledReader_Pacing(t *testing.T) {
	// 50 KB of test data
	size := 50 * 1024
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}

	src := bytes.NewReader(data)
	// Rate limit: 100 KB/s. Burst is 10 KB. Remaining 40 KB should take ~400ms.
	rateBytes := 100 * 1024
	tr := NewThrottledReader(context.Background(), src, rateBytes)

	buf := make([]byte, 4096)
	totalRead := 0
	start := time.Now()

	for {
		n, err := tr.Read(buf)
		if n > 0 {
			totalRead += n
		}
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}

	elapsed := time.Since(start)
	assert.Equal(t, size, totalRead)
	// At 100 KB/s with 10 KB burst, reading 50 KB should take at least ~300ms
	assert.GreaterOrEqual(t, elapsed, 300*time.Millisecond)
}

func TestThrottledReader_ContextCancellation(t *testing.T) {
	data := make([]byte, 100*1024)
	src := bytes.NewReader(data)

	ctx, cancel := context.WithCancel(context.Background())
	// Very slow rate: 10 KB/s
	tr := NewThrottledReader(ctx, src, 10*1024)

	// Cancel after 50ms
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	buf := make([]byte, 4096)
	var err error
	for {
		_, err = tr.Read(buf)
		if err != nil {
			break
		}
	}

	assert.ErrorIs(t, err, context.Canceled)
}
