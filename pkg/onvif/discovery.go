package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
)

type DiscoveryServer struct {
	onvifPort  int
	deviceUUID string
	localIP    string
}

func NewDiscoveryServer(onvifPort int, deviceUUID string) *DiscoveryServer {
	if deviceUUID == "" {
		deviceUUID = uuid.New().String()
	}
	return &DiscoveryServer{
		onvifPort:  onvifPort,
		deviceUUID: deviceUUID,
	}
}

func (d *DiscoveryServer) Start(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp4", "239.255.255.250:3702")
	if err != nil {
		return err
	}

	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		// Fallback to unicast if multicast socket permission is restricted
		log.Printf("[WS-Discovery] ⚠️ Could not bind 239.255.255.250:3702 (%v). Retrying on 0.0.0.0:3702...", err)
		conn, err = net.ListenUDP("udp4", &net.UDPAddr{Port: 3702})
		if err != nil {
			log.Printf("[WS-Discovery] ⚠️ UDP 3702 bind failed: %v. WS-Discovery disabled.", err)
			return nil
		}
	}
	defer func() {
		_ = conn.Close()
	}()

	log.Printf("[WS-Discovery] 🛰️ Listening for ONVIF probes on 239.255.255.250:3702")

	buf := make([]byte, 8192)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			continue
		}

		raw := string(buf[:n])
		if strings.Contains(raw, "Probe") {
			d.handleProbe(conn, src, raw)
		}
	}
}

func (d *DiscoveryServer) handleProbe(conn net.PacketConn, src net.Addr, raw string) {
	var probe ProbeEnvelope
	_ = xml.Unmarshal([]byte(raw), &probe)

	msgID := probe.Header.MessageID
	if msgID == "" {
		msgID = "urn:uuid:" + uuid.New().String()
	}

	// Detect local outbound IP for XAddrs
	localIP := getOutboundIP()
	if d.localIP != "" {
		localIP = d.localIP
	}

	xAddrs := fmt.Sprintf("http://%s:%d/onvif/device_service", localIP, d.onvifPort)
	endpointRef := fmt.Sprintf("urn:uuid:%s", d.deviceUUID)

	responseXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Header>
    <a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</a:Action>
    <a:MessageID>urn:uuid:%s</a:MessageID>
    <a:RelatesTo>%s</a:RelatesTo>
    <a:To>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</a:To>
  </s:Header>
  <s:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <a:EndpointReference>
          <a:Address>%s</a:Address>
        </a:EndpointReference>
        <d:Types>dn:NetworkVideoTransmitter tds:Device</d:Types>
        <d:Scopes>onvif://www.onvif.org/type/video_encoder onvif://www.onvif.org/type/audio_encoder onvif://www.onvif.org/Profile/Streaming onvif://www.onvif.org/Profile/T onvif://www.onvif.org/Profile/S onvif://www.onvif.org/hardware/L625 onvif://www.onvif.org/name/Steinel-CAM</d:Scopes>
        <d:XAddrs>%s</d:XAddrs>
        <d:MetadataVersion>1</d:MetadataVersion>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </s:Body>
</s:Envelope>`, uuid.New().String(), msgID, endpointRef, xAddrs)

	_, _ = conn.WriteTo([]byte(responseXML), src)
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer func() {
		_ = conn.Close()
	}()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}
