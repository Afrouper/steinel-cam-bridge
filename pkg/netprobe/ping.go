package netprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Prober defines the interface for testing host reachability over the IP network.
type Prober interface {
	Ping(ctx context.Context, target string, timeout time.Duration) (Result, error)
}

// Result contains diagnostic data for a completed network probe.
type Result struct {
	Reachable bool
	RTT       time.Duration
	Method    string // "icmp_cmd", "icmp_raw", "tcp_refused", "tcp_open"
}

// ICMPHeader represents an RFC 792 ICMP Echo Request/Reply header.
type ICMPHeader struct {
	Type     uint8
	Code     uint8
	Checksum uint16
	ID       uint16
	Seq      uint16
}

// CalculateChecksum computes the standard internet checksum (RFC 1071).
func CalculateChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i < len(b)-1; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for (sum >> 16) > 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// MultiStrategyProber probes host reachability using a sequence of fallback methods:
// 1. System ping utility (if available in PATH, e.g. on macOS / standard Linux)
// 2. Native unprivileged / raw ICMP in Go (works on Linux with CAP_NET_RAW / host_network)
// 3. TCP RST / SYN probe to common camera ports (80, 554, 34567, 5592)
type MultiStrategyProber struct{}

// DefaultProber is the default Prober instance used across the bridge.
var DefaultProber Prober = &MultiStrategyProber{}

// Ping tests target IP reachability with the default prober.
func Ping(ctx context.Context, target string, timeout time.Duration) (Result, error) {
	return DefaultProber.Ping(ctx, target, timeout)
}

// Ping implements Prober.
func (p *MultiStrategyProber) Ping(ctx context.Context, target string, timeout time.Duration) (Result, error) {
	trimmedTarget := strings.TrimSpace(target)
	if trimmedTarget == "" {
		return Result{Reachable: false}, errors.New("empty target IP")
	}

	// Strategy 1: System ping (best on macOS and Linux systems with ping binary)
	if pingPath, err := exec.LookPath("ping"); err == nil && pingPath != "" {
		start := time.Now()
		var cmd *exec.Cmd
		if runtime.GOOS == "darwin" {
			ms := timeout.Milliseconds()
			if ms < 500 {
				ms = 500
			}
			cmd = exec.CommandContext(ctx, pingPath, "-c", "1", "-W", fmt.Sprintf("%d", ms), trimmedTarget)
		} else {
			sec := int(timeout.Seconds())
			if sec < 1 {
				sec = 1
			}
			cmd = exec.CommandContext(ctx, pingPath, "-c", "1", "-W", fmt.Sprintf("%d", sec), trimmedTarget)
		}
		if err := cmd.Run(); err == nil {
			return Result{
				Reachable: true,
				RTT:       time.Since(start),
				Method:    "icmp_cmd",
			}, nil
		}
	}

	// Strategy 2: Native Go ICMP Echo (works on Linux Docker containers with host_network or CAP_NET_RAW)
	conn, err := net.DialTimeout("ip4:icmp", trimmedTarget, timeout)
	if err == nil {
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(timeout))

		pid := uint16(os.Getpid() & 0xffff)
		req := ICMPHeader{Type: 8, Code: 0, ID: pid, Seq: 1}
		var buf bytes.Buffer
		_ = binary.Write(&buf, binary.BigEndian, req)
		buf.WriteString("steinel-ping-probe")
		b := buf.Bytes()
		binary.BigEndian.PutUint16(b[2:4], CalculateChecksum(b))

		start := time.Now()
		if _, writeErr := conn.Write(b); writeErr == nil {
			reply := make([]byte, 1500)
			for {
				n, readErr := conn.Read(reply)
				if readErr != nil {
					break
				}
				if n >= 28 {
					icmpBytes := reply[20:n]
					// Echo Reply (Type 0) matching our PID
					if icmpBytes[0] == 0 && binary.BigEndian.Uint16(icmpBytes[4:6]) == pid {
						return Result{
							Reachable: true,
							RTT:       time.Since(start),
							Method:    "icmp_raw",
						}, nil
					}
				}
			}
		}
	}

	// Strategy 3: TCP Port probing (80, 554, 34567, 5592)
	// Even if a port is closed, an active IP stack sends TCP RST ("connection refused")
	// which unequivocally proves the host is up and answering IP packets.
	ports := []int{34567, 80, 554, 5592}
	portTimeout := timeout / time.Duration(len(ports))
	if portTimeout < 150*time.Millisecond {
		portTimeout = 150 * time.Millisecond
	}

	for _, port := range ports {
		if ctx.Err() != nil {
			break
		}
		start := time.Now()
		targetAddr := net.JoinHostPort(trimmedTarget, fmt.Sprintf("%d", port))
		tConn, tErr := net.DialTimeout("tcp", targetAddr, portTimeout)
		if tErr == nil {
			_ = tConn.Close()
			return Result{
				Reachable: true,
				RTT:       time.Since(start),
				Method:    "tcp_open",
			}, nil
		}
		if strings.Contains(strings.ToLower(tErr.Error()), "refused") {
			return Result{
				Reachable: true,
				RTT:       time.Since(start),
				Method:    "tcp_refused",
			}, nil
		}
	}

	return Result{Reachable: false}, fmt.Errorf("host %s unreachable", trimmedTarget)
}
