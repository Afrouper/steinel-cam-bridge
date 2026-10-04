package nabto

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// BuildDeviceMDNSQuery constructs an mDNS ANY query packet for `<productID>-<deviceID>.local`.
func BuildDeviceMDNSQuery(productID, deviceID string) []byte {
	productID = strings.TrimSpace(productID)
	deviceID = strings.TrimSpace(deviceID)
	if productID == "" {
		productID = "pr-xxxxx"
	}
	if deviceID == "" {
		deviceID = "de-xxxxxxx"
	}

	fullName := fmt.Sprintf("%s-%s", productID, deviceID)
	nameLen := len(fullName)

	// DNS Header: ID=0, Flags=0 (standard query), QDCOUNT=1, ANCOUNT=0, NSCOUNT=0, ARCOUNT=0
	buf := make([]byte, 0, 12+1+nameLen+1+5+1+4)
	buf = append(buf,
		0x00, 0x00, // ID
		0x00, 0x00, // Flags (standard query)
		0x00, 0x01, // QDCOUNT = 1 question
		0x00, 0x00, // ANCOUNT = 0
		0x00, 0x00, // NSCOUNT = 0
		0x00, 0x00, // ARCOUNT = 0
	)

	// Label 1: <productID>-<deviceID>
	buf = append(buf, byte(nameLen))
	buf = append(buf, fullName...)

	// Label 2: "local"
	buf = append(buf, 0x05)
	buf = append(buf, "local"...)

	// Root null label
	buf = append(buf, 0x00)

	// QTYPE: ANY (255 / 0x00ff)
	buf = append(buf, 0x00, 0xff)

	// QCLASS: IN (1 / 0x0001)
	buf = append(buf, 0x00, 0x01)

	return buf
}

// BuildServiceMDNSQuery constructs an mDNS PTR query for `_nabto._udp.local`.
func BuildServiceMDNSQuery() []byte {
	// DNS Header: 1 question
	buf := []byte{
		0x00, 0x00, // ID
		0x00, 0x00, // Flags
		0x00, 0x01, // 1 Question
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		// Label 1: _nabto
		0x06, '_', 'n', 'a', 'b', 't', 'o',
		// Label 2: _udp
		0x04, '_', 'u', 'd', 'p',
		// Label 3: local
		0x05, 'l', 'o', 'c', 'a', 'l',
		// Root
		0x00,
		// QTYPE: PTR (12 / 0x000c)
		0x00, 0x0c,
		// QCLASS: IN (1 / 0x0001)
		0x00, 0x01,
	}
	return buf
}

// SendWakeup sends mDNS wakeup packets to the camera via unicast and multicast to wake it from standby.
func SendWakeup(cameraIP string, cameraPort int, productID, deviceID string) {
	if cameraIP == "" {
		return
	}
	if cameraPort <= 0 {
		cameraPort = 5592
	}

	devQuery := BuildDeviceMDNSQuery(productID, deviceID)
	srvQuery := BuildServiceMDNSQuery()
	queries := [][]byte{devQuery, srvQuery}

	targets := []string{
		fmt.Sprintf("%s:5353", cameraIP),           // Unicast mDNS directly to camera IP
		"224.0.0.251:5353",                         // Multicast mDNS in local LAN
		fmt.Sprintf("%s:%d", cameraIP, cameraPort), // Direct packet to camera Nabto port
	}

	for _, target := range targets {
		raddr, err := net.ResolveUDPAddr("udp4", target)
		if err != nil {
			continue
		}
		conn, err := net.DialUDP("udp4", nil, raddr)
		if err != nil {
			continue
		}
		for i := 0; i < 2; i++ {
			for _, q := range queries {
				_, _ = conn.Write(q)
			}
			time.Sleep(30 * time.Millisecond)
		}
		_ = conn.Close()
	}
}
