package h264x

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestResolutionGuardMatch(t *testing.T) {
	var g ResolutionGuard
	t0 := time.Now()

	// Nothing requested or nothing seen yet: no verdict.
	assert.Equal(t, Verdict{Actual: "720p"}, g.Observe(1280, 720, t0))
	g.Requested("720p", t0)
	assert.Equal(t, Verdict{}, g.Observe(0, 0, t0))

	v := g.Observe(1280, 720, t0.Add(15*time.Second))
	assert.True(t, v.Match)
	assert.True(t, v.Changed)
	assert.False(t, v.Retry)
	assert.Equal(t, "720p", g.Actual())

	v = g.Observe(1280, 720, t0.Add(20*time.Second))
	assert.True(t, v.Match)
	assert.False(t, v.Changed)
}

func TestResolutionGuardRetriesWithBackoff(t *testing.T) {
	var g ResolutionGuard
	t0 := time.Now()
	g.Requested("720p", t0)

	// Camera still delivers 1080p shortly after the request: wait.
	v := g.Observe(1920, 1080, t0.Add(5*time.Second))
	assert.False(t, v.Match)
	assert.False(t, v.Retry)
	assert.Equal(t, "1080p", v.Actual)

	// 10 s after the request the first retry is due.
	now := t0.Add(11 * time.Second)
	v = g.Observe(1920, 1080, now)
	assert.True(t, v.Retry)
	assert.Equal(t, 1, v.Attempt)
	g.Retried(now)

	// Next retry only after 30 s.
	assert.False(t, g.Observe(1920, 1080, now.Add(20*time.Second)).Retry)
	now = now.Add(31 * time.Second)
	v = g.Observe(1920, 1080, now)
	assert.True(t, v.Retry)
	assert.Equal(t, 2, v.Attempt)
	g.Retried(now)

	// Camera follows: recovered, counters reset.
	v = g.Observe(1280, 720, now.Add(5*time.Second))
	assert.True(t, v.Match)
	assert.True(t, v.Recovered)
	assert.True(t, v.Changed)

	// Flips back later (as seen on the L 625): retry schedule starts again
	// relative to the last request.
	now = now.Add(10 * time.Minute)
	v = g.Observe(1920, 1080, now)
	assert.True(t, v.Retry)
	assert.Equal(t, 1, v.Attempt)
}

func TestResolutionGuardGivesUp(t *testing.T) {
	var g ResolutionGuard
	now := time.Now()
	g.Requested("360p", now)

	retries := 0
	gaveUp := 0
	for i := 0; i < 500; i++ {
		now = now.Add(10 * time.Second)
		v := g.Observe(640, 480, now)
		if v.Retry {
			retries++
			g.Retried(now)
		}
		if v.GaveUp {
			gaveUp++
		}
	}
	assert.Equal(t, len(retryDelays), retries)
	assert.Equal(t, 1, gaveUp, "giving up is reported exactly once")

	// A new request by the user starts over.
	g.Requested("720p", now)
	now = now.Add(11 * time.Second)
	assert.True(t, g.Observe(640, 480, now).Retry)
}

func TestIsStandardLabel(t *testing.T) {
	assert.True(t, IsStandardLabel("1080p"))
	assert.True(t, IsStandardLabel("720p"))
	assert.True(t, IsStandardLabel("360p"))
	assert.False(t, IsStandardLabel("640x480"))
	assert.False(t, IsStandardLabel(""))
}
