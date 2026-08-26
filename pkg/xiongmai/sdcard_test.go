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

	// Test StreamThumbnail unsupported
	err = prov.StreamThumbnail(ctx, resp.List[0].ID, nil)
	assert.ErrorIs(t, err, storage.ErrFeatureDisabled)
}
