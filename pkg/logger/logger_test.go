package logger

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
		wantErr  bool
	}{
		{"trace", LevelTrace, false},
		{"TRACE", LevelTrace, false},
		{"debug", slog.LevelDebug, false},
		{"DEBUG", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"INFO", slog.LevelInfo, false},
		{"", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"WARN", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"ERROR", slog.LevelError, false},
		{"invalid", slog.LevelInfo, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			lvl, err := ParseLevel(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expected, lvl)
		})
	}
}

func TestFormatLevel(t *testing.T) {
	assert.Equal(t, "TRACE", FormatLevel(LevelTrace))
	assert.Equal(t, "DEBUG", FormatLevel(slog.LevelDebug))
	assert.Equal(t, "INFO", FormatLevel(slog.LevelInfo))
	assert.Equal(t, "WARN", FormatLevel(slog.LevelWarn))
	assert.Equal(t, "ERROR", FormatLevel(slog.LevelError))
}

func TestConsoleHandlerFormatting(t *testing.T) {
	buf := &bytes.Buffer{}
	lvl := new(slog.LevelVar)
	lvl.Set(LevelTrace)

	handler := newConsoleHandler(buf, lvl)
	l := slog.New(handler).With("component", "RTSP")

	l.Info("Server listening", "port", 8554)
	out := buf.String()

	assert.Contains(t, out, "[INFO]")
	assert.Contains(t, out, "[RTSP]")
	assert.Contains(t, out, "Server listening")
	assert.Contains(t, out, "port=8554")
}

func TestLevelFiltering(t *testing.T) {
	buf := &bytes.Buffer{}
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelInfo) // Info mode: Debug and Trace must be suppressed

	handler := newConsoleHandler(buf, lvl)
	l := slog.New(handler)

	l.Log(context.Background(), LevelTrace, "trace msg")
	l.Debug("debug msg")
	l.Info("info msg")
	l.Warn("warn msg")

	out := buf.String()
	assert.NotContains(t, out, "trace msg")
	assert.NotContains(t, out, "debug msg")
	assert.Contains(t, out, "info msg")
	assert.Contains(t, out, "warn msg")
}

func TestFormatBinary(t *testing.T) {
	assert.Equal(t, "<empty>", FormatBinary(nil))
	assert.Equal(t, "<empty>", FormatBinary([]byte{}))

	data := []byte{0x5A, 0x0F, 0x01, 0x02}
	assert.Equal(t, "5a 0f 01 02 [4 bytes]", FormatBinary(data))

	// Truncated format
	assert.Equal(t, "5a 0f ... [4 bytes total]", FormatBinary(data, 2))
}

func TestDynamicSetLevel(t *testing.T) {
	require.NoError(t, SetLevel("debug"))
	assert.True(t, IsDebug())
	assert.False(t, IsTrace())

	require.NoError(t, SetLevel("trace"))
	assert.True(t, IsTrace())
	assert.True(t, IsDebug())

	require.NoError(t, SetLevel("info"))
	assert.False(t, IsTrace())
	assert.False(t, IsDebug())
	assert.Equal(t, slog.LevelInfo, GetLevel())
}

func TestPrintfFormattingAndEarlyExit(t *testing.T) {
	buf := &bytes.Buffer{}
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelInfo)

	origLevel := currentLevel
	origRoot := rootLogger
	defer func() {
		currentLevel = origLevel
		rootLogger = origRoot
	}()

	currentLevel = lvl
	rootLogger = slog.New(newConsoleHandler(buf, lvl))

	// Info is enabled: should format with printf args
	Info("RTSP", "Server listening at port %d on %s", 8554, "0.0.0.0")

	// Debug and Trace are disabled: should exit early without logging
	Debug("RTSP", "Debug %s %d", "secret", 42)
	Trace("RTSP", "Trace %s %d", "hidden", 99)

	out := buf.String()
	assert.Contains(t, out, "[INFO] [RTSP] Server listening at port 8554 on 0.0.0.0")
	assert.NotContains(t, out, "secret")
	assert.NotContains(t, out, "hidden")

	// Now switch to debug: debug should be logged
	lvl.Set(slog.LevelDebug)
	buf.Reset()
	Debug("WebRTC", "Connecting to peer %s", "192.168.1.50")
	Trace("WebRTC", "Trace payload %s", "not_logged")

	out = buf.String()
	assert.Contains(t, out, "[DEBUG] [WebRTC] Connecting to peer 192.168.1.50")
	assert.NotContains(t, out, "not_logged")
}
