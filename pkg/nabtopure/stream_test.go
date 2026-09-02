package nabtopure

import (
	"bytes"
	"testing"
)

func TestStreamSYNPacketBuildAndParse(t *testing.T) {
	s := &Stream{
		streamID:  0,
		port:      3418125365,
		clientSeq: 1,
	}

	synPkt := s.buildSYNPacket()
	if len(synPkt) < 20 {
		t.Fatalf("SYN packet too short: %d bytes", len(synPkt))
	}

	if synPkt[0] != StreamAppDataType {
		t.Fatalf("expected application data type 0x05, got 0x%02x", synPkt[0])
	}

	hdr, exts, err := s.parseStreamPacket(synPkt)
	if err != nil {
		t.Fatalf("parseStreamPacket failed: %v", err)
	}

	if hdr.flags != StreamFlagSYN {
		t.Fatalf("expected flags 0x80 (SYN), got 0x%02x", hdr.flags)
	}

	foundSYN := false
	foundPort := false
	for _, ext := range exts {
		if ext.extType == ExtSYN {
			foundSYN = true
		}
		if ext.extType == ExtContentType {
			foundPort = true
		}
	}

	if !foundSYN {
		t.Fatalf("expected ExtSYN extension")
	}
	if !foundPort {
		t.Fatalf("expected ExtContentType extension")
	}
}

func TestVarUintRoundtrip(t *testing.T) {
	testVals := []uint64{0, 1, 42, 63, 64, 1000, 16383, 16384, 1000000, 1073741823}
	for _, val := range testVals {
		buf := new(bytes.Buffer)
		writeVarUint(buf, val)
		decoded, n := readVarUint(buf.Bytes())
		if n != buf.Len() {
			t.Errorf("readVarUint read %d bytes, expected %d", n, buf.Len())
		}
		if decoded != val {
			t.Errorf("expected %d, got %d", val, decoded)
		}
	}
}
