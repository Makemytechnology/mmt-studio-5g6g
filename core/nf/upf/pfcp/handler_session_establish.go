// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// handler_session_establish.go — TS 29.244 §7.5.2 Session Establishment.
//
// Holds:
//   - handleSessionEstablishment — top-level §7.5.2 Request handler.
//   - applyCreatePDRToHook  — §7.5.2.2 Create PDR  → ManagerHook.AddPDR + RegisterTEID/UEIP.
//   - applyCreateFARToHook  — §7.5.2.2 Create FAR  → ManagerHook.AddFAR.
//   - applyCreateQERToHook  — §7.5.2.2 Create QER  → ManagerHook.AddQER.
//   - applyCreateURRToHook  — §7.5.2.2 Create URR  → ManagerHook.AddURR.
//
// The Create-* helpers are package-private; they're shared with
// handler_session_modify.go because §7.5.4 Modification may also carry
// Create-* IEs (rules added after Establishment).
package pfcp

import (
	"net"
	"time"

	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// handleSessionEstablishment implements TS 29.244 §7.5.2.
//
// §7.5.2.1 (verbatim): "The PFCP Session Establishment Request
// shall be sent over the Sxa, Sxb, Sxc and N4 interface by the
// CP function to the UP function to establish a new PFCP session."
//
// Decode the full Request via the generated codec. Log IE counts
// so operator tests can confirm wire-level interop. Allocate UP-SEID
// from CP-F-SEID + reply with §7.5.3 Response.
func (h *Handler) handleSessionEstablishment(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	defer h.traceHandler("session_establishment", peer)()

	var req genpfcp.SessionEstablishmentRequest
	if err := req.Decode(payload); err != nil {
		h.log.Warnf("session establish decode from %s: %v — sending Cause=#68 (Invalid length)",
			peer, err)
		h.sendSessionReject(peer, genpfcp.MessageTypeSessionEstablishmentResponse,
			hdr.SEID, hdr.SequenceNumber, 68)
		return
	}

	upSEID := h.nextSEID.Add(1)
	// §7.5.2.1: The Establishment Request header SEID is 0; the
	// CP-allocated SEID is carried in the §8.2.37 CP-F-SEID IE and
	// must be used as the destination SEID in the §7.5.3 Response
	// per §7.2.2.4.2 "…the destination SEID value shall be set to
	// the SEID received in the F-SEID IE of the request".
	cpSEID := req.CPFSEID.SEID

	// §8.2.142 User ID — SUPIF + NAIF decoded to (imsi, pduSessID).
	// Used as the primary key into the UPF C dataplane session
	// table AND as the IMSI prefix on all subsequent log lines for
	// this session.
	imsi, pduSessionID := "", uint8(0)
	if req.UserID != nil {
		// Typed UserID — runtime.UserID has already decoded the
		// §8.2.101 flag-conditional layout. Our SMF puts IMSI in
		// SUPI and pduSessionID in NAI (decimal text); read the
		// typed fields directly.
		imsi = req.UserID.SUPI
		if req.UserID.NAI != "" {
			if n := parseDecUint8(req.UserID.NAI); n != 0 {
				pduSessionID = n
			}
		}
	}

	sess := &HandlerSession{
		UPSEID: upSEID, CPSEID: cpSEID,
		IMSI: imsi, PDUSessionID: pduSessionID,
		Peer: peer, CreatedAt: time.Now(),
		PDRKeys: make(map[uint16]PDRReverseKey),
	}
	h.mu.Lock()
	h.sessions[upSEID] = sess
	if imsi != "" {
		h.byIMSI[imsiPduKey{imsi, pduSessionID}] = sess
	}
	h.mu.Unlock()

	// §8.2.144 S-NSSAI (IE type 231) — present when the SMF carried
	// slice identity in §7.5.2. Decoded purely for log visibility +
	// to drive a per-slice CreateSession parameter (sst, sd) below
	// instead of the legacy hard-coded sst=1.
	var sliceSST uint8
	var sliceSD uint32
	if req.SNSSAI != nil && len(req.SNSSAI.Value) >= 4 {
		sliceSST = req.SNSSAI.Value[0]
		sliceSD = uint32(req.SNSSAI.Value[1])<<16 |
			uint32(req.SNSSAI.Value[2])<<8 |
			uint32(req.SNSSAI.Value[3])
	}

	slog := h.log.WithIMSI(imsi)
	slog.Infof("PFCP Session Establishment from %s pduSessID=%d CP-SEID=%#x UP-SEID=%#x SST=%d SD=%#06x createPDR=%d createFAR=%d createURR=%d createQER=%d (TS 29.244 §7.5.2 / §8.2.144)",
		peer, pduSessionID, cpSEID, upSEID, sliceSST, sliceSD,
		len(req.CreatePDR), len(req.CreateFAR),
		len(req.CreateURR), len(req.CreateQER))

	// TODO(spec: TS 29.244 §7.5.3 Established PDR IE in Response) —
	//   the UPF MUST return the UP-allocated F-TEID per PDR so the
	//   CP function can wire the N3 tunnel downstream. Today we
	//   send Response without Established PDR.

	// Drive the manager hook with the REAL (imsi, pduSessionID)
	// decoded from §8.2.142 UserID — so the UPF C dataplane keys
	// its session table identically to how the SMF Manager called
	// upf.Default.CreateSession/AddPDR/... in cgo mode.
	if h.mgr != nil {
		// Extract spec-typed PDN Type (§8.2.79) and APN-DNN
		// (§8.2.117) when present — both populated by the SMF in
		// the §7.5.2 Establishment IE list.
		var pdnType uint8
		if req.PDNType != nil {
			pdnType = req.PDNType.Value
		}
		dnn := "pfcp-stub"
		if req.APNDNN != nil && req.APNDNN.Value != "" {
			dnn = req.APNDNN.Value
		}
		// Per TS 29.244 v19.5.0 §7.5.2.2 + TS 23.501 §6.3.3, the SST
		// field on the C dataplane CreateSession reflects the slice
		// the session was anchored on. Default to SST=1 (eMBB) when
		// the SMF didn't carry §8.2.144 — preserves prior behaviour.
		sst := sliceSST
		if sst == 0 {
			sst = 1
		}
		if err := h.mgr.CreateSession(imsi, pduSessionID, dnn, sst, sliceSD, 0, pdnType); err != nil {
			slog.Warnf("manager.CreateSession pduSessID=%d UP-SEID=%#x: %v",
				pduSessionID, upSEID, err)
		}
		// Walk Create FAR first so PDRs that reference a FAR-ID
		// find it installed. Likewise Create URR / QER before
		// Create PDR for the same reason. Spec §7.5.2.2 is
		// agnostic on ordering; the C dataplane prefers dep-first.
		for i := range req.CreateFAR {
			applyCreateFARToHook(slog, h.mgr, imsi, pduSessionID, &req.CreateFAR[i])
		}
		for i := range req.CreateURR {
			applyCreateURRToHook(slog, h.mgr, imsi, pduSessionID, &req.CreateURR[i], sess)
		}
		for i := range req.CreateQER {
			applyCreateQERToHook(slog, h.mgr, imsi, pduSessionID, &req.CreateQER[i])
		}
		for i := range req.CreatePDR {
			applyCreatePDRToHook(slog, h.mgr, imsi, pduSessionID, &req.CreatePDR[i], sess)
		}

		// Flush the buffered Create-* + Register-* into one cgo
		// dispatch (docs/PERFORMANCE.md round-2 #1). Errors are logged but
		// don't fail the §7.5.3 response — the UPF would have logged
		// the underlying C-side cause already, and the SMF will
		// notice via subsequent Modify/Delete failures rather than
		// being stuck in a half-established state.
		if err := h.mgr.CommitSession(imsi, pduSessionID); err != nil {
			slog.Warnf("manager.CommitSession pduSessID=%d UP-SEID=%#x: %v",
				pduSessionID, upSEID, err)
		}
	}

	resp := &genpfcp.SessionEstablishmentResponse{
		SEID:           cpSEID,
		SequenceNumber: hdr.SequenceNumber,
		NodeID: genpfcp.NodeID{
			Type: 0, // §8.2.38 IPv4
			IPv4: net.ParseIP("127.0.0.1").To4(),
		},
		Cause: genpfcp.Cause{Value: 1}, // §8.2.1 "Request accepted (success)"
		UPFSEID: &genpfcp.FSEID{
			SEID: upSEID,
			IPv4: net.ParseIP("127.0.0.1").To4(),
		},
	}
	out, err := stripHeader(resp)
	if err != nil {
		h.log.Warnf("session establish response encode: %v", err)
		return
	}
	_ = h.t.SendResponse(peer, genpfcp.MessageTypeSessionEstablishmentResponse,
		cpSEID, hdr.SequenceNumber, out)
}

// applyCreatePDRToHook decodes a §7.5.2.2 Create PDR grouped IE and
// invokes hook.AddPDR. Mirrors the inverse of upfclient's buildCreatePDR;
// both sides share the opaque SDF Filter payload shape (§8.2.5 FD bit).
//
// Per TS 29.244 v19.5.0 §7.5.2.2 the PDI also conveys fast-path match
// keys that the UP function needs to populate its forwarding tables:
//
//   - §8.2.62 UE IP Address IE — when the PDI's Source Interface
//     identifies the core side (downlink), this is the UE's IPv4 the
//     UPF matches against the IP destination of N6-side packets.
//     Octet 5 V4=1 + S/D=1 (PDI use) → octets 6..9 carry the IPv4.
//
//   - §8.2.3 F-TEID IE — when the PDI's Source Interface identifies
//     the access side (uplink), this is the GTP-U TEID + UPF N3
//     IPv4 the UPF uses as ingress demux for incoming N3 GTP-U.
//     Octet 5 CH=0 + V4=1 → octets 6..9 carry the TEID, octets
//     10..13 carry the IPv4.
//
// After AddPDR succeeds we feed both keys to RegisterUEIP /
// RegisterTEID so the dataplane fast-path hash tables are
// populated. Without these calls the C dataplane's ueip_hash and
// teid_hash stay empty and every UE packet is dropped.
//
// Known §7.5.2.2 establishment gaps (single-element parsing today):
//
//   - Multi-QER per PDR — §7.5.2.2 PDR Table line for QER ID:
//     "Several IEs within the same IE type may be present to provide
//     multiple QER IDs in a single PDR." We read pdr.QERID[0] only.
//     Acceptable today because Session-AMBR is enforced via the
//     per-session rte_meter (configured by SetSessionAMBR), NOT via
//     a session-AMBR QER chained into each PDR — so chaining a
//     second QER per PDR isn't load-bearing for our enforcement.
//
//   - Multi-URR per PDR — §7.5.2.2 PDR Table line for URR ID: same
//     "several IEs ... may be present" wording. We read pdr.URRID[0]
//     only. Each PDR thus measures exactly one URR; multi-URR
//     accounting (e.g., separate online/offline charging anchors)
//     would need n>1 support both in the C upf_pdr_t (currently a
//     single uint32 urr_id) and here.
//
//   - Multi-SDF per PDR — pdr.PDI.SDFFilter is decoded as a slice
//     but only [0] is forwarded to the C side. The C upf_pdr_t
//     already holds upf_sdf_filter_t sdf[UPF_MAX_SDF_FILTERS]; the
//     restriction is only this Go-side decode loop.
//
//   - Multi UE-IP per PDR — pdr.PDI.UEIPAddress is iterated only
//     for the first IPv4 entry (intentional for v4-only deployment).
//     Dual-stack v4+v6 PDRs aren't supported until upf_pdr_t.ue_addr6
//     is wired through and the classifier learns the v6 path.
//
//   - §7.5.2.2 Outer Header Removal IE — the C side strips GTP-U
//     unconditionally on UL ingress; we don't honour an explicit
//     OHR IE in the PDR. Functionally correct for N3 GTP-U/IPv4
//     traffic, but a Type=2 (UDP/IPv4) or Type=3 (IPv6) etc. PDI
//     would not get the right strip behaviour without OHR-driven
//     dispatch in upf_pkt_io.c.
//
//   - §7.5.3 Established PDR IE in the §7.5.3 Establishment Response
//     (UP-allocated F-TEID return) — when the SMF requests UP-side
//     F-TEID allocation via PDI.FTEID.CH=1, the UP MUST return the
//     allocated F-TEID per PDR in the response. Today the SMF always
//     pre-allocates the TEID (CH=0), so this path isn't exercised;
//     handleSessionEstablishment carries a TODO for the day it is.
func applyCreatePDRToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, pdr *genpfcp.CreatePDR,
	sess *HandlerSession) {
	var farID, qerID, urrID uint32
	if pdr.FARID != nil {
		farID = pdr.FARID.Value
	}
	if len(pdr.QERID) > 0 {
		qerID = pdr.QERID[0].Value
	}
	if len(pdr.URRID) > 0 {
		urrID = pdr.URRID[0].Value
	}
	var qfi uint8
	if len(pdr.PDI.QFI) > 0 {
		qfi = pdr.PDI.QFI[0].Value
	}
	sdf := ""
	if len(pdr.PDI.SDFFilter) > 0 {
		// Typed read — runtime.SDFFilter has already decoded the
		// §8.2.5 flag-conditional fields. Our forwarding model
		// keys on the FD (Flow Description) string only.
		sdf = pdr.PDI.SDFFilter[0].FlowDescription
	}

	// Extract §8.2.62 UE IP Address (DL match) — first entry's V4
	// only; PDI may carry multiple but our forwarding model uses
	// the destination IPv4 entry. runtime.UEIPAddress decodes the
	// flag-conditional layout; we just read the IPv4 field.
	var ueIPv4 uint32
	for _, ue := range pdr.PDI.UEIPAddress {
		if v4 := ue.IPv4.To4(); v4 != nil {
			ueIPv4 = uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
			break
		}
	}
	// Extract §8.2.3 F-TEID (UL match).
	var teid, n3IPv4 uint32
	if pdr.PDI.FTEID != nil && !pdr.PDI.FTEID.CH {
		teid = pdr.PDI.FTEID.TEID
		if v4 := pdr.PDI.FTEID.IPv4; v4 != nil {
			if v4 := v4.To4(); v4 != nil {
				n3IPv4 = uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
			}
		}
	}

	if err := hook.AddPDR(imsi, pduSessionID, pdr.PDRID.Value, pdr.Precedence.Value,
		pdr.PDI.SourceInterface.Value, qfi, farID, qerID, urrID, sdf,
		ueIPv4, teid, n3IPv4); err != nil {
		log.Warnf("hook.AddPDR pdrID=%d: %v", pdr.PDRID.Value, err)
		return
	}
	log.Infof("  UPF installed PDR-%d pduSessID=%d prec=%d QFI=%d src=%d → FAR-%d QER-%d URR-%d SDF=%q",
		pdr.PDRID.Value, pduSessionID, pdr.Precedence.Value, qfi,
		pdr.PDI.SourceInterface.Value, farID, qerID, urrID, sdf)

	// Populate fast-path indices on the C dataplane. Source Interface
	// values per §8.2.10: 0=Access (UL), 1=Core (DL), 2=SGi-LAN/N6-LAN,
	// 3=CP function. Use the carried key — if both happen to be set
	// (unusual but spec-legal), register both.
	// Track per-PDR (TEID, UE-IP) so §7.5.4.6 Remove PDR releases
	// exactly the keys this rule installed, and §7.5.6 deletion
	// sweeps every key the session still owns. Entry exists for
	// the PDR even if both keys are zero — that anchors the slot
	// so a later Remove PDR doesn't have to special-case "I never
	// recorded this PDR".
	var key PDRReverseKey
	if ueIPv4 != 0 {
		if err := hook.RegisterUEIP(ueIPv4, imsi, pduSessionID); err != nil {
			log.Warnf("hook.RegisterUEIP ueIPv4=0x%08X pdrID=%d: %v",
				ueIPv4, pdr.PDRID.Value, err)
		} else {
			key.UEIP = ueIPv4
			log.Infof("  UPF dataplane registered UE IPv4=%d.%d.%d.%d for pduSessID=%d (TS 29.244 §8.2.62)",
				byte(ueIPv4>>24), byte(ueIPv4>>16), byte(ueIPv4>>8), byte(ueIPv4),
				pduSessionID)
		}
	}
	if teid != 0 {
		if err := hook.RegisterTEID(teid, imsi, pduSessionID); err != nil {
			log.Warnf("hook.RegisterTEID teid=0x%08X pdrID=%d: %v",
				teid, pdr.PDRID.Value, err)
		} else {
			key.TEID = teid
			log.Infof("  UPF dataplane registered UL TEID=0x%08X (N3 IPv4=%d.%d.%d.%d) for pduSessID=%d (TS 29.244 §8.2.3)",
				teid,
				byte(n3IPv4>>24), byte(n3IPv4>>16), byte(n3IPv4>>8), byte(n3IPv4),
				pduSessionID)
		}
	}
	if sess != nil {
		sess.PDRKeys[pdr.PDRID.Value] = key
	}
}

// applyCreateFARToHook decodes §7.5.2.2 Create FAR. Apply Action bits
// map back to Manager's scalar action code (1=FORW, 2=BUFF, 3=DROP).
func applyCreateFARToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, far *genpfcp.CreateFAR) {
	var action uint8
	switch {
	case far.ApplyAction.FORW == 1:
		action = 1
	case far.ApplyAction.BUFF == 1:
		action = 2
	case far.ApplyAction.DROP == 1:
		action = 3
	default:
		action = 1
	}
	var dstIface uint8
	var teid, peerAddr uint32
	var peerPort uint16
	var ohcType uint8
	if fp := far.ForwardingParameters; fp != nil {
		dstIface = fp.DestinationInterface.Value
		if fp.OuterHeaderCreation != nil {
			teid, peerAddr = readOHCGTPUv4(fp.OuterHeaderCreation)
			if teid != 0 || peerAddr != 0 {
				ohcType = 1 // GTP-U/UDP/IPv4
				peerPort = 2152
			}
		}
	}
	if err := hook.AddFAR(imsi, pduSessionID, far.FARID.Value, action, dstIface,
		teid, peerAddr, peerPort, ohcType); err != nil {
		log.Warnf("hook.AddFAR farID=%d: %v", far.FARID.Value, err)
		return
	}
	log.Infof("  UPF installed FAR-%d pduSessID=%d action=%d dstIface=%d teid=%#x peer=%#x",
		far.FARID.Value, pduSessionID, action, dstIface, teid, peerAddr)
}

// applyCreateQERToHook decodes §7.5.2.2 Create QER.
func applyCreateQERToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, qer *genpfcp.CreateQER) {
	var qfi uint8
	if qer.QFI != nil {
		qfi = qer.QFI.Value
	}
	var mbrUL, mbrDL, gbrUL, gbrDL uint64
	if qer.MBR != nil {
		mbrUL, mbrDL = qer.MBR.UL, qer.MBR.DL
	}
	if qer.GBR != nil {
		gbrUL, gbrDL = qer.GBR.UL, qer.GBR.DL
	}
	if err := hook.AddQER(imsi, pduSessionID, qer.QERID.Value, qfi,
		qer.GateStatus.ULGate, qer.GateStatus.DLGate,
		mbrUL, mbrDL, gbrUL, gbrDL); err != nil {
		log.Warnf("hook.AddQER qerID=%d: %v", qer.QERID.Value, err)
		return
	}
	// MBR/GBR IE values are kilobits-per-second per TS 29.244 v19.5.0
	// §8.2.8 / §8.2.9: "The UL/DL MBR fields shall be encoded as
	// kilobits per second (1 kbps = 1000 bps) in binary value." The
	// upf.context summary downstream prints the same numbers as kbps;
	// keeping the unit label consistent here.
	log.Infof("  UPF installed QER-%d pduSessID=%d QFI=%d gateUL=%d gateDL=%d MBR=%d/%d GBR=%d/%d (kbps)",
		qer.QERID.Value, pduSessionID, qfi,
		qer.GateStatus.ULGate, qer.GateStatus.DLGate,
		mbrUL, mbrDL, gbrUL, gbrDL)
}

// applyCreateURRToHook decodes §7.5.2.2 Create URR.
func applyCreateURRToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, urr *genpfcp.CreateURR,
	sess *HandlerSession) {
	// MeasurementMethod bit layout: EVENT=bit2, VOLUM=bit1, DURAT=bit0.
	measMethod := (urr.MeasurementMethod.EVENT&1)<<2 |
		(urr.MeasurementMethod.VOLUM&1)<<1 |
		(urr.MeasurementMethod.DURAT & 1)
	reportTrigger := uint8(urr.ReportingTriggers.Flags & 0xFF)
	var volUL, volDL uint64
	var timeSec uint32
	if urr.VolumeThreshold != nil {
		// Typed §8.2.13 — generator decoded the flag-conditional
		// volume fields. Read UL/DL directly; ignore TotalVolume
		// for now (not used by the dataplane today).
		if urr.VolumeThreshold.UplinkVolume != nil {
			volUL = *urr.VolumeThreshold.UplinkVolume
		}
		if urr.VolumeThreshold.DownlinkVolume != nil {
			volDL = *urr.VolumeThreshold.DownlinkVolume
		}
	}
	if urr.TimeThreshold != nil {
		timeSec = urr.TimeThreshold.Seconds
	}
	if err := hook.AddURR(imsi, pduSessionID, urr.URRID.Value,
		measMethod, reportTrigger, volUL, volDL, timeSec); err != nil {
		log.Warnf("hook.AddURR urrID=%d: %v", urr.URRID.Value, err)
		return
	}
	// Track on the session so handleSessionDeletion can fetch and
	// log final per-URR vol/pkt counters via hook.URRStats.
	if sess != nil {
		sess.URRIDs = append(sess.URRIDs, urr.URRID.Value)
	}
	log.Infof("  UPF installed URR-%d pduSessID=%d measMethod=%#x trigger=%#x volThresh=%d/%d timeThresh=%ds",
		urr.URRID.Value, pduSessionID, measMethod, reportTrigger,
		volUL, volDL, timeSec)
}
