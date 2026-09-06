package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type supervisorMQTTResponse struct {
	Result string `json:"result"`
	Data   struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		SSL      bool   `json:"ssl"`
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"data"`
}

// FetchSupervisorMQTTOptions queries Home Assistant's Supervisor API for the official MQTT broker addon credentials.
func FetchSupervisorMQTTOptions() (broker, user, pass string, err error) {
	token := os.Getenv("SUPERVISOR_TOKEN")
	if token == "" {
		token = os.Getenv("HASSIO_TOKEN")
	}
	if token == "" {
		return "", "", "", fmt.Errorf("no supervisor token available")
	}

	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	urls := []string{
		"http://supervisor/services/mqtt",
		"http://hassio/services/mqtt",
		"http://172.30.32.2/services/mqtt",
	}

	var lastErr error
	for _, u := range urls {
		req, reqErr := http.NewRequest("GET", u, nil)
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Supervisor-Token", token)
		req.Header.Set("Content-Type", "application/json")

		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			bodyStr := string(bodyBytes)
			lastErr = fmt.Errorf("supervisor API (%s) returned HTTP %d: %s", u, resp.StatusCode, strings.TrimSpace(bodyStr))
			continue
		}

		var sResp supervisorMQTTResponse
		decErr := json.NewDecoder(resp.Body).Decode(&sResp)
		_ = resp.Body.Close()
		if decErr != nil {
			lastErr = decErr
			continue
		}

		if sResp.Result == "ok" && sResp.Data.Host != "" {
			port := sResp.Data.Port
			if port <= 0 {
				port = 1883
			}
			scheme := "tcp"
			if sResp.Data.SSL {
				scheme = "ssl"
			}
			broker = fmt.Sprintf("%s://%s:%d", scheme, sResp.Data.Host, port)
			user = sResp.Data.Username
			pass = sResp.Data.Password
			return broker, user, pass, nil
		}
	}

	return "", "", "", lastErr
}
