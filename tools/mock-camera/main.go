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
	1530: "MsgSearchDeviceReq",
	1531: "MsgSearchDeviceResp",
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
	serialNo := flag.String("sn", "0011223344556677", "16-character camera serial number")
	customIP := flag.String("ip", "", "Custom IP address to announce (leave empty for auto-detection)")
	flag.Parse()

	log.Printf("═══════════════════════════════════════════════════════════════════")
	log.Printf("🎥 Steinel L 620 CAM — Mock Sofia Protocol Interception Server")
	log.Printf("📡 Listening on TCP port :%d (Xiongmai Sofia Daemon)", *port)
	log.Printf("🔑 Mock Camera SerialNo / DeviceID: %s", *serialNo)
	if *customIP != "" {
		log.Printf("🌐 Explicit Announcement IP: %s", *customIP)
	}
	if *udpPort > 0 {
		log.Printf("🛰️ Listening on UDP port :%d (Broadcast Discovery)", *udpPort)
		go startUDPDiscoveryListener(*udpPort, *port, *serialNo, *customIP)
	}
	log.Printf("💡 Ready to capture packets from the Steinel macOS / iOS App")
	log.Printf("═══════════════════════════════════════════════════════════════════")

	listener, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", *port))
	if err != nil {
		log.Fatalf("❌ Failed to listen on TCP port %d: %v", *port, err)
	}
	defer func() { _ = listener.Close() }()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("\n🛑 Shutting down mock server...")
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

	cleanedPayload := bytes.TrimRight(payload, "\x00\r\n ")
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, cleanedPayload, "   ", "  "); err == nil {
		log.Printf("   📜 JSON Payload:\n   %s", prettyJSON.String())
	} else {
		log.Printf("   📝 Text Payload: %s", string(cleanedPayload))
		log.Printf("   🔍 Hex Dump:\n%s", hex.Dump(payload))
	}
	log.Printf("───────────────────────────────────────────────────────────────────")
}

func buildMockResponse(msgID uint16, sessionID uint32, reqPayload []byte) []byte {
	sessionHex := fmt.Sprintf("0x%08X", sessionID)

	var resp map[string]interface{}

	switch msgID {
	case 1000: // MsgLoginReq
		resp = map[string]interface{}{
			"Ret":           100, // 100 = Success (EE_OK)
			"SessionID":     sessionHex,
			"AliveInterval": 30,
			"ChannelNum":    1,
			"DeviceType":    "DVR",
			"ExtraChannel":  0,
			"DataUseAES":    false,
		}

	case 1006: // MsgKeepAliveReq
		resp = map[string]interface{}{
			"Ret":       100,
			"SessionID": sessionHex,
			"Data":      "KeepAlive",
		}

	case 1042: // MsgConfigGetReq
		var reqMap map[string]interface{}
		_ = json.Unmarshal(bytes.TrimRight(reqPayload, "\x00\r\n "), &reqMap)
		reqName, _ := reqMap["Name"].(string)

		resp = map[string]interface{}{
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

	case 1040: // MsgConfigSetReq
		var reqMap map[string]interface{}
		_ = json.Unmarshal(bytes.TrimRight(reqPayload, "\x00\r\n "), &reqMap)
		reqName, _ := reqMap["Name"].(string)

		resp = map[string]interface{}{
			"Ret":       100,
			"SessionID": sessionHex,
			"Name":      reqName,
		}

	case 1410: // MsgTalkClaimReq (2-Way Audio Claim)
		resp = map[string]interface{}{
			"Ret":       100,
			"SessionID": sessionHex,
			"AudioFormat": map[string]interface{}{
				"BitRate":    64,
				"SampleBit":  16,
				"SampleRate": 8000,
				"EncodeType": "G711_ALAW",
			},
		}

	default:
		resp = map[string]interface{}{
			"Ret":       100,
			"SessionID": sessionHex,
		}
	}

	data, _ := json.Marshal(resp)
	return append(data, 0x0A, 0x00)
}

// startUDPDiscoveryListener listens for Xiongmai broadcast discovery probes
func startUDPDiscoveryListener(udpPort, tcpPort int, serialNo, customIP string) {
	addr := net.UDPAddr{
		Port: udpPort,
		IP:   net.ParseIP("0.0.0.0"),
	}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		log.Printf("⚠️ UDP discovery listener failed on port %d: %v", udpPort, err)
		return
	}
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 2048)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		if n >= HeaderLength && buf[0] == HeaderMagic {
			msgID := binary.LittleEndian.Uint16(buf[14:16])
			// Only handle discovery requests (MsgSearchDeviceReq = 1530)
			if msgID != 1530 {
				continue
			}

			recvSessionID := binary.LittleEndian.Uint32(buf[4:8])
			recvSeq := binary.LittleEndian.Uint32(buf[8:12])
			dataLen := binary.LittleEndian.Uint32(buf[16:20])

			// Determine local IP on the network that reached the client
			localIP := "127.0.0.1"
			if customIP != "" {
				localIP = customIP
			} else if connAddr, err := net.Dial("udp", remoteAddr.String()); err == nil {
				if lAddr, ok := connAddr.LocalAddr().(*net.UDPAddr); ok {
					localIP = lAddr.IP.String()
				}
				_ = connAddr.Close()
			}

			// Ignore probes from our own machine
			if remoteAddr.IP.String() == localIP || remoteAddr.IP.IsLoopback() {
				continue
			}

			log.Printf("🛰️ [UDP Discovery] Received %s (MsgID: %d, Session: 0x%08X, Seq: %d) from %s", getMsgName(msgID), msgID, recvSessionID, recvSeq, remoteAddr.String())
			log.Printf("   🔍 Raw Request Header (20 bytes):\n%s", hex.Dump(buf[:HeaderLength]))
			if dataLen > 0 && n >= HeaderLength+int(dataLen) {
				log.Printf("   📜 Probe Payload (%d bytes): %s", dataLen, string(buf[HeaderLength:HeaderLength+int(dataLen)]))
			}

			respData := map[string]interface{}{
				"Ret":       100,
				"Name":      "NetWork.NetCommon",
				"SessionID": fmt.Sprintf("0x%08X", recvSessionID),
				"NetWork.NetCommon": map[string]interface{}{
					"ChannelNum":    1,
					"DeviceType":    "IPC",
					"GateWay":       "192.168.88.1",
					"HostIP":        localIP,
					"HostName":      "IPC",
					"HttpPort":      80,
					"MAC":           "00:12:16:a1:b2:c3",
					"MaxBps":        0,
					"MonMode":       "TCP",
					"SSLPort":       8443,
					"Submask":       "255.255.255.0",
					"SubMask":       "255.255.255.0",
					"TCPMaxConn":    10,
					"TCPPort":       tcpPort,
					"TransferPlan":  "Auto",
					"UDPPort":       34568,
					"UseHSDownLoad": false,
				},
			}
			jsonPayload, _ := json.Marshal(respData)
			jsonPayload = append(jsonPayload, 0x0A, 0x00)

			// 1. Binary NetCommon V2 Struct (260 bytes) Response (for FunSDK native C parser)
			binV2Payload := buildBinaryNetCommonV2(serialNo, localIP, tcpPort)
			binV2Packet := buildSofiaPacketWithChannel(buf[1], 1531, recvSessionID, recvSeq, binV2Payload)
			_ = sendUDPPacket(conn, binV2Packet, remoteAddr, udpPort)

			// 2. JSON Discovery Response (for Java / Swift / HTTP layers)
			jsonPacket := buildSofiaPacketWithChannel(buf[1], 1531, recvSessionID, recvSeq, jsonPayload)
			_ = sendUDPPacket(conn, jsonPacket, remoteAddr, udpPort)

			log.Printf("   📤 Sent Discovery Responses (BinV2 + JSON) to %s (DeviceID: %s, Announced IP: %s, TCP Port: %d)", remoteAddr.String(), serialNo, localIP, tcpPort)
		} else {
			log.Printf("🛰️ [UDP Discovery] Received %d bytes from %s (Hex: %x)", n, remoteAddr.String(), buf[:n])
		}
	}
}

func buildSofiaPacketWithChannel(channel byte, msgID uint16, sessionID, seq uint32, payload []byte) []byte {
	respHeader := make([]byte, HeaderLength)
	respHeader[0] = HeaderMagic
	respHeader[1] = channel
	binary.LittleEndian.PutUint32(respHeader[4:8], sessionID)
	binary.LittleEndian.PutUint32(respHeader[8:12], seq)
	binary.LittleEndian.PutUint16(respHeader[14:16], msgID)
	binary.LittleEndian.PutUint32(respHeader[16:20], uint32(len(payload)))
	return append(respHeader, payload...)
}

func sendUDPPacket(conn *net.UDPConn, packet []byte, remoteAddr *net.UDPAddr, standardUDPPort int) error {
	// 1. Send unicast to source ephemeral port
	_, err := conn.WriteToUDP(packet, remoteAddr)

	// 2. Send unicast to well-known client discovery ports on the remote device
	if remoteAddr.IP != nil {
		for _, p := range []int{standardUDPPort, 34570, 34571, 34568} {
			if p != remoteAddr.Port {
				_, _ = conn.WriteToUDP(packet, &net.UDPAddr{IP: remoteAddr.IP, Port: p})
			}
		}
	}
	return err
}

func buildBinaryNetCommonV2(serialNo, hostIP string, tcpPort int) []byte {
	buf := make([]byte, 260)
	copy(buf[0:80], []byte("Steinel-L620-CAM")) // st_00_HostName (0..80)

	ip := net.ParseIP(hostIP).To4()
	if ip == nil {
		ip = net.IPv4(127, 0, 0, 1).To4()
	}
	copy(buf[80:84], ip)                       // st_01_HostIP (80..84)
	copy(buf[84:88], []byte{255, 255, 255, 0}) // st_02_Submask (84..88)
	copy(buf[88:92], []byte{192, 168, 88, 1})  // st_03_Gateway (88..92)

	binary.LittleEndian.PutUint32(buf[92:96], 80)               // st_04_HttpPort (92..96)
	binary.LittleEndian.PutUint32(buf[96:100], uint32(tcpPort)) // st_05_TCPPort (96..100)
	binary.LittleEndian.PutUint32(buf[100:104], 8443)           // st_06_SSLPort (100..104)
	binary.LittleEndian.PutUint32(buf[104:108], 34568)          // st_07_UDPPort (104..108)
	binary.LittleEndian.PutUint32(buf[108:112], 10)             // st_08_MaxConn (108..112)
	binary.LittleEndian.PutUint32(buf[112:116], 0)              // st_09_MonMode (112..116)
	binary.LittleEndian.PutUint32(buf[116:120], 0)              // st_10_MaxBps (116..120)
	binary.LittleEndian.PutUint32(buf[120:124], 0)              // st_11_TransferPlan (120..124)
	buf[124] = 0                                                // st_12_bUseHSDownLoad (124..125)

	copy(buf[125:157], []byte("00:12:16:a1:b2:c3")) // st_13_sMac (125..157)
	copy(buf[157:189], []byte(serialNo))            // st_14_sSn (157..189) -> Exact Serial Number!
	// buf[189:192] is st_151_arg0 (3 bytes padding: 189..192)

	binary.LittleEndian.PutUint32(buf[192:196], 0) // st_15_DeviceType (192..196)
	binary.LittleEndian.PutUint32(buf[196:200], 1) // st_16_ChannelNum (196..200)
	binary.LittleEndian.PutUint32(buf[200:204], 0) // st_17_DeviceTypeV2 (200..204)
	// buf[204:212] st_18_sRandomUser
	// buf[212:220] st_19_sRandomPwd
	// buf[220:244] st_20_sPid
	// buf[244:260] st_21_sResume

	return buf
}
