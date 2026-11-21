package onvif

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// debugCall records what the diagnostic endpoint passed on to a camera.
type debugCall struct {
	cmd, action string
	info        map[string]interface{}
	timeout     time.Duration
}

// recordingDebugFunc returns a DebugCommandFunc that records its calls and
// answers with the given replies and error.
func recordingDebugFunc(calls *[]debugCall, replies []json.RawMessage, err error) DebugCommandFunc {
	return func(cmd, action string, info map[string]interface{}, timeout time.Duration) ([]json.RawMessage, error) {
		*calls = append(*calls, debugCall{cmd, action, info, timeout})
		return replies, err
	}
}

func postDebug(s *Server, target, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.handleAPIDebugCommand(w, httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)))
	return w
}

func TestDebugCommandAPI_DisabledAndMethod(t *testing.T) {
	// Switched off: the endpoint does not exist.
	s := &Server{}
	assert.Equal(t, http.StatusNotFound, postDebug(s, "/api/debug/command", `{"cmd":"get_device_info"}`).Code)

	var calls []debugCall
	s.SetDebugCommandFunc(recordingDebugFunc(&calls, nil, nil))

	w := httptest.NewRecorder()
	s.handleAPIDebugCommand(w, httptest.NewRequest(http.MethodGet, "/api/debug/command", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Empty(t, calls)
}

func TestDebugCommandAPI_Validation(t *testing.T) {
	var calls []debugCall
	s := &Server{}
	s.SetDebugCommandFunc(recordingDebugFunc(&calls, nil, nil))

	assert.Equal(t, http.StatusBadRequest, postDebug(s, "/api/debug/command", `not json`).Code)
	for _, cmd := range []string{"", "x", "GET_device_info", "get device", "get-device", "1get", "get_info;reboot"} {
		body, _ := json.Marshal(map[string]interface{}{"cmd": cmd, "write": true})
		assert.Equal(t, http.StatusBadRequest, postDebug(s, "/api/debug/command", string(body)).Code, "cmd %q", cmd)
	}

	// Anything but a get_ command needs "write": true.
	for _, cmd := range []string{"set_volume_setting", "reboot", "tran_ctl", "getx_setting"} {
		body, _ := json.Marshal(map[string]interface{}{"cmd": cmd})
		assert.Equal(t, http.StatusForbidden, postDebug(s, "/api/debug/command", string(body)).Code, "cmd %q", cmd)
	}
	assert.Empty(t, calls, "nothing may reach the camera when a request is refused")

	assert.Equal(t, http.StatusOK, postDebug(s, "/api/debug/command", `{"cmd":"set_volume_setting","info":{"volume":70},"write":true}`).Code)
	require.Len(t, calls, 1)
	assert.Equal(t, "set_volume_setting", calls[0].cmd)
	assert.Equal(t, float64(70), calls[0].info["volume"])
}

func TestDebugCommandAPI_RepliesAndErrors(t *testing.T) {
	var calls []debugCall
	s := &Server{}
	s.SetDebugCommandFunc(recordingDebugFunc(&calls, []json.RawMessage{json.RawMessage(`{"resp":"get_video_setting","result":"success"}`)}, nil))

	w := postDebug(s, "/api/debug/command", `{"cmd":"get_video_setting","action":"all","info":{},"timeout_ms":1500}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var res struct {
		Cmd     string                   `json:"cmd"`
		Replies []map[string]interface{} `json:"replies"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.Equal(t, "get_video_setting", res.Cmd)
	require.Len(t, res.Replies, 1)
	assert.Equal(t, "success", res.Replies[0]["result"])
	require.Len(t, calls, 1)
	assert.Equal(t, debugCall{"get_video_setting", "all", map[string]interface{}{}, 1500 * time.Millisecond}, calls[0])

	// No or an unreasonable timeout falls back to four seconds.
	for _, body := range []string{`{"cmd":"get_time_setting"}`, `{"cmd":"get_time_setting","timeout_ms":-5}`, `{"cmd":"get_time_setting","timeout_ms":60000}`} {
		calls = nil
		require.Equal(t, http.StatusOK, postDebug(s, "/api/debug/command", body).Code)
		require.Len(t, calls, 1)
		assert.Equal(t, 4*time.Second, calls[0].timeout, body)
	}

	// The camera answered nothing: an empty list, not null.
	s.SetDebugCommandFunc(recordingDebugFunc(&calls, nil, nil))
	w = postDebug(s, "/api/debug/command", `{"cmd":"get_time_setting"}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"replies":[]`)

	// The camera could not be reached: 502 with the reason.
	s.SetDebugCommandFunc(recordingDebugFunc(&calls, nil, errors.New("data channel not available")))
	w = postDebug(s, "/api/debug/command", `{"cmd":"get_time_setting"}`)
	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Contains(t, w.Body.String(), "data channel not available")
}

func TestDebugCommandAPI_CameraParameter(t *testing.T) {
	var first, second []debugCall
	s := &Server{channels: []CameraChannel{
		{Index: 1, Name: "Einfahrt", Path: "einfahrt", DebugCommandFunc: recordingDebugFunc(&first, nil, nil)},
		{Index: 2, Name: "Garten", Path: "garten", DebugCommandFunc: recordingDebugFunc(&second, nil, nil)},
		{Index: 3, Name: "Keller", Path: "keller"},
	}}
	const body = `{"cmd":"get_device_info"}`

	// The endpoint stays switched off as a whole, whatever the channels carry.
	assert.Equal(t, http.StatusNotFound, postDebug(s, "/api/debug/command?camera=garten", body).Code)
	assert.Empty(t, second)

	s.SetDebugCommandFunc(s.channels[0].DebugCommandFunc)

	// Without the parameter the first camera answers.
	require.Equal(t, http.StatusOK, postDebug(s, "/api/debug/command", body).Code)
	assert.Len(t, first, 1)
	assert.Empty(t, second)

	// By path or by name, as for /api/status.
	require.Equal(t, http.StatusOK, postDebug(s, "/api/debug/command?camera=garten", body).Code)
	require.Equal(t, http.StatusOK, postDebug(s, "/api/debug/command?camera=Garten", body).Code)
	assert.Len(t, first, 1)
	assert.Len(t, second, 2)

	// Unknown camera, or one without diagnostic commands: nothing is sent.
	assert.Equal(t, http.StatusNotFound, postDebug(s, "/api/debug/command?camera=dach", body).Code)
	assert.Equal(t, http.StatusNotFound, postDebug(s, "/api/debug/command?camera=keller", body).Code)
	assert.Len(t, first, 1)
	assert.Len(t, second, 2)
}
