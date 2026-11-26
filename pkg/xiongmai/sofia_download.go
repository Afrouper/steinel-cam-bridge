package xiongmai

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/Afrouper/steinel-cam-bridge/pkg/storage"
)

// maxSofiaDownloadFrameBytes bounds a single Xiongmai record frame inside an OPPlayBack stream.
const maxSofiaDownloadFrameBytes = 2 * 1024 * 1024

// SofiaDownloader downloads recorded H.264/HEVC video files from a Xiongmai camera
// over the Sofia protocol (Port 34567) using OPPlayBack (MsgID 1420 / 1424) and
// remuxes the elementary video stream into an MP4 container via storage.VideoMuxer.
type SofiaDownloader struct {
	cameraIP      string
	port          int
	client        *Client
	user          string
	password      string
	muxer         storage.VideoMuxer
	rateLimitKBps int
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
		muxer:    storage.NewFFmpegExtractor(""),
	}
}

// SetRateLimit configures the download rate limit in KB/s. 0 disables rate limiting (unlimited).
func (d *SofiaDownloader) SetRateLimit(kbps int) {
	d.rateLimitKBps = kbps
}

// RateLimit returns the configured download rate limit in KB/s.
func (d *SofiaDownloader) RateLimit() int {
	return d.rateLimitKBps
}

// SetMuxer allows injecting a custom or mock VideoMuxer (e.g. for unit testing).
func (d *SofiaDownloader) SetMuxer(m storage.VideoMuxer) {
	d.muxer = m
}

// Download fetches the requested recording from the camera, demuxes the Xiongmai media stream,
// remuxes the video into an MP4 container, and writes the result to w.
func (d *SofiaDownloader) Download(ctx context.Context, rec *storage.RecordingItem, w io.Writer, onStart func(name string, size int64)) error {
	if rec == nil {
		return errors.New("recording item is nil")
	}
	if rec.FileName == "" {
		return errors.New("recording file name is empty")
	}

	targetAddr := fmt.Sprintf("%s:%d", d.cameraIP, d.port)
	if d.client != nil {
		if addr := d.client.GetAddr(); addr != "" {
			targetAddr = addr
		}
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
	defer func() { _ = conn.Close() }()

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
	startReq := OPPlayBackReq{
		Name:      "OPPlayBack",
		SessionID: sessionHex,
		OPPlayBack: OPPlayBackInfo{
			Action: "DownloadStart",
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
		return fmt.Errorf("failed to marshal OPPlayBack DownloadStart: %w", err)
	}

	sentOnControl := false
	if d.client != nil && d.client.IsLoggedIn() {
		logger.Debug("Xiongmai Download", "📡 Sending OPPlayBack DownloadStart (MsgID 1420) on control socket...")
		if _, err := d.client.SendPacket(MsgPlayReq, startPayload); err == nil {
			sentOnControl = true
			logger.Debug("Xiongmai Download", "✅ OPPlayBack DownloadStart acknowledged on control socket")
		} else {
			logger.Warn("Xiongmai Download", "⚠️ OPPlayBack DownloadStart on control socket failed: %v, attempting fallback on media socket", err)
		}
	}

	if !sentOnControl {
		startPayloadWithNul := append(startPayload, 0x00)
		startHdr := Header{
			Magic:      HeaderMagic,
			Channel:    0x00,
			SessionID:  sessionID,
			Sequence:   29,
			TotalPkt:   0x00,
			CurPkt:     0x00,
			MsgID:      MsgPlayReq, // 1420
			DataLength: uint32(len(startPayloadWithNul)),
		}

		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(append(startHdr.Encode(), startPayloadWithNul...)); err != nil {
			return fmt.Errorf("failed to send OPPlayBack DownloadStart: %w", err)
		}
	}

	tmpRaw, err := os.CreateTemp("", "l620_rec_*.raw")
	if err != nil {
		return fmt.Errorf("failed to create temp video file: %w", err)
	}
	tmpRawPath := tmpRaw.Name()
	defer func() {
		_ = tmpRaw.Close()
		_ = os.Remove(tmpRawPath)
	}()

	defer d.sendStop(sessionID, sessionHex, rec.FileName)

	demuxer := newSofiaDownloadDemuxer(tmpRaw)
	var tmpAudio *os.File
	var tmpAudioPath string
	if storage.IncludeAudioFromContext(ctx) {
		if f, aErr := os.CreateTemp("", "l620_rec_*.alaw"); aErr == nil {
			tmpAudio = f
			tmpAudioPath = f.Name()
			demuxer.SetAudioOutput(tmpAudio)
			defer func() {
				_ = tmpAudio.Close()
				_ = os.Remove(tmpAudioPath)
			}()
		}
	}
	packetCount := 0
	hdrBuf := make([]byte, HeaderLength)

	downloadTimeout := 45 * time.Second
	if d.rateLimitKBps > 0 {
		expectedBytes := rec.FileSizeBytes
		if expectedBytes <= 0 {
			if rec.DurationSeconds > 0 {
				expectedBytes = int64(rec.DurationSeconds) * 400 * 1024
			} else {
				expectedBytes = 15 * 1024 * 1024
			}
		}
		transferSec := float64(expectedBytes) / float64(d.rateLimitKBps*1024)
		calcTimeout := time.Duration(transferSec*1.5)*time.Second + 30*time.Second
		if calcTimeout > downloadTimeout {
			downloadTimeout = calcTimeout
		}
	}
	deadline := time.Now().Add(downloadTimeout)

	var mediaReader io.Reader = conn
	if d.rateLimitKBps > 0 {
		mediaReader = storage.NewThrottledReader(ctx, conn, d.rateLimitKBps*1024)
		logger.Debug("Xiongmai Download", "⚡ Rate limiting active: %d KB/s for Sofia download of %s (Timeout: %v)",
			d.rateLimitKBps, rec.ID, downloadTimeout)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			logger.Warn("Xiongmai Download", "⚠️ Download timeout reached for %s (received %d packets, %d bytes)",
				rec.ID, packetCount, demuxer.BytesWritten())
			break
		}

		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		if _, err := io.ReadFull(mediaReader, hdrBuf); err != nil {
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

		if hdr.DataLength > maxSofiaDownloadFrameBytes { // 2MB sanity boundary
			logger.Warn("Xiongmai Download", "⚠️ Oversized packet length %d, stopping", hdr.DataLength)
			break
		}

		payload := make([]byte, hdr.DataLength)
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		if _, err := io.ReadFull(mediaReader, payload); err != nil {
			logger.Warn("Xiongmai Download", "⚠️ Incomplete payload read: %v", err)
			break
		}

		// Handle Media Packets (MsgID 1422 MsgPlayData, 1426 MsgPlayClaimData, or 1412 MsgMonitorData)
		if hdr.MsgID == MsgPlayData || hdr.MsgID == MsgPlayClaimData || hdr.MsgID == MsgMonitorData {
			packetCount++
			if err := demuxer.WriteChunk(payload); err != nil {
				return fmt.Errorf("failed to demux video chunk for %s: %w", rec.ID, err)
			}
		} else {
			logger.Trace("Xiongmai Download", "Ignored non-media packet during download: MsgID=%d, Len=%d", hdr.MsgID, hdr.DataLength)
		}
	}

	if err := demuxer.Flush(); err != nil {
		return fmt.Errorf("failed to flush demuxed video for %s: %w", rec.ID, err)
	}
	_ = tmpRaw.Sync()
	if tmpAudio != nil {
		_ = tmpAudio.Sync()
	}

	totalBytesWritten := demuxer.BytesWritten()
	if totalBytesWritten == 0 {
		return fmt.Errorf("received 0 bytes of video data from camera for %s", rec.ID)
	}

	detectedCodec := demuxer.Codec()
	if detectedCodec == "" {
		detectedCodec = videoCodecH264
	}
	detectedFPS := demuxer.FPS()
	if detectedFPS == 0 && d.client != nil {
		detectedFPS = d.client.GetVideoFPS("Main")
	}

	logger.Info("Xiongmai Download", "✅ Downloaded %d bytes of %s video (%d packets, %d FPS, audio: %d bytes) for %s",
		totalBytesWritten, strings.ToUpper(detectedCodec), packetCount, detectedFPS, demuxer.AudioBytesWritten(), rec.ID)

	muxOpts := storage.MuxOptions{
		Codec: detectedCodec,
		FPS:   detectedFPS,
	}
	if tmpAudioPath != "" && demuxer.AudioBytesWritten() > 0 {
		muxOpts.AudioPath = tmpAudioPath
	}
	return d.convertAndStream(ctx, tmpRawPath, rec.FileName, muxOpts, w, onStart)
}

// sofiaDownloadDemuxer extracts Annex-B H.264/HEVC video frames from chunked Xiongmai
// OPPlayBack streams while discarding metadata (0xF9) records and either stripping or
// demuxing interleaved 8 kHz G.711 A-law audio (0xFA) records when an audio output is set.
type sofiaDownloadDemuxer struct {
	dest        io.Writer
	audioDest   io.Writer
	buf         []byte
	seenMarker  bool
	codec       string
	fps         int
	bytesOut    int64
	audioOut    int64
	framesVideo int
}

func newSofiaDownloadDemuxer(dest io.Writer) *sofiaDownloadDemuxer {
	return &sofiaDownloadDemuxer{dest: dest}
}

func (dm *sofiaDownloadDemuxer) SetAudioOutput(w io.Writer) { dm.audioDest = w }
func (dm *sofiaDownloadDemuxer) BytesWritten() int64        { return dm.bytesOut }
func (dm *sofiaDownloadDemuxer) AudioBytesWritten() int64   { return dm.audioOut }
func (dm *sofiaDownloadDemuxer) Codec() string              { return dm.codec }
func (dm *sofiaDownloadDemuxer) FPS() int                   { return dm.fps }

func (dm *sofiaDownloadDemuxer) WriteChunk(chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}
	dm.buf = append(dm.buf, chunk...)
	return dm.drain(false)
}

func (dm *sofiaDownloadDemuxer) Flush() error {
	return dm.drain(true)
}

// findNextXiongmaiMarker returns the byte offset of the next 4-byte Xiongmai record start code
// (00 00 01 FC/FE/FD/FA/F9) in data, or -1 if none is present.
func findNextXiongmaiMarker(data []byte) int {
	pos := 0
	for pos+4 <= len(data) {
		idx := bytes.Index(data[pos:], []byte{0x00, 0x00, 0x01})
		if idx < 0 {
			return -1
		}
		abs := pos + idx
		if abs+4 > len(data) {
			return -1
		}
		switch data[abs+3] {
		case 0xFC, 0xFE, 0xFD, 0xFA, 0xF9:
			return abs
		}
		pos = abs + 3
	}
	return -1
}

func (dm *sofiaDownloadDemuxer) writeVideo(data []byte, codecByte byte) error {
	if len(data) == 0 {
		return nil
	}
	if dm.codec == "" {
		if detected := detectSofiaVideoCodec(codecByte, data); detected != "" {
			dm.codec = detected
		}
	}
	n, err := dm.dest.Write(data)
	dm.bytesOut += int64(n)
	if err != nil {
		return err
	}
	dm.framesVideo++
	return nil
}

func (dm *sofiaDownloadDemuxer) drain(eof bool) error {
	consumed := 0
	defer func() {
		if consumed > 0 {
			remaining := copy(dm.buf, dm.buf[consumed:])
			if remaining == 0 && cap(dm.buf) > 256*1024 {
				dm.buf = nil
			} else {
				dm.buf = dm.buf[:remaining]
			}
		}
	}()

	for consumed < len(dm.buf) {
		rem := dm.buf[consumed:]
		idx := findNextXiongmaiMarker(rem)
		if idx < 0 {
			if !dm.seenMarker {
				if eof {
					if err := dm.writeVideo(rem, 0); err != nil {
						return err
					}
					consumed = len(dm.buf)
				} else if len(rem) > 3 {
					flushLen := len(rem) - 3
					if err := dm.writeVideo(rem[:flushLen], 0); err != nil {
						return err
					}
					consumed += flushLen
				}
			} else if eof {
				consumed = len(dm.buf)
			} else if len(rem) > 3 {
				consumed += len(rem) - 3
			}
			return nil
		}

		if idx > 0 {
			if !dm.seenMarker {
				if err := dm.writeVideo(rem[:idx], 0); err != nil {
					return err
				}
			}
			consumed += idx
			rem = dm.buf[consumed:]
		}

		dm.seenMarker = true
		if len(rem) < 4 {
			return nil
		}

		switch rem[3] {
		case 0xFC, 0xFE: // I-Frame (16-byte header: byte 4 codec, byte 5 FPS, bytes 12..16 uint32 LE length)
			if len(rem) < 16 {
				if eof {
					consumed = len(dm.buf)
				}
				return nil
			}
			codecByte := rem[4]
			fpsByte := rem[5]
			if dm.fps == 0 && fpsByte >= 1 && fpsByte <= 60 {
				dm.fps = int(fpsByte)
			}
			rawLen := binary.LittleEndian.Uint32(rem[12:16])
			if rawLen > 0 && rawLen <= maxSofiaDownloadFrameBytes {
				frameLen := int(rawLen)
				if len(rem) < 16+frameLen {
					if eof {
						if err := dm.writeVideo(rem[16:], codecByte); err != nil {
							return err
						}
						consumed = len(dm.buf)
					}
					return nil
				}
				if err := dm.writeVideo(rem[16:16+frameLen], codecByte); err != nil {
					return err
				}
				consumed += 16 + frameLen
			} else {
				next := findNextXiongmaiMarker(rem[16:])
				if next < 0 {
					if !eof {
						return nil
					}
					if err := dm.writeVideo(rem[16:], codecByte); err != nil {
						return err
					}
					consumed = len(dm.buf)
					return nil
				}
				if err := dm.writeVideo(rem[16:16+next], codecByte); err != nil {
					return err
				}
				consumed += 16 + next
			}

		case 0xFD: // P-Frame (8-byte header: bytes 4..8 uint32 LE length)
			if len(rem) < 8 {
				if eof {
					consumed = len(dm.buf)
				}
				return nil
			}
			rawLen := binary.LittleEndian.Uint32(rem[4:8])
			if rawLen > 0 && rawLen <= maxSofiaDownloadFrameBytes {
				frameLen := int(rawLen)
				if len(rem) < 8+frameLen {
					if eof {
						if err := dm.writeVideo(rem[8:], 0); err != nil {
							return err
						}
						consumed = len(dm.buf)
					}
					return nil
				}
				if err := dm.writeVideo(rem[8:8+frameLen], 0); err != nil {
					return err
				}
				consumed += 8 + frameLen
			} else {
				next := findNextXiongmaiMarker(rem[8:])
				if next < 0 {
					if !eof {
						return nil
					}
					if err := dm.writeVideo(rem[8:], 0); err != nil {
						return err
					}
					consumed = len(dm.buf)
					return nil
				}
				if err := dm.writeVideo(rem[8:8+next], 0); err != nil {
					return err
				}
				consumed += 8 + next
			}

		case 0xFA, 0xF9: // Audio (0xFA) or metadata (0xF9) record (8-byte header: bytes 6..8 uint16 LE length)
			if len(rem) < 8 {
				if eof {
					consumed = len(dm.buf)
				}
				return nil
			}
			isAudio := rem[3] == 0xFA
			frameLen := int(binary.LittleEndian.Uint16(rem[6:8]))
			if frameLen > 0 {
				if len(rem) < 8+frameLen {
					if eof {
						if isAudio && dm.audioDest != nil {
							n, err := dm.audioDest.Write(rem[8:])
							dm.audioOut += int64(n)
							if err != nil {
								return err
							}
						}
						consumed = len(dm.buf)
					}
					return nil
				}
				if isAudio && dm.audioDest != nil {
					n, err := dm.audioDest.Write(rem[8 : 8+frameLen])
					dm.audioOut += int64(n)
					if err != nil {
						return err
					}
				}
				consumed += 8 + frameLen
			} else {
				next := findNextXiongmaiMarker(rem[8:])
				if next < 0 {
					if !eof {
						return nil
					}
					if isAudio && dm.audioDest != nil {
						n, err := dm.audioDest.Write(rem[8:])
						dm.audioOut += int64(n)
						if err != nil {
							return err
						}
					}
					consumed = len(dm.buf)
					return nil
				}
				if isAudio && dm.audioDest != nil {
					n, err := dm.audioDest.Write(rem[8 : 8+next])
					dm.audioOut += int64(n)
					if err != nil {
						return err
					}
				}
				consumed += 8 + next
			}
		}
	}
	return nil
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

func (d *SofiaDownloader) convertAndStream(ctx context.Context, rawPath, fileName string, opts storage.MuxOptions, w io.Writer, onStart func(name string, size int64)) error {
	streamFile := func(path string) (int64, error) {
		f, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		defer func() { _ = f.Close() }()

		var size int64
		if info, statErr := f.Stat(); statErr == nil {
			size = info.Size()
		}
		if onStart != nil {
			onStart(fileName, size)
		}
		return io.Copy(w, f)
	}

	if d.muxer == nil || !d.muxer.IsAvailable() {
		_, err := streamFile(rawPath)
		return err
	}

	tmpMP4, err := os.CreateTemp("", "l620_rec_*.mp4")
	if err != nil {
		return fmt.Errorf("failed to create temp mp4 file: %w", err)
	}
	tmpMP4Path := tmpMP4.Name()
	_ = tmpMP4.Close()
	defer func() { _ = os.Remove(tmpMP4Path) }()

	if err := d.muxer.MuxToMP4(ctx, rawPath, tmpMP4Path, opts); err != nil {
		logger.Warn("Xiongmai Download", "⚠️ Remuxing %s -> MP4 failed (%v), falling back to raw copy", strings.ToUpper(opts.Codec), err)
		_, copyErr := streamFile(rawPath)
		return copyErr
	}

	written, err := streamFile(tmpMP4Path)
	if err != nil {
		return fmt.Errorf("failed to write MP4 to destination: %w", err)
	}

	logger.Debug("Xiongmai Download", "🎬 Successfully muxed (%s, %d FPS) and streamed %d bytes of MP4 video",
		strings.ToUpper(opts.Codec), opts.FPS, written)
	return nil
}
