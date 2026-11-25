package xiongmai

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/audio"
	"github.com/Afrouper/steinel-cam-bridge/pkg/logger"
	"github.com/pion/rtp"
)

// maxSofiaStreamBuf caps the reassembly buffer when a malformed record lacks a length or delimiter.
const maxSofiaStreamBuf = 2 * 1024 * 1024

// maxIdleStreamBufCap bounds the backing-array capacity retained between frames.
const maxIdleStreamBufCap = 256 * 1024

// maxSofiaMediaPayload is the sanity limit for a single DVRIP packet on the media socket.
const maxSofiaMediaPayload = 1024 * 1024

// maxSofiaResyncBytes bounds the number of bytes scanned while resynchronizing DVRIP framing.
const maxSofiaResyncBytes = 512 * 1024

// sofiaDiagPayloads is the number of initial MsgID 1412 payloads hex-dumped at debug level.
const sofiaDiagPayloads = 3

var sofiaRecordStartCode = []byte{0x00, 0x00, 0x01}
var annexBLongStartCode = []byte{0x00, 0x00, 0x00, 0x01}

// decodeMediaHeader decodes a DVRIP header received on the media socket and validates its payload length.
func decodeMediaHeader(buf []byte) (Header, error) {
	hdr, err := DecodeHeaderValue(buf)
	if err != nil {
		return Header{}, err
	}
	if hdr.DataLength > maxSofiaMediaPayload {
		return Header{}, fmt.Errorf("implausible payload length %d", hdr.DataLength)
	}
	return hdr, nil
}

// resyncMediaHeader slides byte-by-byte over the media stream until a plausible DVRIP header is found.
func resyncMediaHeader(rd io.ByteReader, hdrBuf []byte) (Header, error) {
	for i := 0; i < maxSofiaResyncBytes; i++ {
		copy(hdrBuf, hdrBuf[1:])
		b, err := rd.ReadByte()
		if err != nil {
			return Header{}, err
		}
		hdrBuf[len(hdrBuf)-1] = b
		if hdrBuf[0] != HeaderMagic {
			continue
		}
		hdr, err := decodeMediaHeader(hdrBuf)
		if err != nil {
			continue
		}
		isValidEnvelope := hdr.Channel <= 1 && hdr.Reserved[0] == 0 && hdr.Reserved[1] == 0 && hdr.DataLength > 0
		isMonitorMsg := hdr.MsgID == MsgMonitorData || hdr.MsgID == MsgMonitorStartResp || hdr.MsgID == MsgMonitorClaimResp
		if isValidEnvelope && isMonitorMsg {
			return hdr, nil
		}
	}
	return Header{}, fmt.Errorf("no valid dvrip header found within %d bytes", maxSofiaResyncBytes)
}

// handleMediaPayload appends incoming Sofia media data to the stream buffer and triggers demuxing.
func (s *SofiaStream) handleMediaPayload(payload []byte) {
	if len(payload) == 0 {
		return
	}

	s.tsMu.Lock()
	defer s.tsMu.Unlock()

	// Skip JSON control responses that might be interleaved on the media socket when idle,
	// without ever dropping binary continuation chunks that happen to start with 0x7B ('{').
	if len(s.streamBuf) == 0 && payload[0] == '{' {
		trimmed := bytes.TrimRight(payload, "\x00\r\n ")
		if len(trimmed) >= 2 && trimmed[len(trimmed)-1] == '}' {
			return
		}
	}

	s.payloadCount++
	if s.payloadCount <= sofiaDiagPayloads {
		logger.Debug("Xiongmai Stream", "🔬 MsgID 1412 payload #%d (raw): %s", s.payloadCount, logger.FormatBinary(payload, 64))
	}

	s.streamBuf = append(s.streamBuf, payload...)
	s.processStreamBufferLocked()
}

// findNextSofiaRecord searches for the next MPEG-PS / Sofia record start code (0x00 0x00 0x01 [0xF8-0xFE]).
func findNextSofiaRecord(buf []byte) int {
	for i := 0; i+3 < len(buf); i++ {
		if buf[i] == 0x00 && buf[i+1] == 0x00 && buf[i+2] == 0x01 {
			marker := buf[i+3]
			if marker >= 0xF8 && marker <= 0xFE {
				return i
			}
		}
	}
	return -1
}

// processStreamBufferLocked parses Xiongmai media markers (I-Frame, P-Frame, Audio) using exact record lengths
// and demuxes H.264/H.265 Annex-B data immediately without waiting for the next frame delimiter.
// Remaining trailing bytes are compacted back to index 0 so the backing array capacity is preserved across frames.
func (s *SofiaStream) processStreamBufferLocked() {
	consumed := 0
	defer func() {
		if consumed > 0 && s.streamBuf != nil {
			remaining := copy(s.streamBuf, s.streamBuf[consumed:])
			s.streamBuf = s.streamBuf[:remaining]
		}
		switch {
		case len(s.streamBuf) == 0:
			if cap(s.streamBuf) > maxIdleStreamBufCap {
				s.streamBuf = nil
			} else {
				s.streamBuf = s.streamBuf[:0]
			}
		case cap(s.streamBuf) > maxIdleStreamBufCap && len(s.streamBuf) <= maxIdleStreamBufCap/4:
			// Release oversized keyframe backing array so a few trailing header bytes do not pin >256 KB.
			s.streamBuf = bytes.Clone(s.streamBuf)
		}
	}()

	for len(s.streamBuf)-consumed >= 4 {
		cur := s.streamBuf[consumed:]
		// Resynchronize on MPEG-PS / Sofia record start code (0x00 0x00 0x01)
		if !bytes.HasPrefix(cur, sofiaRecordStartCode) {
			idx := bytes.Index(cur, sofiaRecordStartCode)
			if idx > 0 {
				consumed += idx
				cur = s.streamBuf[consumed:]
			} else {
				if len(cur) > 2 {
					consumed += len(cur) - 2
				}
				break
			}
			if len(cur) < 4 {
				break
			}
		}

		pType := cur[3]
		var (
			headerLen   int
			payloadSize int
		)

		switch pType {
		case 0xFC, 0xFE:
			// Keyframe / I-Frame
			headerLen = 16
			if len(cur) < headerLen {
				return
			}
			payloadSize = int(binary.LittleEndian.Uint32(cur[12:16]))

		case 0xFD:
			// Inter-frame / P-Frame
			headerLen = 8
			if len(cur) < headerLen {
				return
			}
			payloadSize = int(binary.LittleEndian.Uint32(cur[4:8]))

		case 0xFA, 0xF9:
			// Audio frame (0xFA) or metadata frame (0xF9)
			headerLen = 8
			if len(cur) < headerLen {
				return
			}
			payloadSize = int(binary.LittleEndian.Uint16(cur[6:8]))

		default:
			// Unknown marker byte: advance past the start code to resynchronize
			consumed += 3
			continue
		}

		totalRecordSize, ok := s.resolveRecordSizeAtLocked(cur, headerLen, payloadSize, &consumed)
		if !ok {
			return
		}

		if totalRecordSize <= headerLen || totalRecordSize > maxSofiaStreamBuf {
			consumed += 3
			continue
		}

		if len(cur) < totalRecordSize {
			// Record not complete yet, wait for more data
			break
		}

		record := cur[:totalRecordSize]
		consumed += totalRecordSize

		switch pType {
		case 0xFC, 0xFE, 0xFD:
			s.dispatchVideoRecordLocked(pType, record[:headerLen], record[headerLen:])
		case 0xFA:
			audioData := record[headerLen:]
			s.dispatchAudioLocked(audioData)
		case 0xF9:
			// 0xF9 is stream/encoder metadata or sync marker in Xiongmai DVRIP protocol, NOT audio samples.
			// Silently advance buffer without forwarding to audio pipeline to eliminate clicking noise (Issue #74).
		}
	}
}

func (s *SofiaStream) resolveRecordSizeAtLocked(cur []byte, headerLen, payloadSize int, consumed *int) (int, bool) {
	if payloadSize > 0 {
		return headerLen + payloadSize, true
	}
	if nextIdx := findNextSofiaRecord(cur[headerLen:]); nextIdx >= 0 {
		return headerLen + nextIdx, true
	}
	if len(cur) > maxSofiaStreamBuf {
		logger.Warn("Xiongmai Stream", "⚠️ Sofia stream buffer exceeded %d bytes without record boundary; resetting buffer", maxSofiaStreamBuf)
		*consumed = 0
		s.streamBuf = nil
		s.gotKeyframe = false
		if s.keyframeNotify == nil {
			s.keyframeNotify = make(chan struct{})
		}
	}
	return 0, false
}

// dispatchVideoRecordLocked routes a complete Sofia video record (I- or P-frame) through the
// active codec packetizer. Codec detection runs on every I-frame so an H.265 stream is switched
// automatically even if Simplify.Encode reported H.264.
func (s *SofiaStream) dispatchVideoRecordLocked(pType byte, hdr []byte, payload []byte) {
	// Record receipt of a genuine video record before the keyframe gate so cameras with long
	// GOP intervals (e.g. 15-20s) are not mistaken for audio-only/stalled streams while waiting
	// for the next I-frame.
	s.lastVideoRecordTime.Store(time.Now().UnixNano())

	isKeyframe := pType == 0xFC || pType == 0xFE
	if isKeyframe {
		s.ensureVideoCodecLocked(hdr, payload)
		if !s.gotKeyframe && s.droppedBeforeKey > 0 {
			logger.Debug("Xiongmai Stream", "⏭️ Dropped %d P-frame(s) before the first keyframe", s.droppedBeforeKey)
		}
		s.gotKeyframe = true
		s.keyframeWaitStart = time.Time{}
		s.lastKeyframeTime = time.Now()
	} else if !s.gotKeyframe {
		// Decoders cannot start on a P-frame: wait for the first I-frame of the session.
		s.droppedBeforeKey++
		return
	}

	if !s.isCodecUnsupported {
		s.dispatchVideoFrameLocked(payload)
	}
	if isKeyframe && s.keyframeNotify != nil {
		close(s.keyframeNotify)
		s.keyframeNotify = nil
	}
}

// ensureVideoCodecLocked selects or switches the active videoPacketizer based on an I-frame.
func (s *SofiaStream) ensureVideoCodecLocked(hdr []byte, payload []byte) {
	var codecByte, fpsByte, widthBlocks, heightBlocks byte
	if len(hdr) >= 8 {
		codecByte = hdr[4]
		fpsByte = hdr[5]
		widthBlocks = hdr[6]
		heightBlocks = hdr[7]
	}

	detected := detectSofiaVideoCodec(codecByte, payload)
	if detected == "" {
		if s.packetizer != nil {
			return
		}
		detected = videoCodecH264
	}

	codecChanged := s.packetizer == nil || s.packetizer.codec() != detected
	if fpsByte >= 1 && fpsByte <= 60 {
		s.fps = int(fpsByte)
	}
	if !s.hasLoggedCodec || codecChanged {
		nalHdr := firstAnnexBNALU(payload)
		if len(nalHdr) > 2 {
			nalHdr = nalHdr[:2]
		}
		logger.Info("Xiongmai Stream", "🎞️ Sofia video codec detected: %s (Xiongmai record header: %s, first NAL header: %s, %dx%d @ %d fps)",
			strings.ToUpper(detected), logger.FormatBinary(hdr), logger.FormatBinary(nalHdr),
			int(widthBlocks)*8, int(heightBlocks)*8, fpsByte)

		if detected == videoCodecH265 && s.client != nil {
			stType := s.activeStreamType
			if stType == "" {
				stType = "Main"
			}
			if comp := s.client.GetVideoCompression(stType); comp != "" && !strings.Contains(strings.ToUpper(comp), "265") {
				logger.Warn("Xiongmai Stream", "⚠️ Camera reports Compression=%s for stream '%s' (Simplify.Encode), but the native Sofia stream actually carries H.265/HEVC. Announcing H.265 via RTSP.", comp, stType)
			}
		}
	}

	s.detectedCodec = detected
	s.hasLoggedCodec = true

	if !codecChanged {
		return
	}

	pktz, err := newVideoPacketizer(detected)
	if err != nil {
		logger.Error("Xiongmai Stream", "❌ Failed to initialize %s packetizer: %v", strings.ToUpper(detected), err)
		s.isCodecUnsupported = true
		return
	}

	// Publish parameter sets before SetVideoCodec creates the new RTSP ServerStream so
	// immediate client DESCRIBE requests receive complete sprop-vps/sps/pps parameters.
	publishKeyframeParameters(detected, payload, s.rtspServer)

	if sw, ok := s.rtspServer.(videoCodecSwitcher); ok {
		sw.SetVideoCodec(detected)
	} else if detected != videoCodecH264 {
		logger.Error("Xiongmai Stream", "❌ Camera delivers %s but the RTSP output only supports H.264; dropping video.", strings.ToUpper(detected))
		s.isCodecUnsupported = true
		return
	}

	s.packetizer = pktz
	s.isCodecUnsupported = false
}

// dispatchVideoFrameLocked unmarshals an Annex-B frame, packetizes it via the active codec
// packetizer, assigns a strictly monotonic 90 kHz timestamp, and forwards it to the RTSP server.
func (s *SofiaStream) dispatchVideoFrameLocked(data []byte) {
	if len(data) == 0 || s.rtspServer == nil || s.packetizer == nil {
		return
	}

	// Resynchronize on first Annex-B start code if leading padding exists
	if !bytes.HasPrefix(data, sofiaRecordStartCode) && !bytes.HasPrefix(data, annexBLongStartCode) {
		if idx := bytes.Index(data, sofiaRecordStartCode); idx > 0 {
			data = data[idx:]
		}
	}

	// Single-pass SIMD Annex-B start-code splitter reusing s.nalusBuf without heap allocation.
	nalus, ok := splitAnnexBNALUs(s.nalusBuf[:0], data)
	if !ok || len(nalus) == 0 {
		logger.Trace("Xiongmai Stream", "⚠️ Failed to split Annex-B NALUs (%d bytes)", len(data))
		return
	}
	defer func() {
		clear(nalus)
		s.nalusBuf = nalus[:0]
	}()

	pkts, err := s.packetizer.packetize(nalus, s.rtspServer)
	if err != nil || len(pkts) == 0 {
		if err != nil {
			logger.Trace("Xiongmai Stream", "⚠️ Failed to RTP-encode %s NALUs: %v", strings.ToUpper(s.packetizer.codec()), err)
		}
		return
	}

	ts := s.generateTimestampLocked()
	s.lastPacketTime.Store(time.Now().UnixNano())
	for _, pkt := range pkts {
		pkt.Timestamp = ts
		s.rtspServer.WriteVideoPacket(pkt)
	}
}

// dispatchAudioLocked processes camera microphone audio (8 kHz G.711 A-law) and forwards it to the RTSP server.
func (s *SofiaStream) dispatchAudioLocked(audioData []byte) {
	if len(audioData) == 0 || len(audioData) > 4096 || s.rtspServer == nil || s.audioCodec == "none" {
		return
	}
	if s.audioTranscoder != nil {
		// Xiongmai microphone default is G.711 A-law (PCMA)
		_ = s.audioTranscoder.ProcessPCMA(audioData)
	} else if audioWriter, ok := s.rtspServer.(interface {
		WriteAudioPacket(pkt *rtp.Packet)
	}); ok {
		pcmuPayload := audio.TranscodePCMAToPCMU(audioData)
		now := time.Now()
		if s.audioTimestamp == 0 {
			if s.streamStartTime.IsZero() {
				s.streamStartTime = now
			}
			s.audioTimestamp = uint32(now.Sub(s.streamStartTime).Seconds() * 8000)
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    0,
				SequenceNumber: s.audioSeq,
				Timestamp:      s.audioTimestamp,
				SSRC:           s.audioSSRC,
			},
			Payload: pcmuPayload,
		}
		s.audioSeq++
		s.audioTimestamp += uint32(len(pcmuPayload))
		audioWriter.WriteAudioPacket(pkt)
	}
}

// generateTimestampLocked generates strictly monotonic 90 kHz RTP timestamps anchored to streamStartTime.
// This prevents timestamp drift during TCP/Wi-Fi burst packet delivery while ensuring strict monotonicity.
func (s *SofiaStream) generateTimestampLocked() uint32 {
	now := time.Now()
	if s.streamStartTime.IsZero() {
		s.streamStartTime = now
	}

	wallTicks := uint32(now.Sub(s.streamStartTime).Seconds() * 90000)

	if s.lastFrameTime.IsZero() {
		s.lastFrameTime = now
		s.currentTS = wallTicks
		return s.currentTS
	}

	// RFC 3550 modular comparison: if wallTicks has not advanced beyond currentTS (due to sub-millisecond
	// TCP bursts or jitter), advance by 1 tick to preserve strict monotonicity without drifting ahead.
	if int32(wallTicks-s.currentTS) <= 0 {
		s.currentTS++
	} else {
		s.currentTS = wallTicks
	}

	s.lastFrameTime = now
	return s.currentTS
}
