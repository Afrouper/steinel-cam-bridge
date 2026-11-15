package redact

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONRedactsCredentials(t *testing.T) {
	in := `{"resp":"get_wifi_setting","info":{"ssid":"Home","password":"hunter2","PSK":"abc","nested":[{"pairPwd":"x","ok":1},{"access_token":"t","list":["a"]}],"apiKey":"k","sct":"s","quality":{"sub1":{"resolution":"1080p"}}}}`
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal(JSON([]byte(in)), &got))

	info := got["info"].(map[string]interface{})
	assert.Equal(t, "get_wifi_setting", got["resp"])
	assert.Equal(t, "Home", info["ssid"])
	for _, k := range []string{"password", "PSK", "apiKey", "sct"} {
		assert.Equal(t, Placeholder, info[k], k)
	}
	nested := info["nested"].([]interface{})
	assert.Equal(t, Placeholder, nested[0].(map[string]interface{})["pairPwd"])
	assert.Equal(t, float64(1), nested[0].(map[string]interface{})["ok"])
	assert.Equal(t, Placeholder, nested[1].(map[string]interface{})["access_token"])
	assert.Equal(t, "1080p", info["quality"].(map[string]interface{})["sub1"].(map[string]interface{})["resolution"])

	assert.NotContains(t, string(JSON([]byte(in))), "hunter2")
}

func TestJSONHidesUnparsableInput(t *testing.T) {
	out := string(JSON([]byte(`password=hunter2`)))
	assert.NotContains(t, out, "hunter2")
	assert.Contains(t, out, Placeholder)
}

func TestSensitiveObjectIsReplacedWhole(t *testing.T) {
	out := string(JSON([]byte(`{"auth":{"user":"a","value":"b"},"plain":"x"}`)))
	assert.NotContains(t, out, `"value"`)
	assert.Contains(t, out, `"plain":"x"`)
}
