package xiongmai

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client manages the TCP control connection to a Xiongmai/Steinel L 620 CAM on port 34567.
type Client struct {
	addr              string
	user              string
	password          string
	effectivePassword string
	conn              net.Conn
	sessionID         uint32
	sequence          uint32
	mu                sync.Mutex
	isLoggedIn        bool
	debug             bool
	closeChan         chan struct{}
	closed            atomic.Bool
}

// NewClient creates a new Xiongmai Sofia protocol client.
func NewClient(cameraIP string, port int, user, password string, debug bool) *Client {
	if port <= 0 {
		port = DefaultPort
	}
	if user == "" {
		user = "admin"
	}
	return &Client{
		addr:              fmt.Sprintf("%s:%d", cameraIP, port),
		user:              user,
		password:          password,
		effectivePassword: password,
		debug:             debug,
		closeChan:         make(chan struct{}),
	}
}

// GetEffectivePassword returns the actual working password discovered during login.
func (c *Client) GetEffectivePassword() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.effectivePassword
}

// Connect dials the camera and performs the Sofia login handshake.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("failed to connect to camera on %s: %w", c.addr, err)
	}
	c.conn = conn
	c.closed.Store(false)

	// Step 1: Login
	if err := c.loginLocked(); err != nil {
		_ = c.conn.Close()
		c.conn = nil
		return fmt.Errorf("xiongmai login failed: %w", err)
	}

	if c.debug {
		log.Printf("[Xiongmai] ✅ Successfully connected and logged in to %s (SessionID: 0x%08X)", c.addr, c.sessionID)
	}

	return nil
}

// HashPassword generates the 8-character Sofia password hash used by Xiongmai DVR-IP / Sofia daemons.
// The algorithm computes the MD5 digest of the plaintext password, processes byte pairs with modulo 62 (0x3E),
// and maps each pair to the pseudo-base62 alphabet [0-9A-Za-z].
// Note: If secret is empty, an empty string is returned (standard for unauthenticated / default accounts).
//
// CodeQL [go/weak-crypto-password-hashing] Mandated by legacy Xiongmai camera firmware protocol specification.
// CodeQL [go/weak-sensitive-data-hashing] Mandated by legacy Xiongmai camera firmware protocol specification.
// CodeQL [go/weak-crypto-algorithm] Mandated by legacy Xiongmai camera firmware protocol specification.
// lgtm [go/weak-crypto-password-hashing]
// lgtm [go/weak-sensitive-data-hashing]
func HashPassword(secret string) string {
	if secret == "" {
		return ""
	}
	//nolint:gosec // Required by Xiongmai hardware protocol specification
	// CodeQL [go/weak-crypto-password-hashing] Mandated by Xiongmai camera protocol
	digest := md5.Sum([]byte(secret)) // CodeQL [go/weak-crypto-password-hashing] // lgtm [go/weak-crypto-password-hashing]

	var result strings.Builder
	result.Grow(8)
	for i := 0; i < 8; i++ {
		pairSum := int(digest[2*i]) + int(digest[2*i+1])
		pairMod := pairSum % 0x3E
		var charCode byte
		if pairMod <= 9 {
			charCode = byte(pairMod + 0x30) // '0'-'9'
		} else if pairMod <= 35 {
			charCode = byte(pairMod + 0x37) // 'A'-'Z'
		} else {
			charCode = byte(pairMod + 0x3D) // 'a'-'z'
		}
		result.WriteByte(charCode)
	}
	return result.String()
}

// formatLoginError returns a user-friendly error description for Xiongmai login return codes.
func formatLoginError(code int) string {
	switch code {
	case 124:
		return fmt.Sprintf("invalid username or password (code %d: check 'camera_user' and 'camera_password')", code)
	case 125:
		return fmt.Sprintf("user does not exist (code %d: check 'camera_user')", code)
	case 126:
		return fmt.Sprintf("user account is locked (code %d: too many failed login attempts, wait 10 minutes or restart camera)", code)
	case 127:
		return fmt.Sprintf("maximum concurrent connections reached (code %d)", code)
	case 128:
		return fmt.Sprintf("permission denied (code %d)", code)
	case 129:
		return fmt.Sprintf("password format error (code %d)", code)
	default:
		return fmt.Sprintf("login rejected by camera with code %d", code)
	}
}

// passwordCandidate represents a login attempt variant.
type passwordCandidate struct {
	label       string
	user        string
	password    string
	encryptType string
	loginType   string
}

func (c *Client) getPasswordCandidates() []passwordCandidate {
	cleanUser := strings.TrimSpace(c.user)
	if cleanUser == "" {
		cleanUser = "admin"
	}
	cleanPwd := strings.TrimSpace(c.password)

	var candidates []passwordCandidate

	if cleanPwd != "" {
		sofiaHash := HashPassword(cleanPwd)
		//nolint:gosec // Required by Xiongmai hardware protocol specification
		md5Digest := md5.Sum([]byte(cleanPwd))
		hexMD5Lower := hex.EncodeToString(md5Digest[:])
		hexMD5Upper := strings.ToUpper(hexMD5Lower)

		// 1. Sofia 8-char hash with DVRIP-Web, Mobile, MobileDVR, and empty LoginType
		candidates = append(candidates,
			passwordCandidate{label: "Sofia 8-char hash (LoginType: DVRIP-Web)", user: cleanUser, password: sofiaHash, encryptType: "MD5", loginType: "DVRIP-Web"},
			passwordCandidate{label: "Sofia 8-char hash (LoginType: Mobile)", user: cleanUser, password: sofiaHash, encryptType: "MD5", loginType: "Mobile"},
			passwordCandidate{label: "Sofia 8-char hash (LoginType: MobileDVR)", user: cleanUser, password: sofiaHash, encryptType: "MD5", loginType: "MobileDVR"},
			passwordCandidate{label: "Sofia 8-char hash (no LoginType)", user: cleanUser, password: sofiaHash, encryptType: "MD5", loginType: ""},
			// 2. Plaintext password with DVRIP-Web and Mobile
			passwordCandidate{label: "Plaintext password (LoginType: DVRIP-Web)", user: cleanUser, password: cleanPwd, encryptType: "NONE", loginType: "DVRIP-Web"},
			passwordCandidate{label: "Plaintext password (LoginType: Mobile)", user: cleanUser, password: cleanPwd, encryptType: "NONE", loginType: "Mobile"},
			// 3. 32-char Hex MD5 (lower & UPPER)
			passwordCandidate{label: "32-char Hex MD5 lowercase", user: cleanUser, password: hexMD5Lower, encryptType: "MD5", loginType: "DVRIP-Web"},
			passwordCandidate{label: "32-char Hex MD5 UPPERCASE", user: cleanUser, password: hexMD5Upper, encryptType: "MD5", loginType: "DVRIP-Web"},
		)
	}

	// 4. Empty password variants
	candidates = append(candidates,
		passwordCandidate{label: "Empty password (NONE, DVRIP-Web)", user: cleanUser, password: "", encryptType: "NONE", loginType: "DVRIP-Web"},
		passwordCandidate{label: "Empty password (MD5, DVRIP-Web)", user: cleanUser, password: "", encryptType: "MD5", loginType: "DVRIP-Web"},
		passwordCandidate{label: "Empty password (NONE, Mobile)", user: cleanUser, password: "", encryptType: "NONE", loginType: "Mobile"},
		passwordCandidate{label: "Empty password (no LoginType)", user: cleanUser, password: "", encryptType: "NONE", loginType: ""},
	)

	// 5. Alternate default accounts on Xiongmai hardware
	if cleanUser == "admin" {
		candidates = append(candidates,
			passwordCandidate{label: "User 'default' (empty)", user: "default", password: "", encryptType: "NONE", loginType: "DVRIP-Web"},
			passwordCandidate{label: "User 'default' (pass: tluafed)", user: "default", password: "tluafed", encryptType: "NONE", loginType: "DVRIP-Web"},
			passwordCandidate{label: "User 'root' (empty)", user: "root", password: "", encryptType: "NONE", loginType: "DVRIP-Web"},
		)
		if cleanPwd != "" {
			candidates = append(candidates,
				passwordCandidate{label: "User 'default' (Sofia hash)", user: "default", password: HashPassword(cleanPwd), encryptType: "MD5", loginType: "DVRIP-Web"},
				passwordCandidate{label: "User 'root' (Sofia hash)", user: "root", password: HashPassword(cleanPwd), encryptType: "MD5", loginType: "DVRIP-Web"},
			)
		}
	}

	return candidates
}

// MaskPassword returns a safely masked representation of a password for debug logging.
// It shows the first 3 characters followed by asterisks for the remaining characters.
// If the password has 3 or fewer characters, all characters are masked as asterisks.
func MaskPassword(pwd string) string {
	if pwd == "" {
		return "<empty>"
	}
	runes := []rune(pwd)
	if len(runes) <= 3 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:3]) + strings.Repeat("*", len(runes)-3)
}

// loginLocked performs the OPUserLogin command with automated password format fallback.
func (c *Client) loginLocked() error {
	if c.debug {
		log.Printf("[Xiongmai] 🔍 Login check: user=%q, password=%s (length: %d chars)",
			c.user, MaskPassword(c.password), len(c.password))
	}

	candidates := c.getPasswordCandidates()
	var lastErr error

	for i, cand := range candidates {
		loginReq := LoginReq{
			EncryptType: cand.encryptType,
			LoginType:   cand.loginType,
			PassWord:    cand.password,
			UserName:    cand.user,
		}

		payload, err := json.Marshal(loginReq)
		if err != nil {
			return err
		}

		respData, err := c.sendPacketLocked(MsgLoginReq, payload)
		if err != nil {
			return err
		}

		var resp LoginResp
		if err := json.Unmarshal(respData, &resp); err != nil {
			return fmt.Errorf("failed to parse login response: %w (raw: %s)", err, string(respData))
		}

		if resp.Ret == 100 || resp.Ret == 0 {
			sessionStr := strings.TrimPrefix(resp.SessionID, "0x")
			if sessionStr != "" {
				if sID, err := strconv.ParseUint(sessionStr, 16, 32); err == nil {
					c.sessionID = uint32(sID)
				}
			}

			if cand.password == "" {
				c.effectivePassword = ""
			} else {
				c.effectivePassword = cand.password
			}

			c.isLoggedIn = true
			log.Printf("[Xiongmai] 🔑 Authenticated successfully using %s (User: %s, SessionID: 0x%08X)", cand.label, cand.user, c.sessionID)
			return nil
		}

		log.Printf("[Xiongmai] ℹ️ Candidate #%d [%s] rejected by camera (Ret: %d)", i+1, cand.label, resp.Ret)
		if c.debug {
			log.Printf("[Xiongmai] 🔍 Raw response payload: %s", string(respData))
		}
		lastErr = fmt.Errorf("camera login rejected: %s", formatLoginError(resp.Ret))
		if resp.Ret != 124 {
			return lastErr
		}
	}

	log.Printf("[Xiongmai] ❌ All %d authentication candidates rejected by camera (check username and device password)", len(candidates))
	return lastErr
}

// EnableRTSP ensures that the internal RTSP server on port 554 is activated on the camera.
func (c *Client) EnableRTSP() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	req := RTSPConfigReq{
		Name: "NetWork.RTSP",
		NetWorkRTSP: RTSPServer{
			IsServer: true,
		},
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}

	if c.debug {
		log.Printf("[Xiongmai] 📡 Enabling RTSP server on camera...")
	}

	_, err = c.sendPacketLocked(MsgConfigSetReq, payload)
	if err != nil {
		return fmt.Errorf("failed to enable RTSP server: %w", err)
	}

	log.Printf("[Xiongmai] 🎥 RTSP server enabled on camera port %d", RTSPPort)
	return nil
}

// SetLightState switches the main lamp on or off via FbExtraStateCtrl.
func (c *Client) SetLightState(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ison := 0
	if on {
		ison = 1
	}

	req := LightCtrlReq{
		Name: "FbExtraStateCtrl",
		FbExtraStateCtrl: FbExtraStateCtrlVal{
			IsOn: ison,
		},
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}

	_, err = c.sendPacketLocked(MsgConfigSetReq, payload)
	return err
}

// QueryLightState queries the current light state from FbExtraStateCtrl.
func (c *Client) QueryLightState() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	req := map[string]string{
		"Name": "FbExtraStateCtrl",
	}
	payload, _ := json.Marshal(req)

	respData, err := c.sendPacketLocked(MsgConfigGetReq, payload)
	if err != nil {
		return false, err
	}

	var resp LightCtrlReq
	if err := json.Unmarshal(respData, &resp); err != nil {
		return false, err
	}

	return resp.FbExtraStateCtrl.IsOn == 1, nil
}

// QueryMCUConfig queries the Steinel MCU configuration frame ("BFbU").
func (c *Client) QueryMCUConfig() (*MCUConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	req := SerialPortsReq{
		Name: "SerialPortsInfo",
		SerialPortsInfo: SerialPortsData{
			SerialPortsType: 0,
			SerialPortsData: BuildQueryMCUCommand(),
		},
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	respData, err := c.sendPacketLocked(MsgSysManagerReq, payload)
	if err != nil {
		return nil, err
	}

	var resp SerialPortsReq
	if err := json.Unmarshal(respData, &resp); err != nil {
		// Response might be raw string
		return ParseMCUString(string(respData))
	}

	return ParseMCUString(resp.SerialPortsInfo.SerialPortsData)
}

// SendMCUCommand sends a raw MCU serial port command (e.g. "BXaU").
func (c *Client) SendMCUCommand(cmd string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	req := SerialPortsReq{
		Name: "SerialPortsInfo",
		SerialPortsInfo: SerialPortsData{
			SerialPortsType: 0,
			SerialPortsData: cmd,
		},
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}

	_, err = c.sendPacketLocked(MsgSysManagerReq, payload)
	return err
}

// SetLux sets the twilight threshold in lux.
func (c *Client) SetLux(lux int) error {
	return c.SendMCUCommand(BuildSetLuxCommand(lux))
}

// SetDistance sets the PIR motion detection distance (1-10 meters).
func (c *Client) SetDistance(dist int) error {
	return c.SendMCUCommand(BuildSetDistanceCommand(dist))
}

// SetHighlight sets the main light brightness percentage (10-100%).
func (c *Client) SetHighlight(percent int) error {
	return c.SendMCUCommand(BuildSetHighlightCommand(percent))
}

// SetLowlight sets the nightlight brightness percentage (0-50%).
func (c *Client) SetLowlight(percent int) error {
	return c.SendMCUCommand(BuildSetLowlightCommand(percent))
}

// SetHighlightDelay sets the main light delay in seconds.
func (c *Client) SetHighlightDelay(seconds int) error {
	return c.SendMCUCommand(BuildSetHighlightDelayCommand(seconds))
}

// SetLowlightDuration sets the nightlight duration.
func (c *Client) SetLowlightDuration(dur int) error {
	return c.SendMCUCommand(BuildSetLowlightDurationCommand(dur))
}

// SendKeepAlive sends a heartbeat packet to prevent connection timeout.
func (c *Client) SendKeepAlive() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	req := map[string]interface{}{
		"Name":      "KeepAlive",
		"SessionID": fmt.Sprintf("0x%08X", c.sessionID),
	}
	payload, _ := json.Marshal(req)
	_, err := c.sendPacketLocked(MsgKeepAliveReq, payload)
	return err
}

// sendPacketLocked encodes the Sofia header, sends the packet and reads the response.
func (c *Client) sendPacketLocked(msgID uint16, payload []byte) ([]byte, error) {
	if c.conn == nil {
		return nil, errors.New("connection is closed")
	}

	c.sequence++
	// Sofia payloads typically end with a null terminator or newline
	dataWithTerminator := append(payload, 0x0A, 0x00)

	hdr := Header{
		Magic:      HeaderMagic,
		Channel:    0,
		SessionID:  c.sessionID,
		Sequence:   c.sequence,
		MsgID:      msgID,
		DataLength: uint32(len(dataWithTerminator)),
	}

	packet := append(hdr.Encode(), dataWithTerminator...)

	_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write(packet); err != nil {
		return nil, fmt.Errorf("write error: %w", err)
	}

	// Read response header (20 bytes)
	respHdrBuf := make([]byte, HeaderLength)
	if _, err := io.ReadFull(c.conn, respHdrBuf); err != nil {
		return nil, fmt.Errorf("failed to read response header: %w", err)
	}

	respHdr, err := DecodeHeader(respHdrBuf)
	if err != nil {
		return nil, err
	}

	if respHdr.DataLength > 65535 {
		return nil, fmt.Errorf("response payload too large: %d bytes", respHdr.DataLength)
	}

	respPayload := make([]byte, respHdr.DataLength)
	if _, err := io.ReadFull(c.conn, respPayload); err != nil {
		return nil, fmt.Errorf("failed to read response payload: %w", err)
	}

	// Trim trailing null/newlines
	cleanPayload := strings.TrimRight(string(respPayload), "\x00\r\n ")
	return []byte(cleanPayload), nil
}

// Close gracefully logs out and closes the connection.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	close(c.closeChan)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		if c.isLoggedIn {
			req := map[string]interface{}{
				"Name":      "OPUserLogout",
				"SessionID": fmt.Sprintf("0x%08X", c.sessionID),
			}
			payload, _ := json.Marshal(req)
			_, _ = c.sendPacketLocked(MsgLogoutReq, payload)
			c.isLoggedIn = false
		}
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}
