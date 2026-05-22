// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package n2

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Manager owns the single SCTP association the N3IWF holds toward
// the AMF, runs the inbound dispatcher, and exposes per-UE send
// helpers to the IKE handler.
//
// Concurrency model: one recv goroutine reading from the SCTP
// association demuxes by RAN-UE-NGAP-ID and fans out to the per-UE
// callbacks the IKE handler registered. Sends serialize through a
// mutex on the underlying Conn (SCTP one-to-one is safe to share
// between goroutines, but the Send path packs an SCTP_SNDRCV cmsg
// per call and that has to be atomic with the data write).
//
// The Manager does NOT own UE state — that lives in nf/n3iwf/ctx.
// It only holds the routing table from RAN-UE-NGAP-ID to the
// callbacks the IKE handler registered.
type Manager struct {
	conn Conn
	log  *logger.Logger

	// stream0Mu serialises non-UE-associated procedures (NG Setup,
	// NG Reset, AMF Configuration Update). Per TS 38.412 §7 these
	// must go on stream 0; only one outstanding procedure per stream
	// pair so a separate mutex is the simplest enforcement.
	stream0Mu sync.Mutex

	// ueByRANID is the dispatch table for inbound DL NAS / ICS /
	// UEContextRelease. Keyed on the RAN-UE-NGAP-ID the N3IWF
	// allocated when the UE first registered.
	ueByRANID sync.Map // uint32 -> *UENGAPCtx

	// nextRANID hands out fresh RAN-UE-NGAP-IDs. The IE is uint32
	// (TS 38.413 §9.3.3.2 RANUENGAPID = INTEGER(0..4294967295)).
	// Starting at 1 because some peers treat 0 as "unassigned".
	nextRANIDMu sync.Mutex
	nextRANID   uint32

	cancelRecv context.CancelFunc
	recvDone   chan struct{}
}

// UENGAPCtx is the routing slot the IKE handler registers for each
// UE so the Manager's recv loop can deliver inbound AMF→N3IWF
// messages back to the right UE.
type UENGAPCtx struct {
	RANUEID uint32

	// OnDownlinkNAS is invoked from the recv goroutine when a
	// DownlinkNASTransport for this UE arrives. The handler is
	// expected to be quick (queue or hand to a UE-owned worker);
	// blocking it stalls the entire SCTP recv path.
	OnDownlinkNAS func(*DownlinkNAS)

	// OnInitialContextSetup is invoked when an Initial Context
	// Setup Request lands. The handler must not block — it should
	// either queue, or do its derivation work and return so the
	// recv goroutine continues. Sending the corresponding response
	// is the handler's responsibility (it calls Manager.Send).
	OnInitialContextSetup func(*InitialContextSetup)
}

// ManagerConfig parameterises NewManager.
type ManagerConfig struct {
	Dial    DialConfig
	NGSetup *NGSetupConfig
}

// NewManager dials the AMF, completes the NG Setup procedure, and
// spawns the recv goroutine. Returns a ready-to-use Manager or an
// error if the SCTP connect fails or the AMF rejected NG Setup.
func NewManager(ctx context.Context, cfg ManagerConfig) (*Manager, error) {
	if cfg.NGSetup == nil {
		return nil, errors.New("n2: ManagerConfig.NGSetup nil")
	}
	log := logger.Get("n3iwf.n2.manager")

	conn, err := Dial(ctx, cfg.Dial)
	if err != nil {
		return nil, fmt.Errorf("n2: dial AMF: %w", err)
	}

	m := &Manager{
		conn:      conn,
		log:       log,
		nextRANID: 1,
		recvDone:  make(chan struct{}),
	}

	// NG Setup is a single-shot synchronous procedure on stream 0.
	// Send the request, recv exactly one PDU back, decode either
	// NGSetupResponse or NGSetupFailure.
	if err := m.runNGSetup(ctx, cfg.NGSetup); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("n2: NG Setup: %w", err)
	}

	// Spawn the steady-state recv loop. Cancel propagates from
	// Manager.Close.
	rctx, cancel := context.WithCancel(context.Background())
	m.cancelRecv = cancel
	go m.recvLoop(rctx)
	return m, nil
}

// runNGSetup ships an NGSetupRequest and blocks for the
// corresponding response/failure. Called once at startup before the
// steady-state recv loop is running.
func (m *Manager) runNGSetup(ctx context.Context, cfg *NGSetupConfig) error {
	pdu, err := EncodeNGSetupRequest(cfg)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	m.stream0Mu.Lock()
	if err := m.conn.Send(pdu, 0); err != nil {
		m.stream0Mu.Unlock()
		return fmt.Errorf("send: %w", err)
	}
	m.stream0Mu.Unlock()

	for {
		buf, _, err := m.conn.Recv()
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}
		// Skip unrelated streams' traffic (shouldn't happen during
		// NG Setup but be defensive).
		pcode, ok := wire.PeekProcedureCode(buf)
		if !ok {
			m.log.Debugf("NG Setup: discarding undecodable %d-byte PDU", len(buf))
			continue
		}
		if pcode != ProcCodeNGSetup {
			m.log.Debugf("NG Setup: discarding procedureCode=%d during setup", pcode)
			continue
		}
		// Try response first, then failure.
		if resp, err := DecodeNGSetupResponse(buf); err == nil {
			m.log.Infof("NG Setup OK with %s (AMF response had %d IEs)",
				m.conn.RemoteAddr(), len(resp.ProtocolIEs))
			return nil
		}
		if fail, err := DecodeNGSetupFailure(buf); err == nil {
			return fmt.Errorf("AMF rejected NG Setup (%d IEs in failure)", len(fail.ProtocolIEs))
		}
		return fmt.Errorf("NG Setup: ambiguous response (%d bytes)", len(buf))
	}
}

// AllocateRANUEID hands out a fresh RAN-UE-NGAP-ID. Called by the
// IKE handler when a new UE finishes IKE_AUTH and needs an NGAP
// identity. Wraps at uint32 max.
func (m *Manager) AllocateRANUEID() uint32 {
	m.nextRANIDMu.Lock()
	defer m.nextRANIDMu.Unlock()
	id := m.nextRANID
	m.nextRANID++
	if m.nextRANID == 0 {
		m.nextRANID = 1 // skip 0
	}
	return id
}

// RegisterUE installs a UE's routing slot in the dispatch table.
// Call before SendInitialUEMessage so the corresponding ICS lands
// in the right callback.
func (m *Manager) RegisterUE(u *UENGAPCtx) {
	if u == nil || u.RANUEID == 0 {
		return
	}
	m.ueByRANID.Store(u.RANUEID, u)
}

// UnregisterUE removes a UE from the routing table — call from the
// IKE handler after IKE/IPsec teardown.
func (m *Manager) UnregisterUE(ranUEID uint32) {
	m.ueByRANID.Delete(ranUEID)
}

// SendInitialUEMessage ships the §9.2.5.3 PDU on a UE-associated
// stream (stream 1 is fine for the simple single-stream layout —
// future improvement: hash the ranUEID into a stream pool).
func (m *Manager) SendInitialUEMessage(cfg *InitialUEMessageConfig) error {
	pdu, err := EncodeInitialUEMessage(cfg)
	if err != nil {
		return err
	}
	return m.conn.Send(pdu, 1)
}

// SendUplinkNAS ships the §9.2.5.1 PDU for every subsequent UE→AMF
// NAS forward.
func (m *Manager) SendUplinkNAS(amfUEID uint64, ranUEID uint32, nas []byte, ueIP net.IP, uePort uint16) error {
	pdu, err := EncodeUplinkNASTransport(amfUEID, ranUEID, nas, ueIP, uePort)
	if err != nil {
		return err
	}
	return m.conn.Send(pdu, 1)
}

// SendInitialContextSetupResponse ships the success-direction
// response to an InitialContextSetupRequest. Called from the UE
// handler once it has installed the IPsec child SA derived from
// Knh per TS 24.502 §7.4.
func (m *Manager) SendInitialContextSetupResponse(amfUEID uint64, ranUEID uint32) error {
	pdu, err := EncodeInitialContextSetupResponse(amfUEID, ranUEID)
	if err != nil {
		return err
	}
	return m.conn.Send(pdu, 1)
}

// recvLoop reads PDUs off the SCTP association and demuxes them by
// procedure code. UE-associated procedures (DL NAS / ICS) are
// dispatched to the per-UE callback; non-UE-associated procedures
// (AMF Configuration Update, AMF Status Indication, etc.) are
// logged and skipped — they're outside this Manager's scope.
//
// Errors from a single PDU don't tear down the loop. Recv-level
// errors (EOF, ECONNRESET) terminate it; the IKE handler is then
// expected to call Manager.Close, drop UEs, and reconnect via a
// new NewManager.
func (m *Manager) recvLoop(ctx context.Context) {
	defer close(m.recvDone)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		buf, _, err := m.conn.Recv()
		if err != nil {
			m.log.Warnf("N2 recv: %v — recv loop exiting", err)
			return
		}
		pcode, ok := wire.PeekProcedureCode(buf)
		if !ok {
			m.log.Debugf("recv: undecodable %d-byte PDU", len(buf))
			continue
		}
		switch pcode {
		case ProcCodeDownlinkNASTransport:
			dl, err := DecodeDownlinkNASTransport(buf)
			if err != nil {
				m.log.Warnf("DL NAS decode: %v", err)
				continue
			}
			m.dispatchDL(dl)
		case ProcCodeInitialContextSetup:
			ics, err := DecodeInitialContextSetupRequest(buf)
			if err != nil {
				m.log.Warnf("ICS decode: %v", err)
				continue
			}
			m.dispatchICS(ics)
		default:
			m.log.Debugf("recv: unhandled procedureCode=%d (%d bytes)", pcode, len(buf))
		}
	}
}

func (m *Manager) dispatchDL(dl *DownlinkNAS) {
	if v, ok := m.ueByRANID.Load(dl.RANUENGAPID); ok {
		ueCtx := v.(*UENGAPCtx)
		if ueCtx.OnDownlinkNAS != nil {
			ueCtx.OnDownlinkNAS(dl)
			return
		}
	}
	m.log.Warnf("DL NAS for unknown RAN-UE-NGAP-ID %d (dropped)", dl.RANUENGAPID)
}

func (m *Manager) dispatchICS(ics *InitialContextSetup) {
	if v, ok := m.ueByRANID.Load(ics.RANUENGAPID); ok {
		ueCtx := v.(*UENGAPCtx)
		if ueCtx.OnInitialContextSetup != nil {
			ueCtx.OnInitialContextSetup(ics)
			return
		}
	}
	m.log.Warnf("ICS for unknown RAN-UE-NGAP-ID %d (dropped)", ics.RANUENGAPID)
}

// Close tears the SCTP association down and waits for the recv loop
// to exit. Idempotent.
func (m *Manager) Close() error {
	if m.cancelRecv != nil {
		m.cancelRecv()
	}
	err := m.conn.Close()
	<-m.recvDone
	return err
}
