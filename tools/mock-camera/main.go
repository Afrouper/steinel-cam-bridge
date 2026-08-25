package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

// Sofia Header constants
const (
	HeaderMagic  byte = 0xFF
	HeaderLength int  = 20
)

// Known Xiongmai Message IDs
var msgNames = map[uint16]string{
	1000: "MsgLoginReq",
	1001: "MsgLoginResp",
	1002: "MsgLogoutReq",
	1006: "MsgKeepAliveReq",
	1007: "MsgKeepAliveResp",
	1020: "MsgSysManagerReq",
	1021: "MsgSysManagerResp",
	1040: "MsgConfigSetReq",
	1041: "MsgConfigSetResp",
	1042: "MsgConfigGetReq",
	1043: "MsgConfigGetResp",
	1410: "MsgTalkClaimReq",
	1411: "MsgTalkClaimResp",
	1412: "MsgTalkSendData",
	1420: "MsgMonitorReq",
	1421: "MsgMonitorResp",
	1500: "MsgAlarmReq",
	1501: "MsgAlarmResp",
}

func getMsgName(msgID uint16) string {
	if name, ok := msgNames[msgID]; ok {
		return fmt.Sprintf("%s (%d)", name, msgID)
	}
	return fmt.Sprintf("UnknownMsgID (%d)", msgID)
}

func main() {
	port := flag.Int("port", 34567, "TCP port to listen on for Xiongmai Sofia protocol")
	udpPort := flag.Int("udp", 34569, "UDP port for Xiongmai broadcast discovery (0 to disable)")
	flag.Parse()

	log.Printf("═══════════════════════════════════════════════════════════════════")
	log.Printf("🎥 Steinel L 620 CAM — Mock Sofia Protocol Interception Server")
	log.Printf("📡 Listening on TCP port :%d (Xiongmai Sofia Daemon)", *port)
	if *udpPort > 0 {
		log.Printf("🛰️ Listening on UDP port :%d (Broadcast Discovery)", *udpPort)
		go startUDPDiscoveryListener(*udpPort)
	}
	log.Printf("💡 Ready to capture packets from the Steinel macOS / iOS App")
	log.Printf("═══════════════════════════════════════════════════════════════════")

	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", *port))
	if err != nil {
		log.Fatalf("❌ Failed to listen on TCP port %d: %v", *port, err)
	}
	defer func() { _ = listener.Close() }()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("\n🛑 Shutting down mock server...")
		_ = listener.Close()
		os.Exit(0)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-sigChan:
				return
			default:
				log.Printf("⚠️ Accept error: %v", err)
				continue
			}
		}
		go handleClient(conn)
	}
}

func handleClient(conn net.Conn) {
	remoteAddr := conn.RemoteAddr().String()
	log.Printf("\n🟢 [Client Connected] New connection from %s", remoteAddr)
	defer func() {
		log.Printf("🔴 [Client Disconnected] Connection closed from %s\n", remoteAddr)
		_ = conn.Close()
	}()

	seq := uint32(0)
	sessionID := uint32(0x00000001)

	headerBuf := make([]byte, HeaderLength)

	for {
		// Read 20-byte Sofia header
		if _, err := io.ReadFull(conn, headerBuf); err != nil {
			if err != io.EOF {
				log.Printf("[%s] ⚠️ Header read error: %v", remoteAddr, err)
			}
			return
		}

		if headerBuf[0] != HeaderMagic {
			log.Printf("[%s] ⚠️ Invalid magic byte: 0x%02X (expected 0xFF)", remoteAddr, headerBuf[0])
			return
		}

		recvSessionID := binary.LittleEndian.Uint32(headerBuf[4:8])
		recvSeq := binary.LittleEndian.Uint32(headerBuf[8:12])
		msgID := binary.LittleEndian.Uint16(headerBuf[14:16])
		dataLen := binary.LittleEndian.Uint32(headerBuf[16:20])

		if recvSessionID != 0 {
			sessionID = recvSessionID
		}

		// Read payload
		payload := make([]byte, dataLen)
		if dataLen > 0 {
			if _, err := io.ReadFull(conn, payload); err != nil {
				log.Printf("[%s] ⚠️ Payload read error (%d bytes): %v", remoteAddr, dataLen, err)
				return
			}
		}

		// Format and display received message
		printCapturedPacket(remoteAddr, msgID, recvSessionID, recvSeq, payload)

		// Send appropriate mock response
		respPayload := buildMockResponse(msgID, sessionID, payload)
		respMsgID := msgID + 1

		respHeader := make([]byte, HeaderLength)
		respHeader[0] = HeaderMagic
		respHeader[1] = 0x00
		binary.LittleEndian.PutUint32(respHeader[4:8], sessionID)
		binary.LittleEndian.PutUint32(respHeader[8:12], seq)
		seq++
		respHeader[12] = 0 // TotalPacket
		respHeader[13] = 0 // CurPacket
		binary.LittleEndian.PutUint16(respHeader[14:16], respMsgID)
		binary.LittleEndian.PutUint32(respHeader[16:20], uint32(len(respPayload)))

		if _, err := conn.Write(respHeader); err != nil {
			log.Printf("[%s] ⚠️ Failed to send response header: %v", remoteAddr, err)
			return
		}
		if len(respPayload) > 0 {
			if _, err := conn.Write(respPayload); err != nil {
				log.Printf("[%s] ⚠️ Failed to send response payload: %v", remoteAddr, err)
				return
			}
		}

		log.Printf("[%s] 📤 Sent Response: %s (Payload: %d bytes)", remoteAddr, getMsgName(respMsgID), len(respPayload))
	}
}

func printCapturedPacket(remote string, msgID uint16, sessionID, seq uint32, payload []byte) {
	log.Printf("───────────────────────────────────────────────────────────────────")
	log.Printf("📥 [%s] CAPTURED: %s", remote, getMsgName(msgID))
	log.Printf("   SessionID: 0x%08X | Sequence: %d | Payload Length: %d bytes", sessionID, seq, len(payload))

	if len(payload) == 0 {
		return
	}

	// Try to format as JSON
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, payload, "   ", "  "); err == nil {
		log.Printf("   📜 JSON Payload:\n   %s", prettyJSON.String())
	} else {
		log.Printf("   📝 Text Payload: %s", string(payload))
		if len(payload) <= 128 {
			log.Printf("   🔍 Hex Dump:\n%s", hex.Dump(payload))
		}
	}
	log.Printf("───────────────────────────────────────────────────────────────────")
}

func buildMockResponse(msgID uint16, sessionID uint32, reqPayload []byte) []byte {
	sessionHex := fmt.Sprintf("0x%08X", sessionID)

	switch msgID {
	case 1000: // MsgLoginReq
		resp := map[string]interface{}{
			"Ret":           100, // 100 = Success (EE_OK)
			"SessionID":     sessionHex,
			"AliveInterval": 30,
			"ChannelNum":    1,
			"DeviceType ":   "DVR",
			"ExtraChannel":  0,
			"DataUseAES":    false,
		}
		data, _ := json.Marshal(resp)
		return data

	case 1006: // MsgKeepAliveReq
		resp := map[string]interface{}{
			"Ret":       100,
			"SessionID": sessionHex,
			"Data":      "KeepAlive",
		}
		data, _ := json.Marshal(resp)
		return data

	case 1042: // MsgConfigGetReq
		var reqMap map[string]interface{}
		_ = json.Unmarshal(reqPayload, &reqMap)
		reqName, _ := reqMap["Name"].(string)

		resp := map[string]interface{}{
			"Ret":       100,
			"SessionID": sessionHex,
			"Name":      reqName,
		}

		// Provide mock answers for common config queries
		switch reqName {
		case "SystemInfo":
			resp["SystemInfo"] = map[string]interface{}{
				"DeviceModel":     "Steinel-L620-CAM",
				"SerialNo":        "0011223344556677",
				"SoftWareVersion": "V4.02.R12.00012345.10001",
				"BuildTime":       "2023-01-01 12:00:00",
				"HardwareVersion": "L620_V1.0",
				"ChannelNum":      1,
				"AlarmInChannel":  1,
				"AlarmOutChannel": 1,
				"ExtraChannel":    0,
				"VideoInChannel":  1,
				"CombineSwitch":   0,
				"DigChannel":      0,
				"TalkInChannel":   1,
				"TalkOutChannel":  1,
				"AudioInChannel":  1,
			}
		case "NetWork.RTSP":
			resp["NetWork.RTSP"] = map[string]interface{}{
				"IsServer": true,
				"Port":     554,
			}
		default:
			resp["Data"] = map[string]interface{}{
				"Status": "OK",
			}
		}

		data, _ := json.Marshal(resp)
		return data

	case 1040: // MsgConfigSetReq
		var reqMap map[string]interface{}
		_ = json.Unmarshal(reqPayload, &reqMap)
		reqName, _ := reqMap["Name"].(string)

		resp := map[string]interface{}{
			"Ret":       100,
			"SessionID": sessionHex,
			"Name":      reqName,
		}
		data, _ := json.Marshal(resp)
		return data

	case 1410: // MsgTalkClaimReq (2-Way Audio Claim)
		resp := map[string]interface{}{
			"Ret":       100,
			"SessionID": sessionHex,
			"AudioFormat": map[string]interface{}{
				"BitRate":    64,
				"SampleBit":  16,
				"SampleRate": 8000,
				"EncodeType": "G711_ALAW",
			},
		}
		data, _ := json.Marshal(resp)
		return data

	default:
		resp := map[string]interface{}{
			"Ret":       100,
			"SessionID": sessionHex,
		}
		data, _ := json.Marshal(resp)
		return data
	}
}

// startUDPDiscoveryListener listens for Xiongmai broadcast discovery probes
func startUDPDiscoveryListener(port int) {
	addr := net.UDPAddr{
		Port: port,
		IP:   net.ParseIP("0.0.0.0"),
	}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		log.Printf("⚠️ UDP discovery listener failed on port %d: %v", port, err)
		return
	}
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 2048)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		log.Printf("🛰️ [UDP Discovery] Received %d bytes probe from %s", n, remoteAddr.String())
		if n > 0 {
			log.Printf("   Payload: %s", string(buf[:n]))
		}
	}
}
