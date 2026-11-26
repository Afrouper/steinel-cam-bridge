package xiongmai

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync/atomic"
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

		// Packet B2: Metadata marker + 8 bytes header (should be skipped)
		metaPayload := append(MarkerMeta, bytes.Repeat([]byte{0x00}, 8)...)
		dataHdrMeta := Header{
			Magic:      HeaderMagic,
			MsgID:      MsgPlayData,
			DataLength: uint32(len(metaPayload)),
		}
		_, _ = conn.Write(append(dataHdrMeta.Encode(), metaPayload...))

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

func TestSofiaDownloader_DownloadPlayClaimData_MsgID1426(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := l.Addr().(*net.TCPAddr).Port

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

		// 2. Read OPPlayBack DownloadStart (MsgID 1420 fallback on media socket)
		if _, err := io.ReadFull(conn, hdrBuf); err != nil {
			return
		}
		hdr, _ = DecodeHeader(hdrBuf)
		startPayload := make([]byte, hdr.DataLength)
		_, _ = io.ReadFull(conn, startPayload)

		// 3. Send video packet with MsgID 1426 (MsgPlayClaimData / DOWNLOAD_DATA)
		iFramePayload := append(MarkerIFrame, bytes.Repeat([]byte{0x00}, 12)...)
		sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x01, 0x41, 0xfb, 0x01, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x03, 0xc0, 0xf1, 0x42, 0x99, 0x60}
		pps := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xcb, 0x8c, 0xb2}
		idr := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10, 0xff, 0x00}
		iFramePayload = append(iFramePayload, sps...)
		iFramePayload = append(iFramePayload, pps...)
		iFramePayload = append(iFramePayload, idr...)

		dataHdr := Header{
			Magic:      HeaderMagic,
			MsgID:      MsgPlayClaimData, // 1426!
			DataLength: uint32(len(iFramePayload)),
		}
		_, _ = conn.Write(append(dataHdr.Encode(), iFramePayload...))

		// EOF
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
		ID:              "1794233583",
		FileName:        "/ide0/2026-11-09/001/14.15.00-14.15.30[M][@0][0].h264",
		StartTime:       time.Date(2026, 11, 9, 14, 15, 0, 0, time.UTC),
		EndTime:         time.Date(2026, 11, 9, 14, 15, 30, 0, time.UTC),
		DurationSeconds: 30,
	}

	err = dl.Download(context.Background(), rec, &outBuf, nil)
	require.NoError(t, err)
	assert.Greater(t, outBuf.Len(), 0)
}

func TestSofiaDownloader_DownloadControlSocketAcknowledged(t *testing.T) {
	// Single port listener simulating Xiongmai Sofia Port 34567
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port

	var startReceivedOnControl atomic.Bool

	go func() {
		// 1. First connection: Control connection (client.conn)
		ctrlConn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = ctrlConn.Close() }()

		go func() {
			hdrBuf := make([]byte, HeaderLength)
			for {
				if _, err := io.ReadFull(ctrlConn, hdrBuf); err != nil {
					return
				}
				hdr, _ := DecodeHeader(hdrBuf)
				payload := make([]byte, hdr.DataLength)
				_, _ = io.ReadFull(ctrlConn, payload)

				// Acknowledge DownloadStart (1420) on control connection
				if hdr.MsgID == MsgPlayReq {
					startReceivedOnControl.Store(true)

					respBody := []byte(`{"Ret":100,"Name":"OPPlayBack","SessionID":"0x0000000001"}` + "\x00")
					respHdr := Header{
						Magic:      HeaderMagic,
						MsgID:      MsgPlayResp,
						DataLength: uint32(len(respBody)),
					}
					_, _ = ctrlConn.Write(append(respHdr.Encode(), respBody...))
				}
			}
		}()

		// 2. Second connection: Media connection (from dl.Download)
		mediaConn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = mediaConn.Close() }()

		hdrBuf := make([]byte, HeaderLength)

		// Read OPPlayBack Claim (1424) on media socket
		if _, err := io.ReadFull(mediaConn, hdrBuf); err != nil {
			return
		}
		hdr, _ := DecodeHeader(hdrBuf)
		claimPayload := make([]byte, hdr.DataLength)
		_, _ = io.ReadFull(mediaConn, claimPayload)

		// Respond with Claim Ack (MsgID 1425)
		respBody := []byte(`{"Ret":100,"Name":"OPPlayBack","SessionID":"0x0000000001"}` + "\x00")
		respHdr := Header{
			Magic:      HeaderMagic,
			MsgID:      MsgPlayClaimResp,
			DataLength: uint32(len(respBody)),
		}
		_, _ = mediaConn.Write(append(respHdr.Encode(), respBody...))

		// Wait until DownloadStart is received on control socket
		for i := 0; i < 20; i++ {
			if startReceivedOnControl.Load() {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}

		// Send video data on media socket
		iFramePayload := append(MarkerIFrame, bytes.Repeat([]byte{0x00}, 12)...)
		sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x01, 0x41, 0xfb, 0x01, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x03, 0xc0, 0xf1, 0x42, 0x99, 0x60}
		pps := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xcb, 0x8c, 0xb2}
		idr := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10, 0xff, 0x00}
		iFramePayload = append(iFramePayload, sps...)
		iFramePayload = append(iFramePayload, pps...)
		iFramePayload = append(iFramePayload, idr...)

		dataHdr := Header{
			Magic:      HeaderMagic,
			MsgID:      MsgPlayData,
			DataLength: uint32(len(iFramePayload)),
		}
		_, _ = mediaConn.Write(append(dataHdr.Encode(), iFramePayload...))

		// EOF
		eofHdr := Header{
			Magic:      HeaderMagic,
			MsgID:      MsgPlayEOF,
			DataLength: 0,
		}
		_, _ = mediaConn.Write(eofHdr.Encode())
	}()

	// Connect client to control connection
	client := NewClient("127.0.0.1", port, "admin", "")
	ctrlConn, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)
	defer func() { _ = ctrlConn.Close() }()
	client.conn = ctrlConn
	client.sessionID = 1
	client.isLoggedIn = true

	dl := NewSofiaDownloader("127.0.0.1", port, client, "admin", "")
	var outBuf bytes.Buffer

	rec := &storage.RecordingItem{
		ID:              "1794233584",
		FileName:        "/ide0/2026-11-09/001/14.20.00-14.20.30[M][@0][0].h264",
		StartTime:       time.Date(2026, 11, 9, 14, 20, 0, 0, time.UTC),
		EndTime:         time.Date(2026, 11, 9, 14, 20, 30, 0, time.UTC),
		DurationSeconds: 30,
	}

	err = dl.Download(context.Background(), rec, &outBuf, nil)
	require.NoError(t, err)
	assert.Greater(t, outBuf.Len(), 0)

	// Ensure DownloadStart was genuinely received on the control socket
	assert.True(t, startReceivedOnControl.Load(), "expected DownloadStart to be received on control socket")
}

func TestSofiaDownloadDemuxer_MultiPacketAudioAndMeta(t *testing.T) {
	var out bytes.Buffer
	var demux sofiaDownloadDemuxer

	// 1. I-frame (16-byte header + NAL)
	iHdr := append(append([]byte{}, MarkerIFrame...), make([]byte, 12)...)
	iNAL := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x0a}
	demux.write(append(iHdr, iNAL...), &out)

	// 2. Multi-packet audio record (0xFA) with 12-byte audio payload split across 2 chunks:
	// First chunk has 8-byte header (size=12 LE at bytes 6..7) + 4 bytes audio
	audioHdr := append(append([]byte{}, MarkerAudio...), 0x0e, 0x02, 0x0c, 0x00)
	demux.write(append(audioHdr, []byte{0xAA, 0xBB, 0xCC, 0xDD}...), &out)
	// Second chunk has remaining 8 bytes audio (no marker): must be skipped, NOT written to H.264!
	demux.write([]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}, &out)

	// 3. Multi-packet meta record (0xF9) with 0 size (terminated by next video marker)
	metaHdr := append(append([]byte{}, MarkerMeta...), 0x0e, 0x02, 0x00, 0x00)
	demux.write(append(metaHdr, []byte{0x99, 0x88}...), &out)
	demux.write([]byte{0x77, 0x66}, &out)

	// 4. Subsequent P-frame (8-byte header + NAL)
	pHdr := append(append([]byte{}, MarkerPFrame...), make([]byte, 4)...)
	pNAL := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a}
	demux.write(append(pHdr, pNAL...), &out)

	expected := append(append([]byte{}, iNAL...), pNAL...)
	assert.Equal(t, expected, out.Bytes())
}
