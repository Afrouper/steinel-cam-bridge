package xiongmai

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSofiaDownloader_InvalidParams(t *testing.T) {
	dl := NewSofiaDownloader("127.0.0.1", 34567, nil, "admin", "")
	var buf bytes.Buffer

	err := dl.Download(context.Background(), nil, &buf, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "recording item is nil")

	err = dl.Download(context.Background(), &storage.RecordingItem{}, &buf, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "recording file name is empty")
}

func TestSofiaDownloader_ConnectionFailure(t *testing.T) {
	dl := NewSofiaDownloader("127.0.0.1", 59999, nil, "admin", "")
	var buf bytes.Buffer

	rec := &storage.RecordingItem{
		ID:        "123",
		FileName:  "test.h264",
		StartTime: time.Now().Add(-1 * time.Hour),
		EndTime:   time.Now(),
	}

	err := dl.Download(context.Background(), rec, &buf, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect")
}

func TestSofiaDownloader_DownloadSuccess(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := l.Addr().(*net.TCPAddr).Port

	// Mock server simulating Sofia NetIP OPPlayBack protocol
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		hdrBuf := make([]byte, HeaderLength)

		// 1. Read OPPlayBack Claim (MsgID 1424)
		if _, err := io.ReadFull(conn, hdrBuf); err != nil {
			return
		}
		hdr, _ := DecodeHeader(hdrBuf)
		claimPayload := make([]byte, hdr.DataLength)
		_, _ = io.ReadFull(conn, claimPayload)

		// Respond with Claim Ack (MsgID 1425)
		respBody := []byte(`{"Ret":100,"Name":"OPPlayBack","SessionID":"0x0000000001"}` + "\x00")
		respHdr := Header{
			Magic:      HeaderMagic,
			MsgID:      MsgPlayClaimResp,
			DataLength: uint32(len(respBody)),
		}
		_, _ = conn.Write(append(respHdr.Encode(), respBody...))

		// 2. Read OPPlayBack DownloadStart (MsgID 1420)
		if _, err := io.ReadFull(conn, hdrBuf); err != nil {
			return
		}
		hdr, _ = DecodeHeader(hdrBuf)
		startPayload := make([]byte, hdr.DataLength)
		_, _ = io.ReadFull(conn, startPayload)

		// 3. Send mock video packets:
		// Packet A: I-Frame marker + 16 bytes header + Annex-B NAL (valid SPS/PPS/IDR sequence)
		iFramePayload := append(MarkerIFrame, bytes.Repeat([]byte{0x00}, 12)...) // 16 bytes header total
		// Valid SPS (64x64 baseline)
		sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x01, 0x41, 0xfb, 0x01, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x03, 0xc0, 0xf1, 0x42, 0x99, 0x60}
		// Valid PPS
		pps := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xcb, 0x8c, 0xb2}
		// Valid IDR frame
		idr := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10, 0xff, 0x00}

		iFramePayload = append(iFramePayload, sps...)
		iFramePayload = append(iFramePayload, pps...)
		iFramePayload = append(iFramePayload, idr...)

		dataHdrA := Header{
			Magic:      HeaderMagic,
			MsgID:      MsgPlayData,
			DataLength: uint32(len(iFramePayload)),
		}
		_, _ = conn.Write(append(dataHdrA.Encode(), iFramePayload...))

		// Packet B: P-Frame marker + 8 bytes header + Annex-B NAL slice
		pFramePayload := append(MarkerPFrame, bytes.Repeat([]byte{0x00}, 4)...) // 8 bytes header total
		pFramePayload = append(pFramePayload, []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x01, 0x02}...)

		dataHdrB := Header{
			Magic:      HeaderMagic,
			MsgID:      MsgPlayData,
			DataLength: uint32(len(pFramePayload)),
		}
		_, _ = conn.Write(append(dataHdrB.Encode(), pFramePayload...))

		// Packet C: EOF packet (MsgID 1423 MsgPlayEOF)
		eofHdr := Header{
			Magic:      HeaderMagic,
			MsgID:      MsgPlayEOF,
			DataLength: 0,
		}
		_, _ = conn.Write(eofHdr.Encode())
	}()

	client := NewClient("127.0.0.1", port, "admin", "")
	client.sessionID = 1
	client.isLoggedIn = true

	dl := NewSofiaDownloader("127.0.0.1", port, client, "admin", "")
	var outBuf bytes.Buffer

	rec := &storage.RecordingItem{
		ID:              "1794233582",
		FileName:        "/ide0/2026-11-09/001/14.13.02-14.13.32[M][@0][0].h264",
		StartTime:       time.Date(2026, 11, 9, 14, 13, 2, 0, time.UTC),
		EndTime:         time.Date(2026, 11, 9, 14, 13, 32, 0, time.UTC),
		DurationSeconds: 30,
	}

	var startedName string
	onStart := func(name string, size int64) {
		startedName = name
	}

	err = dl.Download(context.Background(), rec, &outBuf, onStart)
	require.NoError(t, err)

	assert.Equal(t, rec.FileName, startedName)
	assert.Greater(t, outBuf.Len(), 0)
}
