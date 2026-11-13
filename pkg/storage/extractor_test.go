package storage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockExtractor(t *testing.T) {
	tempDir := t.TempDir()
	thumbPath := filepath.Join(tempDir, "test.jpg")
	videoPath := filepath.Join(tempDir, "test.mp4")
	require.NoError(t, os.WriteFile(videoPath, []byte("fake-video"), 0644))

	mock := NewMockExtractor()
	assert.True(t, mock.IsAvailable())

	ctx := context.Background()
	err := mock.ExtractFrame(ctx, videoPath, 5*time.Second, thumbPath)
	require.NoError(t, err)

	data, err := os.ReadFile(thumbPath)
	require.NoError(t, err)
	assert.Equal(t, mock.DummyData, data)

	// Test unavailable mock
	mock.Available = false
	assert.False(t, mock.IsAvailable())
	assert.ErrorIs(t, mock.ExtractFrame(ctx, videoPath, 5*time.Second, thumbPath), ErrExtractorUnavailable)
}

func TestFFmpegExtractorNonExistent(t *testing.T) {
	extractor := NewFFmpegExtractor("/path/does/not/exist/ffmpeg")
	assert.False(t, extractor.IsAvailable())

	err := extractor.ExtractFrame(context.Background(), "fake.mp4", 5*time.Second, "thumb.jpg")
	assert.ErrorIs(t, err, ErrExtractorUnavailable)
}

func TestFFmpegExtractorInvalidVideo(t *testing.T) {
	extractor := NewFFmpegExtractor("")
	if !extractor.IsAvailable() {
		t.Skip("ffmpeg is not installed on this system")
	}

	tempDir := t.TempDir()
	emptyVideo := filepath.Join(tempDir, "empty.mp4")
	require.NoError(t, os.WriteFile(emptyVideo, []byte{}, 0644))

	err := extractor.ExtractFrame(context.Background(), emptyVideo, 5*time.Second, filepath.Join(tempDir, "thumb.jpg"))
	assert.ErrorIs(t, err, ErrInvalidVideoFile)
}

func TestFFmpegExtractorRealVideo(t *testing.T) {
	extractor := NewFFmpegExtractor("")
	if !extractor.IsAvailable() {
		t.Skip("ffmpeg is not installed on this system")
	}

	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "sample.mp4")
	thumbPath := filepath.Join(tempDir, "sample.jpg")

	// Generate a 1-second test MP4 using ffmpeg lavfi testsrc
	genCmd := exec.Command(extractor.binaryPath, "-y", "-f", "lavfi", "-i", "testsrc=duration=1:size=320x240:rate=10", videoPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to generate test video: %v (%s)", err, string(out))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := extractor.ExtractFrame(ctx, videoPath, 0, thumbPath)
	require.NoError(t, err)

	data, err := os.ReadFile(thumbPath)
	require.NoError(t, err)
	assert.Greater(t, len(data), 100)
	// Check JPEG magic bytes: FF D8 FF
	assert.Equal(t, byte(0xFF), data[0])
	assert.Equal(t, byte(0xD8), data[1])
	assert.Equal(t, byte(0xFF), data[2])
}

func TestMockExtractorMux(t *testing.T) {
	tempDir := t.TempDir()
	h264Path := filepath.Join(tempDir, "test.h264")
	mp4Path := filepath.Join(tempDir, "test.mp4")
	require.NoError(t, os.WriteFile(h264Path, []byte("fake-h264-stream"), 0644))

	mock := NewMockExtractor()
	assert.True(t, mock.IsAvailable())

	ctx := context.Background()
	err := mock.MuxH264ToMP4(ctx, h264Path, mp4Path)
	require.NoError(t, err)

	data, err := os.ReadFile(mp4Path)
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-h264-stream"), data)

	mock.Available = false
	assert.ErrorIs(t, mock.MuxH264ToMP4(ctx, h264Path, mp4Path), ErrExtractorUnavailable)
}

func TestFFmpegExtractorMuxH264ToMP4(t *testing.T) {
	extractor := NewFFmpegExtractor("")
	if !extractor.IsAvailable() {
		t.Skip("ffmpeg is not installed on this system")
	}

	tempDir := t.TempDir()
	h264Path := filepath.Join(tempDir, "sample.h264")
	mp4Path := filepath.Join(tempDir, "sample.mp4")

	// Generate a 1-second raw Annex-B H.264 stream using ffmpeg lavfi testsrc
	genCmd := exec.Command(extractor.binaryPath, "-y", "-f", "lavfi", "-i", "testsrc=duration=1:size=320x240:rate=10",
		"-c:v", "libx264", "-f", "h264", h264Path)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to generate test h264 stream: %v (%s)", err, string(out))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := extractor.MuxH264ToMP4(ctx, h264Path, mp4Path)
	require.NoError(t, err)

	data, err := os.ReadFile(mp4Path)
	require.NoError(t, err)
	assert.Greater(t, len(data), 100)

	// Check MP4 ftyp box or container signature
	// In MP4 faststart, bytes 4..8 usually contain 'ftyp'
	assert.Contains(t, string(data[:32]), "ftyp")
}
