package h264x

import "time"

// retryDelays are the waiting times before the resolution is requested again
// when the camera delivers something else than requested.
var retryDelays = []time.Duration{
	10 * time.Second,
	30 * time.Second,
	time.Minute,
	5 * time.Minute,
	5 * time.Minute,
	5 * time.Minute,
}

// ResolutionGuard compares the resolution the camera really delivers with the
// requested one and decides when the request should be repeated.
// It is not safe for concurrent use; the caller serialises access.
type ResolutionGuard struct {
	requested   string
	actual      string
	attempts    int
	lastRequest time.Time
	gaveUp      bool
}

// Requested records that the given resolution was just requested from the
// camera (by the user, the configuration or a session start).
func (g *ResolutionGuard) Requested(resolution string, now time.Time) {
	if resolution != g.requested {
		g.attempts = 0
		g.gaveUp = false
	}
	g.requested = resolution
	g.lastRequest = now
}

// Verdict is the result of one observation.
type Verdict struct {
	// Actual is the resolution label of the delivered picture ("1080p", "640x480", ...).
	Actual string
	// Changed is true if Actual differs from the previous observation.
	Changed bool
	// Match is true if the camera delivers the requested resolution.
	Match bool
	// Recovered is true if a mismatch has just been resolved.
	Recovered bool
	// Retry is true if the request should be sent again now. The caller must
	// then call Retried.
	Retry bool
	// GaveUp is true once, when no further retries will be made.
	GaveUp bool
	// Attempt is the number of the retry that is due (1-based) when Retry is set.
	Attempt int
}

// Observe evaluates the picture size currently seen in the video stream.
func (g *ResolutionGuard) Observe(width, height int, now time.Time) Verdict {
	v := Verdict{Actual: ResolutionLabel(width, height)}
	if v.Actual == "" || g.requested == "" {
		return v
	}
	v.Changed = v.Actual != g.actual
	g.actual = v.Actual

	if v.Actual == g.requested {
		v.Match = true
		v.Recovered = g.attempts > 0
		g.attempts = 0
		g.gaveUp = false
		return v
	}

	if g.attempts >= len(retryDelays) {
		if !g.gaveUp {
			g.gaveUp = true
			v.GaveUp = true
		}
		return v
	}
	if now.Sub(g.lastRequest) < retryDelays[g.attempts] {
		return v
	}
	v.Retry = true
	v.Attempt = g.attempts + 1
	return v
}

// Retried records that the request was repeated.
func (g *ResolutionGuard) Retried(now time.Time) {
	g.attempts++
	g.lastRequest = now
}

// Actual returns the last observed resolution label ("" while unknown).
func (g *ResolutionGuard) Actual() string {
	return g.actual
}

// IsStandardLabel reports whether the label is one of the selectable resolutions.
func IsStandardLabel(label string) bool {
	return label == "1080p" || label == "720p" || label == "360p"
}
