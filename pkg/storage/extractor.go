package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

var (
	// ErrExtractorUnavailable is returned when no frame extraction engine is present on the system.
	ErrExtractorUnavailable = errors.New("frame extractor engine is not available on this system")

	// ErrInvalidVideoFile is returned when the input video file does not exist or is empty.
	ErrInvalidVideoFile = errors.New("input video file does not exist or is empty")
)

// FrameExtractor defines the abstraction for extracting snapshot thumbnails from video files.
// Encapsulated behind an interface according to Go standards to allow future extensions
// (e.g. native Pure-Go HEVC decoders, hardware acceleration, or cloud extractors).
type FrameExtractor interface {
	// ExtractFrame extracts a single video frame at or near offset and saves it as a JPEG image to thumbPath.
	ExtractFrame(ctx context.Context, videoPath string, offset time.Duration, thumbPath string) error

	// IsAvailable returns whether the extractor engine is available and functional.
	IsAvailable() bool
}

type includeAudioKey struct{}

// WithIncludeAudio returns a derived context configured to include or omit
// the audio track when downloading and muxing a camera recording.
func WithIncludeAudio(ctx context.Context, include bool) context.Context {
	return context.WithValue(ctx, includeAudioKey{}, include)
}

// IncludeAudioFromContext reports whether ctx requests audio track muxing (default true).
func IncludeAudioFromContext(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	if v, ok := ctx.Value(includeAudioKey{}).(bool); ok {
		return v
	}
	return true
}

// MuxOptions configures container muxing of an elementary Annex-B video stream
// and an optional raw 8 kHz G.711 A-law audio stream.
// The zero value defaults to video-only H.264 with the stream's native framerate.
type MuxOptions struct {
	Codec     string
	FPS       int
	AudioPath string
}

// VideoMuxer defines the abstraction for remuxing elementary Annex-B video streams into MP4 containers.
type VideoMuxer interface {
	// MuxToMP4 remuxes an elementary Annex-B H.264 or H.265/HEVC stream into an MP4 container without re-encoding.
	MuxToMP4(ctx context.Context, inputPath, mp4Path string, opts MuxOptions) error

	// MuxH264ToMP4 remuxes an elementary Annex-B H.264 stream into an MP4 container without re-encoding.
	MuxH264ToMP4(ctx context.Context, h264Path, mp4Path string) error

	// IsAvailable returns whether the muxer engine is available and functional.
	IsAvailable() bool
}

var (
	_ FrameExtractor = (*FFmpegExtractor)(nil)
	_ VideoMuxer     = (*FFmpegExtractor)(nil)
	_ FrameExtractor = (*MockExtractor)(nil)
	_ VideoMuxer     = (*MockExtractor)(nil)
)

// FFmpegExtractor extracts frames and muxes containers using the standalone ffmpeg binary.
type FFmpegExtractor struct {
	binaryPath string
	available  bool
}

// NewFFmpegExtractor initializes an FFmpegExtractor. If binaryPath is empty, it searches PATH
// and common system locations (/usr/local/bin/ffmpeg, /usr/bin/ffmpeg, /opt/homebrew/bin/ffmpeg).
func NewFFmpegExtractor(binaryPath string) *FFmpegExtractor {
	if binaryPath == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			binaryPath = p
		} else {
			for _, candidate := range []string{"/usr/local/bin/ffmpeg", "/usr/bin/ffmpeg", "/opt/homebrew/bin/ffmpeg"} {
				if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
					binaryPath = candidate
					break
				}
			}
		}
	}

	available := false
	if binaryPath != "" {
		if fi, err := os.Stat(binaryPath); err == nil && !fi.IsDir() {
			available = true
		}
	}

	if available {
		logger.Debug("Extractor", "🎬 FFmpeg frame extractor initialized using %s", binaryPath)
	} else {
		logger.Debug("Extractor", "ℹ️ FFmpeg binary not found; frame extraction will be disabled")
	}

	return &FFmpegExtractor{
		binaryPath: binaryPath,
		available:  available,
	}
}

// IsAvailable implements FrameExtractor.
func (e *FFmpegExtractor) IsAvailable() bool {
	return e != nil && e.available
}

// ExtractFrame implements FrameExtractor.
func (e *FFmpegExtractor) ExtractFrame(ctx context.Context, videoPath string, offset time.Duration, thumbPath string) error {
	if !e.IsAvailable() {
		return ErrExtractorUnavailable
	}

	fi, err := os.Stat(videoPath)
	if err != nil || fi.Size() == 0 {
		return ErrInvalidVideoFile
	}

	// Ensure destination directory exists
	destDir := filepath.Dir(thumbPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create thumbnail directory: %w", err)
	}

	// Attempt extraction with primary offset
	err = e.runExtraction(ctx, videoPath, offset, thumbPath)
	if err == nil {
		return nil
	}

	// Fallback 1: If requested offset > 1s failed (e.g. video is shorter than 5 seconds), try 1 second
	if offset > time.Second {
		logger.Trace("Extractor", "Offset %v failed, retrying extraction at 1s for %s", offset, filepath.Base(videoPath))
		if errFallback := e.runExtraction(ctx, videoPath, time.Second, thumbPath); errFallback == nil {
			return nil
		}
	}

	// Fallback 2: Try position 0s (very first frame)
	logger.Trace("Extractor", "Retrying extraction at 0s for %s", filepath.Base(videoPath))
	return e.runExtraction(ctx, videoPath, 0, thumbPath)
}

func (e *FFmpegExtractor) runExtraction(ctx context.Context, videoPath string, offset time.Duration, thumbPath string) error {
	extractCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Temporary file to ensure atomic write (preserves .jpg extension and uses .tmp_ prefix)
	tmpThumb := filepath.Join(filepath.Dir(thumbPath), ".tmp_"+filepath.Base(thumbPath))
	defer func() { _ = os.Remove(tmpThumb) }()

	offsetSec := fmt.Sprintf("%.2f", offset.Seconds())

	// Command flags:
	// -ss <offset>: Fast input seeking to nearest keyframe
	// -i <videoPath>: Input video
	// -frames:v 1: Extract exactly 1 frame
	// -vf "scale=640:-1": Downscale width to 640px while preserving aspect ratio
	// -q:v 3: High quality JPEG compression (resulting in ~30-60 KB)
	// -f mjpeg: Force JPEG/MJPEG format even if extension differs
	cmd := exec.CommandContext(extractCtx, e.binaryPath,
		"-y",
		"-ss", offsetSec,
		"-i", videoPath,
		"-frames:v", "1",
		"-vf", "scale=640:-1",
		"-q:v", "3",
		"-f", "mjpeg",
		tmpThumb,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg execution failed (offset: %s): %w, output: %s", offsetSec, err, string(out))
	}

	stat, err := os.Stat(tmpThumb)
	if err != nil || stat.Size() == 0 {
		return fmt.Errorf("ffmpeg produced empty thumbnail file")
	}

	// Atomic rename to final thumbnail path
	if err := os.Rename(tmpThumb, thumbPath); err != nil {
		return fmt.Errorf("failed to save final thumbnail: %w", err)
	}

	logger.Trace("Extractor", "📸 Extracted thumbnail for %s (Size: %d bytes)", filepath.Base(videoPath), stat.Size())
	return nil
}

// MuxH264ToMP4 remuxes an elementary Annex-B H.264 stream into an MP4 container.
func (e *FFmpegExtractor) MuxH264ToMP4(ctx context.Context, h264Path, mp4Path string) error {
	return e.MuxToMP4(ctx, h264Path, mp4Path, MuxOptions{Codec: "h264"})
}

// MuxToMP4 remuxes an elementary Annex-B H.264 or H.265/HEVC stream into an MP4 container
// using -c:v copy and -movflags +faststart. HEVC inputs are tagged with hvc1.
func (e *FFmpegExtractor) MuxToMP4(ctx context.Context, inputPath, mp4Path string, opts MuxOptions) error {
	if !e.IsAvailable() {
		return ErrExtractorUnavailable
	}

	fi, err := os.Stat(inputPath)
	if err != nil || fi.Size() == 0 {
		return ErrInvalidVideoFile
	}

	destDir := filepath.Dir(mp4Path)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	muxCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	inputFormat := "h264"
	isHEVC := false
	switch strings.ToLower(strings.TrimSpace(opts.Codec)) {
	case "h265", "hevc":
		inputFormat = "hevc"
		isHEVC = true
	}

	hasAudio := false
	if opts.AudioPath != "" {
		if afi, aErr := os.Stat(opts.AudioPath); aErr == nil && afi.Size() > 0 {
			hasAudio = true
		}
	}

	buildArgs := func(withAudio bool) []string {
		args := []string{"-y"}
		if opts.FPS > 0 && opts.FPS <= 120 {
			args = append(args, "-r", strconv.Itoa(opts.FPS))
		}
		args = append(args, "-f", inputFormat, "-i", inputPath)
		if withAudio {
			args = append(args, "-f", "alaw", "-ar", "8000", "-ac", "1", "-i", opts.AudioPath)
		}
		args = append(args, "-c:v", "copy")
		if isHEVC {
			args = append(args, "-tag:v", "hvc1")
		}
		if withAudio {
			args = append(args, "-c:a", "aac", "-b:a", "64k")
		}
		return append(args, "-movflags", "+faststart", mp4Path)
	}

	cmd := exec.CommandContext(muxCtx, e.binaryPath, buildArgs(hasAudio)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if hasAudio {
			logger.Warn("Extractor", "⚠️ Audio+video muxing failed (%v), retrying video-only remux", err)
			stderr.Reset()
			cmd = exec.CommandContext(muxCtx, e.binaryPath, buildArgs(false)...)
			cmd.Stderr = &stderr
			if retryErr := cmd.Run(); retryErr != nil {
				return fmt.Errorf("ffmpeg muxing (%s) failed: %w, stderr: %s", inputFormat, retryErr, stderr.String())
			}
		} else {
			return fmt.Errorf("ffmpeg muxing (%s) failed: %w, stderr: %s", inputFormat, err, stderr.String())
		}
	}

	stat, err := os.Stat(mp4Path)
	if err != nil || stat.Size() == 0 {
		return fmt.Errorf("ffmpeg produced empty mp4 file")
	}

	logger.Trace("Extractor", "🎬 Remuxed %s to MP4: %s (FPS: %d, Size: %d bytes)", inputFormat, filepath.Base(mp4Path), opts.FPS, stat.Size())
	return nil
}

// MockExtractor is an in-memory mock implementation of FrameExtractor and VideoMuxer for unit testing.
type MockExtractor struct {
	Available   bool
	ExtractErr  error
	MuxErr      error
	DummyData   []byte
	LastMuxOpts MuxOptions
}

// NewMockExtractor returns a ready-to-use MockExtractor generating valid minimal JPEGs.
func NewMockExtractor() *MockExtractor {
	return &MockExtractor{
		Available: true,
		DummyData: []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0xFF, 0xD9},
	}
}

// ExtractFrame implements FrameExtractor for MockExtractor.
func (m *MockExtractor) ExtractFrame(ctx context.Context, videoPath string, offset time.Duration, thumbPath string) error {
	if !m.Available {
		return ErrExtractorUnavailable
	}
	if m.ExtractErr != nil {
		return m.ExtractErr
	}

	if err := os.MkdirAll(filepath.Dir(thumbPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(thumbPath, m.DummyData, 0644)
}

// MuxH264ToMP4 implements VideoMuxer for MockExtractor.
func (m *MockExtractor) MuxH264ToMP4(ctx context.Context, h264Path, mp4Path string) error {
	return m.MuxToMP4(ctx, h264Path, mp4Path, MuxOptions{Codec: "h264"})
}

// MuxToMP4 implements VideoMuxer for MockExtractor.
func (m *MockExtractor) MuxToMP4(ctx context.Context, inputPath, mp4Path string, opts MuxOptions) error {
	if !m.Available {
		return ErrExtractorUnavailable
	}
	if m.MuxErr != nil {
		return m.MuxErr
	}

	m.LastMuxOpts = opts

	if err := os.MkdirAll(filepath.Dir(mp4Path), 0755); err != nil {
		return err
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	return os.WriteFile(mp4Path, data, 0644)
}

// IsAvailable implements FrameExtractor and VideoMuxer for MockExtractor.
func (m *MockExtractor) IsAvailable() bool {
	return m.Available
}
