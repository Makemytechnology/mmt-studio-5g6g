// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import "fmt"

// Event drives an NGAP per-UE transition. Covers the five elementary
// UE-associated procedures (TS 38.413 §8.2, §8.3), mobility signalling
// (§8.4), and timer expiries.
type Event int

const (
	// §8.1 Initial UE / UL NAS ────────────────────────────────────────
	EvInitialUEMessage Event = iota
	EvUplinkNASTransport

	// §8.3.1 Initial Context Setup ────────────────────────────────────
	EvICSRequestSent
	EvICSResponse
	EvICSFailure

	// §8.3.2 UE Context Modification (AMF-initiated) ─────────────────
	EvUECtxModificationResponse
	EvUECtxModificationFailure

	// §8.3.3/8.3.4 UE Context Release ─────────────────────────────────
	EvUECtxReleaseRequest  // gNB → AMF (§8.3.2)
	EvUECtxReleaseCommand  // AMF → gNB (§8.3.4) — outbound trigger
	EvUECtxReleaseComplete // gNB → AMF reply

	// §8.2.1 PDU Session Resource Setup ───────────────────────────────
	EvPDUResourceSetupRequestSent
	EvPDUResourceSetupResponse
	EvPDUResourceSetupFailure

	// §8.2.2 PDU Session Resource Modify ──────────────────────────────
	EvPDUResourceModifyRequestSent
	EvPDUResourceModifyResponse
	EvPDUResourceModifyFailure

	// §8.2.3 PDU Session Resource Release ─────────────────────────────
	EvPDUResourceReleaseCommandSent
	EvPDUResourceReleaseResponse

	// §8.6 Paging ─────────────────────────────────────────────────────
	EvPagingSent
	EvPagingResponse // actually arrives as a Service Request on NAS

	// Error Indication (§8.7.6) and NG Reset (§8.7.4) — any state
	// may abort back to NotEstablished.
	EvErrorIndication
	EvNGReset

	// §8.4 Mobility ─────────────────────────────────────────────────
	EvHandoverRequired           // source gNB → AMF (§8.4.1)
	EvHandoverRequestAck         // target gNB → AMF ACK (§8.4.2)
	EvHandoverFailure            // target gNB rejected / source gave up (§8.4.2)
	EvHandoverCommand            // AMF → source gNB (§8.4.1 successful outcome)
	EvHandoverNotify             // target gNB → AMF, UE has arrived (§8.4.3)
	EvHandoverCancel             // source gNB → AMF cancellation (§8.4.5)
	EvUplinkRANStatusTransfer    // source gNB → AMF PDCP SN state (§8.4.6)
	EvDownlinkRANStatusTransfer  // AMF → target gNB (forwarded) (§8.4.7)
	EvHandoverSuccess            // AMF → source gNB DAPS success (§8.4.8)
	EvUplinkRANEarlyStatusTransfer   // source gNB → AMF DAPS early PDCP count (§8.4.9)
	EvDownlinkRANEarlyStatusTransfer // AMF → target gNB (forwarded) (§8.4.10)
	EvPathSwitchRequest          // Xn-handover path-switch initiating (§8.4.4)
	EvPathSwitchAck

	// Timer expiries ──────────────────────────────────────────────────
	EvTwaitUECtxReleaseExpired // §9.4.2 default 10s
	EvTICSResponseExpired      // Twait-ICS (implementation-specific)
	EvTHandoverPrepExpired     // §9.4 T_RelocPrep
)

// String renders the event name.
func (e Event) String() string {
	switch e {
	case EvInitialUEMessage:
		return "InitialUEMessage"
	case EvUplinkNASTransport:
		return "UplinkNASTransport"
	case EvICSRequestSent:
		return "ICSRequestSent"
	case EvICSResponse:
		return "InitialContextSetupResponse"
	case EvICSFailure:
		return "InitialContextSetupFailure"
	case EvUECtxModificationResponse:
		return "UEContextModificationResponse"
	case EvUECtxModificationFailure:
		return "UEContextModificationFailure"
	case EvUECtxReleaseRequest:
		return "UEContextReleaseRequest"
	case EvUECtxReleaseCommand:
		return "UEContextReleaseCommand"
	case EvUECtxReleaseComplete:
		return "UEContextReleaseComplete"
	case EvPDUResourceSetupRequestSent:
		return "PDUSessionResourceSetupRequestSent"
	case EvPDUResourceSetupResponse:
		return "PDUSessionResourceSetupResponse"
	case EvPDUResourceSetupFailure:
		return "PDUSessionResourceSetupFailure"
	case EvPDUResourceModifyRequestSent:
		return "PDUSessionResourceModifyRequestSent"
	case EvPDUResourceModifyResponse:
		return "PDUSessionResourceModifyResponse"
	case EvPDUResourceModifyFailure:
		return "PDUSessionResourceModifyFailure"
	case EvPDUResourceReleaseCommandSent:
		return "PDUSessionResourceReleaseCommandSent"
	case EvPDUResourceReleaseResponse:
		return "PDUSessionResourceReleaseResponse"
	case EvPagingSent:
		return "PagingSent"
	case EvPagingResponse:
		return "PagingResponse"
	case EvErrorIndication:
		return "ErrorIndication"
	case EvNGReset:
		return "NGReset"
	case EvHandoverRequired:
		return "HandoverRequired"
	case EvHandoverRequestAck:
		return "HandoverRequestAcknowledge"
	case EvHandoverFailure:
		return "HandoverFailure"
	case EvHandoverCommand:
		return "HandoverCommand"
	case EvHandoverNotify:
		return "HandoverNotify"
	case EvHandoverCancel:
		return "HandoverCancel"
	case EvUplinkRANStatusTransfer:
		return "UplinkRANStatusTransfer"
	case EvDownlinkRANStatusTransfer:
		return "DownlinkRANStatusTransfer"
	case EvHandoverSuccess:
		return "HandoverSuccess"
	case EvUplinkRANEarlyStatusTransfer:
		return "UplinkRANEarlyStatusTransfer"
	case EvDownlinkRANEarlyStatusTransfer:
		return "DownlinkRANEarlyStatusTransfer"
	case EvPathSwitchRequest:
		return "PathSwitchRequest"
	case EvPathSwitchAck:
		return "PathSwitchRequestAcknowledge"
	case EvTHandoverPrepExpired:
		return "T_HandoverPrepExpired"
	case EvTwaitUECtxReleaseExpired:
		return "TwaitUECtxReleaseExpired"
	case EvTICSResponseExpired:
		return "TICSResponseExpired"
	}
	return fmt.Sprintf("Event(%d)", int(e))
}
