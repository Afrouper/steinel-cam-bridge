package xiongmai

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Afrouper/steinel-cam-bridge/pkg/rtsp"
	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildTestH265IFrameRecord() []byte {
	// Minimal Annex-B HEVC access unit: VPS (type 32), SPS (type 33), PPS (type 34), IDR_W_RADL (type 19)
	vps := []byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0x95, 0x98, 0x09}
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5, 0x96, 0x56, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01, 0xe0, 0x80}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
	idr := []byte{0x00, 0x00, 0x00, 0x01, 0x26, 0x01, 0xaf, 0x08, 0x40, 0x50, 0x60, 0x70}

	var au []byte
	au = append(au, vps...)
	au = append(au, sps...)
	au = append(au, pps...)
	au = append(au, idr...)

	rec := make([]byte, 16+len(au))
	copy(rec[:4], MarkerIFrame)
	rec[4] = 0x13 // Xiongmai H.265 codec marker
	rec[5] = 6    // 6 FPS
	binary.LittleEndian.PutUint32(rec[12:16], uint32(len(au)))
	copy(rec[16:], au)
	return rec
}

func TestDriver_PrepareDescribe_FirstAttemptPlaysH265WithoutReconnect(t *testing.T) {
	// Allocate an ephemeral port for the RTSP server.
	probeLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	rtspPort := mustTCPPort(t, probeLn)
	_ = probeLn.Close()

	// Start a mock L 620 Sofia camera that claims H.264 in Simplify.Encode (like V4.03.R12)
	// while streaming actual H.265 video frames on OPMonitor MsgID 1412.
	sofiaLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = sofiaLn.Close() }()
	sofiaPort := mustTCPPort(t, sofiaLn)

	var (
		mediaMu         sync.Mutex
		latestMediaConn net.Conn
	)

	go func() {
		for {
			conn, acceptErr := sofiaLn.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				hdrBuf := make([]byte, HeaderLength)
				for {
					if _, rErr := io.ReadFull(c, hdrBuf); rErr != nil {
						return
					}
					hdr, dErr := DecodeHeader(hdrBuf)
					if dErr != nil {
						return
					}
					payload := make([]byte, hdr.DataLength)
					if _, rErr := io.ReadFull(c, payload); rErr != nil {
						return
					}
					clean := strings.TrimRight(string(payload), "\x00\r\n ")

					switch hdr.MsgID {
					case MsgLoginReq:
						respData, _ := json.Marshal(LoginResp{Ret: 100, SessionID: "0x0000002a"})
						respTerm := append(respData, 0x00)
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x2a, Sequence: hdr.Sequence, MsgID: MsgLoginResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgConfigGetReq:
						// Intentionally report Compression=H.264 even though OPMonitor streams H.265.
						respTerm := []byte(`{"Ret":100,"Name":"Simplify.Encode.[0]","Simplify.Encode.[0]":[{"MainFormat":{"Video":{"Compression":"H.264","FPS":6,"Resolution":"1080P"}}}]}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x2a, Sequence: hdr.Sequence, MsgID: MsgConfigGetResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorClaimReq:
						if strings.Contains(clean, `"Action1":"Start"`) {
							mediaMu.Lock()
							latestMediaConn = c
							mediaMu.Unlock()
						}
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, SessionID: 0x2a, Sequence: hdr.Sequence, MsgID: MsgMonitorClaimResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

					case MsgMonitorStartReq:
						respTerm := []byte(`{"Name":"OPMonitor","Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, Channel: hdr.Channel, SessionID: 0x2a, Sequence: hdr.Sequence, MsgID: MsgMonitorStartResp, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))

						if strings.Contains(clean, `"Action":"Start"`) {
							mediaMu.Lock()
							mc := latestMediaConn
							mediaMu.Unlock()
							if mc != nil {
								go func(mediaConn net.Conn) {
									iRec := buildTestH265IFrameRecord()
									for seq := uint32(1); seq <= 15; seq++ {
										vHdr := Header{Magic: HeaderMagic, Channel: 1, SessionID: 0x2a, Sequence: seq, MsgID: MsgMonitorData, DataLength: uint32(len(iRec))}
										if _, wErr := mediaConn.Write(append(vHdr.Encode(), iRec...)); wErr != nil {
											return
										}
										time.Sleep(40 * time.Millisecond)
									}
								}(mc)
							}
						}

					default:
						respTerm := []byte(`{"Ret":100}` + "\x00")
						rHdr := Header{Magic: HeaderMagic, Channel: hdr.Channel, SessionID: hdr.SessionID, Sequence: hdr.Sequence, MsgID: hdr.MsgID + 1, DataLength: uint32(len(respTerm))}
						_, _ = c.Write(append(rHdr.Encode(), respTerm...))
					}
				}
			}(conn)
		}
	}()

	rtspSrv, err := rtsp.NewServer(rtspPort, "steinel", "aac", "", "")
	require.NoError(t, err)
	require.NoError(t, rtspSrv.Start())
	defer rtspSrv.Close()

	// Initially before any client connects, the server defaults to H.264 with no parameters.
	assert.Equal(t, rtsp.VideoCodecH264, rtspSrv.VideoCodec())
	assert.False(t, rtspSrv.HasVideoParameters())

	d := NewDriver("127.0.0.1", "admin", "", "1080p", rtspSrv, nil)
	d.client.addr = fmt.Sprintf("127.0.0.1:%d", sofiaPort)
	d.sofiaStream.port = sofiaPort
	rtspSrv.SetOnPlayHandler(d.RequestKeyframe)
	defer func() { _ = d.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, d.Start(ctx))

	// Cold-boot ONVIF GetProfiles (Scrypted) resolves the true stream codec before RTSP DESCRIBE.
	assert.Equal(t, rtsp.VideoCodecH265, rtspSrv.ResolveVideoCodec())

	// Connect a single non-reconnecting RTSP client (simulating VLC Desktop's first click).
	u, err := base.ParseURL(fmt.Sprintf("rtsp://127.0.0.1:%d/steinel", rtspPort))
	require.NoError(t, err)

	tr := gortsplib.TransportTCP
	rc := &gortsplib.Client{Transport: &tr}
	require.NoError(t, rc.Start(u.Scheme, u.Host))
	defer rc.Close()

	desc, _, err := rc.Describe(u)
	require.NoError(t, err)

	// The VERY FIRST DESCRIBE must already announce H.265 with populated VPS/SPS/PPS.
	var h265Fmt *format.H265
	h265Media := desc.FindFormat(&h265Fmt)
	require.NotNil(t, h265Media, "very first DESCRIBE must announce H.265 instead of stale H.264")
	assert.NotEmpty(t, h265Fmt.VPS)
	assert.NotEmpty(t, h265Fmt.SPS)
	assert.NotEmpty(t, h265Fmt.PPS)
	assert.True(t, rtspSrv.HasVideoParameters())

	var receivedVideoPkts atomic.Int32
	_, err = rc.Setup(desc.BaseURL, h265Media, 0, 0)
	require.NoError(t, err)
	rc.OnPacketRTP(h265Media, h265Fmt, func(_ *rtp.Packet) {
		receivedVideoPkts.Add(1)
	})

	_, err = rc.Play(nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return receivedVideoPkts.Load() > 0
	}, 2*time.Second, 25*time.Millisecond, "first RTSP PLAY must receive H.265 RTP packets without requiring a reconnect")
}
