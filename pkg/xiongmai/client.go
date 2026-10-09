package xiongmai

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

// Client manages the TCP control connection to a Xiongmai/Steinel L 620 CAM on port 34567.
type Client struct {
	addr              string
	user              string
	password          string
	effectivePassword string
	communicateKey    []byte
	conn              net.Conn
	sessionID         uint32
	sequence          uint32
	mu                sync.Mutex
	isLoggedIn        bool
	closeChan         chan struct{}
	closed            atomic.Bool
}

// NewClient creates a new Xiongmai Sofia protocol client.
func NewClient(cameraIP string, port int, user, password string) *Client {
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

	logger.Trace("Xiongmai", "🔌 Dialing camera TCP control port on %s...", c.addr)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("failed to connect to camera on %s: %w", c.addr, err)
	}
	c.conn = conn
	c.closed.Store(false)
	logger.Trace("Xiongmai", "🔌 TCP socket connected to %s, starting login handshake", c.addr)

	// Step 1: Login
	if err := c.loginLocked(); err != nil {
		_ = c.conn.Close()
		c.conn = nil
		return fmt.Errorf("xiongmai login failed: %w", err)
	}

	if c.isLoggedIn {
		logger.Info("Xiongmai", "✅ Successfully connected and authenticated on %s (SessionID: 0x%08X)", c.addr, c.sessionID)
	} else {
		logger.Info("Xiongmai", "📡 TCP connection active on %s in Resilient Streaming Mode (SessionID: 0x%08X)", c.addr, c.sessionID)
	}

	return nil
}

// computeMD5 returns the 16-byte MD5 digest for data using hash.Hash streaming.
// Mandated by legacy Xiongmai camera firmware protocol specification.
func computeMD5(data []byte) [16]byte {
	//nolint:gosec // Required by Xiongmai hardware protocol specification
	h := md5.New()
	_, _ = h.Write(data)
	var out [16]byte
	copy(out[:], h.Sum(nil))
	return out
}

// computeMD5Hex returns the 32-character lowercase hex MD5 string for data.
// Mandated by legacy Xiongmai camera firmware protocol specification.
func computeMD5Hex(data []byte) string {
	//nolint:gosec // Required by Xiongmai hardware protocol specification
	h := md5.New()
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
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
	digest := computeMD5([]byte(secret))

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

// HashMD5Hex generates the 32-character lowercase hex MD5 password digest mandated by Xiongmai / JFTech Sofia protocol.
//
// CodeQL [go/weak-crypto-password-hashing] Mandated by Xiongmai camera firmware protocol specification.
// CodeQL [go/weak-sensitive-data-hashing] Mandated by Xiongmai camera firmware protocol specification.
// CodeQL [go/weak-crypto-algorithm] Mandated by Xiongmai camera firmware protocol specification.
// lgtm [go/weak-crypto-password-hashing]
// lgtm [go/weak-sensitive-data-hashing]
func HashMD5Hex(raw string) string {
	if raw == "" {
		return ""
	}
	return computeMD5Hex([]byte(raw))
}

// HashDoubleMD5Hex generates the double-MD5 hex password digest (md5(md5(raw))) for Xiongmai Web/Cloud DVR-IP.
//
// CodeQL [go/weak-crypto-password-hashing] Mandated by Xiongmai camera firmware protocol specification.
// CodeQL [go/weak-sensitive-data-hashing] Mandated by Xiongmai camera firmware protocol specification.
// CodeQL [go/weak-crypto-algorithm] Mandated by Xiongmai camera firmware protocol specification.
// lgtm [go/weak-crypto-password-hashing]
// lgtm [go/weak-sensitive-data-hashing]
func HashDoubleMD5Hex(raw string) string {
	if raw == "" {
		return ""
	}
	first := HashMD5Hex(raw)
	return computeMD5Hex([]byte(first))
}

// formatLoginError returns a user-friendly error description for Xiongmai login return codes.
func formatLoginError(code int) string {
	switch code {
	case 100:
		return "success (code 100)"
	case 106:
		return fmt.Sprintf("invalid password (code %d: check 'camera_password')", code)
	case 124:
		return fmt.Sprintf("password encryption algorithm not supported (code %d: EE_ACCOUNT_PWD_ENCRYPT_ERROR / LOGIN_ENC_PWD_NOT_SUP)", code)
	case 125:
		return fmt.Sprintf("user does not exist (code %d: check 'camera_user')", code)
	case 126:
		return fmt.Sprintf("user account is locked (code %d: too many failed login attempts, wait 10 minutes or restart camera)", code)
	case 127:
		return fmt.Sprintf("maximum concurrent connections reached (code %d)", code)
	case 128:
		return fmt.Sprintf("permission denied (code %d)", code)
	case 129:
		return fmt.Sprintf("password format error (code %d: EE_ACCOUNT_PWD_FORMAT_ERROR)", code)
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

		// 1. Sofia 8-character Base62 MD5 Hash (Legacy Xiongmai / V4.02.R12 default)
		candidates = append(candidates,
			passwordCandidate{label: "Sofia 8-char hash (LoginType: DVRIP-Mobile)", user: cleanUser, password: sofiaHash, encryptType: "MD5", loginType: "DVRIP-Mobile"},
			passwordCandidate{label: "Sofia 8-char hash (no LoginType)", user: cleanUser, password: sofiaHash, encryptType: "MD5", loginType: ""},
		)

		// 2. Plaintext Password
		candidates = append(candidates,
			passwordCandidate{label: "Plaintext password (no LoginType)", user: cleanUser, password: cleanPwd, encryptType: "NONE", loginType: ""},
		)
	} else {
		// 3. Empty Password (Unconfigured / Factory-default accounts)
		candidates = append(candidates,
			passwordCandidate{label: "Empty password (no LoginType)", user: cleanUser, password: "", encryptType: "NONE", loginType: ""},
		)
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

// queryClaimLocked queries camera encryption and authentication capabilities via OPMonitor Claim (MsgID 1413).
func (c *Client) queryClaimLocked() (*MonitorClaimResp, error) {
	claimReq := MonitorClaimReq{
		Name: "OPMonitor",
		OPMonitor: MonitorClaimParam{
			Action: "Claim",
			Parameter: MonitorClaimParamDetails{
				Channel:    0,
				CombinMode: "CONNECT_ALL",
				StreamType: "Main",
				TransMode:  "TCP",
			},
		},
		SessionID: "0x000001869f",
	}

	payload, err := json.Marshal(claimReq)
	if err != nil {
		return nil, err
	}

	logger.Trace("Xiongmai", "-> Probing camera capabilities via OPMonitor Claim (MsgID 1413)")
	respData, respHdr, err := c.sendRawPacketWithHeaderLocked(MsgMonitorClaimReq, 0x01, 0x63, 0x0001869F, payload)
	if err != nil {
		return nil, fmt.Errorf("OPMonitor Claim request failed: %w", err)
	}
	if respHdr == nil || respHdr.MsgID != MsgMonitorClaimResp {
		return nil, fmt.Errorf("unexpected response MsgID %d (expected %d)", respHdr.MsgID, MsgMonitorClaimResp)
	}

	var claim MonitorClaimResp
	rawStr := strings.TrimSpace(string(respData))
	if strings.HasPrefix(rawStr, "{") {
		if err := json.Unmarshal([]byte(rawStr), &claim); err != nil {
			return nil, fmt.Errorf("failed to parse plain OPMonitor Claim response: %w", err)
		}
	} else {
		cipherBytes, err := base64.StdEncoding.DecodeString(rawStr)
		if err != nil {
			return nil, fmt.Errorf("failed to base64-decode OPMonitor Claim response: %w", err)
		}
		plainBytes, err := decryptAES128CBC([]byte(DefaultSofiaAESKey), cipherBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt OPMonitor Claim response: %w", err)
		}
		logger.Trace("Xiongmai", "🔐 Decrypted OPMonitor Claim response: %s", string(plainBytes))
		if err := json.Unmarshal(plainBytes, &claim); err != nil {
			return nil, fmt.Errorf("failed to unmarshal decrypted OPMonitor Claim response: %w", err)
		}
	}

	if claim.Ret != 100 && claim.Ret != 0 {
		return nil, fmt.Errorf("OPMonitor Claim returned code %d", claim.Ret)
	}

	return &claim, nil
}

// loginRSALocked performs RSA PKCS#1 v1.5 and AES-128-CBC encrypted authentication (mandated by V4.03.R12).
func (c *Client) loginRSALocked(claim *MonitorClaimResp) error {
	pubKey, err := parseRSAPublicKey(claim.PublicKey)
	if err != nil {
		return fmt.Errorf("invalid RSA public key from camera (%q): %w", claim.PublicKey, err)
	}

	commKeyStr, err := generateCommunicateKey()
	if err != nil {
		return fmt.Errorf("failed to generate communicate key: %w", err)
	}
	c.communicateKey = []byte(commKeyStr)

	cleanUser := strings.TrimSpace(c.user)
	if cleanUser == "" {
		cleanUser = "admin"
	}
	cleanPwd := strings.TrimSpace(c.password)

	encUserHex, err := encryptRSAPublicKey(pubKey, []byte(cleanUser))
	if err != nil {
		return fmt.Errorf("failed to encrypt username with RSA: %w", err)
	}
	encCommHex, err := encryptRSAPublicKey(pubKey, []byte(commKeyStr))
	if err != nil {
		return fmt.Errorf("failed to encrypt communicate key with RSA: %w", err)
	}

	type rsaCand struct {
		label string
		val   string
	}
	var pwdCandidates []rsaCand
	if cleanPwd != "" {
		pwdCandidates = append(pwdCandidates,
			rsaCand{label: "Sofia 8-char hash", val: HashPassword(cleanPwd)},
			rsaCand{label: "Plaintext", val: cleanPwd},
			rsaCand{label: "Standard 32-char Hex MD5 (lowercase)", val: HashMD5Hex(cleanPwd)},
			rsaCand{label: "Standard 32-char Hex MD5 (uppercase)", val: strings.ToUpper(HashMD5Hex(cleanPwd))},
		)
	} else {
		pwdCandidates = append(pwdCandidates,
			rsaCand{label: "Empty password", val: ""},
		)
	}

	var lastErr error
	for i, cand := range pwdCandidates {
		encPassHex, err := encryptRSAPublicKey(pubKey, []byte(cand.val))
		if err != nil {
			return fmt.Errorf("failed to encrypt password with RSA: %w", err)
		}

		loginReq := LoginReq{
			EncryptType:    "MD5",
			LoginType:      "DVRIP-FutureHome",
			UserName:       encUserHex,
			PassWord:       encPassHex,
			CommunicateKey: encCommHex,
		}

		innerJSON, err := json.Marshal(loginReq)
		if err != nil {
			return err
		}

		ciphertext, err := encryptAES128CBC([]byte(DefaultSofiaAESKey), innerJSON)
		if err != nil {
			return fmt.Errorf("failed to AES encrypt login payload: %w", err)
		}

		b64Payload := []byte(base64.StdEncoding.EncodeToString(ciphertext))

		logger.Trace("Xiongmai", "-> Sofia MsgID: %d (0x%04X), Seq: %d [RSA Candidate #%d/%d: %s (User: %s, LoginType: DVRIP-FutureHome)]",
			MsgLoginReq, MsgLoginReq, c.sequence+1, i+1, len(pwdCandidates), cand.label, cleanUser)

		respData, respHdr, err := c.sendRawPacketWithHeaderLocked(MsgLoginReq, 0x01, 0x63, 0x00000000, b64Payload)
		if err != nil {
			return err
		}
		if respHdr != nil {
			logger.Trace("Xiongmai", "<- Sofia MsgID: %d (0x%04X), Seq: %d, Data: %s", respHdr.MsgID, respHdr.MsgID, respHdr.Sequence, string(respData))
		}

		respStr := strings.TrimSpace(string(respData))
		var resp LoginResp
		if strings.HasPrefix(respStr, "{") {
			if err := json.Unmarshal([]byte(respStr), &resp); err != nil {
				return fmt.Errorf("failed to parse login response: %w (raw: %s)", err, respStr)
			}
		} else {
			cipherBytes, decErr := base64.StdEncoding.DecodeString(respStr)
			if decErr == nil {
				plainBytes, decErr2 := decryptAES128CBC([]byte(DefaultSofiaAESKey), cipherBytes)
				if decErr2 == nil {
					_ = json.Unmarshal(plainBytes, &resp)
				}
			}
			if resp.Ret == 0 && resp.SessionID == "" {
				return fmt.Errorf("failed to parse encrypted login response: %s", respStr)
			}
		}

		sessionStr := strings.TrimPrefix(resp.SessionID, "0x")
		var parsedSessionID uint32
		if sessionStr != "" {
			if sID, err := strconv.ParseUint(sessionStr, 16, 32); err == nil {
				parsedSessionID = uint32(sID)
			}
		}

		if resp.Ret == 100 || resp.Ret == 0 {
			c.sessionID = parsedSessionID
			c.effectivePassword = cand.val
			c.isLoggedIn = true
			logger.Info("Xiongmai", "🔑 Authenticated successfully using RSA/AES (%s, User: %s, SessionID: 0x%08X)",
				cand.label, cleanUser, c.sessionID)
			return nil
		}

		logger.Debug("Xiongmai", "ℹ️ RSA Candidate #%d [%s] rejected by camera (Ret: %d: %s)", i+1, cand.label, resp.Ret, formatLoginError(resp.Ret))
		lastErr = fmt.Errorf("camera RSA login rejected: %s", formatLoginError(resp.Ret))
		if resp.Ret != 106 && resp.Ret != 124 && resp.Ret != 129 {
			return lastErr
		}
	}

	return lastErr
}

// loginLocked performs adaptive authentication negotiation:
// 1. Probes camera encryption and authentication capabilities via OPMonitor Claim (MsgID 1413).
// 2. If camera advertises RSA encryption (mandated by V4.03.R12), executes RSA-1024 + AES-128 handshake.
// 3. If Claim is unsupported or camera specifies legacy auth, attempts streamlined legacy candidates.
func (c *Client) loginLocked() error {
	logger.Debug("Xiongmai", "🔍 Login check: user=%q (password configured: %v, length: %d chars)",
		c.user, c.password != "", len(c.password))

	// Step 1: Probe camera encryption and authentication capabilities via OPMonitor Claim
	claim, err := c.queryClaimLocked()
	if err != nil {
		logger.Debug("Xiongmai", "ℹ️ OPMonitor Claim probe not supported or failed (%v), falling back to legacy login", err)
	} else if claim != nil {
		logger.Trace("Xiongmai", "🔐 Camera advertised security: EncryptAlgo=%q Bits=%d RSA=%v MD5=%v NONE=%v",
			claim.EncryptAlgo, claim.Bits, claim.LoginEncryptionType.RSA, claim.LoginEncryptionType.MD5, claim.LoginEncryptionType.NONE)

		if claim.LoginEncryptionType.RSA && claim.PublicKey != "" {
			logger.Debug("Xiongmai", "🔐 Camera mandates RSA encrypted login (Bits: %d, Algo: %s)", claim.Bits, claim.EncryptAlgo)
			if rsaErr := c.loginRSALocked(claim); rsaErr == nil {
				return nil
			} else {
				logger.Warn("Xiongmai", "⚠️ RSA login attempt failed: %v", rsaErr)
			}
		}
	}

	// Step 2: Streamlined legacy authentication fallback
	candidates := c.getPasswordCandidates()
	var lastErr error
	var fallbackSessionID uint32

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

		// Send login packet via sendRawPacketLocked to prevent logging credentials in Trace mode (CodeQL: CWE-312)
		logger.Trace("Xiongmai", "-> Sofia MsgID: %d (0x%04X), Seq: %d [Candidate #%d/%d: %s (User: %s, EncryptType: %s, LoginType: %q, PwdLen: %d)]",
			MsgLoginReq, MsgLoginReq, c.sequence+1, i+1, len(candidates), cand.label, cand.user, cand.encryptType, cand.loginType, len(cand.password))
		respData, respHdr, err := c.sendRawPacketLocked(MsgLoginReq, payload)
		if err != nil {
			return err
		}
		if respHdr != nil {
			logger.Trace("Xiongmai", "<- Sofia MsgID: %d (0x%04X), Seq: %d, Data: %s", respHdr.MsgID, respHdr.MsgID, respHdr.Sequence, string(respData))
		}

		var resp LoginResp
		if err := json.Unmarshal(respData, &resp); err != nil {
			return fmt.Errorf("failed to parse login response: %w (raw: %s)", err, string(respData))
		}

		sessionStr := strings.TrimPrefix(resp.SessionID, "0x")
		if sessionStr != "" {
			if sID, err := strconv.ParseUint(sessionStr, 16, 32); err == nil {
				fallbackSessionID = uint32(sID)
			}
		}

		if resp.Ret == 100 || resp.Ret == 0 {
			c.sessionID = fallbackSessionID
			if cand.password == "" {
				c.effectivePassword = ""
			} else {
				c.effectivePassword = cand.password
			}

			c.isLoggedIn = true
			logger.Info("Xiongmai", "🔑 Authenticated successfully using %s (User: %s, SessionID: 0x%08X)", cand.label, cand.user, c.sessionID)
			return nil
		}

		logger.Debug("Xiongmai", "ℹ️ Candidate #%d [%s] rejected by camera (Ret: %d: %s)", i+1, cand.label, resp.Ret, formatLoginError(resp.Ret))
		logger.Trace("Xiongmai", "🔍 Raw response payload: %s", string(respData))
		if resp.EncryptAlgo != "" || resp.PublicKey != "" || resp.Token != "" || resp.Bits != 0 {
			logger.Trace("Xiongmai", "🔐 Camera advertised security parameters: EncryptAlgo=%q Bits=%d Token=%q PublicKey=%s",
				resp.EncryptAlgo, resp.Bits, resp.Token, resp.PublicKey)
		}
		var rawMap map[string]interface{}
		if err := json.Unmarshal(respData, &rawMap); err == nil {
			for k, v := range rawMap {
				if k != "Name" && k != "Ret" && k != "SessionID" {
					logger.Trace("Xiongmai", "🔍 Camera login response parameter: %s = %v", k, v)
				}
			}
		}

		lastErr = fmt.Errorf("camera login rejected: %s", formatLoginError(resp.Ret))
		if resp.Ret != 124 && resp.Ret != 129 {
			return lastErr
		}
	}

	// Resilient fallback mode when camera returns Code 124 (encryption subsystem mismatch)
	if fallbackSessionID != 0 {
		c.sessionID = fallbackSessionID
		c.effectivePassword = strings.TrimSpace(c.password)
		c.isLoggedIn = false
		logger.Warn("Xiongmai", "⚠️ Sofia login returned code 124 (EE_ACCOUNT_PWD_ENCRYPT_ERROR: auth subsystem inactive or password mismatch)")
		logger.Info("Xiongmai", "📡 Proceeding in Resilient Streaming Mode with assigned SessionID 0x%08X (RTSP Port 554 active)", c.sessionID)
		return nil
	}

	logger.Error("Xiongmai", "❌ All %d authentication candidates rejected by camera (check username and device password)", len(candidates))
	return lastErr
}

// IsLoggedIn returns whether a fully authenticated session exists on port 34567.
func (c *Client) IsLoggedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isLoggedIn
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

	logger.Debug("Xiongmai", "📡 Enabling RTSP server on camera...")

	_, err = c.sendPacketLocked(MsgConfigSetReq, payload)
	if err != nil {
		return fmt.Errorf("failed to enable RTSP server: %w", err)
	}

	logger.Info("Xiongmai", "🎥 RTSP server enabled on camera port %d", RTSPPort)
	return nil
}

// SetLightState switches the main lamp on or off via FbExtraStateCtrl.
func (c *Client) SetLightState(on bool) error {
	logger.Trace("Xiongmai", "-> Setting light state: on=%v", on)
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
	if err == nil {
		logger.Trace("Xiongmai", "<- Light state set successfully to on=%v", on)
	}
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

	logger.Trace("Xiongmai", "<- QueryLightState result: ison=%d", resp.FbExtraStateCtrl.IsOn)
	return resp.FbExtraStateCtrl.IsOn == 1, nil
}

// QueryMCUConfig queries the Steinel MCU configuration frame ("BFbU").
func (c *Client) QueryMCUConfig() (*MCUConfig, error) {
	logger.Trace("Xiongmai MCU", "-> Querying MCU configuration (BFbU)...")
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
		cfg, parseErr := ParseMCUString(string(respData))
		if parseErr == nil && cfg != nil {
			logger.Trace("Xiongmai MCU", "<- Parsed MCU config from raw string: Distance=%d, Highlight=%d%%, Delay=%ds, Lux=%d, Lowlight=%d%%",
				cfg.Distance, cfg.Highlight, cfg.HighlightDelaySec, cfg.TwilightLux, cfg.Lowlight)
		}
		return cfg, parseErr
	}

	cfg, parseErr := ParseMCUString(resp.SerialPortsInfo.SerialPortsData)
	if parseErr == nil && cfg != nil {
		logger.Trace("Xiongmai MCU", "<- Parsed MCU config: Distance=%d, Highlight=%d%%, Delay=%ds, Lux=%d, Lowlight=%d%%",
			cfg.Distance, cfg.Highlight, cfg.HighlightDelaySec, cfg.TwilightLux, cfg.Lowlight)
	}
	return cfg, parseErr
}

// SendMCUCommand sends a raw MCU serial port command (e.g. "BXaU").
func (c *Client) SendMCUCommand(cmd string) error {
	logger.Trace("Xiongmai MCU", "-> MCU Command: %q", cmd)
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
	logger.Trace("Xiongmai", "-> Sending KeepAlive ping (SessionID: 0x%08X)", c.sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()

	req := map[string]interface{}{
		"Name":      "KeepAlive",
		"SessionID": fmt.Sprintf("0x%08X", c.sessionID),
	}
	payload, _ := json.Marshal(req)
	_, err := c.sendPacketLocked(MsgKeepAliveReq, payload)
	if err == nil {
		logger.Trace("Xiongmai", "<- KeepAlive acknowledged")
	}
	return err
}

// sendRawPacketWithHeaderLocked encodes the Sofia header with custom channel, totalPkt and sessionID,
// sends the packet and reads the response without logging request payloads (essential to prevent sensitive
// credentials like PassWord from leaking - CWE-312).
func (c *Client) sendRawPacketWithHeaderLocked(msgID uint16, channel byte, totalPkt byte, sessionID uint32, payload []byte) ([]byte, *Header, error) {
	if c.conn == nil {
		return nil, nil, errors.New("connection is closed")
	}

	c.sequence++
	// Sofia payloads typically end with a null terminator or newline.
	// NetIP protocol (Channel == 0x01) uses a single 0x00 terminator,
	// while legacy DVRIP (Channel == 0x00) uses 0x0A, 0x00.
	var dataWithTerminator []byte
	if channel == 0x01 {
		dataWithTerminator = append(payload, 0x00)
	} else {
		dataWithTerminator = append(payload, 0x0A, 0x00)
	}

	hdr := Header{
		Magic:      HeaderMagic,
		Channel:    channel,
		SessionID:  sessionID,
		Sequence:   c.sequence,
		TotalPkt:   totalPkt,
		MsgID:      msgID,
		DataLength: uint32(len(dataWithTerminator)),
	}

	logger.Trace("Xiongmai", "-> Sofia Raw Packet: MsgID=%d (0x%04X), Seq=%d, Channel=0x%02X, TotalPkt=0x%02X, SessionID=0x%08X, Len=%d",
		hdr.MsgID, hdr.MsgID, hdr.Sequence, hdr.Channel, hdr.TotalPkt, hdr.SessionID, hdr.DataLength)

	packet := append(hdr.Encode(), dataWithTerminator...)

	_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write(packet); err != nil {
		return nil, nil, fmt.Errorf("write error: %w", err)
	}

	// Read response header (20 bytes)
	respHdrBuf := make([]byte, HeaderLength)
	if _, err := io.ReadFull(c.conn, respHdrBuf); err != nil {
		return nil, nil, fmt.Errorf("failed to read response header: %w", err)
	}

	respHdr, err := DecodeHeader(respHdrBuf)
	if err != nil {
		return nil, nil, err
	}

	logger.Trace("Xiongmai", "<- Sofia Raw Header: MsgID=%d (0x%04X), Seq=%d, Channel=0x%02X, TotalPkt=0x%02X, SessionID=0x%08X, Len=%d",
		respHdr.MsgID, respHdr.MsgID, respHdr.Sequence, respHdr.Channel, respHdr.TotalPkt, respHdr.SessionID, respHdr.DataLength)

	if respHdr.DataLength > 65535 {
		return nil, nil, fmt.Errorf("response payload too large: %d bytes", respHdr.DataLength)
	}

	respPayload := make([]byte, respHdr.DataLength)
	if _, err := io.ReadFull(c.conn, respPayload); err != nil {
		return nil, nil, fmt.Errorf("failed to read response payload: %w", err)
	}

	// Trim trailing null/newlines
	cleanPayload := strings.TrimRight(string(respPayload), "\x00\r\n ")
	return []byte(cleanPayload), respHdr, nil
}

// sendRawPacketLocked encodes the default Sofia header, sends the packet and reads the response
// without logging request payloads.
func (c *Client) sendRawPacketLocked(msgID uint16, payload []byte) ([]byte, *Header, error) {
	return c.sendRawPacketWithHeaderLocked(msgID, 0, 0, c.sessionID, payload)
}

// sendPacketLocked logs non-sensitive Sofia request/response payloads and delegates to sendRawPacketLocked.
func (c *Client) sendPacketLocked(msgID uint16, payload []byte) ([]byte, error) {
	logger.Trace("Xiongmai", "-> Sofia MsgID: %d (0x%04X), Seq: %d, Data: %s", msgID, msgID, c.sequence+1, string(payload))
	cleanPayload, respHdr, err := c.sendRawPacketLocked(msgID, payload)
	if err == nil && respHdr != nil {
		logger.Trace("Xiongmai", "<- Sofia MsgID: %d (0x%04X), Seq: %d, Data: %s", respHdr.MsgID, respHdr.MsgID, respHdr.Sequence, string(cleanPayload))
	}
	return cleanPayload, err
}

// SendPacket sends a Sofia message and awaits the response.
func (c *Client) SendPacket(msgID uint16, data []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendPacketLocked(msgID, data)
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

// DiscoveredDevice holds network and identification details of a detected camera.
type DiscoveredDevice struct {
	SerialNo string `json:"sn"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	HostName string `json:"host_name"`
	MAC      string `json:"mac"`
}

// DiscoverDevices sends a UDP broadcast probe (1530) to port 34569 and collects discovery responses (1531).
func DiscoverDevices(timeout time.Duration) ([]DiscoveredDevice, error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	laddr, err := net.ResolveUDPAddr("udp4", ":0")
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	bcastAddr, err := net.ResolveUDPAddr("udp4", "255.255.255.255:34569")
	if err != nil {
		return nil, err
	}

	logger.Trace("Xiongmai Discovery", "📡 Sending UDP search broadcast to %s...", bcastAddr)

	// 20-byte Sofia Header for MsgSearchDeviceReq (1530)
	hdr := Header{
		Magic:      HeaderMagic,
		Channel:    0x00,
		TotalPkt:   0,
		CurPkt:     0,
		MsgID:      MsgSearchDeviceReq,
		DataLength: 0,
	}
	probePkt := hdr.Encode()

	if _, err := conn.WriteToUDP(probePkt, bcastAddr); err != nil {
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var devices []DiscoveredDevice
	seen := make(map[string]bool)
	buf := make([]byte, 2048)

	for {
		n, remoteAddr, err := conn.ReadFrom(buf)
		if err != nil {
			break // Timeout or read error
		}
		if n < HeaderLength {
			continue
		}
		hdr, err := DecodeHeader(buf[:HeaderLength])
		if err != nil || hdr.MsgID != MsgSearchDeviceResp {
			continue
		}

		payload := strings.TrimRight(string(buf[HeaderLength:n]), "\x00\r\n ")
		logger.Trace("Xiongmai Discovery", "<- Discovered device response from %s (%d bytes): %s", remoteAddr, n, payload)
		var respMap map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &respMap); err != nil {
			continue
		}

		var sn, ip, hostName, mac string
		port := DefaultPort

		if netCommon, ok := respMap["NetWork.NetCommon"].(map[string]interface{}); ok {
			if s, ok := netCommon["SN"].(string); ok && s != "" {
				sn = s
			} else if s, ok := netCommon["SerialNo"].(string); ok && s != "" {
				sn = s
			}
			if s, ok := netCommon["HostIP"].(string); ok {
				ip = s
			}
			if s, ok := netCommon["HostName"].(string); ok {
				hostName = s
			}
			if s, ok := netCommon["MAC"].(string); ok {
				mac = s
			}
			if p, ok := netCommon["TCPPort"].(float64); ok && p > 0 {
				port = int(p)
			}
		}

		if ip != "" && !seen[ip] {
			seen[ip] = true
			devices = append(devices, DiscoveredDevice{
				SerialNo: sn,
				IP:       ip,
				Port:     port,
				HostName: hostName,
				MAC:      mac,
			})
		}
	}

	return devices, nil
}
