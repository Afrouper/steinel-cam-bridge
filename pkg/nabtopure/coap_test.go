package nabtopure

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestCoAPEncodeDecode(t *testing.T) {
	req := NewRequest(CodePOST, "/webrtc/tracks", ContentFormatApplicationJSON, []byte("{\"tracks\":[\"frontdoor-video\"]}"))
	req.MessageID = 0x1234
	req.Token = []byte{0xAA, 0xBB, 0xCC, 0xDD}

	encoded, err := req.Encode()
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	decoded, err := DecodeCoAPMessage(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if decoded.Code != CodePOST {
		t.Fatalf("expected Code %d, got %d", CodePOST, decoded.Code)
	}
	if decoded.MessageID != 0x1234 {
		t.Fatalf("expected MessageID 0x1234, got 0x%04x", decoded.MessageID)
	}
	if !bytes.Equal(decoded.Token, req.Token) {
		t.Fatalf("token mismatch: %v vs %v", decoded.Token, req.Token)
	}
	if string(decoded.Payload) != "{\"tracks\":[\"frontdoor-video\"]}" {
		t.Fatalf("payload mismatch: %s", string(decoded.Payload))
	}
	if len(decoded.Options) != 3 { // UriPath "webrtc", UriPath "tracks", ContentFormat
		t.Fatalf("expected 3 options, got %d", len(decoded.Options))
	}
}

func TestCoAPClientMock(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()

	coapClient := NewCoAPClient(clientConn)

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := clientConn.Read(buf)
			if err != nil {
				return
			}
			coapClient.HandleIncomingPacket(buf[:n])
		}
	}()

	go func() {
		buf := make([]byte, 1024)
		n, err := serverConn.Read(buf)
		if err != nil {
			return
		}
		req, err := DecodeCoAPMessage(buf[:n])
		if err != nil {
			return
		}

		// Reply with 2.05 Content: {"SignalingStreamPort": 42}
		resp := &CoAPMessage{
			Type:      TypeACK,
			Code:      69, // 2.05 Content (2<<5 | 5 = 69)
			MessageID: req.MessageID,
			Token:     req.Token,
			Payload:   []byte("{\"SignalingStreamPort\":42}"),
		}
		respBytes, _ := resp.Encode()
		_, _ = serverConn.Write(respBytes)
	}()

	req := NewRequest(CodeGET, "/p2p/webrtc-info", 0, nil)
	resp, err := coapClient.Execute(req, 2*time.Second)
	if err != nil {
		t.Fatalf("coapClient.Execute failed: %v", err)
	}

	if resp.StatusCode() != 205 {
		t.Fatalf("expected status 205, got %d", resp.StatusCode())
	}
	if string(resp.Payload) != "{\"SignalingStreamPort\":42}" {
		t.Fatalf("expected payload, got: %s", string(resp.Payload))
	}
}

func TestCoAPClientClose(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()

	coapClient := NewCoAPClient(clientConn)

	// Read request on serverConn so net.Pipe() Write doesn't block synchronously
	go func() {
		buf := make([]byte, 1024)
		_, _ = serverConn.Read(buf)
	}()

	errChan := make(chan error, 1)
	go func() {
		req := NewRequest(CodeGET, "/p2p/webrtc-info", 0, nil)
		_, err := coapClient.Execute(req, 5*time.Second)
		errChan <- err
	}()

	time.Sleep(50 * time.Millisecond)
	coapClient.Close()

	select {
	case err := <-errChan:
		if err == nil {
			t.Fatalf("expected error from Execute after Close, got nil")
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("Execute did not unblock within 1s after Close")
	}
}

func TestCoAPOptionsSorted(t *testing.T) {
	msg := &CoAPMessage{
		Type: TypeCON,
		Code: CodeGET,
		Options: []Option{
			{Number: OptionContentFormat, Value: []byte{0x32}},
			{Number: OptionUriPath, Value: []byte("test")},
			{Number: OptionUriHost, Value: []byte("host")},
		},
	}

	encoded, err := msg.Encode()
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	decoded, err := DecodeCoAPMessage(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if len(decoded.Options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(decoded.Options))
	}

	// Verify ascending order
	if decoded.Options[0].Number != OptionUriHost ||
		decoded.Options[1].Number != OptionUriPath ||
		decoded.Options[2].Number != OptionContentFormat {
		t.Fatalf("options not in ascending order: %+v", decoded.Options)
	}
}
