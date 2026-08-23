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
	addr       string
	user       string
	password   string
	conn       net.Conn
	sessionID  uint32
	sequence   uint32
	mu         sync.Mutex
	isLoggedIn bool
	debug      bool
	closeChan  chan struct{}
	closed     atomic.Bool
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
		addr:      fmt.Sprintf("%s:%d", cameraIP, port),
		user:      user,
		password:  password,
		debug:     debug,
		closeChan: make(chan struct{}),
	}
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

// HashPassword generates the password representation for Xiongmai Sofia protocol.
func HashPassword(pwd string) string {
	if pwd == "" {
		return ""
	}
	h := md5.New()
	h.Write([]byte(pwd))
	return hex.EncodeToString(h.Sum(nil))
}

// loginLocked performs the OPUserLogin command.
func (c *Client) loginLocked() error {
	loginReq := LoginReq{
		Name: "OPUserLogin",
		OPUserLogin: OPUserLoginInfo{
			UserName:  c.user,
			Password:  HashPassword(c.password),
			LoginType: "DVRIP-Web",
		},
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

	if resp.Ret != 100 && resp.Ret != 0 {
		return fmt.Errorf("camera login rejected with code: %d", resp.Ret)
	}

	// Parse SessionID (e.g. "0x00000001" or decimal)
	sessionStr := strings.TrimPrefix(resp.SessionID, "0x")
	if sessionStr != "" {
		if sID, err := strconv.ParseUint(sessionStr, 16, 32); err == nil {
			c.sessionID = uint32(sID)
		}
	}

	c.isLoggedIn = true
	return nil
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

	req := KeepAliveReq{
		Name: "OPKeepAlive",
		OPKeepAlive: OPKeepAliveVal{
			Time: time.Now().Format("2006-01-02 15:04:05"),
		},
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

// Close gracefully closes the connection.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	close(c.closeChan)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}
