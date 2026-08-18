// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
//go:build linux

package ngap

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"unsafe"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// SCTP constants from the Linux kernel (lksctp-dev headers).
const (
	sctpSOL       = 132 // IPPROTO_SCTP
	sctpBindxAdd  = 0x6001
	sctpPPIDNGAP  = 60
	sctpPort      = 38412
	sctpInitMsg   = 2   // SCTP_INITMSG socket option (SOL_SCTP).
	sctpRtoInfo   = 0   // SCTP_RTOINFO — per-association RTO params (initial/min/max ms).
	sctpAssocInfo = 1   // SCTP_ASSOCINFO — per-association paths/peer params (path_max_retrans).
	sctpEvents    = 11  // SCTP_EVENTS — subscribe to SCTP events + DATA_IO cmsg.
	sctpStatus    = 14  // SCTP_STATUS — read association status incl. negotiated streams.
	sctpDefaultSendParam = 10 // SCTP_DEFAULT_SEND_PARAM — default sctp_sndrcvinfo for sends without cmsg.
	sctpSNDRCV    = 1   // SCTP_SNDRCV cmsg type (ancillary on sendmsg/recvmsg).

	// MSG_NOTIFICATION — flagged in recvmsg's recvflags when the
	// payload is an SCTP notification (<linux/socket.h>, RFC 6458 §6.1).
	sctpMsgNotification = 0x8000
)

// platformListen returns a real SCTP one-to-one listener on Linux.
// Uses raw syscalls — no external Go SCTP library needed. The kernel
// SCTP module (lksctp) must be loaded: `modprobe sctp`.
//
// If SCTP is not available (module not loaded, or Docker without
// --privileged), falls back to the TCP stub so the binary still starts.
func platformListen(cfg ListenConfig) (Listener, error) {
	log := logger.Get("amf.ngap.transport.linux")

	// Try SCTP first.
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, sctpSOL)
	if err != nil {
		log.Warnf("SCTP socket failed (%v) — falling back to TCP stub. Load sctp module: modprobe sctp", err)
		return tcpListen(cfg)
	}
	syscall.CloseOnExec(fd)
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)

	// Operator-tunable SCTP values from infra_config — empty row means
	// "use the hardcoded defaults". Every field read via a 0-safe
	// helper so missing columns (older DB) fall back cleanly.
	sctpCfg := loadSCTPConfig()

	// TS 38.412 v19.0.0 §7 — SCTP stream reservation. Verbatim:
	//   "A single pair of stream identifiers shall be reserved over
	//    at least one SCTP association for the sole use of NGAP
	//    elementary procedures that utilize non UE-associated
	//    signalling.
	//    At least one pair of stream identifiers over one or several
	//    SCTP associations shall be reserved for the sole use of NGAP
	//    elementary procedures that utilize UE-associated signallings.
	//    However, a few pairs (i.e. more than one) should be reserved."
	//
	// Implementation policy mapping those requirements onto stream IDs:
	//   stream 0             — "common" NGAP (NG Setup, NG Reset,
	//                           Error Indication, Paging broadcasts).
	//                           Reserved pair size = 1+1 = 2 per
	//                           reference convention (kernel treats
	//                           each stream as send+recv).
	//   streams 1..(N-1)     — "dedicated" UE-associated pool. The
	//                           UE→stream hash in gnbctx.UEStream spreads
	//                           UEs over this pool so per-UE SCTP
	//                           ordering holds and different UEs don't
	//                           head-of-line-block each other.
	//
	// Minimum N=2 (1 common + 1 dedicated). Python reference defaults
	// to 16; operator-tunable via infra_config.sctp_num_streams.
	// struct sctp_initmsg { u16 num_ostreams, max_instreams, max_attempts, max_init_timeo }
	want := uint16(cfg.NumSCTPStreams)
	if sctpCfg.NumStreams > 0 {
		want = uint16(sctpCfg.NumStreams)
	}
	if want < 2 {
		// Spec floor is 2; 16 is the reference-implementation default.
		want = 16
	}
	var initmsg [8]byte
	binary.LittleEndian.PutUint16(initmsg[0:2], want) // num_ostreams
	binary.LittleEndian.PutUint16(initmsg[2:4], want) // max_instreams
	// leave max_attempts + max_init_timeo as 0 → kernel defaults
	_, _, errno := syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(fd),
		uintptr(sctpSOL), uintptr(sctpInitMsg),
		uintptr(unsafe.Pointer(&initmsg[0])), uintptr(len(initmsg)), 0)
	if errno != 0 {
		log.Warnf("SCTP_INITMSG set failed (%v) — falling back to kernel-default stream count", errno)
	}

	// Tune SCTP retransmit so a briefly slow peer doesn't trip the
	// local ABORT path. Values come from infra_config when present,
	// else the hardcoded defaults (3s / 120s / 1s) that ran stably
	// through the 16-UE bursts.
	//
	// struct sctp_rtoinfo { sctp_assoc_t assoc_id (4); __u32 initial;
	//                       __u32 max; __u32 min; } — 16 bytes.
	var rto [16]byte
	binary.LittleEndian.PutUint32(rto[4:8], nonZero(sctpCfg.RTOInitialMs, 3000))
	binary.LittleEndian.PutUint32(rto[8:12], nonZero(sctpCfg.RTOMaxMs, 120000))
	binary.LittleEndian.PutUint32(rto[12:16], nonZero(sctpCfg.RTOMinMs, 1000))
	_, _, _ = syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(fd),
		uintptr(sctpSOL), uintptr(sctpRtoInfo),
		uintptr(unsafe.Pointer(&rto[0])), uintptr(len(rto)), 0)

	// struct sctp_assocparams { sctp_assoc_t (4); __u16 asocmaxrxt (2);
	//    __u16 number_peer_destinations (2); __u32 peer_rwnd (4);
	//    __u32 local_rwnd (4); __u32 cookie_life (4); } — 20 bytes.
	// asocmaxrxt defaults to 10 retrans before ABORT; raise it so a
	// short pause on the peer side doesn't tear the association down.
	var ap [20]byte
	asocmaxrxt := uint16(nonZero(sctpCfg.AssocMaxRetrans, 30))
	binary.LittleEndian.PutUint16(ap[4:6], asocmaxrxt)
	_, _, _ = syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(fd),
		uintptr(sctpSOL), uintptr(sctpAssocInfo),
		uintptr(unsafe.Pointer(&ap[0])), uintptr(len(ap)), 0)

	// Bigger SO_SNDBUF/RCVBUF so bursts of 16+ UE signalling messages
	// don't saturate the socket buffer, which itself can trigger the
	// same back-pressure / ABORT cascade.
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, 4<<20)
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4<<20)

	// Subscribe to SCTP DATA-IO events so recvmsg delivers the
	// SCTP_SNDRCV cmsg on every received message. Without this, the
	// kernel drops the per-message metadata and we can't tell which
	// stream a bundled DATA chunk arrived on — which matters because
	// TS 38.412 §7 maps each UE to its own SCTP stream (stream 0 for
	// non-UE signalling).
	//
	// SCTP_EVENTS subscription — struct sctp_event_subscribe
	// (<linux/sctp.h>). One __u8 flag per notification type; offsets:
	//
	//   0: sctp_data_io_event        — delivers SCTP_SNDRCV cmsg per message
	//   1: sctp_association_event    — COMM_UP / COMM_LOST / RESTART / SHUTDOWN_COMP / CANT_STR_ASSOC
	//   2: sctp_address_event        — peer-addr state changes (multi-homing paths)
	//   3: sctp_send_failure_event   — outbound DATA that didn't make it
	//   4: sctp_peer_error_event     — OP-ERROR chunks from peer
	//   5: sctp_shutdown_event       — peer sent SHUTDOWN
	//   6: sctp_partial_delivery_event
	//   7: sctp_adaptation_layer_event
	//   8: sctp_authentication_event
	//   9: sctp_sender_dry_event
	//  10: sctp_stream_reset_event
	//
	// Everything except the last five is needed for the SCTP FSM to
	// drive itself from kernel-delivered state changes instead of
	// guessing from syscall errors. Notifications arrive mixed into
	// recvmsg with MSG_NOTIFICATION set in msg_flags; sctpConn.Recv
	// routes them to the FSM and returns nothing to the NGAP stream.
	var ev [11]byte
	ev[0] = 1 // data_io
	ev[1] = 1 // association_change
	ev[2] = 1 // peer_addr_change
	ev[3] = 1 // send_failed
	ev[4] = 1 // peer_error
	ev[5] = 1 // shutdown
	_, _, errno = syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(fd),
		uintptr(sctpSOL), uintptr(sctpEvents),
		uintptr(unsafe.Pointer(&ev[0])), uintptr(len(ev)), 0)
	if errno != 0 {
		log.Warnf("SCTP_EVENTS set failed (%v) — SCTP state FSM + per-stream dispatch both disabled", errno)
	}

	// TS 38.412 §7 — default sinfo_ppid for sends that don't attach a
	// SCTP_SNDRCV cmsg. Every current send path does attach one
	// (sctpConn.Send routes through sctp_sendmsg), but setting the
	// default is cheap belt-and-braces: any future code path that
	// reaches write() still emits DATA chunks with PPID=60 rather
	// than 0. Same struct layout as sctpSendMsg (32 bytes; PPID at
	// offset 8, big-endian for over-the-wire carriage).
	var dfl [32]byte
	binary.BigEndian.PutUint32(dfl[8:12], uint32(sctpPPIDNGAP))
	_, _, errno = syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(fd),
		uintptr(sctpSOL), uintptr(sctpDefaultSendParam),
		uintptr(unsafe.Pointer(&dfl[0])), uintptr(len(dfl)), 0)
	if errno != 0 {
		log.Warnf("SCTP_DEFAULT_SEND_PARAM set failed (%v) — stray write() paths would ship PPID=0", errno)
	}

	// Parse addr.
	host, port, err := parseAddr(cfg.Addr)
	if err != nil {
		syscall.Close(fd)
		return nil, err
	}

	// Binding to 0.0.0.0 is a multi-homing hazard on SCTP: the Linux
	// kernel auto-discovers every local IPv4 and advertises them all in
	// the INIT-ACK's Address Parameter list (RFC 4960 §5.1.2). Peers
	// then treat docker0 (172.17.x.x), TUN interfaces (UPF UE-pool
	// gateway like 10.45.0.1), and link-local addresses as valid
	// secondary paths and heartbeat them. The HBs blackhole through
	// TUN, the kernel marks the paths FAILED, and eventually decides
	// the association is dead even though the primary path is fine.
	// Observable symptom: ECONNRESET on the AMF side with no NGAP-level
	// error, ~asocmaxrxt × T3_init seconds into the session.
	//
	// When the operator asks for wildcard bind, auto-select the single
	// most-likely external IPv4 (non-loopback, non-docker, non-TUN)
	// and single-home onto it. Explicit host (e.g. --ngap-addr
	// 192.168.1.107:38412) is respected as-is; operator takes
	// ownership of the address choice.
	bindHost := host
	if host == "0.0.0.0" || host == "" {
		picked, all := pickPrimaryIPv4()
		if picked == "" {
			log.Warnf("SCTP wildcard bind requested but no suitable IP found — falling back to 0.0.0.0 (INIT-ACK will advertise: %v)", all)
		} else {
			log.Infof("SCTP single-homed: auto-selected %s (skipped docker/TUN/loopback: %v) — pass --ngap-addr <IP>:%d to override",
				picked, excludeFromList(all, picked), port)
			bindHost = picked
		}
	}

	sa := &syscall.SockaddrInet4{Port: port}
	copy(sa.Addr[:], net.ParseIP(bindHost).To4())
	if err := syscall.Bind(fd, sa); err != nil {
		syscall.Close(fd)
		log.Warnf("SCTP bind %s failed (%v) — falling back to TCP stub", cfg.Addr, err)
		return tcpListen(cfg)
	}

	// TS 38.412 §7 / RFC 4960 §5.1.2 — SCTP multi-homing.
	//   "Transport network redundancy can be achieved by SCTP
	//    multi-homing between two end-points, of which one or both
	//    is assigned with multiple IP addresses. SCTP end-points
	//    shall support a multi-homed remote SCTP end-point."
	// Add any operator-declared secondary local IPv4s via
	// SCTP_SOCKOPT_BINDX_ADD (0x6001) so peers receive them in the
	// INIT-ACK / ASCONF Address Parameter list and can fail over.
	// Single-homed deployments pass no entries and this path is a
	// no-op.
	if len(cfg.Multihome) > 0 {
		if err := sctpBindx(fd, cfg.Multihome, port); err != nil {
			log.Warnf("SCTP_BINDX_ADD failed for %v (%v) — association stays single-homed on %s",
				cfg.Multihome, err, bindHost)
		} else {
			log.Infof("SCTP multi-homed on primary=%s + %v (port=%d)", bindHost, cfg.Multihome, port)
		}
	}

	if err := syscall.Listen(fd, 16); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("sctp listen: %w", err)
	}

	boundAddr := fmt.Sprintf("%s:%d", bindHost, port)
	log.Infof("NGAP SCTP listening on %s (real kernel SCTP, streams=%d, PPID=%d)",
		boundAddr, cfg.NumSCTPStreams, sctpPPIDNGAP)
	return &sctpListener{fd: fd, addr: boundAddr, log: log}, nil
}

// sctpBindx binds additional IPv4 addresses to an already-bound SCTP
// socket, mirroring the userspace sctp_bindx() helper via a raw
// setsockopt. Used for multi-homing on the listener side per
// TS 38.412 §7 / RFC 4960 §5.1.2.
//
// The kernel expects a packed array of sockaddr_in entries (one per
// address) with the same port. Address family fits into the first two
// octets of each struct sockaddr.
func sctpBindx(fd int, addrs []string, port int) error {
	// Pack: [ addr1 struct sockaddr_in | addr2 ... ]. Each in4 struct
	// is 16 bytes (sin_family:2, sin_port:2, sin_addr:4, zero-pad:8).
	const inSize = 16
	buf := make([]byte, 0, inSize*len(addrs))
	for _, a := range addrs {
		ip := net.ParseIP(a).To4()
		if ip == nil {
			return fmt.Errorf("bindx: invalid IPv4 %q", a)
		}
		entry := make([]byte, inSize)
		binary.LittleEndian.PutUint16(entry[0:2], uint16(syscall.AF_INET))
		binary.BigEndian.PutUint16(entry[2:4], uint16(port))
		copy(entry[4:8], ip)
		// Bytes 8..15 stay zero per sockaddr_in padding.
		buf = append(buf, entry...)
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(fd),
		uintptr(sctpSOL), uintptr(sctpBindxAdd),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// sctpListener wraps a raw SCTP socket fd.
type sctpListener struct {
	fd   int
	addr string
	log  *logger.Logger
}

func (l *sctpListener) Accept(ctx context.Context) (IncomingConn, error) {
	done := make(chan struct{})
	var nfd int
	var sa syscall.Sockaddr
	var err error
	go func() {
		nfd, sa, err = syscall.Accept(l.fd)
		close(done)
	}()
	select {
	case <-ctx.Done():
		// Linux quirk: close(2) on a listening socket does NOT interrupt
		// another thread blocked in accept(2). The blocked thread sits
		// in the kernel until a connection arrives or it's signalled.
		// At shutdown that means our acceptLoop wedges on s.wg.Wait()
		// and lifecycle's per-step budget (15 s) fires every time the
		// process is asked to stop — see infra/lifecycle/lifecycle.go.
		//
		// shutdown(SHUT_RDWR) DOES wake up blocked accepts on Linux:
		// the kernel marks the socket as no-longer-receiving, all
		// pending accepts return EINVAL, the inner goroutine closes
		// `done`, and we proceed.
		//
		// Errors from Shutdown are best-effort (EBADF if the fd was
		// already closed, ENOTCONN if no peer ever connected — both
		// harmless here) so we ignore them. The actual fd close still
		// happens below to free the kernel resource.
		_ = syscall.Shutdown(l.fd, syscall.SHUT_RDWR)
		<-done
		syscall.Close(l.fd)
		return nil, ctx.Err()
	case <-done:
	}
	if err != nil {
		return nil, err
	}
	remote := "unknown"
	if sa4, ok := sa.(*syscall.SockaddrInet4); ok {
		remote = fmt.Sprintf("%d.%d.%d.%d:%d",
			sa4.Addr[0], sa4.Addr[1], sa4.Addr[2], sa4.Addr[3], sa4.Port)
	}
	return &sctpConn{fd: nfd, remote: remote}, nil
}

// Close shuts the listening socket down and frees the fd. The
// shutdown(SHUT_RDWR) is what actually wakes any thread currently
// blocked in syscall.Accept on this fd — close(fd) by itself does
// not, on Linux. Belt-and-braces: callers may invoke this directly
// (Server.Stop does), or the Accept ctx-cancel branch may fire first;
// either path correctly unblocks the accept goroutine.
func (l *sctpListener) Close() error {
	_ = syscall.Shutdown(l.fd, syscall.SHUT_RDWR)
	return syscall.Close(l.fd)
}
func (l *sctpListener) LocalAddr() string { return l.addr }

// sctpConn wraps a connected SCTP association fd.
type sctpConn struct {
	fd     int
	remote string
	mu     sync.Mutex
}

func (c *sctpConn) Send(data []byte, stream int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// TS 38.412 §7: every NGAP PDU — including stream-0 non-UE
	// signalling (NG Setup, AMF Config Update, NG Reset) — MUST be
	// sent with PPID=60. A plain syscall.Write() leaves PPID=0 on the
	// DATA chunk, which strict peers treat as unknown-PPID and ABORT
	// the association (RFC 4960 §3.3.1). Always route through
	// sctp_sendmsg so sinfo_ppid is carried.
	return sctpSendMsg(c.fd, data, stream, sctpPPIDNGAP)
}

func (c *sctpConn) Recv() ([]byte, int, error) {
	// recvmsg delivers either a DATA payload (common) or an SCTP
	// notification (rare; state changes, send failures, peer address
	// updates). Notifications are flagged MSG_NOTIFICATION in the
	// recvflags return value and are routed to the SCTP FSM via
	// parseSCTPNotification; the caller then sees an empty buffer so
	// nothing is handed to the NGAP dispatcher.
	buf := make([]byte, 65536)
	oob := make([]byte, 256)
	log := logger.Get("amf.ngap.transport.linux")
	for {
		n, oobn, recvflags, _, err := syscall.Recvmsg(c.fd, buf, oob, 0)
		if err != nil {
			return nil, 0, err
		}
		if n == 0 {
			return nil, 0, io.EOF
		}
		// msgNotification = 0x8000 on Linux (<asm-generic/socket.h>).
		if recvflags&sctpMsgNotification != 0 {
			handled := parseSCTPNotification(c.remoteIP(), buf[:n])
			if !handled {
				// Unrecognised notification type — skip and keep reading.
			}
			continue
		}
		stream, ppid, haveCmsg := parseSCTPStreamPPIDCmsg(oob[:oobn])
		// TS 38.412 §7 — inbound NGAP DATA chunks MUST carry PPID=60.
		// Peer sending something else is either mis-configured (common
		// with home-grown testers that forget to set sinfo_ppid) or
		// forwarding a non-NGAP protocol on our listener. Either way
		// we drop + WARN: feeding a non-NGAP PDU to the dispatcher
		// would surface as a confusing APER decode error and hide the
		// real configuration bug.
		//
		// If the kernel didn't deliver the SCTP_SNDRCV cmsg at all
		// (SCTP_EVENTS subscription failed at listen time), we can't
		// enforce PPID — log once at Debug and let the PDU through so
		// the service degrades gracefully rather than silently dropping
		// every inbound PDU.
		if !haveCmsg {
			log.Debugf("NGAP RX from %s: no SCTP_SNDRCV cmsg — PPID unchecked", c.remote)
			return buf[:n], stream, nil
		}
		if ppid != sctpPPIDNGAP {
			log.Warnf("NGAP RX dropped from %s: PPID=%d != 60 (TS 38.412 §7) stream=%d size=%d",
				c.remote, ppid, stream, n)
			continue
		}
		return buf[:n], stream, nil
	}
}

// remoteIP returns just the IP portion of the "ip:port" remote string
// so SCTP-FSM keys are stable across reconnects on different ephemeral
// ports.
func (c *sctpConn) remoteIP() string {
	for i := len(c.remote) - 1; i >= 0; i-- {
		if c.remote[i] == ':' {
			return c.remote[:i]
		}
	}
	return c.remote
}

// parseSCTPStreamPPIDCmsg walks the ancillary-data buffer returned by
// recvmsg and extracts sinfo_stream + sinfo_ppid from the first
// SCTP_SNDRCV cmsg. The `haveCmsg` return distinguishes "kernel never
// delivered the cmsg" (→ false; caller can't enforce PPID) from
// "cmsg present, PPID=0 deliberately set by peer" (→ true with ppid=0;
// caller treats as a spec violation).
//
// PPID is carried network-byte-order on the wire AND in the cmsg
// (sctp_sendmsg puts it there; recvmsg delivers it unchanged). The
// other sctp_sndrcvinfo fields are native-endian. Sucks but that's
// the kernel ABI.
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
			continue
		}
		// struct sctp_sndrcvinfo layout — see comment above sctpSendMsg.
		stream = int(binary.LittleEndian.Uint16(m.Data[0:2]))
		ppid = int(binary.BigEndian.Uint32(m.Data[8:12]))
		return stream, ppid, true
	}
	return 0, 0, false
}

func (c *sctpConn) RemoteAddr() string { return c.remote }

// Close tears the association down so any goroutine parked in
// syscall.Recvmsg on this fd unblocks promptly. Same Linux quirk as
// sctpListener.Close (see lines 320-340): close(2) by itself does not
// wake another thread blocked in a recv on the same fd — the kernel
// keeps the fd alive until the recv finishes naturally, which under
// Server.Stop means s.wg.Wait() sits there until the lifecycle
// per-step budget (15 s) fires. shutdown(SHUT_RDWR) makes the kernel
// fault every blocked read with EOF/error, the goroutine returns,
// and only then do we close(fd) to free the descriptor.
//
// Errors from Shutdown are best-effort (EBADF if the fd was already
// closed, ENOTCONN on a torn association — both harmless here).
func (c *sctpConn) Close() error {
	_ = syscall.Shutdown(c.fd, syscall.SHUT_RDWR)
	return syscall.Close(c.fd)
}

// SCTPStatus returns the runtime association stats the kernel keeps
// (RFC 6458 §8.2.1 sctp_status). Operator /api/amf/ngap/sctp reads
// these so rwnd / unackdata / penddata are visible live.
type SCTPStatus struct {
	AssocID            int32  `json:"assoc_id"`
	State              int32  `json:"kernel_state"`
	RWND               uint32 `json:"rwnd"`
	UnackData          uint16 `json:"unackdata"`
	PendData           uint16 `json:"penddata"`
	InStreams          uint16 `json:"in_streams"`
	OutStreams         uint16 `json:"out_streams"`
	FragmentationPoint uint32 `json:"fragmentation_point"`
}

// Status returns the current sctp_status struct for this connection,
// or (nil, false) when the kernel reports the association as gone.
func (c *sctpConn) Status() (*SCTPStatus, bool) {
	const statusSize = 160
	buf := make([]byte, statusSize)
	optlen := uint32(statusSize)
	_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(c.fd),
		uintptr(sctpSOL), uintptr(sctpStatus),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&optlen)), 0)
	if errno != 0 || optlen < 24 {
		return nil, false
	}
	return &SCTPStatus{
		AssocID:            int32(binary.LittleEndian.Uint32(buf[0:4])),
		State:              int32(binary.LittleEndian.Uint32(buf[4:8])),
		RWND:               binary.LittleEndian.Uint32(buf[8:12]),
		UnackData:          binary.LittleEndian.Uint16(buf[12:14]),
		PendData:           binary.LittleEndian.Uint16(buf[14:16]),
		InStreams:          binary.LittleEndian.Uint16(buf[16:18]),
		OutStreams:         binary.LittleEndian.Uint16(buf[18:20]),
		FragmentationPoint: binary.LittleEndian.Uint32(buf[20:24]),
	}, true
}

// NegotiatedOutStreams reads SCTP_STATUS to get the association's real
// outbound stream count (min of our SCTP_INITMSG and the peer's advertised
// max_instreams). Returns (n, true) on success, (0, false) on error —
// callers should then keep their conservative default.
//
// struct sctp_status (first 20 bytes we need):
//
//	sctp_assoc_t sstat_assoc_id;    // s32
//	__s32        sstat_state;
//	__u32        sstat_rwnd;
//	__u16        sstat_unackdata;
//	__u16        sstat_penddata;
//	__u16        sstat_instrms;
//	__u16        sstat_outstrms;     // ← this one
//	__u32        sstat_fragmentation_point;
//	... sstat_primary (sctp_paddrinfo) follows — we only need the first 20 bytes.
func (c *sctpConn) NegotiatedOutStreams() (int, bool) {
	const statusSize = 160 // sizeof(struct sctp_status) on typical Linux — over-allocate is fine
	buf := make([]byte, statusSize)
	optlen := uint32(statusSize)
	_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(c.fd),
		uintptr(sctpSOL), uintptr(sctpStatus),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&optlen)), 0)
	if errno != 0 {
		logger.Get("amf.ngap.transport.linux").
			Debugf("SCTP_STATUS getsockopt fd=%d: errno=%d (%v)", c.fd, int(errno), errno)
		return 0, false
	}
	if optlen < 20 {
		logger.Get("amf.ngap.transport.linux").
			Debugf("SCTP_STATUS getsockopt fd=%d: short response optlen=%d", c.fd, optlen)
		return 0, false
	}
	// sstat_outstrms sits at offset 18 (see layout above).
	out := binary.LittleEndian.Uint16(buf[18:20])
	if out == 0 {
		return 0, false
	}
	return int(out), true
}

// sctpSendMsg sends data on a specific SCTP stream using sendmsg syscall
// with SCTP_SNDRCV ancillary data.
//
// struct sctp_sndrcvinfo (Linux <linux/sctp.h>):
//
//	__u16 sinfo_stream;        // offset 0
//	__u16 sinfo_ssn;           // offset 2
//	__u16 sinfo_flags;         // offset 4
//	                           // 2 bytes of compiler alignment padding
//	__u32 sinfo_ppid;          // offset 8 (NOT 6!)
//	__u32 sinfo_context;       // offset 12
//	__u32 sinfo_timetolive;    // offset 16
//	__u32 sinfo_tsn;           // offset 20
//	__u32 sinfo_cumtsn;        // offset 24
//	sctp_assoc_t sinfo_assoc_id; // offset 28 (4 bytes), struct total 32.
//
// Writing PPID at the wrong offset (6) left sinfo_ppid = 0 and the peer
// reading a zero PPID; some 5G testers ABORT the association on unknown
// PPID, which showed up as ECONNRESET/EPIPE on subsequent sendmsg calls
// after the first few UEs registered.
func sctpSendMsg(fd int, data []byte, stream, ppid int) error {
	var info [32]byte
	binary.LittleEndian.PutUint16(info[0:2], uint16(stream))
	// PPID is carried network-byte-order on the wire; the kernel reads
	// this field verbatim and emits it in the DATA chunk's PPID slot.
	binary.BigEndian.PutUint32(info[8:12], uint32(ppid))

	cmsgLen := syscall.CmsgLen(len(info))
	cmsg := make([]byte, cmsgLen)
	// cmsg_level = IPPROTO_SCTP (132), cmsg_type = SCTP_SNDRCV (1)
	hdr := (*syscall.Cmsghdr)(unsafe.Pointer(&cmsg[0]))
	hdr.Level = sctpSOL
	hdr.Type = 1 // SCTP_SNDRCV
	hdr.SetLen(cmsgLen)
	copy(cmsg[syscall.CmsgLen(0):], info[:])

	iov := syscall.Iovec{Base: &data[0], Len: uint64(len(data))}
	msg := syscall.Msghdr{
		Iov:    &iov,
		Iovlen: 1,
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

// pickPrimaryIPv4 walks the host's interfaces and returns the best
// single IPv4 to single-home the SCTP association onto, plus the full
// list considered (for logging). Selection rules, in order:
//
//  1. Interface must be up, running, and not loopback.
//  2. Interface name must not start with "docker", "br-" (docker
//     custom bridges), "tun", "tap", "gtp" (our UPF GTP-U TUN), or
//     "cni" (k8s container network).
//  3. Address must be IPv4 (SCTP listener is AF_INET today) and
//     must not fall in 127.0.0.0/8, 169.254.0.0/16 (link-local),
//     or 172.17.0.0/16 (Docker's *default* bridge — `docker0`).
//
// Private-range 10.x, 192.168.x, and any 172.x outside docker0 (e.g.
// user-defined bridges like 172.30/16 used by mmt-studio-orchestrate)
// are allowed — those are legitimate operator management networks.
// If no candidate passes, returns "" and the caller falls back to
// 0.0.0.0 with a warning. Earlier this filter blanket-excluded all of
// 172.16.0.0/12, which silently rejected the orchestrate bridge IP
// and forced 0.0.0.0 bind → multi-homing INIT-ACK → SCTP HB blackhole
// (see commit 0ae041e in mmt_studio_core_tester for the symptom).
func pickPrimaryIPv4() (primary string, all []string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", nil
	}
	for _, iface := range ifaces {
		all = append(all, ifaceIPv4s(iface)...)
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := iface.Name
		if hasAnyPrefix(name, "docker", "br-", "tun", "tap", "gtp", "cni", "veth", "virbr") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip := ipv4Of(a)
			if ip == nil {
				continue
			}
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			// 172.17.0.0/16 — Docker's default `docker0` bridge.
			// Don't pick it as a management IP. Custom bridges
			// (172.18/16 … 172.31/16) including the
			// mmt-studio-orchestrate mmtnet (172.30.0.0/16) are
			// fair game and pass through.
			if ip[0] == 172 && ip[1] == 17 {
				continue
			}
			if primary == "" {
				primary = ip.String()
			}
		}
	}
	return primary, all
}

func ifaceIPv4s(iface net.Interface) []string {
	var out []string
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if ip := ipv4Of(a); ip != nil {
			out = append(out, fmt.Sprintf("%s(%s)", ip, iface.Name))
		}
	}
	return out
}

func ipv4Of(a net.Addr) net.IP {
	var ip net.IP
	switch v := a.(type) {
	case *net.IPNet:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	default:
		return nil
	}
	return ip.To4()
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}

func excludeFromList(all []string, drop string) []string {
	out := all[:0:0]
	for _, s := range all {
		if s == drop {
			continue
		}
		// Entries are "IP(iface)" — match against the IP prefix too.
		if len(s) > len(drop) && s[:len(drop)] == drop && s[len(drop)] == '(' {
			continue
		}
		out = append(out, s)
	}
	return out
}

func parseAddr(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "0.0.0.0", sctpPort, nil
	}
	if host == "" {
		host = "0.0.0.0"
	}
	port := sctpPort
	if portStr != "" {
		if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
			return host, sctpPort, nil
		}
	}
	return host, port, nil
}
