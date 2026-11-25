package xiongmai

import (
	"bytes"
	"context"
	"encoding/binary"
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

// buildSofiaRecord constructs a Xiongmai Sofia media record (I-Frame, P-Frame, Audio, or Meta)
// with its exact binary length header as transmitted inside OPPlayBack 32 KB blocks.
func buildSofiaRecord(marker []byte, codecByte, fpsByte byte, payload []byte) []byte {
	switch {
	case bytes.Equal(marker, MarkerIFrame):
		hdr := make([]byte, 16)
		copy(hdr[0:4], MarkerIFrame)
		hdr[4] = codecByte
		hdr[5] = fpsByte
		binary.LittleEndian.PutUint32(hdr[12:16], uint32(len(payload)))
		return append(hdr, payload...)
	case bytes.Equal(marker, MarkerPFrame):
		hdr := make([]byte, 8)
		copy(hdr[0:4], MarkerPFrame)
		binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(payload)))
		return append(hdr, payload...)
	default:
		hdr := make([]byte, 8)
		copy(hdr[0:4], marker)
		binary.LittleEndian.PutUint16(hdr[6:8], uint16(len(payload)))
		return append(hdr, payload...)
	}
}

func TestSofiaDownloadDemuxer(t *testing.T) {
	t.Parallel()

	hevcVPS := []byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF}
	hevcSPS := []byte{0x00, 0x00, 0x00, 0x01, 0x42, 0x01, 0x01, 0x01, 0x60, 0x00}
	hevcIDR := []byte{0x00, 0x00, 0x00, 0x01, 0x26, 0x01, 0xAF, 0x08, 0x11, 0x22}
	hevcIFrame := append(append(append([]byte(nil), hevcVPS...), hevcSPS...), hevcIDR...)
	hevcPFrame := []byte{0x00, 0x00, 0x00, 0x01, 0x02, 0x01, 0xD0, 0x04, 0x33, 0x44}

	h264IFrame := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0xC0, 0x1E,
		0x00, 0x00, 0x00, 0x01, 0x68, 0xCE, 0x38, 0x80,
		0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x21,
	}
	h264PFrame := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A, 0x22, 0x33}

	audioJunk := bytes.Repeat([]byte{0xAA}, 320)
	metaJunk := bytes.Repeat([]byte{0x55}, 12)

	hevcStream := make([]byte, 0, 1024)
	hevcStream = append(hevcStream, buildSofiaRecord(MarkerIFrame, sofiaCodecH265, 6, hevcIFrame)...)
	hevcStream = append(hevcStream, buildSofiaRecord(MarkerAudio, 0, 0, audioJunk)...)
	hevcStream = append(hevcStream, buildSofiaRecord(MarkerMeta, 0, 0, metaJunk)...)
	hevcStream = append(hevcStream, buildSofiaRecord(MarkerPFrame, 0, 0, hevcPFrame)...)
	hevcStream = append(hevcStream, buildSofiaRecord(MarkerAudio, 0, 0, audioJunk)...)
	hevcStream = append(hevcStream, buildSofiaRecord(MarkerPFrame, 0, 0, hevcPFrame)...)

	wantHEVCOut := append(append(append([]byte(nil), hevcIFrame...), hevcPFrame...), hevcPFrame...)

	h264Stream := make([]byte, 0, 512)
	h264Stream = append(h264Stream, buildSofiaRecord(MarkerIFrame, sofiaCodecH264, 15, h264IFrame)...)
	h264Stream = append(h264Stream, buildSofiaRecord(MarkerAudio, 0, 0, audioJunk)...)
	h264Stream = append(h264Stream, buildSofiaRecord(MarkerPFrame, 0, 0, h264PFrame)...)

	wantH264Out := append(append([]byte(nil), h264IFrame...), h264PFrame...)

	tests := []struct {
		name      string
		stream    []byte
		chunkSize int
		wantOut   []byte
		wantCodec string
		wantFPS   int
	}{
		{
			name:      "hevc single block strips audio and meta",
			stream:    hevcStream,
			chunkSize: len(hevcStream),
			wantOut:   wantHEVCOut,
			wantCodec: videoCodecH265,
			wantFPS:   6,
		},
		{
			name:      "hevc split across 7-byte chunks mid-marker and mid-frame",
			stream:    hevcStream,
			chunkSize: 7,
			wantOut:   wantHEVCOut,
			wantCodec: videoCodecH265,
			wantFPS:   6,
		},
		{
			name:      "hevc split across 37-byte misaligned chunks",
			stream:    hevcStream,
			chunkSize: 37,
			wantOut:   wantHEVCOut,
			wantCodec: videoCodecH265,
			wantFPS:   6,
		},
		{
			name:      "h264 stream with audio stripping",
			stream:    h264Stream,
			chunkSize: 19,
			wantOut:   wantH264Out,
			wantCodec: videoCodecH264,
			wantFPS:   15,
		},
		{
			name:      "raw headerless hevc annex-b passthrough",
			stream:    hevcIFrame,
			chunkSize: 11,
			wantOut:   hevcIFrame,
			wantCodec: videoCodecH265,
			wantFPS:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			dm := newSofiaDownloadDemuxer(&out)

			for off := 0; off < len(tc.stream); off += tc.chunkSize {
				end := off + tc.chunkSize
				if end > len(tc.stream) {
					end = len(tc.stream)
				}
				require.NoError(t, dm.WriteChunk(tc.stream[off:end]))
			}
			require.NoError(t, dm.Flush())

			assert.Equal(t, tc.wantCodec, dm.Codec())
			assert.Equal(t, tc.wantFPS, dm.FPS())
			assert.Equal(t, int64(len(tc.wantOut)), dm.BytesWritten())
			assert.Equal(t, tc.wantOut, out.Bytes())
		})
	}

	t.Run("extracts 0xFA audio when audio output is configured while discarding 0xF9 meta", func(t *testing.T) {
		t.Parallel()

		var videoOut, audioOut bytes.Buffer
		dm := newSofiaDownloadDemuxer(&videoOut)
		dm.SetAudioOutput(&audioOut)

		for off := 0; off < len(hevcStream); off += 13 {
			end := off + 13
			if end > len(hevcStream) {
				end = len(hevcStream)
			}
			require.NoError(t, dm.WriteChunk(hevcStream[off:end]))
		}
		require.NoError(t, dm.Flush())

		wantAudio := append(append([]byte(nil), audioJunk...), audioJunk...)
		assert.Equal(t, wantHEVCOut, videoOut.Bytes())
		assert.Equal(t, wantAudio, audioOut.Bytes())
		assert.Equal(t, int64(len(wantAudio)), dm.AudioBytesWritten())
	})
}

func TestSofiaDownloader_DownloadHEVCChunked(t *testing.T) {
	t.Parallel()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := l.Addr().(*net.TCPAddr).Port

	hevcIFrame := []byte{
		0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF,
		0x00, 0x00, 0x00, 0x01, 0x42, 0x01, 0x01, 0x01, 0x60, 0x00,
		0x00, 0x00, 0x00, 0x01, 0x26, 0x01, 0xAF, 0x08, 0x11, 0x22,
	}
	hevcPFrame := []byte{0x00, 0x00, 0x00, 0x01, 0x02, 0x01, 0xD0, 0x04, 0x33, 0x44}
	audioPayload := bytes.Repeat([]byte{0xBB}, 320)
	metaPayload := bytes.Repeat([]byte{0x44}, 16)

	rawRecording := make([]byte, 0, 1024)
	rawRecording = append(rawRecording, buildSofiaRecord(MarkerIFrame, sofiaCodecH265, 6, hevcIFrame)...)
	rawRecording = append(rawRecording, buildSofiaRecord(MarkerAudio, 0, 0, audioPayload)...)
	rawRecording = append(rawRecording, buildSofiaRecord(MarkerMeta, 0, 0, metaPayload)...)
	rawRecording = append(rawRecording, buildSofiaRecord(MarkerPFrame, 0, 0, hevcPFrame)...)

	wantCleanVideo := append(append([]byte(nil), hevcIFrame...), hevcPFrame...)

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		hdrBuf := make([]byte, HeaderLength)
		if _, err := io.ReadFull(conn, hdrBuf); err != nil {
			return
		}
		hdr, _ := DecodeHeader(hdrBuf)
		_, _ = io.ReadFull(conn, make([]byte, hdr.DataLength))

		respBody := []byte(`{"Ret":100,"Name":"OPPlayBack","SessionID":"0x0000000001"}` + "\x00")
		respHdr := Header{Magic: HeaderMagic, MsgID: MsgPlayClaimResp, DataLength: uint32(len(respBody))}
		_, _ = conn.Write(append(respHdr.Encode(), respBody...))

		if _, err := io.ReadFull(conn, hdrBuf); err != nil {
			return
		}
		hdr, _ = DecodeHeader(hdrBuf)
		_, _ = io.ReadFull(conn, make([]byte, hdr.DataLength))

		const blockSize = 64
		for off := 0; off < len(rawRecording); off += blockSize {
			end := off + blockSize
			if end > len(rawRecording) {
				end = len(rawRecording)
			}
			chunk := rawRecording[off:end]
			dataHdr := Header{Magic: HeaderMagic, MsgID: MsgPlayClaimData, DataLength: uint32(len(chunk))}
			_, _ = conn.Write(append(dataHdr.Encode(), chunk...))
		}

		eofHdr := Header{Magic: HeaderMagic, MsgID: MsgPlayEOF, DataLength: 0}
		_, _ = conn.Write(eofHdr.Encode())
	}()

	client := NewClient("127.0.0.1", port, "admin", "")
	client.sessionID = 1
	client.isLoggedIn = true

	mockMuxer := storage.NewMockExtractor()
	dl := NewSofiaDownloader("127.0.0.1", port, client, "admin", "")
	dl.SetMuxer(mockMuxer)

	var outBuf bytes.Buffer
	rec := &storage.RecordingItem{
		ID:              "1794950079",
		FileName:        "/idea0/2026-11-17/001/22.14.39-22.15.10[M][@32eb][0].h264",
		StartTime:       time.Date(2026, 11, 17, 21, 14, 39, 0, time.UTC),
		EndTime:         time.Date(2026, 11, 17, 21, 15, 10, 0, time.UTC),
		DurationSeconds: 31,
		FileSizeBytes:   int64(len(rawRecording)),
	}

	var gotName string
	var gotSize int64
	err = dl.Download(context.Background(), rec, &outBuf, func(name string, size int64) {
		gotName = name
		gotSize = size
	})
	require.NoError(t, err)

	assert.Equal(t, videoCodecH265, mockMuxer.LastMuxOpts.Codec)
	assert.Equal(t, 6, mockMuxer.LastMuxOpts.FPS)
	assert.NotEmpty(t, mockMuxer.LastMuxOpts.AudioPath)
	assert.Equal(t, rec.FileName, gotName)
	assert.Equal(t, int64(len(wantCleanVideo)), gotSize)
	assert.Equal(t, wantCleanVideo, outBuf.Bytes())
}

func TestSofiaDownloader_DownloadHEVCAudioOptOut(t *testing.T) {
	t.Parallel()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	port := l.Addr().(*net.TCPAddr).Port

	hevcIFrame := []byte{
		0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF,
		0x00, 0x00, 0x00, 0x01, 0x42, 0x01, 0x01, 0x01, 0x60, 0x00,
		0x00, 0x00, 0x00, 0x01, 0x26, 0x01, 0xAF, 0x08, 0x11, 0x22,
	}
	audioPayload := bytes.Repeat([]byte{0xD5}, 320)

	rawRecording := make([]byte, 0, 1024)
	rawRecording = append(rawRecording, buildSofiaRecord(MarkerIFrame, sofiaCodecH265, 6, hevcIFrame)...)
	rawRecording = append(rawRecording, buildSofiaRecord(MarkerAudio, 0, 0, audioPayload)...)

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		hdrBuf := make([]byte, HeaderLength)
		if _, err := io.ReadFull(conn, hdrBuf); err != nil {
			return
		}
		hdr, _ := DecodeHeader(hdrBuf)
		_, _ = io.ReadFull(conn, make([]byte, hdr.DataLength))

		respBody := []byte(`{"Ret":100,"Name":"OPPlayBack","SessionID":"0x0000000001"}` + "\x00")
		respHdr := Header{Magic: HeaderMagic, MsgID: MsgPlayClaimResp, DataLength: uint32(len(respBody))}
		_, _ = conn.Write(append(respHdr.Encode(), respBody...))

		if _, err := io.ReadFull(conn, hdrBuf); err != nil {
			return
		}
		hdr, _ = DecodeHeader(hdrBuf)
		_, _ = io.ReadFull(conn, make([]byte, hdr.DataLength))

		dataHdr := Header{Magic: HeaderMagic, MsgID: MsgPlayClaimData, DataLength: uint32(len(rawRecording))}
		_, _ = conn.Write(append(dataHdr.Encode(), rawRecording...))

		eofHdr := Header{Magic: HeaderMagic, MsgID: MsgPlayEOF, DataLength: 0}
		_, _ = conn.Write(eofHdr.Encode())
	}()

	client := NewClient("127.0.0.1", port, "admin", "")
	client.sessionID = 1
	client.isLoggedIn = true

	mockMuxer := storage.NewMockExtractor()
	dl := NewSofiaDownloader("127.0.0.1", port, client, "admin", "")
	dl.SetMuxer(mockMuxer)

	var outBuf bytes.Buffer
	rec := &storage.RecordingItem{
		ID:              "1794950079",
		FileName:        "/idea0/2026-11-17/001/22.14.39-22.15.10[M][@32eb][0].h264",
		StartTime:       time.Date(2026, 11, 17, 21, 14, 39, 0, time.UTC),
		EndTime:         time.Date(2026, 11, 17, 21, 15, 10, 0, time.UTC),
		DurationSeconds: 31,
	}

	ctx := storage.WithIncludeAudio(context.Background(), false)
	require.NoError(t, dl.Download(ctx, rec, &outBuf, nil))
	assert.Equal(t, storage.MuxOptions{Codec: videoCodecH265, FPS: 6}, mockMuxer.LastMuxOpts)
}
