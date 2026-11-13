package xiongmai

import (
	"context"
	"encoding/json"
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
