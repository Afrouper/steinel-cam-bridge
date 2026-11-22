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

func TestMockExtractorMuxToMP4(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	hevcPath := filepath.Join(tempDir, "test.hevc")
	mp4Path := filepath.Join(tempDir, "test.mp4")
	require.NoError(t, os.WriteFile(hevcPath, []byte("fake-hevc-stream"), 0644))

	mock := NewMockExtractor()
	ctx := context.Background()

	opts := MuxOptions{Codec: "h265", FPS: 6}
	require.NoError(t, mock.MuxToMP4(ctx, hevcPath, mp4Path, opts))
	assert.Equal(t, opts, mock.LastMuxOpts)

	data, err := os.ReadFile(mp4Path)
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-hevc-stream"), data)

	mock.Available = false
	assert.ErrorIs(t, mock.MuxToMP4(ctx, hevcPath, mp4Path, opts), ErrExtractorUnavailable)
}

func TestIncludeAudioContext(t *testing.T) {
	t.Parallel()

	assert.False(t, IncludeAudioFromContext(context.Background()))
	ctxWith := WithIncludeAudio(context.Background(), true)
	assert.True(t, IncludeAudioFromContext(ctxWith))
	ctxWithout := WithIncludeAudio(ctxWith, false)
	assert.False(t, IncludeAudioFromContext(ctxWithout))
}

func TestFFmpegExtractorMuxToMP4(t *testing.T) {
	t.Parallel()

	extractor := NewFFmpegExtractor("")
	if !extractor.IsAvailable() {
		t.Skip("ffmpeg is not installed on this system")
	}

	tests := []struct {
		name         string
		opts         MuxOptions
		withAudio    bool
		ffmpegEnc    string
		ffmpegFmt    string
		wantCodecTag string
	}{
		{
			name:      "h264 with explicit fps",
			opts:      MuxOptions{Codec: "h264", FPS: 10},
			ffmpegEnc: "libx264",
			ffmpegFmt: "h264",
		},
		{
			name:         "hevc with hvc1 tag and low fps",
			opts:         MuxOptions{Codec: "h265", FPS: 6},
			ffmpegEnc:    "libx265",
			ffmpegFmt:    "hevc",
			wantCodecTag: "hvc1",
		},
		{
			name:         "hevc with optional g711 alaw audio muxed as aac",
			opts:         MuxOptions{Codec: "h265", FPS: 6},
			withAudio:    true,
			ffmpegEnc:    "libx265",
			ffmpegFmt:    "hevc",
			wantCodecTag: "hvc1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			rawPath := filepath.Join(tempDir, "sample.raw")
			mp4Path := filepath.Join(tempDir, "sample.mp4")
			thumbPath := filepath.Join(tempDir, "thumb.jpg")

			genCmd := exec.Command(extractor.binaryPath, "-y", "-f", "lavfi",
				"-i", "testsrc=duration=1:size=320x240:rate=6",
				"-c:v", tc.ffmpegEnc, "-f", tc.ffmpegFmt, rawPath)
			if out, err := genCmd.CombinedOutput(); err != nil {
				t.Skipf("ffmpeg encoder %s unavailable in test environment: %v (%s)", tc.ffmpegEnc, err, string(out))
			}

			opts := tc.opts
			if tc.withAudio {
				audioPath := filepath.Join(tempDir, "sample.alaw")
				alawSilence := make([]byte, 8000)
				for i := range alawSilence {
					alawSilence[i] = 0xD5
				}
				require.NoError(t, os.WriteFile(audioPath, alawSilence, 0644))
				opts.AudioPath = audioPath
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			require.NoError(t, extractor.MuxToMP4(ctx, rawPath, mp4Path, opts))

			data, err := os.ReadFile(mp4Path)
			require.NoError(t, err)
			require.Greater(t, len(data), 100)
			assert.Contains(t, string(data[:32]), "ftyp")
			if tc.wantCodecTag != "" {
				assert.Contains(t, string(data), tc.wantCodecTag)
			}
			if tc.withAudio {
				assert.Contains(t, string(data), "mp4a")
			}

			require.NoError(t, extractor.ExtractFrame(ctx, mp4Path, 0, thumbPath))
			thumb, err := os.ReadFile(thumbPath)
			require.NoError(t, err)
			require.Greater(t, len(thumb), 100)
			assert.Equal(t, []byte{0xFF, 0xD8, 0xFF}, thumb[:3])
		})
	}
}
