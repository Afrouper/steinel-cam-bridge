package xiongmai

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	// Real-world test vector verified against Steinel iOS App packet capture
	if got := HashPassword("12345678A"); got != "EAyIB8vx" {
		t.Errorf("expected Sofia hash 'EAyIB8vx' for '12345678A', got %q", got)
	}

	pwd := "admin123"
	got := HashPassword(pwd)
	if len(got) != 8 {
		t.Errorf("expected 8-char Sofia hash, got %q (len %d)", got, len(got))
	}
}

func TestHashMD5HexAndDoubleMD5(t *testing.T) {
	if got := HashMD5Hex(""); got != "" {
		t.Errorf("empty input should return empty string, got %q", got)
	}
	if got := HashDoubleMD5Hex(""); got != "" {
		t.Errorf("empty input should return empty string, got %q", got)
	}

	// Standard lowercase 32-char hex MD5: md5("admin") = "21232f297a57a5a743894a0e4a801fc3"
	if got := HashMD5Hex("admin"); got != "21232f297a57a5a743894a0e4a801fc3" {
		t.Errorf("expected '21232f297a57a5a743894a0e4a801fc3', got %q", got)
	}

	// Double MD5: md5(md5("admin")) = md5("21232f297a57a5a743894a0e4a801fc3") = "c3284d0f94606de1fd2af172aba15bf3"
	if got := HashDoubleMD5Hex("admin"); got != "c3284d0f94606de1fd2af172aba15bf3" {
		t.Errorf("expected 'c3284d0f94606de1fd2af172aba15bf3', got %q", got)
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
	if cfg.HighlightDelaySec != 60 {
		t.Errorf("expected delay 60s, got %d", cfg.HighlightDelaySec)
	}
	// 'z' = 25 -> 25 - 25 = 0 -> LuxTable[0] = 2 Lux (night only)
	if cfg.TwilightLux != 2 {
		t.Errorf("expected min lux 2, got %d", cfg.TwilightLux)
	}
	if cfg.Highlight <= 0 {
		t.Errorf("expected positive highlight, got %d", cfg.Highlight)
	}
}

func TestMCUCommandBuilders(t *testing.T) {
	if q := BuildQueryMCUCommand(); q != "BFbU" {
		t.Errorf("expected query cmd 'BFbU', got %q", q)
	}

	// 100 Lux -> LuxTable[12] -> 25 - 12 = 13 ('n') -> "BXnU"
	if cmd := BuildSetLuxCommand(100); cmd != "BXnU" {
		t.Errorf("expected set lux cmd 'BXnU', got %q", cmd)
	}
	// 175 Lux -> LuxTable[15] -> 25 - 15 = 10 ('k') -> "BXkU"
	if cmd := BuildSetLuxCommand(175); cmd != "BXkU" {
		t.Errorf("expected set lux cmd 'BXkU', got %q", cmd)
	}

	if cmd := BuildSetLowlightCommand(20); !strings.HasPrefix(cmd, "BL") || !strings.HasSuffix(cmd, "U") {
		t.Errorf("invalid set lowlight cmd: %q", cmd)
	}

	// Maximum distance (10m) -> 20 ('u') -> "BDuU"
	if cmd := BuildSetDistanceCommand(10); cmd != "BDuU" {
		t.Errorf("expected set distance cmd 'BDuU', got %q", cmd)
	}
	// Medium distance (6m) -> 15 ('p') -> "BDpU"
	if cmd := BuildSetDistanceCommand(6); cmd != "BDpU" {
		t.Errorf("expected set distance cmd 'BDpU', got %q", cmd)
	}
	// Minimum distance (3m) -> 10 ('k') -> "BDkU"
	if cmd := BuildSetDistanceCommand(3); cmd != "BDkU" {
		t.Errorf("expected set distance cmd 'BDkU', got %q", cmd)
	}

	// 68% -> 68 / 4 = 17 ('r') -> "BHrU"
	if cmd := BuildSetHighlightCommand(68); cmd != "BHrU" {
		t.Errorf("expected set highlight cmd 'BHrU', got %q", cmd)
	}

	// 60s (1 min) -> "BTbU"
	if cmd := BuildSetHighlightDelayCommand(60); cmd != "BTbU" {
		t.Errorf("expected set delay cmd 'BTbU', got %q", cmd)
	}
}

func TestClientLoginAndAutoRTSP(t *testing.T) {
	// Start mock TCP server simulating Xiongmai port 34567
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock TCP listener: %v", err)
	}
	defer func() { _ = listener.Close() }()

	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

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

			case MsgTransSendData:
				respMsgID = MsgTransSendResp
				respPayload = []byte(`{"Ret":100}`)

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

			if hdr.MsgID == MsgTransSendData {
				telemetryData := []byte("BubzdzfzazOU\x0A\x00")
				tHdr := Header{
					Magic:      HeaderMagic,
					Channel:    0,
					SessionID:  0x42,
					Sequence:   hdr.Sequence + 1,
					MsgID:      MsgTransSerialData,
					DataLength: uint32(len(telemetryData)),
				}
				_, _ = conn.Write(append(tHdr.Encode(), telemetryData...))
			}
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Connect & Login
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() { _ = client.Close() }()

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
	defer func() { _ = listener.Close() }()

	port := listener.Addr().(*net.TCPAddr).Port
	receivedAudioFrames := make(chan []byte, 10)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

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

			switch hdr.MsgID {
			case MsgLoginReq:
				resp, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x01"})
				respWithTerm := append(resp, 0x0A, 0x00)
				respHdr := Header{Magic: HeaderMagic, SessionID: 1, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respWithTerm))}
				_, _ = conn.Write(append(respHdr.Encode(), respWithTerm...))
			case MsgTalkClaimReq, MsgTalkClaimV2Req:
				resp := []byte(`{"Name":"OPTalk","Ret":100}`)
				respWithTerm := append(resp, 0x0A, 0x00)
				respHdr := Header{Magic: HeaderMagic, SessionID: 1, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respWithTerm))}
				_, _ = conn.Write(append(respHdr.Encode(), respWithTerm...))
			case MsgTalkControlReq:
				resp := []byte(`{"Name":"OPTalk","Ret":100}`)
				respWithTerm := append(resp, 0x0A, 0x00)
				respHdr := Header{Magic: HeaderMagic, SessionID: 1, Sequence: hdr.Sequence, MsgID: MsgTalkControlResp, DataLength: uint32(len(respWithTerm))}
				_, _ = conn.Write(append(respHdr.Encode(), respWithTerm...))
			case MsgTalkSendData, MsgTalkAudioData:
				receivedAudioFrames <- payload
			default:
				resp := []byte(`{"Ret":100}`)
				respWithTerm := append(resp, 0x0A, 0x00)
				respHdr := Header{Magic: HeaderMagic, SessionID: 1, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respWithTerm))}
				_, _ = conn.Write(append(respHdr.Encode(), respWithTerm...))
			}
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() { _ = client.Close() }()

	talk := NewTalkClient(client)

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

func TestSanitizeRTSPURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "With username and password",
			input:    "rtsp://admin:supersecret@192.168.1.100:554/stream=0",
			expected: "rtsp://admin:xxxxx@192.168.1.100:554/stream=0",
		},
		{
			name:     "With username only",
			input:    "rtsp://admin@192.168.1.100:554/stream=0",
			expected: "rtsp://admin@192.168.1.100:554/stream=0",
		},
		{
			name:     "Without auth",
			input:    "rtsp://192.168.1.100:554/stream=0",
			expected: "rtsp://192.168.1.100:554/stream=0",
		},
		{
			name:     "Steinel path format with password in query",
			input:    "rtsp://192.168.178.5:554/user=admin_password=supersecret_channel=1_stream=0.sdp?real_stream",
			expected: "rtsp://192.168.178.5:554/user=admin_password=***_channel=1_stream=0.sdp?real_stream",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeRTSPURL(tc.input)
			if got != tc.expected {
				t.Errorf("SanitizeRTSPURL(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestClientLoginFallback(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer func() { _ = listener.Close() }()

	port := listener.Addr().(*net.TCPAddr).Port

	// Mock server that rejects Sofia hash with 124, but accepts empty password
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

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
				var req LoginReq
				_ = json.Unmarshal(payload, &req)

				var respPayload []byte
				if req.PassWord == "" {
					// Accept empty password
					resp := LoginResp{
						Name:      "OPUserLogin",
						Ret:       100,
						SessionID: "0x00000007",
					}
					respPayload, _ = json.Marshal(resp)
				} else {
					// Reject others with 124
					resp := LoginResp{
						Name: "OPUserLogin",
						Ret:  124,
					}
					respPayload, _ = json.Marshal(resp)
				}

				respHdr := &Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  0,
					Sequence:   hdr.Sequence,
					TotalPkt:   1,
					CurPkt:     0,
					MsgID:      MsgLoginResp,
					DataLength: uint32(len(respPayload)),
				}
				_, _ = conn.Write(respHdr.Encode())
				_, _ = conn.Write(respPayload)
			} else {
				respPayload := []byte(`{"Ret":100}` + "\n\x00")
				respHdr := &Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  0,
					Sequence:   hdr.Sequence,
					TotalPkt:   1,
					CurPkt:     0,
					MsgID:      hdr.MsgID + 1,
					DataLength: uint32(len(respPayload)),
				}
				_, _ = conn.Write(respHdr.Encode())
				_, _ = conn.Write(respPayload)
			}
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "someWrongPassword")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if client.sessionID != 0x07 {
		t.Fatalf("expected session ID 0x07, got 0x%08X", client.sessionID)
	}
}

func TestMaskPassword(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "<empty>"},
		{"a", "*"},
		{"ab", "**"},
		{"abc", "***"},
		{"abcd", "abc*"},
		{"secret123", "sec******"},
		{"superlongpassword", "sup**************"},
	}

	for _, tc := range tests {
		got := MaskPassword(tc.input)
		if got != tc.expected {
			t.Errorf("MaskPassword(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestGetPasswordCandidates(t *testing.T) {
	client := NewClient("192.168.1.100", 34567, "admin", "testPass123")
	candidates := client.getPasswordCandidates()

	if len(candidates) != 3 {
		t.Fatalf("expected 3 candidates for configured password, got %d", len(candidates))
	}

	foundSofiaMobile := false
	foundSofiaPlain := false
	foundPlaintext := false

	for _, cand := range candidates {
		if cand.label == "Sofia 8-char hash (LoginType: DVRIP-Mobile)" {
			foundSofiaMobile = true
			if cand.loginType != "DVRIP-Mobile" {
				t.Errorf("expected loginType 'DVRIP-Mobile', got %q", cand.loginType)
			}
			if len(cand.password) != 8 {
				t.Errorf("expected 8-char Sofia hash, got length %d", len(cand.password))
			}
		}
		if cand.label == "Sofia 8-char hash (no LoginType)" {
			foundSofiaPlain = true
			if cand.loginType != "" {
				t.Errorf("expected empty loginType, got %q", cand.loginType)
			}
		}
		if cand.label == "Plaintext password (no LoginType)" {
			foundPlaintext = true
			if cand.password != "testPass123" {
				t.Errorf("expected plaintext password 'testPass123', got %q", cand.password)
			}
		}
	}

	if !foundSofiaMobile {
		t.Error("expected Sofia 8-char hash (LoginType: DVRIP-Mobile) candidate")
	}
	if !foundSofiaPlain {
		t.Error("expected Sofia 8-char hash (no LoginType) candidate")
	}
	if !foundPlaintext {
		t.Error("expected Plaintext candidate")
	}

	// Verify unconfigured client generates exactly 1 empty password variant
	emptyClient := NewClient("192.168.1.100", 34567, "admin", "")
	emptyCandidates := emptyClient.getPasswordCandidates()
	if len(emptyCandidates) != 1 {
		t.Fatalf("expected 1 candidate for unconfigured client, got %d", len(emptyCandidates))
	}
	if emptyCandidates[0].password != "" {
		t.Errorf("expected empty password for unconfigured client, got %q", emptyCandidates[0].password)
	}
}

func TestAES128CBC(t *testing.T) {
	key := []byte(DefaultSofiaAESKey)
	plain := []byte(`{"Name":"TestPayload","Data":12345}`)

	cipherBytes, err := encryptAES128CBC(key, plain)
	if err != nil {
		t.Fatalf("encryptAES128CBC failed: %v", err)
	}
	if len(cipherBytes)%16 != 0 {
		t.Errorf("ciphertext length %d not a multiple of 16", len(cipherBytes))
	}

	decrypted, err := decryptAES128CBC(key, cipherBytes)
	if err != nil {
		t.Fatalf("decryptAES128CBC failed: %v", err)
	}

	if string(decrypted) != string(plain) {
		t.Errorf("decrypted mismatch: got %q, want %q", string(decrypted), string(plain))
	}

	// Error handling tests
	if _, err := decryptAES128CBC(key, nil); err == nil {
		t.Error("expected error for empty ciphertext")
	}
	if _, err := decryptAES128CBC(key, []byte{1, 2, 3}); err == nil {
		t.Error("expected error for unaligned ciphertext")
	}
	if _, err := encryptAES128CBC([]byte("short"), plain); err == nil {
		t.Error("expected error for invalid key size")
	}
}

func TestRSACrypto(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	modulusHex := strings.ToUpper(fmt.Sprintf("%X", privKey.N))
	pubKeyStr := fmt.Sprintf("%s,010001", modulusHex)

	parsedPub, err := parseRSAPublicKey(pubKeyStr)
	if err != nil {
		t.Fatalf("parseRSAPublicKey failed: %v", err)
	}
	if parsedPub.N.Cmp(privKey.N) != 0 || parsedPub.E != privKey.E {
		t.Fatal("parsed public key components do not match")
	}

	encHex, err := encryptRSAPublicKey(parsedPub, []byte("testSecret"))
	if err != nil {
		t.Fatalf("encryptRSAPublicKey failed: %v", err)
	}
	if len(encHex) != 256 {
		t.Errorf("expected 256 hex chars for 1024-bit RSA, got %d", len(encHex))
	}

	rawCipher, err := hex.DecodeString(encHex)
	if err != nil {
		t.Fatalf("hex decode failed: %v", err)
	}
	//nolint:staticcheck // Verifying RSA_V1.5 decryption in test
	plain, err := rsa.DecryptPKCS1v15(rand.Reader, privKey, rawCipher)
	if err != nil {
		t.Fatalf("RSA decryption failed: %v", err)
	}
	if string(plain) != "testSecret" {
		t.Errorf("decrypted secret mismatch: got %q, want 'testSecret'", string(plain))
	}

	// Error handling
	if _, err := parseRSAPublicKey(""); err == nil {
		t.Error("expected error for empty public key")
	}
	if _, err := parseRSAPublicKey("INVALID_HEX,010001"); err == nil {
		t.Error("expected error for invalid hex modulus")
	}
}

func TestAdaptiveRSALogin(t *testing.T) {
	// Generates an RSA key to act as the camera
	privKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("failed to generate test RSA key: %v", err)
	}

	modulusHex := strings.ToUpper(fmt.Sprintf("%X", privKey.N))
	pubKeyStr := fmt.Sprintf("%s,010001", modulusHex)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer func() { _ = listener.Close() }()

	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

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

			switch hdr.MsgID {
			case MsgMonitorClaimReq: // 1413
				claimResp := MonitorClaimResp{
					Ret:         100,
					Bits:        1024,
					EncryptAlgo: "RSA_V1.5",
					PublicKey:   pubKeyStr,
					LoginEncryptionType: LoginEncryptionType{
						RSA:  true,
						MD5:  false,
						NONE: false,
					},
					NotEncryptMsgID: []int{1000, 1001, 1008, 1009},
				}
				claimBytes, _ := json.Marshal(claimResp)
				encClaim, _ := encryptAES128CBC([]byte(DefaultSofiaAESKey), claimBytes)
				b64Claim := base64.StdEncoding.EncodeToString(encClaim)

				respPayloadWithTerm := append([]byte(b64Claim), 0x0A, 0x00)
				respHdr := &Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  hdr.SessionID,
					Sequence:   hdr.Sequence,
					MsgID:      MsgMonitorClaimResp, // 1414
					DataLength: uint32(len(respPayloadWithTerm)),
				}
				_, _ = conn.Write(append(respHdr.Encode(), respPayloadWithTerm...))

			case MsgLoginReq: // 1000
				cleanB64 := strings.TrimRight(string(payload), "\x00\r\n ")
				cipherBytes, decErr := base64.StdEncoding.DecodeString(cleanB64)
				if decErr != nil {
					return
				}
				plainJSON, decErr := decryptAES128CBC([]byte(DefaultSofiaAESKey), cipherBytes)
				if decErr != nil {
					return
				}

				var req LoginReq
				if err := json.Unmarshal(plainJSON, &req); err != nil {
					return
				}

				// Validate that request uses RSA-encrypted fields and LoginType DVRIP-FutureHome
				if req.LoginType != "DVRIP-FutureHome" || req.EncryptType != "MD5" {
					return
				}

				// Decrypt username & password
				userCipher, _ := hex.DecodeString(req.UserName)
				//nolint:staticcheck // Mock server decrypting RSA_V1.5 in test
				userPlain, _ := rsa.DecryptPKCS1v15(rand.Reader, privKey, userCipher)

				pwdCipher, _ := hex.DecodeString(req.PassWord)
				//nolint:staticcheck // Mock server decrypting RSA_V1.5 in test
				pwdPlain, _ := rsa.DecryptPKCS1v15(rand.Reader, privKey, pwdCipher)

				expectedSofiaHash := HashPassword("testCameraPassword")
				if string(userPlain) != "admin" || string(pwdPlain) != expectedSofiaHash {
					resp := LoginResp{Name: "OPUserLogin", Ret: 106}
					respData, _ := json.Marshal(resp)
					respTerm := append(respData, 0x0A, 0x00)
					rHdr := Header{Magic: HeaderMagic, Channel: 1, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
					_, _ = conn.Write(append(rHdr.Encode(), respTerm...))
					continue
				}

				// Successful login response
				resp := LoginResp{
					Name:          "OPUserLogin",
					Ret:           100,
					SessionID:     "0x000000AA",
					AliveInterval: 20,
					DeviceType:    "IPC",
				}
				respData, _ := json.Marshal(resp)
				respTerm := append(respData, 0x0A, 0x00)
				rHdr := Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  0xAA,
					Sequence:   hdr.Sequence,
					MsgID:      MsgLoginResp,
					DataLength: uint32(len(respTerm)),
				}
				_, _ = conn.Write(append(rHdr.Encode(), respTerm...))

			default:
				respPayloadWithTerm := []byte(`{"Ret":100}` + "\n\x00")
				respHdr := &Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  hdr.SessionID,
					Sequence:   hdr.Sequence,
					MsgID:      hdr.MsgID + 1,
					DataLength: uint32(len(respPayloadWithTerm)),
				}
				_, _ = conn.Write(append(respHdr.Encode(), respPayloadWithTerm...))
			}
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "testCameraPassword")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("expected successful RSA login, got error: %v", err)
	}
	defer func() { _ = client.Close() }()

	if !client.IsLoggedIn() {
		t.Fatal("expected client to be logged in")
	}
	if client.sessionID != 0xAA {
		t.Fatalf("expected session ID 0xAA, got 0x%08X", client.sessionID)
	}
	if client.GetEffectivePassword() != HashPassword("testCameraPassword") {
		t.Fatalf("expected effective password to be Sofia hash %q, got %q",
			HashPassword("testCameraPassword"), client.GetEffectivePassword())
	}
}

func TestGenerateCommunicateKey(t *testing.T) {
	for i := 0; i < 20; i++ {
		key, err := generateCommunicateKey()
		if err != nil {
			t.Fatalf("failed to generate communicate key: %v", err)
		}
		if len(key) != 16 {
			t.Fatalf("expected length 16, got %d (key: %q)", len(key), key)
		}
		for _, ch := range key {
			isAlphaNum := (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
			if !isAlphaNum {
				t.Fatalf("invalid non-alphanumeric character %c in key %q", ch, key)
			}
		}
	}
}

func TestAdaptiveRSALoginCandidatesFallback(t *testing.T) {
	// Generates an RSA key to act as the camera
	privKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("failed to generate test RSA key: %v", err)
	}

	modulusHex := strings.ToUpper(fmt.Sprintf("%X", privKey.N))
	pubKeyStr := fmt.Sprintf("%s,010001", modulusHex)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer func() { _ = listener.Close() }()

	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

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

			switch hdr.MsgID {
			case MsgMonitorClaimReq: // 1413
				claimResp := MonitorClaimResp{
					Ret:         100,
					Bits:        1024,
					EncryptAlgo: "RSA_V1.5",
					PublicKey:   pubKeyStr,
					LoginEncryptionType: LoginEncryptionType{
						RSA:  true,
						MD5:  false,
						NONE: false,
					},
					NotEncryptMsgID: []int{1000, 1001, 1008, 1009},
				}
				claimBytes, _ := json.Marshal(claimResp)
				encClaim, _ := encryptAES128CBC([]byte(DefaultSofiaAESKey), claimBytes)
				b64Claim := base64.StdEncoding.EncodeToString(encClaim)

				respPayloadWithTerm := append([]byte(b64Claim), 0x0A, 0x00)
				respHdr := &Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  hdr.SessionID,
					Sequence:   hdr.Sequence,
					MsgID:      MsgMonitorClaimResp, // 1414
					DataLength: uint32(len(respPayloadWithTerm)),
				}
				_, _ = conn.Write(append(respHdr.Encode(), respPayloadWithTerm...))

			case MsgLoginReq: // 1000
				cleanB64 := strings.TrimRight(string(payload), "\x00\r\n ")
				cipherBytes, decErr := base64.StdEncoding.DecodeString(cleanB64)
				if decErr != nil {
					return
				}
				plainJSON, decErr := decryptAES128CBC([]byte(DefaultSofiaAESKey), cipherBytes)
				if decErr != nil {
					return
				}

				var req LoginReq
				if err := json.Unmarshal(plainJSON, &req); err != nil {
					return
				}

				// Decrypt CommunicateKey and verify it is a 16-char alphanumeric string
				commCipher, _ := hex.DecodeString(req.CommunicateKey)
				//nolint:staticcheck // Mock server decrypting RSA_V1.5 in test
				commPlain, _ := rsa.DecryptPKCS1v15(rand.Reader, privKey, commCipher)
				if len(commPlain) != 16 {
					t.Errorf("expected 16-byte communicate key, got %d", len(commPlain))
				}

				// Decrypt password
				pwdCipher, _ := hex.DecodeString(req.PassWord)
				//nolint:staticcheck // Mock server decrypting RSA_V1.5 in test
				pwdPlain, _ := rsa.DecryptPKCS1v15(rand.Reader, privKey, pwdCipher)

				// Candidate 1: Sofia hash -> reject with Ret: 124 (LOGIN_ENC_PWD_NOT_SUP)
				if string(pwdPlain) == HashPassword("testPlainPassword") {
					resp := LoginResp{Name: "OPUserLogin", Ret: 124}
					respData, _ := json.Marshal(resp)
					respTerm := append(respData, 0x0A, 0x00)
					rHdr := Header{Magic: HeaderMagic, Channel: 1, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
					_, _ = conn.Write(append(rHdr.Encode(), respTerm...))
					continue
				}

				// Candidate 2: Plaintext password -> accept with Ret: 100
				if string(pwdPlain) == "testPlainPassword" {
					resp := LoginResp{
						Name:          "OPUserLogin",
						Ret:           100,
						SessionID:     "0x000000BB",
						AliveInterval: 20,
						DeviceType:    "IPC",
					}
					respData, _ := json.Marshal(resp)
					respTerm := append(respData, 0x0A, 0x00)
					rHdr := Header{
						Magic:      HeaderMagic,
						Channel:    1,
						SessionID:  0xBB,
						Sequence:   hdr.Sequence,
						MsgID:      MsgLoginResp,
						DataLength: uint32(len(respTerm)),
					}
					_, _ = conn.Write(append(rHdr.Encode(), respTerm...))
					continue
				}

				// Unexpected
				resp := LoginResp{Name: "OPUserLogin", Ret: 106}
				respData, _ := json.Marshal(resp)
				respTerm := append(respData, 0x0A, 0x00)
				rHdr := Header{Magic: HeaderMagic, Channel: 1, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
				_, _ = conn.Write(append(rHdr.Encode(), respTerm...))

			default:
				respPayloadWithTerm := []byte(`{"Ret":100}` + "\n\x00")
				respHdr := &Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  hdr.SessionID,
					Sequence:   hdr.Sequence,
					MsgID:      hdr.MsgID + 1,
					DataLength: uint32(len(respPayloadWithTerm)),
				}
				_, _ = conn.Write(append(respHdr.Encode(), respPayloadWithTerm...))
			}
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "testPlainPassword")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("expected successful RSA login with candidate fallback, got error: %v", err)
	}
	defer func() { _ = client.Close() }()

	if !client.IsLoggedIn() {
		t.Fatal("expected client to be logged in")
	}
	if client.sessionID != 0xBB {
		t.Fatalf("expected session ID 0xBB, got 0x%08X", client.sessionID)
	}
	if client.GetEffectivePassword() != "testPlainPassword" {
		t.Fatalf("expected effective password to be plaintext 'testPlainPassword', got %q",
			client.GetEffectivePassword())
	}
}

func TestFormatLoginError(t *testing.T) {
	tests := []struct {
		code     int
		contains string
	}{
		{100, "success"},
		{106, "check 'camera_password'"},
		{124, "LOGIN_ENC_PWD_NOT_SUP"},
		{125, "user does not exist"},
		{126, "locked"},
		{127, "concurrent"},
		{128, "permission denied"},
		{129, "format error"},
		{999, "rejected by camera with code 999"},
	}

	for _, tc := range tests {
		msg := formatLoginError(tc.code)
		if !strings.Contains(msg, tc.contains) {
			t.Errorf("formatLoginError(%d) = %q, want it to contain %q", tc.code, msg, tc.contains)
		}
	}
}

func TestShouldEncryptWhitelist(t *testing.T) {
	c := NewClient("127.0.0.1", 34567, "admin", "secret")
	// Initially, encryption is disabled
	if c.shouldEncrypt(MsgConfigSetReq) {
		t.Error("expected shouldEncrypt to be false when dataEncryptionAES is inactive")
	}

	// Activate encryption with 16-byte CommunicateKey
	c.communicateKey = []byte("0123456789ABCDEF")
	c.dataEncryptionAES = true
	c.initNotEncryptMsgIDs([]int{1000, 1001, 1008, 1009, 1413, 1414, 1432})

	// Whitelisted messages must NOT be encrypted
	if c.shouldEncrypt(MsgLoginReq) {
		t.Error("MsgLoginReq (1000) should not be encrypted")
	}
	if c.shouldEncrypt(MsgLoginResp) {
		t.Error("MsgLoginResp (1001) should not be encrypted")
	}
	if c.shouldEncrypt(MsgLogoutReq) {
		t.Error("MsgLogoutReq (1008) should not be encrypted")
	}
	if c.shouldEncrypt(MsgMonitorClaimReq) {
		t.Error("MsgMonitorClaimReq (1413) should not be encrypted")
	}
	if c.shouldEncrypt(MsgTalkAudioData) {
		t.Error("MsgTalkAudioData (1432) should not be encrypted")
	}

	// Non-whitelisted control messages MUST be encrypted
	if !c.shouldEncrypt(MsgConfigSetReq) {
		t.Error("MsgConfigSetReq (1040) must be encrypted")
	}
	if !c.shouldEncrypt(MsgConfigGetReq) {
		t.Error("MsgConfigGetReq (1042) must be encrypted")
	}
	if !c.shouldEncrypt(MsgSysManagerReq) {
		t.Error("MsgSysManagerReq (1020) must be encrypted")
	}
	if !c.shouldEncrypt(MsgKeepAliveReq) {
		t.Error("MsgKeepAliveReq (1006) must be encrypted")
	}
	if !c.shouldEncrypt(MsgFileSearchReq) {
		t.Error("MsgFileSearchReq (1440) must be encrypted")
	}
}

func TestEncryptedPostLoginControlMessages(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("failed to generate test RSA key: %v", err)
	}

	modulusHex := strings.ToUpper(fmt.Sprintf("%X", privKey.N))
	pubKeyStr := fmt.Sprintf("%s,010001", modulusHex)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer func() { _ = listener.Close() }()

	port := listener.Addr().(*net.TCPAddr).Port

	var capturedCommKey []byte
	receivedEncrypted1578 := make(chan bool, 1)
	receivedEncrypted1572Light := make(chan bool, 1)
	receivedEncrypted1572Query := make(chan bool, 1)
	receivedEncrypted1006 := make(chan bool, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

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

			cleanPayload := strings.TrimRight(string(payload), "\x00\r\n ")

			switch hdr.MsgID {
			case MsgMonitorClaimReq: // 1413
				claimResp := MonitorClaimResp{
					Ret:         100,
					Bits:        1024,
					EncryptAlgo: "RSA_V1.5",
					PublicKey:   pubKeyStr,
					LoginEncryptionType: LoginEncryptionType{
						RSA: true,
					},
					DataEncryptionType: map[string]bool{
						"AES":   true,
						"AESV2": true,
					},
					NotEncryptMsgID: []int{1000, 1001, 1008, 1009, 1413, 1414, 1432, 1434, 1435, 1580, 1582},
				}
				claimBytes, _ := json.Marshal(claimResp)
				encClaim, _ := encryptAES128CBC([]byte(DefaultSofiaAESKey), claimBytes)
				b64Claim := base64.StdEncoding.EncodeToString(encClaim)

				respPayloadWithTerm := append([]byte(b64Claim), 0x00)
				respHdr := &Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  hdr.SessionID,
					Sequence:   hdr.Sequence,
					MsgID:      MsgMonitorClaimResp,
					DataLength: uint32(len(respPayloadWithTerm)),
				}
				_, _ = conn.Write(append(respHdr.Encode(), respPayloadWithTerm...))

			case MsgLoginReq: // 1000
				cipherBytes, _ := base64.StdEncoding.DecodeString(cleanPayload)
				plainJSON, _ := decryptAES128CBC([]byte(DefaultSofiaAESKey), cipherBytes)

				var req LoginReq
				_ = json.Unmarshal(plainJSON, &req)

				commCipher, _ := hex.DecodeString(req.CommunicateKey)
				//nolint:staticcheck // Mock server decrypting RSA_V1.5 in test
				capturedCommKey, _ = rsa.DecryptPKCS1v15(rand.Reader, privKey, commCipher)

				resp := LoginResp{
					Name:          "OPUserLogin",
					Ret:           100,
					SessionID:     "0x000000CC",
					AliveInterval: 20,
					DeviceType:    "IPC",
				}
				respData, _ := json.Marshal(resp)
				respTerm := append(respData, 0x00)
				rHdr := Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  0xCC,
					Sequence:   hdr.Sequence,
					MsgID:      MsgLoginResp,
					DataLength: uint32(len(respTerm)),
				}
				_, _ = conn.Write(append(rHdr.Encode(), respTerm...))

			case MsgTransStartReq: // 1578 (OPTrans RS232 start)
				if len(cleanPayload) > 0 && cleanPayload[0] != '{' {
					decB64, _ := base64.StdEncoding.DecodeString(cleanPayload)
					plain, _ := decryptAES128CBC(capturedCommKey, decB64)
					if strings.Contains(string(plain), "OPTrans") {
						receivedEncrypted1578 <- true
					}
				}

				respJSON := []byte(`{"Name":"OPTrans","Ret":100}`)
				encResp, _ := encryptAES128CBC(capturedCommKey, respJSON)
				b64Resp := base64.StdEncoding.EncodeToString(encResp)
				respTerm := append([]byte(b64Resp), 0x00)

				rHdr := Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  0xCC,
					Sequence:   hdr.Sequence,
					MsgID:      MsgTransStartResp, // 1579
					DataLength: uint32(len(respTerm)),
				}
				_, _ = conn.Write(append(rHdr.Encode(), respTerm...))

			case MsgTransSendData: // 1572 (MCU commands via OPTrans)
				var plainCmd string
				if len(cleanPayload) > 0 && cleanPayload[0] != '{' {
					decB64, _ := base64.StdEncoding.DecodeString(cleanPayload)
					plain, _ := decryptAES128CBC(capturedCommKey, decB64)
					plainCmd = string(plain)
				} else {
					plainCmd = cleanPayload
				}

				if strings.Contains(plainCmd, "BMdU") {
					receivedEncrypted1572Light <- true
				} else if strings.Contains(plainCmd, "BFbU") {
					receivedEncrypted1572Query <- true
				}

				// 1. Send MsgTransSendResp (1573) ack
				respJSON := []byte(`{"Name":"OPTrans","Ret":100}`)
				encResp, _ := encryptAES128CBC(capturedCommKey, respJSON)
				b64Resp := base64.StdEncoding.EncodeToString(encResp)
				respTerm := append([]byte(b64Resp), 0x00)

				rHdr := Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  0xCC,
					Sequence:   hdr.Sequence,
					MsgID:      MsgTransSendResp, // 1573
					DataLength: uint32(len(respTerm)),
				}
				_, _ = conn.Write(append(rHdr.Encode(), respTerm...))

				// 2. Immediately push unencrypted MsgTransSerialData (1582) telemetry frame
				// Mode 'd' indicates light on (conversion 3)
				telemetryData := []byte("BpbndzazazOU\x00")
				tHdr := Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  0xCC,
					Sequence:   hdr.Sequence + 1,
					MsgID:      MsgTransSerialData, // 1582
					DataLength: uint32(len(telemetryData)),
				}
				_, _ = conn.Write(append(tHdr.Encode(), telemetryData...))

			case MsgKeepAliveReq: // 1006 (SendKeepAlive)
				if len(cleanPayload) > 0 && cleanPayload[0] != '{' {
					decB64, _ := base64.StdEncoding.DecodeString(cleanPayload)
					plain, _ := decryptAES128CBC(capturedCommKey, decB64)
					if strings.Contains(string(plain), "KeepAlive") {
						receivedEncrypted1006 <- true
					}
				}

				respJSON := []byte(`{"Name":"KeepAlive","Ret":100}`)
				encResp, _ := encryptAES128CBC(capturedCommKey, respJSON)
				b64Resp := base64.StdEncoding.EncodeToString(encResp)
				respTerm := append([]byte(b64Resp), 0x00)

				rHdr := Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  0xCC,
					Sequence:   hdr.Sequence,
					MsgID:      MsgKeepAliveResp, // 1007
					DataLength: uint32(len(respTerm)),
				}
				_, _ = conn.Write(append(rHdr.Encode(), respTerm...))

			default:
				respJSON := []byte(`{"Ret":100}`)
				encResp, _ := encryptAES128CBC(capturedCommKey, respJSON)
				b64Resp := base64.StdEncoding.EncodeToString(encResp)
				respTerm := append([]byte(b64Resp), 0x00)
				rHdr := Header{
					Magic:      HeaderMagic,
					Channel:    1,
					SessionID:  0xCC,
					Sequence:   hdr.Sequence,
					MsgID:      hdr.MsgID + 1,
					DataLength: uint32(len(respTerm)),
				}
				_, _ = conn.Write(append(rHdr.Encode(), respTerm...))
			}
		}
	}()

	client := NewClient("127.0.0.1", port, "admin", "testCameraPassword")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() { _ = client.Close() }()

	if !client.IsLoggedIn() {
		t.Fatal("expected client to be logged in")
	}

	// Verify MsgTransStartReq (1578) was sent automatically on login
	select {
	case <-receivedEncrypted1578:
	case <-time.After(1 * time.Second):
		t.Error("timed out waiting for encrypted MsgTransStartReq (1578)")
	}

	// 1. Test SetLightState via MsgTransSendData (1572) -> "BMdU"
	if err := client.SetLightState(true); err != nil {
		t.Fatalf("SetLightState failed: %v", err)
	}
	select {
	case <-receivedEncrypted1572Light:
	case <-time.After(1 * time.Second):
		t.Error("timed out waiting for encrypted MsgID 1572 (Light ON BMdU)")
	}

	// 2. Test QueryMCUConfig via MsgTransSendData (1572) -> "BFbU"
	mcuCfg, err := client.QueryMCUConfig()
	if err != nil {
		t.Fatalf("QueryMCUConfig failed: %v", err)
	}
	if mcuCfg == nil || !mcuCfg.IsLightOn() {
		t.Errorf("expected light on in mcuCfg, got %+v", mcuCfg)
	}
	select {
	case <-receivedEncrypted1572Query:
	case <-time.After(1 * time.Second):
		t.Error("timed out waiting for encrypted MsgID 1572 (Query BFbU)")
	}

	// 3. Test QueryLightState using the received telemetry
	lightOn, err := client.QueryLightState()
	if err != nil {
		t.Fatalf("QueryLightState failed: %v", err)
	}
	if !lightOn {
		t.Error("expected lightOn == true")
	}

	// 4. Test SendKeepAlive (MsgID 1006)
	if err := client.SendKeepAlive(); err != nil {
		t.Fatalf("SendKeepAlive failed: %v", err)
	}
	select {
	case <-receivedEncrypted1006:
	case <-time.After(1 * time.Second):
		t.Error("timed out waiting for encrypted MsgID 1006")
	}
}

func TestMCUTelemetryLiveTraces(t *testing.T) {
	// Test the actual live telemetry strings captured from Steinel L 620 CAM
	// 1. Idle (Sensor Mode / Off): "BpbnbzasazOU" (Issue #45)
	cfg1, err := ParseMCUString("BpbnbzasazOU")
	if err != nil {
		t.Fatalf("failed to parse live frame 1: %v", err)
	}
	if cfg1.IsLightOn() {
		t.Errorf("expected light off in frame 1, got mode %d", cfg1.Mode)
	}
	if cfg1.Mode != 1 { // 'b' == 1 (Sensor Auto)
		t.Errorf("expected mode 1, got %d", cfg1.Mode)
	}
	// 'n' = 13 -> 25 - 13 = 12 -> LuxTable[12] = 100 Lux
	if cfg1.TwilightLux != 100 {
		t.Errorf("expected 100 lux in frame 1, got %d", cfg1.TwilightLux)
	}
	// 'b' = 1 -> 1 * 60 = 60s (1 min)
	if cfg1.HighlightDelaySec != 60 {
		t.Errorf("expected 60s delay in frame 1, got %d", cfg1.HighlightDelaySec)
	}
	// 's' = 18 -> 18 * 4 = 72%
	if cfg1.Highlight != 72 {
		t.Errorf("expected 72%% highlight in frame 1, got %d", cfg1.Highlight)
	}

	// 2. Light ON (Continuous Mode): "BpbndzazazOU"
	cfg2, err := ParseMCUString("BpbndzazazOU")
	if err != nil {
		t.Fatalf("failed to parse live frame 2: %v", err)
	}
	if !cfg2.IsLightOn() {
		t.Errorf("expected light on in frame 2, got mode %d", cfg2.Mode)
	}
	if cfg2.Mode != 3 { // 'd' == 3 (Continuous Light)
		t.Errorf("expected mode 3, got %d", cfg2.Mode)
	}

	// 3. Highlight changed to 68%: "BpbnbzarazOU"
	cfg4, err := ParseMCUString("BpbnbzarazOU")
	if err != nil {
		t.Fatalf("failed to parse live frame 4: %v", err)
	}
	// 'r' = 17 -> 17 * 4 = 68%
	if cfg4.Highlight != 68 {
		t.Errorf("expected 68%% highlight in frame 4, got %d", cfg4.Highlight)
	}

	// 4. Live frame from Tracelog.txt (Issue #45 with 175 Lux): "BpbkbzacazOU"
	cfg5, err := ParseMCUString("BpbkbzacazOU")
	if err != nil {
		t.Fatalf("failed to parse live frame 5: %v", err)
	}
	// 'p' = 15 -> Medium -> 6m
	if cfg5.Distance != 6 {
		t.Errorf("expected 6m distance in frame 5, got %d", cfg5.Distance)
	}
	// 'b' = 1 -> 60s (1 min)
	if cfg5.HighlightDelaySec != 60 {
		t.Errorf("expected 60s delay in frame 5, got %d", cfg5.HighlightDelaySec)
	}
	// 'k' = 10 -> 25 - 10 = 15 -> LuxTable[15] = 175 Lux
	if cfg5.TwilightLux != 175 {
		t.Errorf("expected 175 lux in frame 5, got %d", cfg5.TwilightLux)
	}
	// 'b' = 1 (Sensor Auto)
	if cfg5.Mode != 1 || cfg5.IsLightOn() {
		t.Errorf("expected sensor auto mode (light off) in frame 5, got mode %d", cfg5.Mode)
	}
	// 'a' = 0 (Lowlight Off)
	if cfg5.Lowlight != 0 {
		t.Errorf("expected 0%% lowlight in frame 5, got %d", cfg5.Lowlight)
	}
}

func TestFragmentedTelemetryReassembly(t *testing.T) {
	client := NewClient("127.0.0.1", 34567, "admin", "secret")

	// Chunk 1: "Bpbkbzac" (8 bytes, without terminating 'U')
	client.handleTelemetryPushLocked("Bpbkbzac")
	if client.GetLastMCUConfig() != nil {
		t.Errorf("expected nil config before receiving terminating 'U', got %+v", client.GetLastMCUConfig())
	}
	if client.telemetryBuf != "Bpbkbzac" {
		t.Errorf("expected buffered fragment 'Bpbkbzac', got %q", client.telemetryBuf)
	}

	// Chunk 2: "azOU" (4 bytes, terminating with 'U')
	client.handleTelemetryPushLocked("azOU")
	cfg := client.GetLastMCUConfig()
	if cfg == nil {
		t.Fatal("expected reassembled MCUConfig, got nil")
	}
	if client.telemetryBuf != "" {
		t.Errorf("expected cleared telemetryBuf, got %q", client.telemetryBuf)
	}

	// Verify fields of reassembled "BpbkbzacazOU"
	if cfg.Distance != 6 {
		t.Errorf("expected 6m distance, got %d", cfg.Distance)
	}
	if cfg.HighlightDelaySec != 60 {
		t.Errorf("expected 60s delay, got %d", cfg.HighlightDelaySec)
	}
	if cfg.TwilightLux != 175 {
		t.Errorf("expected 175 lux, got %d", cfg.TwilightLux)
	}
	if cfg.IsLightOn() {
		t.Errorf("expected light off in reassembled frame")
	}
}
