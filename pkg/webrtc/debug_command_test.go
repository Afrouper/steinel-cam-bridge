package webrtc

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/redact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tapIsFree reports whether no debug command is running on b.
func tapIsFree(b *Bridge) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.debugTap == nil
}

func TestDebugCommand_NoDataChannel(t *testing.T) {
	_, err := (&Bridge{}).DebugCommand("get_device_info", "", nil, time.Second)
	assert.ErrorContains(t, err, "data channel not available")
}

func TestDebugCommand_CollectsReplyFiltersTelemetryAndRedacts(t *testing.T) {
	b := &Bridge{}
	var sent map[string]interface{}
	send := func(data []byte) error {
		if err := json.Unmarshal(data, &sent); err != nil {
			return err
		}
		// The camera's answers arrive through the DataChannel handler.
		go func() {
			b.feedDebugTap([]byte(`{"resp":"tran_ctl","result":"success","info":{"data":"WgIBAA=="}}`))
			b.feedDebugTap([]byte(`{"cmd":"tran_report","info":{"data":"WgIBAA=="}}`))
			b.feedDebugTap([]byte(`no json at all`))
			b.feedDebugTap([]byte(`{"resp":"get_device_info","result":"success","info":{"FW_version":"0.1.2.31","wifi":{"RSSI":57,"password":"hunter2"},"p2p_token":"abc123"}}`))
		}()
		return nil
	}

	start := time.Now()
	out, err := b.debugCommand(send, "get_device_info", "all", map[string]interface{}{"n": 1}, 5*time.Second)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "returns shortly after the reply, not at the timeout")

	// What went to the camera.
	assert.Equal(t, "get_device_info", sent["cmd"])
	assert.Equal(t, "all", sent["action"])
	assert.Equal(t, "Android", sent["from"])
	assert.NotEmpty(t, sent["msgid"])
	assert.Equal(t, map[string]interface{}{"n": float64(1)}, sent["info"])

	// Only the real answer comes back, without the credentials in it.
	require.Len(t, out, 1)
	var reply map[string]interface{}
	require.NoError(t, json.Unmarshal(out[0], &reply))
	assert.Equal(t, "get_device_info", reply["resp"])
	info := reply["info"].(map[string]interface{})
	assert.Equal(t, "0.1.2.31", info["FW_version"])
	assert.Equal(t, redact.Placeholder, info["p2p_token"])
	wifi := info["wifi"].(map[string]interface{})
	assert.Equal(t, float64(57), wifi["RSSI"])
	assert.Equal(t, redact.Placeholder, wifi["password"])
	assert.NotContains(t, string(out[0]), "hunter2")
	assert.NotContains(t, string(out[0]), "abc123")

	assert.True(t, tapIsFree(b), "the tap is released when the command is done")
}

// A tran_ctl that was sent on purpose gets its reply; other telemetry stays out.
func TestDebugCommand_ExplicitTranCtl(t *testing.T) {
	b := &Bridge{}
	send := func([]byte) error {
		go func() {
			b.feedDebugTap([]byte(`{"cmd":"tran_report","info":{"data":"WgIBAA=="}}`))
			b.feedDebugTap([]byte(`{"resp":"tran_ctl","result":"success"}`))
		}()
		return nil
	}
	out, err := b.debugCommand(send, "tran_ctl", "", map[string]interface{}{"data": "WgIBAA=="}, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Contains(t, string(out[0]), `"resp":"tran_ctl"`)
}

func TestDebugCommand_TimeoutWithoutReply(t *testing.T) {
	b := &Bridge{}
	start := time.Now()
	out, err := b.debugCommand(func([]byte) error { return nil }, "get_time_setting", "", nil, 150*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond)
	assert.True(t, tapIsFree(b))
}

func TestDebugCommand_SendError(t *testing.T) {
	b := &Bridge{}
	_, err := b.debugCommand(func([]byte) error { return errors.New("channel closed") }, "get_time_setting", "", nil, time.Second)
	assert.ErrorContains(t, err, "channel closed")
	assert.True(t, tapIsFree(b))
}

// Only one debug command runs at a time; a second one is refused without
// sending anything.
func TestDebugCommand_SingleFlight(t *testing.T) {
	b := &Bridge{}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = b.debugCommand(func([]byte) error { return nil }, "get_time_setting", "", nil, 600*time.Millisecond)
	}()
	require.Eventually(t, func() bool { return !tapIsFree(b) }, 2*time.Second, 5*time.Millisecond)

	var secondSent atomic.Bool
	_, err := b.debugCommand(func([]byte) error { secondSent.Store(true); return nil }, "get_volume_setting", "", nil, time.Second)
	assert.ErrorContains(t, err, "another debug command is still running")
	assert.False(t, secondSent.Load())

	<-firstDone
	assert.True(t, tapIsFree(b))
	_, err = b.debugCommand(func([]byte) error { return nil }, "get_volume_setting", "", nil, 50*time.Millisecond)
	assert.NoError(t, err)
}
