package onvif

import "encoding/xml"

// SOAP 1.2 / ONVIF Envelopes

type SOAPEnvelope struct {
	XMLName xml.Name   `xml:"http://www.w3.org/2003/05/soap-envelope Envelope"`
	Header  SOAPHeader `xml:"Header"`
	Body    SOAPBody   `xml:"Body"`
}

type SOAPHeader struct {
	Action    string    `xml:"http://www.w3.org/2005/08/addressing Action,omitempty"`
	MessageID string    `xml:"http://www.w3.org/2005/08/addressing MessageID,omitempty"`
	To        string    `xml:"http://www.w3.org/2005/08/addressing To,omitempty"`
	RelatesTo string    `xml:"http://www.w3.org/2005/08/addressing RelatesTo,omitempty"`
	AppSeq    *WSAppSeq `xml:"http://schemas.xmlsoap.org/ws/2005/04/discovery AppSequence,omitempty"`
}

type WSAppSeq struct {
	InstanceId uint64 `xml:"InstanceId,attr"`
	MessageSeq uint64 `xml:"MessageNumber,attr"`
}

type SOAPBody struct {
	RawContent []byte `xml:",innerxml"`
}

// WS-Discovery

type ProbeEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Header  SOAPHeader
	Body    struct {
		Probe struct {
			Types  string `xml:"Types"`
			Scopes string `xml:"Scopes"`
		} `xml:"Probe"`
	}
}

// Standard Namespaces
const (
	NS_SOAP_ENV = "http://www.w3.org/2003/05/soap-envelope"
	NS_WSDD     = "http://schemas.xmlsoap.org/ws/2005/04/discovery"
	NS_WSA      = "http://www.w3.org/2005/08/addressing"
	NS_TDS      = "http://www.onvif.org/ver10/device/wsdl"
	NS_TRT      = "http://www.onvif.org/ver10/media/wsdl"
	NS_TT       = "http://www.onvif.org/ver10/schema"
	NS_TEV      = "http://www.onvif.org/ver10/events/wsdl"
	NS_TIO      = "http://www.onvif.org/ver10/deviceIO/wsdl"
	NS_WSNT     = "http://docs.oasis-open.org/wsn/b-2"
	NS_WSRF_R   = "http://docs.oasis-open.org/wsrf/r-2"
)
