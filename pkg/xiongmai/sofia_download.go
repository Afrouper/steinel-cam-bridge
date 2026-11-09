package xiongmai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
)

// SofiaDownloader handles downloading recorded H.264 video files from a Xiongmai camera
// over the Sofia protocol (Port 34567) using OPPlayBack (MsgID 1420 / 1424).
// Follows Single Responsibility Principle by encapsulating all download-specific
// socket communication, Xiongmai media demuxing, and MP4 container conversion.
type SofiaDownloader struct {
	cameraIP string
	port     int
	client   *Client
	user     string
	password string
}

// NewSofiaDownloader creates a new Sofia video recording downloader.
func NewSofiaDownloader(cameraIP string, port int, client *Client, user, password string) *SofiaDownloader {
	if port <= 0 {
		port = DefaultPort
	}
	if user == "" {
		user = "admin"
	}
	return &SofiaDownloader{
		cameraIP: cameraIP,
		port:     port,
		client:   client,
		user:     user,
		password: password,
	}
}

// Download fetches the requested recording from the camera, demuxes Xiongmai H.264 packets,
// converts the stream into an MP4 container, and streams the result into w.
func (d *SofiaDownloader) Download(ctx context.Context, rec *storage.RecordingItem, w io.Writer, onStart func(name string, size int64)) error {
	if rec == nil {
		return errors.New("recording item is nil")
	}
	if rec.FileName == "" {
		return errors.New("recording file name is empty")
	}

	targetAddr := fmt.Sprintf("%s:%d", d.cameraIP, d.port)
	if d.client != nil && d.client.addr != "" {
		targetAddr = d.client.addr
	}

	sessionID := uint32(0)
	if d.client != nil {
		sessionID = d.client.GetSessionID()
	}
	sessionHex := fmt.Sprintf("0x%010x", sessionID)

	logger.Info("Xiongmai Download", "📥 Initiating Sofia download for %s (%s, %ds) to %s...",
		rec.ID, rec.FileName, rec.DurationSeconds, targetAddr)

	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to Sofia download port %s: %w", targetAddr, err)
	}
	defer conn.Close()

	// Step 1: Send OPPlayBack Claim (MsgID 1424)
	startTimeStr := rec.StartTime.Local().Format("2006-01-02 15:04:05")
	endTimeStr := rec.EndTime.Local().Format("2006-01-02 15:04:05")

	claimReq := OPPlayBackReq{
		Name:      "OPPlayBack",
		SessionID: sessionHex,
		OPPlayBack: OPPlayBackInfo{
			Action: "Claim",
			Parameter: &OPPlayBackParam{
				PlayMode:   "ByName",
				FileName:   rec.FileName,
				StreamType: 0,
				Value:      0,
				TransMode:  "TCP",
			},
			StartTime: startTimeStr,
			EndTime:   endTimeStr,
		},
	}

	claimPayload, err := json.Marshal(claimReq)
	if err != nil {
		return fmt.Errorf("failed to marshal OPPlayBack Claim: %w", err)
	}
	claimPayload = append(claimPayload, 0x00)

	claimHdr := Header{
		Magic:      HeaderMagic,
		Channel:    0x00,
		SessionID:  sessionID,
		Sequence:   28,
		TotalPkt:   0x00,
		CurPkt:     0x00,
		MsgID:      MsgPlayClaimReq, // 1424
		DataLength: uint32(len(claimPayload)),
	}

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(append(claimHdr.Encode(), claimPayload...)); err != nil {
		return fmt.Errorf("failed to send OPPlayBack Claim: %w", err)
	}

	// Read Claim response (Header + JSON body)
	respHdrBuf := make([]byte, HeaderLength)
	if _, err := io.ReadFull(conn, respHdrBuf); err != nil {
		return fmt.Errorf("failed to read OPPlayBack Claim response header: %w", err)
	}
	respHdr, err := DecodeHeader(respHdrBuf)
	if err != nil {
		return fmt.Errorf("invalid OPPlayBack Claim response header: %w", err)
	}

	if respHdr.DataLength > 0 && respHdr.DataLength < 65536 {
		respBody := make([]byte, respHdr.DataLength)
		if _, err := io.ReadFull(conn, respBody); err != nil {
			return fmt.Errorf("failed to read OPPlayBack Claim response body: %w", err)
		}
		var claimResp struct {
			Ret int `json:"Ret"`
		}
		_ = json.Unmarshal([]byte(strings.TrimRight(string(respBody), "\x00\r\n ")), &claimResp)
		if claimResp.Ret != 100 && claimResp.Ret != 0 {
			logger.Warn("Xiongmai Download", "⚠️ OPPlayBack Claim returned Ret=%d (proceeding with start)", claimResp.Ret)
		}
	}

	// Step 2: Send OPPlayBack DownloadStart (MsgID 1420)
	startActions := []string{"DownloadStart", "Start"}
	var startSucceeded bool

	for _, action := range startActions {
		startReq := OPPlayBackReq{
			Name:      "OPPlayBack",
			SessionID: sessionHex,
			OPPlayBack: OPPlayBackInfo{
				Action: action,
				Parameter: &OPPlayBackParam{
					PlayMode:   "ByName",
					FileName:   rec.FileName,
					StreamType: 0,
					Value:      0,
					TransMode:  "TCP",
				},
				StartTime: startTimeStr,
				EndTime:   endTimeStr,
			},
		}

		startPayload, err := json.Marshal(startReq)
		if err != nil {
			return fmt.Errorf("failed to marshal OPPlayBack %s: %w", action, err)
		}
		startPayload = append(startPayload, 0x00)

		startHdr := Header{
			Magic:      HeaderMagic,
			Channel:    0x00,
			SessionID:  sessionID,
			Sequence:   29,
			TotalPkt:   0x00,
			CurPkt:     0x00,
			MsgID:      MsgPlayReq, // 1420
			DataLength: uint32(len(startPayload)),
		}

		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(append(startHdr.Encode(), startPayload...)); err != nil {
			return fmt.Errorf("failed to send OPPlayBack %s: %w", action, err)
		}

		startSucceeded = true
		break
	}

	if !startSucceeded {
		return errors.New("failed to start OPPlayBack stream")
	}

	if onStart != nil {
		onStart(rec.FileName, int64(rec.FileSizeBytes))
	}

	// Create temporary file to buffer demuxed Annex-B H.264 elementary stream
	tmpH264, err := os.CreateTemp("", "l620_rec_*.h264")
	if err != nil {
		return fmt.Errorf("failed to create temp h264 file: %w", err)
	}
	tmpH264Path := tmpH264.Name()
	defer func() {
		_ = tmpH264.Close()
		_ = os.Remove(tmpH264Path)
	}()

	// Ensure we send Stop when finished
	defer d.sendStop(sessionID, sessionHex, rec.FileName)

	// Step 3: Stream and demux Xiongmai binary video packets
	totalBytesWritten := int64(0)
	packetCount := 0
	hdrBuf := make([]byte, HeaderLength)

	downloadTimeout := 45 * time.Second
	deadline := time.Now().Add(downloadTimeout)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			logger.Warn("Xiongmai Download", "⚠️ Download timeout reached for %s (received %d packets, %d bytes)",
				rec.ID, packetCount, totalBytesWritten)
			break
		}

		_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		if _, err := io.ReadFull(conn, hdrBuf); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				break
			}
			logger.Debug("Xiongmai Download", "Read header error or EOF: %v", err)
			break
		}

		hdr, err := DecodeHeader(hdrBuf)
		if err != nil {
			continue
		}

		// Check for EOF packet (MsgID 1423 MsgPlayEOF or zero length after packets received)
		if hdr.MsgID == MsgPlayEOF || (hdr.DataLength == 0 && packetCount > 0) {
			logger.Debug("Xiongmai Download", "🏁 Playback EOF received from camera (MsgID %d)", hdr.MsgID)
			break
		}

		if hdr.DataLength > 2*1024*1024 { // 2MB sanity boundary
			logger.Warn("Xiongmai Download", "⚠️ Oversized packet length %d, stopping", hdr.DataLength)
			break
		}

		payload := make([]byte, hdr.DataLength)
		_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		if _, err := io.ReadFull(conn, payload); err != nil {
			logger.Warn("Xiongmai Download", "⚠️ Incomplete payload read: %v", err)
			break
		}

		// Handle Media Packets (MsgID 1422 MsgPlayData or 1412 MsgMonitorData)
		if hdr.MsgID == MsgPlayData || hdr.MsgID == MsgMonitorData {
			packetCount++
			written := d.demuxAndWriteH264(payload, tmpH264)
			totalBytesWritten += int64(written)
		}
	}

	_ = tmpH264.Sync()

	if totalBytesWritten == 0 {
		return fmt.Errorf("received 0 bytes of video data from camera for %s", rec.ID)
	}

	logger.Info("Xiongmai Download", "✅ Downloaded %d bytes of H.264 video (%d packets) for %s",
		totalBytesWritten, packetCount, rec.ID)

	// Step 4: Convert Annex-B H.264 to standard MP4 container
	return d.convertAndStream(ctx, tmpH264Path, w)
}

func (d *SofiaDownloader) demuxAndWriteH264(payload []byte, dest io.Writer) int {
	if len(payload) >= 4 {
		marker := payload[0:4]

		if bytes.Equal(marker, MarkerIFrame) {
			// I-Frame: skip 16-byte Xiongmai record header
			if len(payload) > 16 {
				n, _ := dest.Write(payload[16:])
				return n
			}
			return 0
		}

		if bytes.Equal(marker, MarkerPFrame) {
			// P-Frame: skip 8-byte Xiongmai record header
			if len(payload) > 8 {
				n, _ := dest.Write(payload[8:])
				return n
			}
			return 0
		}

		if bytes.Equal(marker, MarkerAudio) {
			// Audio frame: skip to prevent H.264 elementary stream corruption
			return 0
		}
	}

	// Continuation chunk of current video frame
	n, _ := dest.Write(payload)
	return n
}

func (d *SofiaDownloader) sendStop(sessionID uint32, sessionHex, fileName string) {
	if d.client == nil || !d.client.IsLoggedIn() {
		return
	}
	stopReq := OPPlayBackReq{
		Name:      "OPPlayBack",
		SessionID: sessionHex,
		OPPlayBack: OPPlayBackInfo{
			Action: "DownloadStop",
			Parameter: &OPPlayBackParam{
				FileName: fileName,
			},
		},
	}
	payload, err := json.Marshal(stopReq)
	if err == nil {
		_, _ = d.client.SendPacket(MsgPlayReq, append(payload, 0x00))
	}
}

func (d *SofiaDownloader) convertAndStream(ctx context.Context, h264Path string, w io.Writer) error {
	ffmpegPath := findFFmpeg()
	if ffmpegPath == "" {
		// Fallback: If ffmpeg is not available, write the H.264 stream directly to w
		f, err := os.Open(h264Path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	}

	tmpMP4, err := os.CreateTemp("", "l620_rec_*.mp4")
	if err != nil {
		return fmt.Errorf("failed to create temp mp4 file: %w", err)
	}
	tmpMP4Path := tmpMP4.Name()
	_ = tmpMP4.Close()
	defer os.Remove(tmpMP4Path)

	// Mux H.264 Annex-B into MP4 container with faststart for instant streaming
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-y",
		"-f", "h264",
		"-i", h264Path,
		"-c:v", "copy",
		"-movflags", "+faststart",
		tmpMP4Path,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Warn("Xiongmai Download", "⚠️ FFmpeg H.264 -> MP4 muxing error (%v): %s (falling back to raw copy)",
			err, stderr.String())
		// Fallback to raw copy if muxing failed
		f, errRaw := os.Open(h264Path)
		if errRaw != nil {
			return errRaw
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	}

	// Stream converted MP4 to destination writer
	mp4File, err := os.Open(tmpMP4Path)
	if err != nil {
		return fmt.Errorf("failed to open converted MP4 file: %w", err)
	}
	defer mp4File.Close()

	written, err := io.Copy(w, mp4File)
	if err != nil {
		return fmt.Errorf("failed to write MP4 to destination: %w", err)
	}

	logger.Debug("Xiongmai Download", "🎬 Successfully muxed and streamed %d bytes of MP4 video", written)
	return nil
}

func findFFmpeg() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	for _, candidate := range []string{"/usr/local/bin/ffmpeg", "/usr/bin/ffmpeg", "/opt/homebrew/bin/ffmpeg"} {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	return ""
}
