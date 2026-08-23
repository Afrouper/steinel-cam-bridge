package xiongmai

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestHeaderEncodeDecode(t *testing.T) {
	orig := &Header{
		Magic:      HeaderMagic,
		Channel:    1,
		Reserved:   [2]byte{0x00, 0x00},
		SessionID:  0x12345678,
		Sequence:   42,
		TotalPkt:   1,
		CurPkt:     0,
		MsgID:      MsgLoginReq,
		DataLength: 128,
	}

	encoded := orig.Encode()
	if len(encoded) != HeaderLength {
		t.Fatalf("expected header length %d, got %d", HeaderLength, len(encoded))
	}

	decoded, err := DecodeHeader(encoded)
	if err != nil {
		t.Fatalf("failed to decode header: %v", err)
	}

	if decoded.Magic != orig.Magic ||
		decoded.SessionID != orig.SessionID ||
		decoded.Sequence != orig.Sequence ||
		decoded.MsgID != orig.MsgID ||
		decoded.DataLength != orig.DataLength {
		t.Fatalf("decoded header mismatch: got %+v, want %+v", decoded, orig)
	}
}

func TestHashPassword(t *testing.T) {
	if got := HashPassword(""); got != "" {
		t.Errorf("empty password hash should be empty, got %q", got)
	}

	pwd := "admin123"
	got := HashPassword(pwd)
	if len(got) != 32 {
		t.Errorf("expected 32-char hex MD5, got %q (len %d)", got, len(got))
	}
}

func TestParseMCUString(t *testing.T) {
	// Standard default string from Steinel Android App (XMDetectSettingPresenter.java)
	raw := "BubzbzfzazOU"
	cfg, err := ParseMCUString(raw)
	if err != nil {
		t.Fatalf("unexpected error parsing %q: %v", raw, err)
	}

	if cfg.Distance != 10 {
		t.Errorf("expected distance 10, got %d", cfg.Distance)
	}
	if cfg.HighlightDelaySec != 120 {
		t.Errorf("expected delay 120s, got %d", cfg.HighlightDelaySec)
	}
	if cfg.TwilightLux != 1000 {
		t.Errorf("expected max lux 1000, got %d", cfg.TwilightLux)
	}
	if cfg.Highlight <= 0 {
		t.Errorf("expected positive highlight, got %d", cfg.Highlight)
	}
}

func TestMCUCommandBuilders(t *testing.T) {
	if q := BuildQueryMCUCommand(); q != "BFbU" {
		t.Errorf("expected query cmd 'BFbU', got %q", q)
	}

	if cmd := BuildSetLuxCommand(2); !strings.HasPrefix(cmd, "BX") || !strings.HasSuffix(cmd, "U") {
		t.Errorf("invalid set lux cmd: %q", cmd)
	}

	if cmd := BuildSetLowlightCommand(20); !strings.HasPrefix(cmd, "BL") || !strings.HasSuffix(cmd, "U") {
		t.Errorf("invalid set lowlight cmd: %q", cmd)
	}

	if cmd := BuildSetDistanceCommand(10); !strings.HasPrefix(cmd, "BD") || !strings.HasSuffix(cmd, "U") {
		t.Errorf("invalid set distance cmd: %q", cmd)
	}

	if cmd := BuildSetHighlightCommand(80); !strings.HasPrefix(cmd, "BH") || !strings.HasSuffix(cmd, "U") {
		t.Errorf("invalid set highlight cmd: %q", cmd)
	}

	if cmd := BuildSetHighlightDelayCommand(300); !strings.HasPrefix(cmd, "BT") || !strings.HasSuffix(cmd, "U") {
		t.Errorf("invalid set delay cmd: %q", cmd)
	}
}

func TestClientLoginAndAutoRTSP(t *testing.T) {
	// Start mock TCP server simulating Xiongmai port 34567
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock TCP listener: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			hdrBuf := make([]byte, HeaderLength)
			if _, err := io.ReadFull(conn, hdrBuf); err != nil {
				return
			}
			hdr, err := DecodeHeader(hdrBuf)
			if err != nil {
				return
			}

			payload := make([]byte, hdr.DataLength)
			if _, err := io.ReadFull(conn, payload); err != nil {
				return
			}

			var respPayload []byte
			var respMsgID uint16

			switch hdr.MsgID {
			case MsgLoginReq:
				respMsgID = MsgLoginResp
				resp := LoginResp{
					Name:      "OPUserLogin",
					Ret:       100,
					SessionID: "0x00000042",
				}
				respPayload, _ = json.Marshal(resp)

			case MsgConfigSetReq:
				respMsgID = MsgConfigSetResp
				respPayload = []byte(`{"Name":"NetWork.RTSP","Ret":100}`)

			case MsgConfigGetReq:
				respMsgID = MsgConfigGetResp
				respPayload = []byte(`{"Name":"FbExtraStateCtrl","FbExtraStateCtrl":{"ison":1}}`)

			case MsgSysManagerReq:
				respMsgID = MsgSysManagerResp
				respPayload = []byte(`{"Name":"SerialPortsInfo","SerialPortsInfo":{"SerialPortsType":0,"SerialPortsData":"BubzbzfzazOU"}}`)

			default:
				respMsgID = hdr.MsgID + 1
				respPayload = []byte(`{"Ret":100}`)
			}

			respPayloadWithTerm := append(respPayload, 0x0A, 0x00)
			respHdr := Header{
				Magic:      HeaderMagic,
				Channel:    0,
				SessionID:  0x42,
				Sequence:   hdr.Sequence,
				MsgID:      respMsgID,
				DataLength: uint32(len(respPayloadWithTerm)),
			}

			_, _ = conn.Write(append(respHdr.Encode(), respPayloadWithTerm...))
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "secret", true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Connect & Login
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	if client.sessionID != 0x42 {
		t.Errorf("expected sessionID 0x42, got 0x%08X", client.sessionID)
	}

	// Auto-RTSP Enablement
	if err := client.EnableRTSP(); err != nil {
		t.Errorf("failed to enable RTSP: %v", err)
	}

	// Light state query
	lightOn, err := client.QueryLightState()
	if err != nil {
		t.Errorf("failed to query light state: %v", err)
	}
	if !lightOn {
		t.Errorf("expected lightOn=true, got false")
	}

	// MCU Config query
	mcuCfg, err := client.QueryMCUConfig()
	if err != nil {
		t.Errorf("failed to query MCU config: %v", err)
	}
	if mcuCfg == nil || mcuCfg.Distance != 10 {
		t.Errorf("expected MCU distance 10, got %+v", mcuCfg)
	}
}

func TestTalkAudioPacketForwarding(t *testing.T) {
	// Start mock TCP server for Talk
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock TCP listener: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	receivedAudioFrames := make(chan []byte, 10)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			hdrBuf := make([]byte, HeaderLength)
			if _, err := io.ReadFull(conn, hdrBuf); err != nil {
				return
			}
			hdr, err := DecodeHeader(hdrBuf)
			if err != nil {
				return
			}

			payload := make([]byte, hdr.DataLength)
			if _, err := io.ReadFull(conn, payload); err != nil {
				return
			}

			if hdr.MsgID == MsgLoginReq {
				resp, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x01"})
				respWithTerm := append(resp, 0x0A, 0x00)
				respHdr := Header{Magic: HeaderMagic, SessionID: 1, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respWithTerm))}
				_, _ = conn.Write(append(respHdr.Encode(), respWithTerm...))
			} else if hdr.MsgID == MsgTalkClaimReq {
				resp := []byte(`{"Name":"OPTalk","Ret":100}`)
				respWithTerm := append(resp, 0x0A, 0x00)
				respHdr := Header{Magic: HeaderMagic, SessionID: 1, Sequence: hdr.Sequence, MsgID: MsgTalkClaimResp, DataLength: uint32(len(respWithTerm))}
				_, _ = conn.Write(append(respHdr.Encode(), respWithTerm...))
			} else if hdr.MsgID == MsgTalkSendData {
				receivedAudioFrames <- payload
			}
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "", false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	talk := NewTalkClient(client, true)

	// Send an RTP G.711 Audio Packet from RTSP Backchannel
	rtpPkt := &rtp.Packet{
		Header: rtp.Header{
			PayloadType:    0,
			SequenceNumber: 100,
			Timestamp:      160,
			SSRC:           0x1234,
		},
		Payload: []byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55},
	}

	if err := talk.SendAudioPacket(rtpPkt); err != nil {
		t.Fatalf("failed to send audio packet: %v", err)
	}

	select {
	case frame := <-receivedAudioFrames:
		if len(frame) != len(rtpPkt.Payload) {
			t.Errorf("expected frame length %d, got %d", len(rtpPkt.Payload), len(frame))
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for talk audio frame on TCP socket")
	}
}
