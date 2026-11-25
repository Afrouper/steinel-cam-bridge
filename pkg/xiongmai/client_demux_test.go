package xiongmai

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlDemux_DiscardsStaleDelayedResponseBySequence(t *testing.T) {
	port := startMockSofiaServer(t, func(conn net.Conn, hdr Header, _ map[string]interface{}, _ string) bool {
		switch hdr.MsgID {
		case MsgConfigGetReq:
			// Inject a stale/delayed response carrying an old Sequence number (e.g. Seq=1)
			// right before the genuine response matching hdr.Sequence.
			staleBody := []byte(`{"Name":"WrongStaleResponse","Ret":100}` + "\x00")
			staleHdr := Header{
				Magic:      HeaderMagic,
				SessionID:  0x42,
				Sequence:   1,
				MsgID:      MsgFileSearchResp,
				DataLength: uint32(len(staleBody)),
			}
			_, _ = conn.Write(append(staleHdr.Encode(), staleBody...))

			sofiaMockWriteJSON(conn, hdr, MsgConfigGetResp, map[string]any{
				"Name":      "Simplify.Encode",
				"Ret":       100,
				"SessionID": "0x00000042",
				"Simplify.Encode": []map[string]any{
					{
						"MainFormat": map[string]any{
							"AudioEnable": true,
							"Video": map[string]any{
								"Compression": "H.264",
								"Resolution":  "1080P",
								"FPS":         20,
								"BitRate":     2048,
							},
						},
					},
				},
			})
			return true
		default:
			sofiaMockWriteJSON(conn, hdr, hdr.MsgID+1, map[string]any{"Ret": 100})
			return true
		}
	})

	client := NewClient("127.0.0.1", port, "admin", "secret")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	// Must ignore the stale Seq=1 packet and cleanly parse Simplify.Encode from the matching response.
	require.NoError(t, client.EnsureH264Encoding())
	assert.Equal(t, "H.264", client.GetVideoCompression())
	assert.Equal(t, "1080P", client.GetVideoResolution())
	assert.Equal(t, 20, client.GetVideoFPS())
}

func TestControlDemux_RealTimeIdlePushDeliveryWithoutKeepAlive(t *testing.T) {
	pushTrigger := make(chan struct{})
	var alarmAckReceived atomic.Bool

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	tcpAddr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := tcpAddr.Port

	go func() {
		conn, acceptErr := l.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		pushDone := false
		for {
			hdr, _, _, rErr := sofiaMockReadPacket(conn)
			if rErr != nil {
				return
			}
			switch hdr.MsgID {
			case MsgMonitorClaimReq:
				sofiaMockWriteJSON(conn, hdr, MsgMonitorClaimResp, map[string]any{"Ret": 106})
			case MsgLoginReq:
				sofiaMockWriteJSON(conn, hdr, MsgLoginResp, map[string]any{
					"Name":      "OPUserLogin",
					"Ret":       100,
					"SessionID": "0x00000042",
				})
			case MsgTransStartReq:
				sofiaMockWriteJSON(conn, hdr, MsgTransStartResp, map[string]any{"Ret": 100})
				if !pushDone {
					pushDone = true
					go func() {
						<-pushTrigger
						// 1. Split 2-chunk 1582 MCU telemetry frame while client is completely idle
						chunk1 := []byte("Bkbwdza")
						h1 := Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: 700, MsgID: MsgTransSerialData, DataLength: uint32(len(chunk1))}
						_, _ = conn.Write(append(h1.Encode(), chunk1...))

						time.Sleep(15 * time.Millisecond)

						chunk2 := []byte("qazoU\x00")
						h2 := Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: 701, MsgID: MsgTransSerialData, DataLength: uint32(len(chunk2))}
						_, _ = conn.Write(append(h2.Encode(), chunk2...))

						// 2. Spontaneous 1500 motion alarm while client is completely idle
						alarmBody := []byte(`{"Name":"AlarmInfo","SessionID":"0x00000042","AlarmInfo":{"Event":"MotionDetect","State":"Start","Channel":0}}` + "\x00")
						hAlarm := Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: 702, MsgID: MsgAlarmReq, DataLength: uint32(len(alarmBody))}
						_, _ = conn.Write(append(hAlarm.Encode(), alarmBody...))
					}()
				}
			case MsgAlarmResp:
				alarmAckReceived.Store(true)
			default:
				sofiaMockWriteJSON(conn, hdr, hdr.MsgID+1, map[string]any{"Ret": 100})
			}
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "secret")
	telemetryCh := make(chan *MCUConfig, 1)
	alarmCh := make(chan string, 1)

	client.SetOnTelemetry(func(cfg *MCUConfig) {
		select {
		case telemetryCh <- cfg:
		default:
		}
	})
	client.SetOnAlarm(func(event string, isStart bool) {
		if isStart {
			select {
			case alarmCh <- event:
			default:
			}
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	// Release idle pushes WITHOUT calling SendKeepAlive or any other request.
	close(pushTrigger)

	select {
	case cfg := <-telemetryCh:
		assert.Equal(t, 3, cfg.Mode, "expected continuous light mode 'd' -> 3 from split 1582 chunks")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected real-time 1582 telemetry callback while idle without KeepAlive")
	}

	select {
	case ev := <-alarmCh:
		assert.Equal(t, "MotionDetect", ev)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected real-time 1500 alarm callback while idle without KeepAlive")
	}

	require.Eventually(t, alarmAckReceived.Load, 500*time.Millisecond, 10*time.Millisecond,
		"camera must receive immediate MsgAlarmResp (1501) acknowledgement")
}

func TestControlDemux_TelemetryArrivingBeforeTransSendRespWakesImmediately(t *testing.T) {
	var transSendCount atomic.Int32
	port := startMockSofiaServer(t, func(conn net.Conn, hdr Header, _ map[string]interface{}, _ string) bool {
		if hdr.MsgID == MsgTransSendData {
			transSendCount.Add(1)
			// Camera pushes 1582 telemetry BEFORE sending 1573 MsgTransSendResp!
			sofiaMockWriteTelemetry(conn, hdr.Sequence+100, 'd')
			sofiaMockWriteJSON(conn, hdr, MsgTransSendResp, map[string]any{"Ret": 100})
			return true
		}
		sofiaMockWriteJSON(conn, hdr, hdr.MsgID+1, map[string]any{"Ret": 100})
		return true
	})

	client := NewClient("127.0.0.1", port, "admin", "secret")
	client.mcuTelemetryTimeout = 2 * time.Second

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	start := time.Now()
	require.NoError(t, client.SendMCUCommand("BMdU"))
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 400*time.Millisecond, "SendMCUCommand must not stall for mcuTelemetryTimeout when 1582 arrived before 1573")
	assert.Equal(t, int32(1), transSendCount.Load(), "must not send redundant follow-up BFbU when 1582 already arrived")
	cfg := client.GetLastMCUConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, 3, cfg.Mode)
}

func TestControlDemux_ConcurrentRequestsAndPushesRaceSafe(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	tcpAddr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := tcpAddr.Port

	stopServerPushes := make(chan struct{})
	defer close(stopServerPushes)

	go func() {
		conn, acceptErr := l.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		var writeMu sync.Mutex
		writePkt := func(h Header, body []byte) {
			writeMu.Lock()
			defer writeMu.Unlock()
			_, _ = conn.Write(append(h.Encode(), body...))
		}

		for {
			hdr, _, _, rErr := sofiaMockReadPacket(conn)
			if rErr != nil {
				return
			}
			switch hdr.MsgID {
			case MsgMonitorClaimReq:
				resp, _ := json.Marshal(map[string]any{"Ret": 106})
				resp = append(resp, 0x00)
				writePkt(Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(resp))}, resp)
			case MsgLoginReq:
				resp, _ := json.Marshal(map[string]any{"Name": "OPUserLogin", "Ret": 100, "SessionID": "0x00000042"})
				resp = append(resp, 0x00)
				writePkt(Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(resp))}, resp)
			case MsgTransStartReq:
				resp, _ := json.Marshal(map[string]any{"Ret": 100})
				resp = append(resp, 0x00)
				writePkt(Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: hdr.Sequence, MsgID: MsgTransStartResp, DataLength: uint32(len(resp))}, resp)
				go func() {
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					var seq uint32 = 5000
					for {
						select {
						case <-stopServerPushes:
							return
						case <-ticker.C:
							seq++
							frame := []byte("BkbwbzaqazoU\x00")
							writePkt(Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: seq, MsgID: MsgTransSerialData, DataLength: uint32(len(frame))}, frame)
							seq++
							alarm := []byte(`{"Name":"AlarmInfo","SessionID":"0x00000042","AlarmInfo":{"Event":"MotionDetect","State":"Start","Channel":0}}` + "\x00")
							writePkt(Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: seq, MsgID: MsgAlarmReq, DataLength: uint32(len(alarm))}, alarm)
						}
					}
				}()
			case MsgTransSendData:
				resp := []byte(`{"Ret":100}` + "\x00")
				writePkt(Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: hdr.Sequence, MsgID: MsgTransSendResp, DataLength: uint32(len(resp))}, resp)
				frame := []byte("BkbwbzaqazoU\x00")
				writePkt(Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: hdr.Sequence + 10000, MsgID: MsgTransSerialData, DataLength: uint32(len(frame))}, frame)
			case MsgAlarmResp:
				continue
			default:
				resp := []byte(`{"Ret":100}` + "\x00")
				writePkt(Header{Magic: HeaderMagic, SessionID: 0x42, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(resp))}, resp)
			}
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "secret")
	var telemetryCallbacks atomic.Int32
	var alarmCallbacks atomic.Int32
	client.SetOnTelemetry(func(*MCUConfig) { telemetryCallbacks.Add(1) })
	client.SetOnAlarm(func(string, bool) { alarmCallbacks.Add(1) })

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))

	var wg sync.WaitGroup
	for g := range 6 {
		id := g
		wg.Go(func() {
			for range 10 {
				if id%2 == 0 {
					assert.NoError(t, client.SendKeepAlive())
				} else {
					cfg, qErr := client.QueryMCUConfig()
					assert.NoError(t, qErr)
					assert.NotNil(t, cfg)
				}
			}
		})
	}
	wg.Wait()

	assert.Greater(t, telemetryCallbacks.Load(), int32(0))
	assert.Greater(t, alarmCallbacks.Load(), int32(0))
	require.NoError(t, client.Close())
}

func TestControlDemux_AcceptsSessionIncrementedSequenceAfterMediaClaim(t *testing.T) {
	port := startMockSofiaServer(t, func(conn net.Conn, hdr Header, _ map[string]interface{}, _ string) bool {
		if hdr.MsgID == MsgMonitorStartReq {
			// First inject a genuinely stale MsgMonitorStartResp from an older sequence (< hdr.Sequence):
			// must be discarded!
			staleBody := []byte(`{"Name":"OPMonitor","Ret":127}` + "\x00")
			staleHdr := Header{
				Magic:      HeaderMagic,
				SessionID:  0x42,
				Sequence:   hdr.Sequence - 1,
				MsgID:      MsgMonitorStartResp,
				DataLength: uint32(len(staleBody)),
			}
			_, _ = conn.Write(append(staleHdr.Encode(), staleBody...))

			// Then send the real V4.03.R12 response where the camera's per-session sequence counter
			// advanced by +1 due to OPMonitor Claim on the secondary media socket (Seq = hdr.Sequence + 1).
			okBody := []byte(`{"Name":"OPMonitor","Ret":100,"SessionID":"0x00000145"}` + "\x00")
			okHdr := Header{
				Magic:      HeaderMagic,
				SessionID:  0x42,
				Sequence:   hdr.Sequence + 1,
				MsgID:      MsgMonitorStartResp,
				DataLength: uint32(len(okBody)),
			}
			_, _ = conn.Write(append(okHdr.Encode(), okBody...))
			return true
		}
		sofiaMockWriteJSON(conn, hdr, hdr.MsgID+1, map[string]any{"Ret": 100})
		return true
	})

	client := NewClient("127.0.0.1", port, "admin", "secret")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	respBytes, err := client.SendPacket(MsgMonitorStartReq, []byte(`{"Name":"OPMonitor"}`))
	require.NoError(t, err)
	assert.Contains(t, string(respBytes), `"Ret":100`)
}

func TestControlDemux_IgnoresUnsolicitedPushWithCollidingSequenceAndSerializesDemuxConnWrites(t *testing.T) {
	port := startMockSofiaServer(t, func(conn net.Conn, hdr Header, _ map[string]interface{}, _ string) bool {
		if hdr.MsgID == MsgKeepAliveReq {
			// Inject an unsolicited firmware status push (e.g. MsgID 2062 seen on L 620 V4.03.R12)
			// carrying the EXACT same Sequence number as the in-flight KeepAlive request.
			pushBody := []byte(`{"Name":"NetWork.PushStatus","Ret":607}` + "\x00")
			pushHdr := Header{
				Magic:      HeaderMagic,
				SessionID:  0x42,
				Sequence:   hdr.Sequence,
				MsgID:      2062,
				DataLength: uint32(len(pushBody)),
			}
			_, _ = conn.Write(append(pushHdr.Encode(), pushBody...))

			// Follow up with the genuine MsgKeepAliveResp (1007).
			sofiaMockWriteJSON(conn, hdr, MsgKeepAliveResp, map[string]any{"Name": "KeepAlive", "Ret": 100})
			return true
		}
		sofiaMockWriteJSON(conn, hdr, hdr.MsgID+1, map[string]any{"Ret": 100})
		return true
	})

	client := NewClient("127.0.0.1", port, "admin", "secret")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	defer func() { _ = client.Close() }()

	// Verify MsgID check prevents the colliding Seq push (2062) from hijacking MsgKeepAliveReq (1006).
	resp, err := client.SendPacket(MsgKeepAliveReq, []byte(`{"Name":"KeepAlive"}`))
	require.NoError(t, err)
	assert.Contains(t, string(resp), `"KeepAlive"`)

	// Verify concurrent direct writes via demuxConn.WritePacketWithTimeout (used by TalkClient)
	// and SendKeepAlive are race-free under -race.
	talk := NewTalkClient(client)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 10 {
			assert.NoError(t, client.SendKeepAlive())
		}
	})
	wg.Go(func() {
		client.mu.Lock()
		dc, ok := client.conn.(*demuxConn)
		client.mu.Unlock()
		if assert.True(t, ok, "client.conn must be wrapped in *demuxConn") {
			dummyHdr := Header{Magic: HeaderMagic, SessionID: client.GetSessionID(), MsgID: MsgTalkAudioData, DataLength: 4}
			pkt := append(dummyHdr.Encode(), 0x55, 0x55, 0x55, 0x55)
			for range 10 {
				assert.NoError(t, dc.WritePacketWithTimeout(pkt, time.Second))
			}
		}
	})
	wg.Wait()
	_ = talk
}

type nonUnblockingReadConn struct {
	net.Conn
	blockCh chan struct{}
}

func (c *nonUnblockingReadConn) Read(_ []byte) (int, error) {
	<-c.blockCh
	return 0, io.EOF
}

func (c *nonUnblockingReadConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *nonUnblockingReadConn) Close() error                { return nil }
func (c *nonUnblockingReadConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

func TestControlDemux_ShutdownUnblocksPendingWaitersImmediately(t *testing.T) {
	stubConn := &nonUnblockingReadConn{blockCh: make(chan struct{})}
	defer close(stubConn.blockCh)

	client := NewClient("127.0.0.1", DefaultPort, "admin", "secret")
	client.mu.Lock()
	demux := client.attachDemuxLocked(stubConn)
	client.isLoggedIn = true
	client.sessionID = 0x55
	client.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		_, err := client.SendPacket(MsgKeepAliveReq, []byte(`{"Name":"KeepAlive"}`))
		errCh <- err
	}()

	require.Eventually(t, func() bool {
		demux.mu.Lock()
		n := len(demux.pending)
		demux.mu.Unlock()
		return n == 1
	}, time.Second, 5*time.Millisecond)

	start := time.Now()
	require.NoError(t, demux.shutdown())
	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Less(t, time.Since(start), 200*time.Millisecond, "closedCh must unblock pending waiters even when Read has not returned yet")
	case <-time.After(time.Second):
		t.Fatal("SendPacket stayed blocked after demux.shutdown()")
	}
}

func TestEncryptAES128CBC_DoesNotMutateCallerBackingSlice(t *testing.T) {
	backing := []byte(`{"Name":"OPMonitor","Action":"Start"}` + "\x00EXTRA_SENTINEL")
	sub := backing[:len(`{"Name":"OPMonitor","Action":"Start"}`)]
	key := []byte(DefaultSofiaAESKey)

	ct, err := encryptAES128CBC(key, sub)
	require.NoError(t, err)
	assert.NotEmpty(t, ct)
	assert.Equal(t, "\x00EXTRA_SENTINEL", string(backing[len(sub):]), "encryptAES128CBC must never overwrite bytes past len(plaintext) in caller backing array")

	pt, err := decryptAES128CBC(key, ct)
	require.NoError(t, err)
	assert.Equal(t, string(sub), string(pt))
}
