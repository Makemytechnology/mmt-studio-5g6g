// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Common transport types used by every platform build of the NGAP listener.
// The actual SCTP (Linux) / TCP stub (other) wiring is in the
// build-tagged companion files transport_linux.go / transport_stub.go.
package ngap

import (
	"context"
	"fmt"
	"strings"
)

// Listener is the accept-loop surface the AMF NGAP server talks to.
type Listener interface {
	Accept(ctx context.Context) (IncomingConn, error)
	Close() error
	LocalAddr() string
}

// IncomingConn is a single gNB SCTP association. On non-Linux dev builds it
// is backed by TCP; on Linux it will wrap an SCTP association.
type IncomingConn interface {
	// Send writes an NGAP PDU on the given stream. Stream 0 is the non-UE
	// signalling stream (TS 38.412 §7).
	Send(data []byte, stream int) error
	// Recv blocks until a full PDU arrives and returns (data, stream).
	// Returns (nil, 0, io.EOF) on clean close.
	Recv() ([]byte, int, error)
	RemoteAddr() string
	Close() error
}

// StreamQuerier is implemented by transports that know the negotiated
// outbound stream count (SCTP_STATUS on Linux). The server uses this to
// size GnbCtx.NumSCTPStreams so UEStream never picks a stream the peer
// refused to accept. TCP stub returns (0, false).
type StreamQuerier interface {
	NegotiatedOutStreams() (int, bool)
}

// ListenConfig controls the listener.
type ListenConfig struct {
	Addr           string // "host:port" (default ":38412")
	NumSCTPStreams int    // offered in SCTP INIT (default 16)
	Multihome      []string // additional local addresses for SCTP bindx
}

// DefaultListenConfig mirrors the Python reference defaults.
func DefaultListenConfig() ListenConfig {
	return ListenConfig{Addr: ":38412", NumSCTPStreams: 16}
}

// Listen returns a Listener bound to cfg.Addr. The concrete backend is
// chosen at build time: SCTP on Linux, TCP stub everywhere else.
func Listen(cfg ListenConfig) (Listener, error) {
	if cfg.Addr == "" {
		cfg.Addr = ":38412"
	}
	if cfg.NumSCTPStreams <= 0 {
		cfg.NumSCTPStreams = 16
	}
	return platformListen(cfg)
}

// ParseGnbIP extracts the IP part from a "host:port" remote-address string.
// Returns the full string if it doesn't look like host:port.
func ParseGnbIP(addr string) string {
	// Trim ipv6 brackets first so strings.LastIndex on ':' still finds the separator.
	a := addr
	if strings.HasPrefix(a, "[") {
		if close := strings.Index(a, "]"); close > 0 {
			return a[1:close]
		}
	}
	if i := strings.LastIndex(a, ":"); i >= 0 {
		return a[:i]
	}
	return a
}

// assertTCPOnly is a helper for build-tagged files that want to note when a
// specific platform is using the stub backend.
func assertTCPOnly(platform string) error {
	return fmt.Errorf("ngap: %s build uses TCP stub — see README_PORT.md for SCTP wiring", platform)
}
