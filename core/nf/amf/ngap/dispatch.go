// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// NGAP PDU dispatcher — demultiplexes on procedureCode per TS 38.413 §9.3.3.2.
//
// Each handler ports one Python file under nf/amf/ngap/. For this skeleton
// we register NG Setup only — the remaining procedures (Initial UE Message,
// DownlinkNASTransport, Initial Context Setup, PDU Session Resource Setup,
// Handover, Paging, UE Context Release, …) land as follow-up commits; the
// Python references are listed in nf/amf/ngap/README_PORT.md.
package ngap

import (
	"sync"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/errind"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ProcedureCode values defined by TS 38.413 §9.3.3.2 (source:
// specs/3gpp/ts_138413v190200p.pdf §9.4 —
// grep id-<Name>[[:space:]]+ProcedureCode ::=).
//
// Three values here were previously WRONG (id-HandoverPreparation was
// 0, id-ErrorIndication was 11, id-AMFConfigurationUpdate was 1).
// The net-visible effect was that a real HandoverRequired (code 12)
// fell through to the "no handler" warning, and an inbound
// HandoverNotify (code 11) would have been routed to the Error
// Indication handler. Both routings were broken; fixed here.
const (
	ProcCodeAMFConfigurationUpdate      = 0  // §9.4
	ProcCodeDownlinkRANStatusTransfer   = 7  // §8.4.7
	ProcCodeErrorIndication             = 9  // §8.7.6
	ProcCodeHandoverCancel              = 10 // §8.4.5
	ProcCodeHandoverNotification        = 11 // §8.4.3
	ProcCodeHandoverPreparation         = 12 // §8.4.1
	ProcCodeHandoverResourceAllocation  = 13 // §8.4.2
	ProcCodeInitialContextSetup         = 14 // §8.3.1
	ProcCodeInitialUEMessage            = 15 // §8.6.3
	ProcCodeNGReset                     = 20 // §8.7.4
	ProcCodeNGSetup                     = 21 // §8.7.1
	ProcCodePaging                      = 24 // §8.6
	ProcCodePWSCancel                   = 32 // §8.9.2 — AMF → NG-RAN (req) / NG-RAN → AMF (resp), non-UE-associated
	ProcCodePWSFailureIndication        = 33 // §8.9.4 — NG-RAN → AMF, non-UE-associated
	ProcCodePWSRestartIndication        = 34 // §8.9.3 — NG-RAN → AMF, non-UE-associated
	ProcCodeWriteReplaceWarning         = 51 // §8.9.1 — AMF → NG-RAN (req) / NG-RAN → AMF (resp), non-UE-associated
	// TS 38.413 proc-code table (ground truth from §9.4 of the PDF;
	// matches generated IdPDUSessionResourceXxx constants). Earlier
	// this block had Modify at 27 and swapped the §8.2.2 / §8.2.3
	// labels — a Modify Response from the gNB would have landed in
	// "no handler for procedureCode=26" and a Modify we sent at 27
	// would have been rejected by the gNB.
	ProcCodePDUSessionResourceModify    = 26 // §8.2.3
	ProcCodePDUSessionResourceRelease   = 28 // §8.2.2
	ProcCodePDUSessionResourceSetup     = 29 // §8.2.1
	ProcCodePathSwitchRequest           = 25 // §8.4.4
	ProcCodeRANConfigurationUpdate      = 35 // §8.7.2
	ProcCodeUEContextRelease            = 41 // §8.3.4
	ProcCodeUEContextReleaseReq         = 42 // §8.3.3
	ProcCodeDownlinkNASTransport        = 4  // §8.6.2
	ProcCodeUERadioCapabilityInfoIndication = 44 // §8.14.1 — gNB → AMF
	ProcCodeUplinkNASTransport          = 46 // §8.6.1
	ProcCodeUplinkRANStatusTransfer     = 49 // §8.4.6
	ProcCodeHandoverSuccess             = 61 // §8.4.8
	ProcCodeUplinkRANEarlyStatusTransfer   = 62 // §8.4.9
	ProcCodeDownlinkRANEarlyStatusTransfer = 63 // §8.4.10
	// ProcCodeRRCInactiveTransitionReport — TS 38.413 v19.2.0
	// §8.3.5. Verified from the generated NGAP constants
	// (codecs/asn1-go/protocols/ngap/generated/ngap_constants.go
	// IdRRCInactiveTransitionReport = 37). NG-RAN → AMF only.
	ProcCodeRRCInactiveTransitionReport = 37 // §8.3.5
)

// Handler is the signature for a per-procedure handler. env is the parsed
// NGAP-PDU envelope — env.Value is the APER-encoded inner procedure PDU
// (e.g. NGSetupRequest, InitialUEMessage) and stream is the SCTP stream
// the message arrived on.
type Handler func(gnb *gnbctx.GnbCtx, env *wire.Envelope, stream int)

var (
	handlersMu sync.RWMutex
	handlers   = map[int]Handler{}
)

// Register installs a handler for a procedure code. Called from the init()
// of each procedure package (e.g. ngap/ngsetup → Register(ProcCodeNGSetup, …)).
// Replacing an existing handler is allowed (hot-swap / test injection).
func Register(procedureCode int, h Handler) {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	handlers[procedureCode] = h
}

// DefaultDispatcher parses the NGAP-PDU envelope and hands the result to
// the procedure-specific handler registered via Register.
//
// Decode errors are logged and the PDU dropped — a production SCTP server
// should additionally raise an Error Indication (TS 38.413 §8.7.6) back to
// the gNB; that follows when the Error Indication handler lands.
func DefaultDispatcher(gnb *gnbctx.GnbCtx, data []byte, stream int) {
	log := logger.Get("amf.ngap.dispatch")
	if len(data) == 0 {
		return
	}
	// Do NOT short-circuit on gnb.Conn()==nil. Buffered PDUs arrived
	// BEFORE peer SHUTDOWN / reset and represent legitimate UE
	// requests (PSR Response, ICS Response, UL NAS) that must be
	// dispatched so their FSMs see the event. Handlers that need to
	// send DL traffic check conn themselves and get ErrNoTransport
	// (now a WARN, not an ERROR). See server.go defer ordering:
	// worker drain runs before MarkDisconnected so during the drain
	// conn is still valid.
	env, err := wire.Decode(data)
	if err != nil {
		log.Warnf("NGAP envelope decode from %s stream=%d: %v (% x)", gnb.GnbIP, stream, err, data)
		// TS 38.413 v19.2.0 §8.7.5.1: "The Error Indication procedure
		// is initiated by a node in order to report detected errors
		// in one incoming message, provided they cannot be reported
		// by an appropriate failure message." A wire-decode failure
		// means we don't even know the procedure — Error Indication
		// is the only spec-mandated response. Cause = transferSyntaxError
		// (TS 38.413 §9.3.1.2 protocol-cause group).
		if e := errind.Send(gnb, 0, 0,
			errind.CauseProtocol(genngap.CauseProtocolTransferSyntaxError)); e != nil {
			log.Warnf("errind.Send (decode-fail) gNB=%s: %v", gnb.GnbIP, e)
		}
		return
	}
	handlersMu.RLock()
	h := handlers[int(env.ProcedureCode)]
	handlersMu.RUnlock()
	if h == nil {
		log.Warnf("No handler for NGAP %s procedureCode=%d len=%d from %s",
			env.Type, env.ProcedureCode, len(env.Value), gnb.GnbIP)
		// Procedure not implemented at the AMF — same §8.7.5.1
		// rationale; cause = abstractSyntaxError-IgnoreAndNotify
		// (the "ignore and notify" sub-cause is the spec's wording
		// for "we ignored your message but want to tell you").
		if e := errind.Send(gnb, 0, 0,
			errind.CauseProtocol(genngap.CauseProtocolAbstractSyntaxErrorIgnoreAndNotify)); e != nil {
			log.Warnf("errind.Send (no-handler proc=%d) gNB=%s: %v",
				env.ProcedureCode, gnb.GnbIP, e)
		}
		return
	}
	h(gnb, env, stream)
}
