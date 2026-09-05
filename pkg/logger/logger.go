package logger

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// LevelTrace defines the custom TRACE log level below DEBUG.
const LevelTrace slog.Level = -8

// Global LevelVar to support dynamic runtime log-level adjustments without restart.
var (
	currentLevel = new(slog.LevelVar)
	rootLogger   *slog.Logger
)

func init() {
	currentLevel.Set(slog.LevelInfo)
	rootLogger = slog.New(newConsoleHandler(os.Stdout, currentLevel))
	slog.SetDefault(rootLogger)
}

// ParseLevel parses a string representation into a valid slog.Level.
// Supported values (case-insensitive): "trace", "debug", "info", "warn", "warning", "error".
// Defaults to slog.LevelInfo if empty or unknown.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level: %q (valid: trace, debug, info, warn, error)", s)
	}
}

// FormatLevel returns the standardized uppercase string for a level.
func FormatLevel(l slog.Level) string {
	switch {
	case l <= LevelTrace:
		return "TRACE"
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

// Init configures the root logger with the desired level and output format.
// format can be "console" (default) or "json".
func Init(levelStr string, format string) {
	lvl, err := ParseLevel(levelStr)
	if err != nil {
		lvl = slog.LevelInfo
	}
	currentLevel.Set(lvl)

	var handler slog.Handler
	if strings.ToLower(strings.TrimSpace(format)) == "json" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: currentLevel,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.LevelKey {
					a.Value = slog.StringValue(FormatLevel(a.Value.Any().(slog.Level)))
				}
				return a
			},
		})
	} else {
		handler = newConsoleHandler(os.Stdout, currentLevel)
	}

	rootLogger = slog.New(handler)
	slog.SetDefault(rootLogger)
}

// SetLevel dynamically adjusts the active log level at runtime.
func SetLevel(levelStr string) error {
	lvl, err := ParseLevel(levelStr)
	if err != nil {
		return err
	}
	currentLevel.Set(lvl)
	return nil
}

// GetLevel returns the current active log level.
func GetLevel() slog.Level {
	return currentLevel.Level()
}

// IsEnabled reports whether the given log level is enabled.
func IsEnabled(l slog.Level) bool {
	return currentLevel.Level() <= l
}

// IsTrace reports whether the TRACE level is enabled.
func IsTrace() bool {
	return IsEnabled(LevelTrace)
}

// IsDebug reports whether DEBUG (or TRACE) is enabled.
func IsDebug() bool {
	return IsEnabled(slog.LevelDebug)
}

// For returns a sub-logger tagged with a specific component name.
// E.g.: logger.For("RTSP") -> output will include "[RTSP]".
func For(component string) *slog.Logger {
	return rootLogger.With("component", component)
}

// logf formats and outputs a log record if the given level is enabled.
// If level is not enabled, it returns immediately without evaluating formatting.
func logf(level slog.Level, component string, format string, args ...any) {
	if !IsEnabled(level) {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	For(component).Log(context.Background(), level, msg)
}

// Trace logs at LevelTrace with printf-style formatting.
func Trace(component string, format string, args ...any) {
	logf(LevelTrace, component, format, args...)
}

// Debug logs at LevelDebug with printf-style formatting.
func Debug(component string, format string, args ...any) {
	logf(slog.LevelDebug, component, format, args...)
}

// Info logs at LevelInfo with printf-style formatting.
func Info(component string, format string, args ...any) {
	logf(slog.LevelInfo, component, format, args...)
}

// Warn logs at LevelWarn with printf-style formatting.
func Warn(component string, format string, args ...any) {
	logf(slog.LevelWarn, component, format, args...)
}

// Error logs at LevelError with printf-style formatting.
func Error(component string, format string, args ...any) {
	logf(slog.LevelError, component, format, args...)
}

// FormatBinary formats raw byte slices into a human-readable hex string.
// E.g. FormatBinary([]byte{0x5A, 0x0F, 0x01}) -> "5a 0f 01 [3 bytes]"
// If maxBytes is specified and data exceeds it, the output is truncated.
func FormatBinary(data []byte, maxBytes ...int) string {
	if len(data) == 0 {
		return "<empty>"
	}
	limit := len(data)
	if len(maxBytes) > 0 && maxBytes[0] > 0 && maxBytes[0] < limit {
		limit = maxBytes[0]
	}

	var sb strings.Builder
	for i := 0; i < limit; i++ {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(hex.EncodeToString(data[i : i+1]))
	}
	if limit < len(data) {
		fmt.Fprintf(&sb, " ... [%d bytes total]", len(data))
	} else {
		fmt.Fprintf(&sb, " [%d bytes]", len(data))
	}
	return sb.String()
}

// --- Custom Console Handler for clean, human-readable terminal output ---

type consoleHandler struct {
	out   io.Writer
	level slog.Leveler
	mu    sync.Mutex
	attrs []slog.Attr
}

func newConsoleHandler(out io.Writer, level slog.Leveler) *consoleHandler {
	return &consoleHandler{
		out:   out,
		level: level,
	}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var component string
	var extraAttrs []slog.Attr

	// Extract component from handler's pre-bound attributes or record attributes
	for _, a := range h.attrs {
		if a.Key == "component" {
			component = a.Value.String()
		} else {
			extraAttrs = append(extraAttrs, a)
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "component" && component == "" {
			component = a.Value.String()
		} else {
			extraAttrs = append(extraAttrs, a)
		}
		return true
	})

	timeStr := r.Time.Format("2006/01/02 15:04:05")
	lvlStr := FormatLevel(r.Level)

	var sb strings.Builder
	// Format: 2026/09/05 20:30:00 [INFO] [Component] Message
	sb.WriteString(timeStr)
	sb.WriteString(" [")
	sb.WriteString(lvlStr)
	sb.WriteString("]")

	if component != "" {
		sb.WriteString(" [")
		sb.WriteString(component)
		sb.WriteString("]")
	}

	sb.WriteString(" ")
	sb.WriteString(r.Message)

	for _, a := range extraAttrs {
		sb.WriteString(" ")
		sb.WriteString(a.Key)
		sb.WriteString("=")
		fmt.Fprintf(&sb, "%v", a.Value.Any())
	}
	sb.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, sb.String())
	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &consoleHandler{
		out:   h.out,
		level: h.level,
		attrs: newAttrs,
	}
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	// For console logging, maintain flat readable structure
	return h
}
