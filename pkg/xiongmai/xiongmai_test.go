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
