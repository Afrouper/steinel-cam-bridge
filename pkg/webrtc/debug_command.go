package webrtc

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/redact"

	"github.com/google/uuid"
)

// DebugCommand sends a single JSON command over the DataChannel and returns
// every JSON message the camera sends back until a reply to that command has
// arrived or the timeout expires. It exists to explore what the camera
// firmware supports; all returned messages have credentials removed.
func (b *Bridge) DebugCommand(cmdName, action string, info map[string]interface{}, timeout time.Duration) ([]json.RawMessage, error) {
	if timeout <= 0 {
		timeout = 4 * time.Second
	}

	b.mu.Lock()
	dc := b.dc
	if dc == nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("data channel not available")
	}
	if b.debugTap != nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("another debug command is still running")
	}
	tap := make(chan []byte, 64)
	b.debugTap = tap
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.debugTap = nil
		b.mu.Unlock()
	}()

	cmd := map[string]interface{}{
		"from":  "Android",
		"cmd":   cmdName,
		"msgid": uuid.New().String(),
	}
	if action != "" {
		cmd["action"] = action
	}
	if info != nil {
		cmd["info"] = info
	}
	data, _ := json.Marshal(cmd)
	logger.Info("DataChannel", "🧪 Debug command '%s' sent to camera", cmdName)
	if err := dc.Send(data); err != nil {
		return nil, err
	}

	var out []json.RawMessage
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case msg := <-tap:
			var root map[string]interface{}
			if err := json.Unmarshal(msg, &root); err != nil {
				continue
			}
			name, _ := root["resp"].(string)
			if name == "" {
				name, _ = root["cmd"].(string)
			}
			if name == "tran_ctl" || name == "tran_report" {
				continue // periodic lamp telemetry, not an answer
			}
			out = append(out, json.RawMessage(redact.JSON(msg)))
			if name == cmdName {
				// Give trailing messages a short moment, then finish.
				deadline.Reset(300 * time.Millisecond)
			}
		case <-deadline.C:
			return out, nil
		}
	}
}

// feedDebugTap copies an incoming JSON control message to a running debug command.
func (b *Bridge) feedDebugTap(data []byte) {
	b.mu.Lock()
	tap := b.debugTap
	b.mu.Unlock()
	if tap == nil {
		return
	}
	select {
	case tap <- append([]byte(nil), data...):
	default:
	}
}
