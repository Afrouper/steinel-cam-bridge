package nabto

import "strings"

// ParseQRCode extracts camera credentials from the Steinel app QR-code payload string
// e.g. "did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx"
func ParseQRCode(qr string, cfg *Config) {
	if cfg == nil {
		return
	}
	parts := strings.Split(qr, ",")
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			k, v := kv[0], kv[1]
			switch k {
			case "did":
				cfg.DeviceID = v
			case "pid":
				cfg.ProductID = v
			case "sct":
				cfg.SCT = v
			case "pairPwd":
				cfg.PairPwd = v
			}
		}
	}
}
