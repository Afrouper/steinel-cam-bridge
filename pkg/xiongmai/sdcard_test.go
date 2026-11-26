package xiongmai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXiongmaiSDCardManager(t *testing.T) {
	server, clientConn := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = clientConn.Close() }()

	xmClient := &Client{
		conn:       clientConn,
		sessionID:  0x42,
		isLoggedIn: true,
	}

	sdm := NewSDCardManager(xmClient, "192.168.1.50", "admin", "pass")

	// Server mock goroutine responding to MsgFileSearchReq (1440)
	go func() {
		for {
			buf := make([]byte, 1024)
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			if n >= 20 {
				reqHdr, err := DecodeHeader(buf[:20])
				if err != nil {
					return
				}

				queryResp := map[string]interface{}{
					"Name": "OPFileQuery",
					"OPFileQuery": []map[string]interface{}{
						{
							"BeginTime":  "2026-08-26 18:30:00",
							"EndTime":    "2026-08-26 18:30:45",
							"FileLength": "1048576",
							"FileName":   "/mnt/mtd/rec/2026-08-26/001.h264",
						},
					},
					"Ret":       100,
					"SessionID": "0x00000042",
				}
				payload, _ := json.Marshal(queryResp)

				respHdr := &Header{
					Magic:      HeaderMagic,
					SessionID:  reqHdr.SessionID,
					Sequence:   reqHdr.Sequence,
					MsgID:      MsgFileSearchResp,
					DataLength: uint32(len(payload)),
				}
				respBytes := append(respHdr.Encode(), payload...)
				_, _ = server.Write(respBytes)
			}
		}
	}()

	var prov storage.RecordingProvider = sdm
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := prov.ListRecordings(ctx, time.Now().Add(-1*time.Hour), time.Now(), 0, 10, "")
	assert.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, resp.Count)
	assert.Contains(t, resp.List[0].FileName, "001.h264")
	assert.Equal(t, 45, resp.List[0].DurationSeconds)
	assert.Contains(t, resp.List[0].VideoURL, "/api/sdcard/events/")

	// Test StreamThumbnail unsupported directly
	err = prov.StreamThumbnail(ctx, resp.List[0].ID, nil)
	assert.ErrorIs(t, err, storage.ErrFeatureDisabled)

	// Test StreamVideo attempts Sofia download (fails gracefully with connection error against mock dummy IP)
	err = prov.StreamVideo(ctx, resp.List[0].ID, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to Sofia download port")
}

func TestSDCardManager_ListRecordings_SortingDescending(t *testing.T) {
	server, clientConn := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = clientConn.Close() }()

	client := &Client{
		conn:       clientConn,
		sessionID:  0x42,
		sequence:   1,
		isLoggedIn: true,
	}

	sdm := NewSDCardManager(client, "127.0.0.1", "admin", "secret")

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			reqHdr, err := DecodeHeader(buf[:n])
			if err != nil {
				return
			}

			if reqHdr.MsgID == MsgFileSearchReq {
				// Camera sends files in ascending chronological order (oldest first)
				queryResp := map[string]interface{}{
					"Name": "OPFileQuery",
					"OPFileQuery": []map[string]interface{}{
						{
							"BeginTime":  "2026-11-13 10:00:00",
							"EndTime":    "2026-11-13 10:00:30",
							"FileLength": "1000",
							"FileName":   "oldest.h264",
						},
						{
							"BeginTime":  "2026-11-13 10:30:00",
							"EndTime":    "2026-11-13 10:30:30",
							"FileLength": "2000",
							"FileName":   "middle.h264",
						},
						{
							"BeginTime":  "2026-11-13 11:00:00",
							"EndTime":    "2026-11-13 11:00:30",
							"FileLength": "3000",
							"FileName":   "newest.h264",
						},
					},
					"Ret":       100,
					"SessionID": "0x00000042",
				}
				payload, _ := json.Marshal(queryResp)

				respHdr := &Header{
					Magic:      HeaderMagic,
					SessionID:  reqHdr.SessionID,
					Sequence:   reqHdr.Sequence,
					MsgID:      MsgFileSearchResp,
					DataLength: uint32(len(payload)),
				}
				respBytes := append(respHdr.Encode(), payload...)
				_, _ = server.Write(respBytes)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := sdm.ListRecordings(ctx, time.Now().Add(-2*time.Hour), time.Now(), 0, 10, "")
	assert.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, 3, resp.Count)

	// Verify descending order: newest recording must be at index 0
	assert.Equal(t, "newest.h264", resp.List[0].FileName)
	assert.Equal(t, "middle.h264", resp.List[1].FileName)
	assert.Equal(t, "oldest.h264", resp.List[2].FileName)
	assert.True(t, resp.List[0].StartTime.After(resp.List[1].StartTime))
	assert.True(t, resp.List[1].StartTime.After(resp.List[2].StartTime))
}

func TestSDCardManager_ListRecordings_Pagination(t *testing.T) {
	t.Parallel()

	server, clientConn := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = clientConn.Close() }()

	client := &Client{
		conn:       clientConn,
		sessionID:  0x42,
		sequence:   1,
		isLoggedIn: true,
	}
	sdm := NewSDCardManager(client, "127.0.0.1", "admin", "secret")

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			reqHdr, err := DecodeHeader(buf[:n])
			if err != nil {
				return
			}
			if reqHdr.MsgID == MsgFileSearchReq {
				queryResp := map[string]interface{}{
					"Name": "OPFileQuery",
					"OPFileQuery": []map[string]interface{}{
						{"BeginTime": "2026-11-13 10:00:00", "EndTime": "2026-11-13 10:00:30", "FileLength": "1000", "FileName": "rec1.h264"},
						{"BeginTime": "2026-11-13 10:10:00", "EndTime": "2026-11-13 10:10:30", "FileLength": "2000", "FileName": "rec2.h264"},
						{"BeginTime": "2026-11-13 10:20:00", "EndTime": "2026-11-13 10:20:30", "FileLength": "3000", "FileName": "rec3.h264"},
						{"BeginTime": "2026-11-13 10:30:00", "EndTime": "2026-11-13 10:30:30", "FileLength": "4000", "FileName": "rec4.h264"},
					},
					"Ret":       100,
					"SessionID": "0x00000042",
				}
				payload, _ := json.Marshal(queryResp)
				respHdr := &Header{
					Magic:      HeaderMagic,
					SessionID:  reqHdr.SessionID,
					Sequence:   reqHdr.Sequence,
					MsgID:      MsgFileSearchResp,
					DataLength: uint32(len(payload)),
				}
				_, _ = server.Write(append(respHdr.Encode(), payload...))
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp0, err := sdm.ListRecordings(ctx, time.Now().Add(-2*time.Hour), time.Now(), 0, 2, "")
	require.NoError(t, err)
	require.Equal(t, 2, resp0.Count)
	assert.Equal(t, 4, resp0.Total)
	assert.Equal(t, "rec4.h264", resp0.List[0].FileName)
	assert.Equal(t, "rec3.h264", resp0.List[1].FileName)

	resp1, err := sdm.ListRecordings(ctx, time.Now().Add(-2*time.Hour), time.Now(), 1, 2, "")
	require.NoError(t, err)
	require.Equal(t, 2, resp1.Count)
	assert.Equal(t, 4, resp1.Total)
	assert.Equal(t, "rec2.h264", resp1.List[0].FileName)
	assert.Equal(t, "rec1.h264", resp1.List[1].FileName)

	respOOR, err := sdm.ListRecordings(ctx, time.Now().Add(-2*time.Hour), time.Now(), 5, 2, "")
	require.NoError(t, err)
	assert.Equal(t, 0, respOOR.Count)
	assert.Equal(t, 4, respOOR.Total)
	assert.Empty(t, respOOR.List)

	// Overflow-prone page/limit combinations must not panic or wrap to a negative slice index.
	for _, tc := range []struct{ page, limit int }{
		{math.MaxInt, 2},
		{math.MaxInt/2 + 1, 2},
		{2, math.MaxInt},
	} {
		respOverflow, ovErr := sdm.ListRecordings(ctx, time.Now().Add(-2*time.Hour), time.Now(), tc.page, tc.limit, "")
		require.NoError(t, ovErr)
		assert.Equal(t, 0, respOverflow.Count)
		assert.Equal(t, 4, respOverflow.Total)
		assert.Empty(t, respOverflow.List)
	}
}

func TestSDCardManager_GetRecording_HistoricalTimestampWindow(t *testing.T) {
	t.Parallel()

	server, clientConn := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = clientConn.Close() }()

	client := &Client{
		conn:       clientConn,
		sessionID:  0x42,
		sequence:   1,
		isLoggedIn: true,
	}
	sdm := NewSDCardManager(client, "127.0.0.1", "admin", "secret")

	targetLocal := time.Date(2026, 11, 17, 22, 14, 39, 0, time.Local)
	targetID := fmt.Sprintf("%d", targetLocal.Unix())
	wantBegin := targetLocal.Add(-2 * time.Minute).Format("2006-01-02 15:04:05")
	wantEnd := targetLocal.Add(2 * time.Minute).Format("2006-01-02 15:04:05")

	gotQueryCh := make(chan map[string]interface{}, 1)

	go func() {
		hdrBuf := make([]byte, HeaderLength)
		if _, err := io.ReadFull(server, hdrBuf); err != nil {
			return
		}
		reqHdr, err := DecodeHeader(hdrBuf)
		if err != nil {
			return
		}
		reqPayload := make([]byte, reqHdr.DataLength)
		if _, err := io.ReadFull(server, reqPayload); err != nil {
			return
		}
		var parsed map[string]interface{}
		_ = json.Unmarshal(bytes.TrimRight(reqPayload, "\x00"), &parsed)
		gotQueryCh <- parsed

		queryResp := map[string]interface{}{
			"Name": "OPFileQuery",
			"OPFileQuery": []map[string]interface{}{
				{
					"BeginTime":  targetLocal.Format("2006-01-02 15:04:05"),
					"EndTime":    targetLocal.Add(31 * time.Second).Format("2006-01-02 15:04:05"),
					"FileLength": "2029568",
					"FileName":   "/idea0/2026-11-17/001/22.14.39-22.15.10[M][@32eb][0].h264",
				},
			},
			"Ret":       100,
			"SessionID": "0x00000042",
		}
		payload, _ := json.Marshal(queryResp)
		respHdr := &Header{
			Magic:      HeaderMagic,
			SessionID:  reqHdr.SessionID,
			Sequence:   reqHdr.Sequence,
			MsgID:      MsgFileSearchResp,
			DataLength: uint32(len(payload)),
		}
		_, _ = server.Write(append(respHdr.Encode(), payload...))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rec, err := sdm.GetRecording(ctx, targetID)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, targetID, rec.ID)
	assert.Equal(t, "/idea0/2026-11-17/001/22.14.39-22.15.10[M][@32eb][0].h264", rec.FileName)

	parsed := <-gotQueryCh
	queryObj, ok := parsed["OPFileQuery"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, wantBegin, queryObj["BeginTime"])
	assert.Equal(t, wantEnd, queryObj["EndTime"])
}

func TestSDCardManager_StreamVideo_ConcurrencyBusy(t *testing.T) {
	t.Parallel()

	sdm := NewSDCardManager(nil, "127.0.0.1", "admin", "secret")

	sdm.transferMu.Lock()
	defer sdm.transferMu.Unlock()

	err := sdm.StreamVideo(context.Background(), "1794950079", io.Discard, nil)
	assert.ErrorIs(t, err, storage.ErrStorageBusy)
}

func TestSDCardManager_GetRecording_Latest(t *testing.T) {
	t.Parallel()

	server, clientConn := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = clientConn.Close() }()

	client := &Client{
		conn:       clientConn,
		sessionID:  0x42,
		sequence:   1,
		isLoggedIn: true,
	}
	sdm := NewSDCardManager(client, "127.0.0.1", "admin", "secret")

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			reqHdr, err := DecodeHeader(buf[:n])
			if err != nil {
				return
			}
			if reqHdr.MsgID == MsgFileSearchReq {
				queryResp := map[string]interface{}{
					"Name": "OPFileQuery",
					"OPFileQuery": []map[string]interface{}{
						{"BeginTime": "2026-11-13 10:00:00", "EndTime": "2026-11-13 10:00:30", "FileLength": "1000", "FileName": "older.h264"},
						{"BeginTime": "2026-11-13 11:00:00", "EndTime": "2026-11-13 11:00:30", "FileLength": "2000", "FileName": "latest.h264"},
					},
					"Ret":       100,
					"SessionID": "0x00000042",
				}
				payload, _ := json.Marshal(queryResp)
				respHdr := &Header{
					Magic:      HeaderMagic,
					SessionID:  reqHdr.SessionID,
					Sequence:   reqHdr.Sequence,
					MsgID:      MsgFileSearchResp,
					DataLength: uint32(len(payload)),
				}
				_, _ = server.Write(append(respHdr.Encode(), payload...))
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rec, err := sdm.GetRecording(ctx, "latest")
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "latest.h264", rec.FileName)
}
