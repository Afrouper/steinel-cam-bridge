package xiongmai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
)

const (
	maxControlPayloadBytes    = 65535
	controlPayloadReadTimeout = 10 * time.Second
	controlCloseWaitTimeout   = 500 * time.Millisecond
)

var errControlClosed = errors.New("xiongmai: connection is closed")

type controlPacket struct {
	hdr     *Header
	payload []byte
}

type pendingControlReq struct {
	seq      uint32
	reqMsgID uint16
	ch       chan controlPacket
}

// controlDemux owns reading from a single Sofia TCP control connection (port 34567),
// serializes outgoing socket writes, correlates responses by Header.Sequence, and
// dispatches unsolicited push frames (MsgID 1582 MCU telemetry, MsgID 1500 alarms)
// in real time without short partial-read deadlines.
type controlDemux struct {
	client  *Client
	conn    net.Conn
	writeMu sync.Mutex

	mu       sync.Mutex
	pending  map[uint32]*pendingControlReq
	readErr  error
	closed   bool
	closedCh chan struct{}
	done     chan struct{}
}

// demuxConn wraps the underlying Sofia control net.Conn so callers writing directly to
// Client.conn (e.g. TalkClient audio frames) serialize writes and deadlines through
// controlDemux.writeMu and shut down the demux cleanly on Close().
type demuxConn struct {
	net.Conn
	demux *controlDemux
}

func (c *demuxConn) Write(p []byte) (int, error) {
	c.demux.writeMu.Lock()
	defer c.demux.writeMu.Unlock()
	return c.Conn.Write(p)
}

func (c *demuxConn) WritePacketWithTimeout(packet []byte, timeout time.Duration) error {
	return c.demux.writePacket(packet, timeout)
}

func (c *demuxConn) Close() error {
	return c.demux.shutdown()
}

func unwrapDemuxConn(conn net.Conn) net.Conn {
	if dc, ok := conn.(*demuxConn); ok {
		return dc.Conn
	}
	return conn
}

func newControlDemux(client *Client, conn net.Conn) *controlDemux {
	d := &controlDemux{
		client:   client,
		conn:     unwrapDemuxConn(conn),
		pending:  make(map[uint32]*pendingControlReq),
		closedCh: make(chan struct{}),
		done:     make(chan struct{}),
	}
	go d.readLoop()
	return d
}

func (c *Client) attachDemuxLocked(conn net.Conn) *controlDemux {
	rawConn := unwrapDemuxConn(conn)
	if c.demux != nil && c.demux.conn != rawConn {
		_ = c.demux.shutdown()
	}
	d := newControlDemux(c, rawConn)
	c.demux = d
	c.conn = &demuxConn{Conn: rawConn, demux: d}
	return d
}

func (c *Client) ensureDemuxLocked() (*controlDemux, error) {
	if c.conn == nil {
		return nil, errControlClosed
	}
	rawConn := unwrapDemuxConn(c.conn)
	if c.demux == nil || c.demux.conn != rawConn {
		if c.demux != nil {
			_ = c.demux.shutdown()
		}
		d := newControlDemux(c, rawConn)
		c.demux = d
		c.conn = &demuxConn{Conn: rawConn, demux: d}
	}
	return c.demux, nil
}

func (d *controlDemux) shutdown() error {
	d.mu.Lock()
	alreadyClosed := d.closed
	d.closed = true
	if d.readErr == nil {
		d.readErr = errControlClosed
	}
	d.pending = make(map[uint32]*pendingControlReq)
	if !alreadyClosed && d.closedCh != nil {
		close(d.closedCh)
	}
	d.mu.Unlock()

	if alreadyClosed {
		return nil
	}
	return d.conn.Close()
}

func (d *controlDemux) waitClosed(timeout time.Duration) {
	if timeout <= 0 {
		timeout = controlCloseWaitTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-d.done:
	case <-timer.C:
	}
}

func (d *controlDemux) writePacket(packet []byte, timeout time.Duration) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	if timeout > 0 {
		_ = d.conn.SetWriteDeadline(time.Now().Add(timeout))
	}
	_, err := d.conn.Write(packet)
	_ = d.conn.SetWriteDeadline(time.Time{})
	return err
}

func (d *controlDemux) registerPending(seq uint32, reqMsgID uint16) (*pendingControlReq, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		if d.readErr != nil {
			return nil, d.readErr
		}
		return nil, errControlClosed
	}
	if d.readErr != nil {
		return nil, d.readErr
	}

	req := &pendingControlReq{
		seq:      seq,
		reqMsgID: reqMsgID,
		ch:       make(chan controlPacket, 1),
	}
	d.pending[seq] = req
	return req, nil
}

func (d *controlDemux) unregisterPending(seq uint32, req *pendingControlReq) {
	d.mu.Lock()
	if cur, ok := d.pending[seq]; ok && cur == req {
		delete(d.pending, seq)
	}
	d.mu.Unlock()
}

func (d *controlDemux) getReadErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.readErr != nil {
		return d.readErr
	}
	return errControlClosed
}

func (d *controlDemux) failAll(err error) {
	d.mu.Lock()
	if d.readErr == nil {
		d.readErr = err
	}
	d.pending = make(map[uint32]*pendingControlReq)
	d.mu.Unlock()
}

func isMatchingResponseMsgID(reqMsgID, respMsgID uint16) bool {
	if respMsgID == reqMsgID+1 || respMsgID == reqMsgID {
		return true
	}
	if reqMsgID == MsgLogoutReq && respMsgID == 1009 {
		return true
	}
	return false
}

func (d *controlDemux) deliverPushIfRequested(hdr *Header, payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for seq, req := range d.pending {
		if req.reqMsgID == hdr.MsgID {
			delete(d.pending, seq)
			select {
			case req.ch <- controlPacket{hdr: hdr, payload: payload}:
			default:
			}
			return
		}
	}
}

func (d *controlDemux) deliverResponse(hdr *Header, payload []byte) {
	d.mu.Lock()
	var matched *pendingControlReq
	if hdr.Sequence != 0 {
		if req, ok := d.pending[hdr.Sequence]; ok && isMatchingResponseMsgID(req.reqMsgID, hdr.MsgID) {
			delete(d.pending, hdr.Sequence)
			matched = req
		}
	}
	if matched == nil && len(d.pending) == 1 {
		// On real Xiongmai V4.03.R12 firmware, claiming a secondary media/download socket
		// under the same SessionID increments the camera's session sequence counter, so the
		// control-socket response (e.g. MsgMonitorStartResp 1411 or MsgPlayResp 1421) arrives
		// with Sequence >= req.seq (and simplified unit-test mocks may send Sequence == 0).
		// Accept when MsgID matches the expected response pair and Sequence is not older (< req.seq).
		for seq, req := range d.pending {
			if (hdr.Sequence == 0 || hdr.Sequence >= req.seq) && isMatchingResponseMsgID(req.reqMsgID, hdr.MsgID) {
				delete(d.pending, seq)
				matched = req
			}
		}
	}
	d.mu.Unlock()

	if matched == nil {
		logger.Debug("Xiongmai", "⚠️ Discarding unexpected or stale Sofia control packet: MsgID=%d (0x%04X), Seq=%d, Len=%d",
			hdr.MsgID, hdr.MsgID, hdr.Sequence, len(payload))
		return
	}

	select {
	case matched.ch <- controlPacket{hdr: hdr, payload: payload}:
	default:
	}
}

func (d *controlDemux) readLoop() {
	defer close(d.done)

	hdrBuf := make([]byte, HeaderLength)
	for {
		if _, err := io.ReadFull(d.conn, hdrBuf); err != nil {
			d.failAll(fmt.Errorf("failed to read response header: %w", err))
			return
		}

		respHdr, err := DecodeHeader(hdrBuf)
		if err != nil {
			d.failAll(fmt.Errorf("invalid response header: %w", err))
			return
		}

		logger.Trace("Xiongmai", "<- Sofia Raw Header: MsgID=%d (0x%04X), Seq=%d, Channel=0x%02X, TotalPkt=0x%02X, SessionID=0x%08X, Len=%d",
			respHdr.MsgID, respHdr.MsgID, respHdr.Sequence, respHdr.Channel, respHdr.TotalPkt, respHdr.SessionID, respHdr.DataLength)

		if respHdr.DataLength > maxControlPayloadBytes {
			d.failAll(fmt.Errorf("response payload too large: %d bytes", respHdr.DataLength))
			return
		}

		var respPayload []byte
		if respHdr.DataLength > 0 {
			respPayload = make([]byte, respHdr.DataLength)
			_ = d.conn.SetReadDeadline(time.Now().Add(controlPayloadReadTimeout))
			if _, err := io.ReadFull(d.conn, respPayload); err != nil {
				_ = d.conn.SetReadDeadline(time.Time{})
				d.failAll(fmt.Errorf("failed to read response payload: %w", err))
				return
			}
			_ = d.conn.SetReadDeadline(time.Time{})
		}

		cleanBytes := bytes.TrimRight(respPayload, "\x00\r\n ")

		if respHdr.MsgID == MsgTransSerialData {
			if d.client.handleTelemetryPush(string(cleanBytes)) {
				d.deliverPushIfRequested(respHdr, cleanBytes)
			}
			continue
		}

		if respHdr.MsgID == MsgAlarmReq {
			d.client.handleAlarmPush(d, respHdr, respPayload)
			d.deliverPushIfRequested(respHdr, cleanBytes)
			continue
		}

		d.deliverResponse(respHdr, cleanBytes)
	}
}

func (c *Client) clearTelemetryBuf() {
	c.pushMu.Lock()
	c.telemetryBuf = ""
	c.pushMu.Unlock()
}

func (c *Client) telemetrySnapshot() (uint64, chan struct{}) {
	c.pushMu.Lock()
	defer c.pushMu.Unlock()
	if c.telemetryNotify == nil {
		c.telemetryNotify = make(chan struct{})
	}
	return c.telemetrySeq.Load(), c.telemetryNotify
}

func (c *Client) waitForTelemetrySinceLocked(beforeSeq uint64, notifyCh chan struct{}, timeout time.Duration) bool {
	if c.telemetrySeq.Load() > beforeSeq {
		return true
	}
	if c.conn == nil {
		return false
	}
	if timeout <= 0 {
		timeout = defaultMCUTelemetryTimeout
	}

	var (
		demuxDone   <-chan struct{}
		demuxClosed <-chan struct{}
	)
	if c.demux != nil && c.demux.conn == unwrapDemuxConn(c.conn) {
		demuxDone = c.demux.done
		demuxClosed = c.demux.closedCh
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-notifyCh:
		return c.telemetrySeq.Load() > beforeSeq
	case <-demuxClosed:
		return c.telemetrySeq.Load() > beforeSeq
	case <-demuxDone:
		return c.telemetrySeq.Load() > beforeSeq
	case <-c.closeChan:
		return c.telemetrySeq.Load() > beforeSeq
	case <-timer.C:
		return c.telemetrySeq.Load() > beforeSeq
	}
}

func (c *Client) handleTelemetryPush(raw string) bool {
	raw = strings.Trim(raw, "\x00\r\n ")
	if raw == "" {
		return false
	}

	c.pushMu.Lock()
	full := c.telemetryBuf + raw

	// If the accumulated string does not end with "U", buffer it and wait for next chunk
	if !strings.HasSuffix(full, "U") {
		if startIdx := strings.LastIndex(full, "B"); startIdx >= 0 {
			c.telemetryBuf = full[startIdx:]
		} else {
			c.telemetryBuf = ""
		}
		c.pushMu.Unlock()
		return false
	}

	// Full frame received: locate starting "B"
	c.telemetryBuf = ""
	startIdx := strings.LastIndex(full, "B")
	if startIdx < 0 {
		c.pushMu.Unlock()
		return false
	}
	frame := full[startIdx:]

	cfg, err := ParseMCUString(frame)
	if err != nil {
		c.pushMu.Unlock()
		logger.Debug("Xiongmai", "⚠️ Could not parse MsgID 1582 telemetry string %q: %v", frame, err)
		return false
	}

	c.lastMCUConfig = cfg
	seq := c.telemetrySeq.Add(1)
	if c.telemetryNotify != nil {
		close(c.telemetryNotify)
	}
	c.telemetryNotify = make(chan struct{})
	cb := c.onTelemetry
	cfgCopy := *cfg
	c.pushMu.Unlock()

	logger.Debug("Xiongmai", "💡 Received MCU Telemetry (1582): %q -> LightOn=%v, Dim=%d%%, Lux=%d, Dist=%dm, Delay=%ds, Lowlight=%d%% (%s)",
		frame, cfgCopy.IsLightOn(), cfgCopy.Highlight, cfgCopy.TwilightLux, cfgCopy.Distance, cfgCopy.HighlightDelaySec, cfgCopy.Lowlight, cfgCopy.LowlightDuration)

	if cb != nil {
		go func() {
			c.telemetryMu.Lock()
			defer c.telemetryMu.Unlock()
			if c.telemetrySeq.Load() != seq {
				return
			}
			cb(&cfgCopy)
			c.appliedTelemetrySeq = seq
		}()
	}
	return true
}

func (c *Client) handleTelemetryPushLocked(raw string) bool {
	return c.handleTelemetryPush(raw)
}

// runSerializedTelemetry runs fn serialized with async telemetry callbacks and
// invalidates any older queued telemetry goroutines that have not started yet.
// alreadyApplied is true when the latest received telemetry frame was already
// delivered to onTelemetry, allowing callers to skip duplicate status updates.
func (c *Client) runSerializedTelemetry(fn func(cfg *MCUConfig, alreadyApplied bool)) {
	c.telemetryMu.Lock()
	defer c.telemetryMu.Unlock()
	latestSeq := c.telemetrySeq.Load()
	alreadyApplied := latestSeq > 0 && c.appliedTelemetrySeq == latestSeq
	if !alreadyApplied {
		nextSeq := c.telemetrySeq.Add(1)
		c.appliedTelemetrySeq = nextSeq
	}
	cfg := c.GetLastMCUConfig()
	fn(cfg, alreadyApplied)
}

func (c *Client) handleAlarmPush(d *controlDemux, hdr *Header, rawPayload []byte) {
	cleanPayload := strings.Trim(string(rawPayload), "\x00\r\n ")
	logger.Debug("Xiongmai", "🚨 Received Alarm notification (MsgID 1500): %s", cleanPayload)

	ack := map[string]any{
		"Name":      "AlarmInfo",
		"SessionID": fmt.Sprintf("0x%08X", hdr.SessionID),
		"Ret":       100,
	}
	ackBytes, _ := json.Marshal(ack)
	ackBytes = append(ackBytes, 0x00)

	ackHdr := Header{
		Magic:      HeaderMagic,
		Channel:    hdr.Channel,
		TotalPkt:   hdr.TotalPkt,
		SessionID:  hdr.SessionID,
		Sequence:   hdr.Sequence,
		MsgID:      MsgAlarmResp,
		DataLength: uint32(len(ackBytes)),
	}
	if d != nil {
		_ = d.writePacket(append(ackHdr.Encode(), ackBytes...), 2*time.Second)
	}

	c.pushMu.Lock()
	cb := c.onAlarm
	c.pushMu.Unlock()
	if cb == nil {
		return
	}

	var push AlarmPushResp
	if err := json.Unmarshal([]byte(cleanPayload), &push); err == nil {
		eventType := push.AlarmInfo.Event
		if eventType == "" {
			eventType = push.Event
		}
		if eventType == "" {
			eventType = "MotionDetect"
		}
		isStart := !strings.EqualFold(push.AlarmInfo.State, "Stop") && !strings.EqualFold(push.Status, "Stop")
		go cb(eventType, isStart)
		return
	}

	lower := strings.ToLower(cleanPayload)
	if strings.Contains(lower, "motion") || strings.Contains(lower, "detect") {
		go cb("MotionDetect", true)
	}
}
