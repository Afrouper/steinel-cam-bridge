// stream-sim plays an Annex-B H.264 file through the bridge's RTSP layer the
// way the L 625 delivers its live stream (fixed frame rate, keyframe every
// GOP, no audio). It can simulate camera outages and session restarts with
// fresh RTP numbering, so instant start, stream continuity and the hold frame
// can be checked with real players (ffprobe, ffmpeg, VLC) without a lamp.
//
//	go run ./tools/stream-sim -file cam.h264 -port 8600 -outage-every 20 -outage-for 8
package main

import (
	"bytes"
	"flag"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"

	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
)

func splitAnnexB(data []byte) [][]byte {
	var nals [][]byte
	start := -1
	i := 0
	for i+3 <= len(data) {
		if data[i] == 0 && data[i+1] == 0 && (data[i+2] == 1 || (i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1)) {
			scLen := 3
			if data[i+2] == 0 {
				scLen = 4
			}
			if start >= 0 {
				nals = append(nals, bytes.TrimRight(data[start:i], "\x00"))
			}
			start = i + scLen
			i += scLen
			continue
		}
		i++
	}
	if start >= 0 && start < len(data) {
		nals = append(nals, data[start:])
	}
	return nals
}

// accessUnits groups NAL units into frames: everything up to and including a slice.
func accessUnits(nals [][]byte) [][][]byte {
	var aus [][][]byte
	var cur [][]byte
	for _, n := range nals {
		if len(n) == 0 {
			continue
		}
		cur = append(cur, n)
		if t := n[0] & 0x1F; t == 1 || t == 5 {
			aus = append(aus, cur)
			cur = nil
		}
	}
	return aus
}

func main() {
	file := flag.String("file", "", "Annex-B H.264 file")
	port := flag.Int("port", 8600, "RTSP port")
	path := flag.String("path", "steinel", "RTSP path")
	fps := flag.Int("fps", 15, "frames per second")
	instant := flag.Bool("instant", true, "instant start for new viewers")
	hold := flag.Int("hold", 300, "hold last frame for N seconds during outages (0 = off)")
	audio := flag.String("audio", "none", "audio codec of the published stream (aac|pcmu|none)")
	outageEvery := flag.Int("outage-every", 0, "simulate a camera outage every N seconds (0 = never)")
	outageFor := flag.Int("outage-for", 8, "outage duration in seconds")
	flag.Parse()

	data, err := os.ReadFile(*file)
	if err != nil {
		log.Fatalf("read %s: %v", *file, err)
	}
	aus := accessUnits(splitAnnexB(data))
	if len(aus) == 0 {
		log.Fatal("no frames found")
	}
	log.Printf("loaded %d frames", len(aus))

	srv, err := rtsp.NewServer(*port, *path, *audio, "", "")
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}
	defer srv.Close()
	st := srv.PrimaryStream()
	st.SetVideoOptions(*instant, time.Duration(*hold)*time.Second)

	newEncoder := func() (func(au [][]byte, ts uint32) []*rtp.Packet, uint32) {
		f := &format.H264{PayloadTyp: 102, PacketizationMode: 1}
		enc, err := f.CreateEncoder()
		if err != nil {
			log.Fatal(err)
		}
		return func(au [][]byte, ts uint32) []*rtp.Packet {
			pkts, err := enc.Encode(au)
			if err != nil {
				log.Fatal(err)
			}
			for _, p := range pkts {
				p.Timestamp = ts
				p.SSRC = 1
			}
			return pkts
		}, rand.Uint32()
	}

	encode, ts := newEncoder()
	frameDur := time.Second / time.Duration(*fps)
	tsStep := uint32(90000 / *fps)
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()

	sessionStart := time.Now()
	lastReport := time.Now()
	idx := 0
	for range ticker.C {
		if *outageEvery > 0 && time.Since(sessionStart) > time.Duration(*outageEvery)*time.Second {
			log.Printf("=== simulated camera outage for %d s ===", *outageFor)
			time.Sleep(time.Duration(*outageFor) * time.Second)
			// The camera returns with a new session: fresh RTP numbering, and the
			// stream resumes in the middle of a group of pictures.
			encode, ts = newEncoder()
			idx += 7
			sessionStart = time.Now()
			log.Printf("=== camera back (new RTP session) ===")
		}

		for _, p := range encode(aus[idx%len(aus)], ts) {
			st.WriteVideoPacket(p)
		}
		idx++
		ts += tsStep

		if time.Since(lastReport) > 5*time.Second {
			lastReport = time.Now()
			log.Printf("stats: %+v clients=%d", st.VideoStats(), st.ActiveClients())
		}
	}
}
