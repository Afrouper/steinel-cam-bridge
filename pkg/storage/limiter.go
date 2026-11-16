package storage

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

// ThrottledReader wraps an io.Reader and limits read throughput to a specified number of bytes per second.
// If bytesPerSec <= 0, no throttling is performed (passthrough to underlying reader).
type ThrottledReader struct {
	r       io.Reader
	limiter *rate.Limiter
	ctx     context.Context
}

// NewThrottledReader creates a new ThrottledReader.
// If bytesPerSec <= 0, no rate limiting is enforced.
func NewThrottledReader(ctx context.Context, r io.Reader, bytesPerSec int) *ThrottledReader {
	var limiter *rate.Limiter
	if bytesPerSec > 0 {
		// Set burst size to at most 32 KB or 10% of bytesPerSec (min 4 KB) to avoid large initial unthrottled bursts
		burst := 32 * 1024
		if bytesPerSec/10 < burst {
			burst = bytesPerSec / 10
		}
		if burst < 4096 {
			burst = 4096
		}
		limiter = rate.NewLimiter(rate.Limit(bytesPerSec), burst)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &ThrottledReader{
		r:       r,
		limiter: limiter,
		ctx:     ctx,
	}
}

// Read reads from the underlying reader and waits on the rate limiter to pace data transfer.
func (t *ThrottledReader) Read(p []byte) (int, error) {
	if t.limiter == nil {
		return t.r.Read(p)
	}

	n, err := t.r.Read(p)
	if n > 0 {
		// Wait in slices up to limiter's burst size to prevent ErrLimitExceeded
		remaining := n
		burst := t.limiter.Burst()
		if burst <= 0 {
			burst = 1024
		}

		for remaining > 0 {
			toWait := remaining
			if toWait > burst {
				toWait = burst
			}
			if waitErr := t.limiter.WaitN(t.ctx, toWait); waitErr != nil {
				return n, waitErr
			}
			remaining -= toWait
		}
	}
	return n, err
}
