package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// FFmpegExtractor extracts frames using the standalone ffmpeg binary.
type FFmpegExtractor struct {
	binaryPath string
	available  bool
}

// NewFFmpegExtractor initializes an FFmpegExtractor. If binaryPath is empty, it searches PATH
// and common system locations (/usr/local/bin/ffmpeg, /usr/bin/ffmpeg).
func NewFFmpegExtractor(binaryPath string) *FFmpegExtractor {
	if binaryPath == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			binaryPath = p
		} else {
			for _, candidate := range []string{"/usr/local/bin/ffmpeg", "/usr/bin/ffmpeg"} {
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

	// Temporary file to ensure atomic write
	tmpThumb := thumbPath + ".tmp"
	defer func() { _ = os.Remove(tmpThumb) }()

	offsetSec := fmt.Sprintf("%.2f", offset.Seconds())

	// Command flags:
	// -ss <offset>: Fast input seeking to nearest keyframe
	// -i <videoPath>: Input video
	// -frames:v 1: Extract exactly 1 frame
	// -vf "scale=640:-1": Downscale width to 640px while preserving aspect ratio
	// -q:v 3: High quality JPEG compression (resulting in ~30-60 KB)
	cmd := exec.CommandContext(extractCtx, e.binaryPath,
		"-y",
		"-ss", offsetSec,
		"-i", videoPath,
		"-frames:v", "1",
		"-vf", "scale=640:-1",
		"-q:v", "3",
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

// MockExtractor is an in-memory mock implementation of FrameExtractor for unit testing.
type MockExtractor struct {
	Available  bool
	ExtractErr error
	DummyData  []byte
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

// IsAvailable implements FrameExtractor for MockExtractor.
func (m *MockExtractor) IsAvailable() bool {
	return m.Available
}
