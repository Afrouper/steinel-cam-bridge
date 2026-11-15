// Package redact removes credentials from JSON documents before they are
// shown to a person or written to a log.
package redact

import (
	"encoding/json"
	"strings"
)

// Placeholder replaces every redacted value.
const Placeholder = "[redacted]"

// sensitiveKeyParts are matched case-insensitively against JSON object keys.
var sensitiveKeyParts = []string{
	"pass", "pwd", "psk", "token", "secret", "key", "sct", "auth", "cert", "credential", "sign",
}

// IsSensitiveKey reports whether a JSON key is likely to hold a credential.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// JSON returns the document with the values of all sensitive keys replaced.
// Input that is not valid JSON is replaced entirely, since it cannot be inspected.
func JSON(raw []byte) []byte {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		out, _ := json.Marshal(map[string]string{"unparsed": Placeholder})
		return out
	}
	out, err := json.Marshal(Value(v))
	if err != nil {
		out, _ = json.Marshal(map[string]string{"unparsed": Placeholder})
	}
	return out
}

// Value redacts a decoded JSON value in place and returns it.
func Value(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			if IsSensitiveKey(k) {
				t[k] = Placeholder
				continue
			}
			t[k] = Value(val)
		}
		return t
	case []interface{}:
		for i := range t {
			t[i] = Value(t[i])
		}
		return t
	default:
		return v
	}
}
