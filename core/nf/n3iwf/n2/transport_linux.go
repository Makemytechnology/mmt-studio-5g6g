// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
//go:build linux

// Package n2 implements the N2 reference point from the N3IWF side
// (TS 23.501 §4.2.3 — N2 carries NGAP between the (R)AN/N3IWF and the
// AMF; the NGAP protocol itself is TS 38.413).
//
// The N3IWF behaves toward the AMF as a RAN-like NGAP peer: it opens
// an SCTP association on port 38412, sends NG SETUP REQUEST identifying
// itself via GlobalRANNodeID = GlobalN3IWFID (TS 38.413 §9.3.1.5), and
// then forwards UE NAS PDUs from EAP-5G as InitialUEMessage /
// UplinkNASTransport (TS 23.502 §4.12.x).
//
// This file contains the Linux client transport. The corresponding
// listener on the AMF side lives in nf/amf/ngap/transport_linux.go;
// the wire-level conventions are shared:
//
//   - PPID 60 on every outbound DATA chunk (TS 38.412 v19.0.0 §7).
//   - Stream 0 reserved for non-UE-associated procedures (NG Setup,
//     NG Reset). UE-associated streams negotiated at NG Setup time.
//   - One SCTP one-to-one association per N3IWF↔AMF pair.
//
// SCTP support requires the kernel `sctp` module (modprobe sctp).
// Without it the client falls back to TCP/38412 so unit tests on
// dev hosts without lksctp still drive the transport — same fallback
// policy as the AMF listener.
package n2

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"unsafe"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// SCTP constants — same set as nf/amf/ngap/transport_linux.go but
// re-stated here so the n2 package is self-contained and doesn't pull
// the AMF transport.
const (
	sctpSOL      = 132 // IPPROTO_SCTP
	sctpPPIDNGAP = 60  // TS 38.412 §7
	sctpSNDRCV   = 1   // SCTP_SNDRCV cmsg
	sctpDefPort  = 38412

	sctpMsgNotification = 0x8000
)

// Conn is the N3IWF-side view of the SCTP association to the AMF.
// Send and Recv are stream-aware so the caller can place common-NGAP
// procedures (NG Setup, NG Reset) on stream 0 and UE-associated
// procedures on dedicated streams per TS 38.412 §7.
type Conn interface {
	Send(data []byte, stream int) error
	Recv() (data []byte, stream int, err error)
	RemoteAddr() string
	Close() error
}

// DialConfig parameterises the N3IWF→AMF connect.
type DialConfig struct {
	// AMF SCTP endpoint. Host may be "ip:port"; if no port, 38412
	// is assumed.
	AMFAddr string

	// LocalIP optionally pins the source IP. Empty = let the kernel
	// pick (route table). Useful when the operator declares a
	// dedicated N2 interface separate from the management plane.
	LocalIP string
}

// Dial opens an SCTP one-to-one association to the AMF. Returns a
// concrete client conn ready for Send/Recv. On hosts without SCTP it
// falls back to TCP so tests can still run end-to-end.
func Dial(ctx context.Context, cfg DialConfig) (Conn, error) {
	log := logger.Get("n3iwf.n2.transport.linux")
	host, port, err := splitAMFAddr(cfg.AMFAddr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		// Resolve hostname → first A record.
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("n2: resolve %q: %w", host, err)
		}
		ip = ips[0].To4()
		if ip == nil {
			return nil, fmt.Errorf("n2: no IPv4 for %q", host)
		}
	}

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, sctpSOL)
	if err != nil {
		log.Warnf("SCTP socket failed (%v) — falling back to TCP. Load the sctp module: modprobe sctp", err)
		return tcpDial(ctx, ip, port)
	}
	syscall.CloseOnExec(fd)

	// Optional source-IP bind.
	if cfg.LocalIP != "" {
		lip := net.ParseIP(cfg.LocalIP).To4()
		if lip == nil {
			syscall.Close(fd)
			return nil, fmt.Errorf("n2: invalid LocalIP %q", cfg.LocalIP)
		}
		la := &syscall.SockaddrInet4{}
		copy(la.Addr[:], lip)
		if err := syscall.Bind(fd, la); err != nil {
			syscall.Close(fd)
			return nil, fmt.Errorf("n2: bind %s: %w", cfg.LocalIP, err)
		}
	}

	// Connect — blocking. ctx cancellation is racy on a blocking
	// connect; in practice 38412 either accepts in <1s on a healthy
	// AMF or fails fast with ECONNREFUSED, so we don't bother with a
	// non-blocking + epoll dance here. If the user wants stricter
	// timeouts they can ctx.WithTimeout outside and we honour it via
	// SetSockOpt SO_SNDTIMEO. (Future improvement.)
	ra := &syscall.SockaddrInet4{Port: port}
	copy(ra.Addr[:], ip)
	if err := syscall.Connect(fd, ra); err != nil {
		syscall.Close(fd)
		log.Warnf("SCTP connect to %s:%d failed (%v) — falling back to TCP",
			ip, port, err)
		return tcpDial(ctx, ip, port)
	}

	remote := fmt.Sprintf("%d.%d.%d.%d:%d", ip[0], ip[1], ip[2], ip[3], port)
	log.Infof("N2 SCTP connected to %s (real kernel SCTP, PPID=%d)", remote, sctpPPIDNGAP)
	return &sctpConn{fd: fd, remote: remote, log: log}, nil
}

// splitAMFAddr accepts "host", "host:port", or "ip:port" and returns
// the host and a port (defaulting to 38412 if absent).
func splitAMFAddr(s string) (string, int, error) {
	if s == "" {
		return "", 0, errors.New("n2: AMFAddr empty")
	}
	host, portS, err := net.SplitHostPort(s)
	if err != nil {
		// No port → assume 38412.
		return s, sctpDefPort, nil
	}
	if portS == "" {
		return host, sctpDefPort, nil
	}
	var p int
	if _, err := fmt.Sscanf(portS, "%d", &p); err != nil || p <= 0 || p > 65535 {
		return "", 0, fmt.Errorf("n2: invalid port %q", portS)
	}
	return host, p, nil
}

type sctpConn struct {
	fd     int
	remote string
	log    *logger.Logger
	mu     sync.Mutex
}

// Send writes an NGAP PDU on the given stream with PPID=60 per
// TS 38.412 §7. A plain syscall.Write would emit PPID=0 on the DATA
// chunk and the AMF would ABORT the association (RFC 4960 §3.3.1).
func (c *sctpConn) Send(data []byte, stream int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return sctpSendMsg(c.fd, data, stream, sctpPPIDNGAP)
}

// Recv blocks until the next NGAP DATA chunk arrives. SCTP
// notifications (state changes, send failures) are silently consumed
// — the N3IWF doesn't yet drive an SCTP-FSM separate from the
// association lifecycle. Inbound PPID is checked: anything ≠ 60 is
// dropped + WARN'd, mirroring the AMF listener policy so the dispatch
// layer sees only valid NGAP PDUs.
func (c *sctpConn) Recv() ([]byte, int, error) {
	buf := make([]byte, 65536)
	oob := make([]byte, 256)
	for {
		n, oobn, recvflags, _, err := syscall.Recvmsg(c.fd, buf, oob, 0)
		if err != nil {
			return nil, 0, err
		}
		if n == 0 {
			return nil, 0, io.EOF
		}
		if recvflags&sctpMsgNotification != 0 {
			// Skip notifications silently for now. A future SCTP-FSM
			// would dispatch SHUTDOWN_EVENT etc. here.
			continue
		}
		stream, ppid, haveCmsg := parseSCTPStreamPPIDCmsg(oob[:oobn])
		if !haveCmsg {
			c.log.Debugf("N2 RX from %s: no SCTP_SNDRCV cmsg — PPID unchecked", c.remote)
			return buf[:n], stream, nil
		}
		if ppid != sctpPPIDNGAP {
			c.log.Warnf("N2 RX dropped from %s: PPID=%d != 60 (TS 38.412 §7) stream=%d size=%d",
				c.remote, ppid, stream, n)
			continue
		}
		return buf[:n], stream, nil
	}
}

func (c *sctpConn) RemoteAddr() string { return c.remote }

func (c *sctpConn) Close() error {
	// Same Linux quirk as the AMF listener: shutdown(SHUT_RDWR)
	// wakes a thread parked in Recvmsg so the read goroutine can
	// exit promptly. close(2) alone keeps the fd alive until the
	// recv finishes naturally.
	_ = syscall.Shutdown(c.fd, syscall.SHUT_RDWR)
	return syscall.Close(c.fd)
}

// sctpSendMsg sends `data` on `stream` with `ppid` via sendmsg + an
// SCTP_SNDRCV cmsg. The kernel ABI for sctp_sndrcvinfo: most fields
// native-endian, but PPID is network-byte-order.
func sctpSendMsg(fd int, data []byte, stream, ppid int) error {
	if len(data) == 0 {
		return errors.New("n2: empty NGAP PDU")
	}
	var info [32]byte
	binary.LittleEndian.PutUint16(info[0:2], uint16(stream))
	binary.BigEndian.PutUint32(info[8:12], uint32(ppid))

	cmsgLen := syscall.CmsgLen(len(info))
	cmsg := make([]byte, cmsgLen)
	hdr := (*syscall.Cmsghdr)(unsafe.Pointer(&cmsg[0]))
	hdr.Level = sctpSOL
	hdr.Type = sctpSNDRCV
	hdr.SetLen(cmsgLen)
	copy(cmsg[syscall.CmsgLen(0):], info[:])

	iov := syscall.Iovec{Base: &data[0], Len: uint64(len(data))}
	msg := syscall.Msghdr{
		Iov:        &iov,
		Iovlen:     1,
		Control:    &cmsg[0],
		Controllen: uint64(len(cmsg)),
	}
	_, _, errno := syscall.Syscall(syscall.SYS_SENDMSG, uintptr(fd),
		uintptr(unsafe.Pointer(&msg)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// parseSCTPStreamPPIDCmsg walks the ancillary buffer from recvmsg and
// extracts the SCTP_SNDRCV cmsg's stream and ppid. Returns haveCmsg=
// false when the kernel didn't deliver one (SCTP_EVENTS not subscribed
// at connect time on some old kernels) — in that case PPID can't be
// enforced and the caller logs at Debug.
func parseSCTPStreamPPIDCmsg(oob []byte) (stream, ppid int, haveCmsg bool) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return 0, 0, false
	}
	for _, m := range msgs {
		if m.Header.Level != sctpSOL || m.Header.Type != sctpSNDRCV {
			continue
		}
		if len(m.Data) < 12 {
			return 0, 0, false
		}
		stream = int(binary.LittleEndian.Uint16(m.Data[0:2]))
		ppid = int(binary.BigEndian.Uint32(m.Data[8:12]))
		return stream, ppid, true
	}
	return 0, 0, false
}

// tcpDial is the dev-host fallback used when the kernel sctp module
// isn't loaded. It exposes the same Conn surface as sctpConn so the
// rest of the N3IWF doesn't care which transport it got.
//
// Important: TCP has no streams and no PPID. Recv ignores PPID checks
// and returns stream=0; Send ignores the stream argument. Strict-mode
// integration tests should use the SCTP path.
func tcpDial(ctx context.Context, ip net.IP, port int) (Conn, error) {
	d := net.Dialer{}
	c, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%d.%d.%d.%d:%d",
		ip[0], ip[1], ip[2], ip[3], port))
	if err != nil {
		return nil, fmt.Errorf("n2: tcp fallback dial: %w", err)
	}
	return &tcpConn{c: c, log: logger.Get("n3iwf.n2.transport.tcp-fallback")}, nil
}

type tcpConn struct {
	c   net.Conn
	log *logger.Logger
	mu  sync.Mutex
}

// Send length-prefixes the PDU with a 4-byte BE length so the
// receiver can frame it. Real SCTP does framing in the kernel; on
// TCP we have to do it ourselves or the receiver has no way to know
// where one PDU ends.
func (c *tcpConn) Send(data []byte, stream int) error {
	_ = stream
	c.mu.Lock()
	defer c.mu.Unlock()
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(data)))
	if _, err := c.c.Write(hdr); err != nil {
		return err
	}
	_, err := c.c.Write(data)
	return err
}

func (c *tcpConn) Recv() ([]byte, int, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c.c, hdr); err != nil {
		return nil, 0, err
	}
	n := binary.BigEndian.Uint32(hdr)
	if n > 65536 {
		return nil, 0, fmt.Errorf("n2: tcp framed PDU too large: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.c, buf); err != nil {
		return nil, 0, err
	}
	return buf, 0, nil
}

func (c *tcpConn) RemoteAddr() string { return c.c.RemoteAddr().String() }
func (c *tcpConn) Close() error       { return c.c.Close() }
