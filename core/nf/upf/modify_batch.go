// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// modify_batch.go — coalesced post-establishment PFCP Session
// Modification (TS 29.244 v19.5.0 §7.5.4).
//
// The wire path is straightforward — §7.5.4.2 lets ONE Session
// Modification Request carry an arbitrary mix of Create-* /
// Update-* / Remove-* IEs. The SMF policy layer typically wants
// to install / tear down several rules atomically (for example
// the (QER, UL FAR) pair the PCF→SMF UpdateNotify path adds per
// new media flow). Without this batch entry point each rule
// hits PfcpBridge.AddX/RemoveX as a separate call, each emits its
// own §7.5.4 Modification, and a 4-call VoNR fan-out fires 8
// sequential UDP round-trips when 1 would suffice.
//
// The batch struct mirrors the §7.5.4 IE shape: each list maps
// 1:1 to a Create-* / Update-* / Remove-* IE list in the wire
// message. Empty lists are omitted from the encoded message so
// the bridge stays compatible with UPFs that reject extra empty
// IE groups (none of ours do, but the spec doesn't require them).
//
// Spec anchors for fields that sit inside the batch:
//   * §7.5.4.2  Update PDR (UpdatePDRs unused today; reserved)
//   * §7.5.4.3  Update FAR (UpdateFARs — Apply Action + Outer
//                            Header Creation per §8.2.26 / §8.2.56)
//   * §7.5.4.4  Update URR (unused today)
//   * §7.5.4.5  Update QER (unused today)
//   * §7.5.4.6  Remove PDR
//   * §7.5.4.7  Remove FAR
//   * §7.5.4.8  Remove URR
//   * §7.5.4.9  Remove QER
//   * §7.5.4.17 Create PDR / FAR / URR / QER inside Modification
//
// Per-IE bodies reuse the same FAR/QER/PDR/URR Go structs the
// Establishment path uses (Manager.AddPDR / AddFAR / etc.); see
// upf.go §75-128 for those shapes.

package upf

// ModifyBatch carries every IE that should ride a single post-
// establishment §7.5.4 Modification Request. Fields are list-
// shaped to mirror the wire's `0..N` cardinality on each IE
// group; nil/empty means "no change in this category".
type ModifyBatch struct {
	// CreatePDRs / CreateFARs / CreateQERs / CreateURRs — §7.5.4.17
	// Create-* IEs ride INSIDE a Modification Request (rather than the
	// Establishment Request) when a new rule is added after the
	// session is up. Most common case: PCF→SMF UpdateNotify adds a
	// new GBR flow at SIP INVITE time.
	CreatePDRs []PDR
	CreateFARs []FAR
	CreateQERs []QER
	CreateURRs []URR

	// UpdateFARs — §7.5.4.3 Update FAR. Used for Service Request
	// reactivation (§4.2.3.2 step 12: new gNB DL TEID), AN-Release
	// deactivation (§4.2.6 step 6a: BUFF), and FAR retargeting.
	// FARID alone identifies the rule; teid/peerAddr/peerPort/=0
	// keeps the existing tunnel info; non-zero installs a new
	// §8.2.56 Outer Header Creation. Apply Action defaults to FORW
	// when peerAddr != 0 and BUFF when both teid and peerAddr are 0.
	UpdateFARs []UpdateFARSpec

	// Remove-* IEs — §7.5.4.6/.7/.8/.9. Each entry is the rule ID
	// to be deleted. Idempotent: removing a rule the UPF doesn't
	// have is a no-op (Cause IE stays Request accepted).
	RemovePDRs []uint16
	RemoveFARs []uint32
	RemoveQERs []uint32
	RemoveURRs []uint32

	// SessionAMBR — when non-nil, refreshes the session-scope QER
	// (qerID 0xFFFFFFFE per UPF convention) carrying TS 23.501
	// §5.7.2.6 Session-AMBR. nil = leave AMBR unchanged.
	SessionAMBR *AMBRSpec
}

// UpdateFARSpec is the §7.5.4.3 Update FAR IE in batch form — a
// FARID plus the new §8.2.56 Outer Header Creation tuple. Apply
// Action (§8.2.26) is implied: peerAddr == 0 → BUFF (deactivate /
// AN-Release); peerAddr != 0 → FORW (Service Request reactivate).
type UpdateFARSpec struct {
	FARID    uint32
	TEID     uint32
	PeerAddr uint32
	PeerPort uint16
}

// AMBRSpec is the §8.2.28 Session-AMBR pair (UL/DL bps).
type AMBRSpec struct {
	UL uint64
	DL uint64
}

// IsEmpty returns true when the batch carries no changes — the
// caller can short-circuit before invoking the bridge.
func (b *ModifyBatch) IsEmpty() bool {
	return len(b.CreatePDRs) == 0 && len(b.CreateFARs) == 0 &&
		len(b.CreateQERs) == 0 && len(b.CreateURRs) == 0 &&
		len(b.UpdateFARs) == 0 &&
		len(b.RemovePDRs) == 0 && len(b.RemoveFARs) == 0 &&
		len(b.RemoveQERs) == 0 && len(b.RemoveURRs) == 0 &&
		b.SessionAMBR == nil
}

// ApplyModifyBatch is the Manager-level entry point for batched
// post-establishment modifications. It (1) updates the local
// session bookkeeping (PDR/FAR/QER/URR slices) so the existing
// /api/upf/sessions readout stays accurate, then (2) hands the
// batch to the installed UPFBridge, which may emit a single PFCP
// Modification (PfcpBridge) or iterate (cgo / goBridge).
//
// Returns the bridge's first non-nil error. Per-IE causes are not
// surfaced individually — TS 29.244 §7.5.5 returns a single Cause
// for the whole Modification Response, so we do too.
func (m *Manager) ApplyModifyBatch(imsi string, id uint8, batch ModifyBatch) error {
	if batch.IsEmpty() {
		return nil
	}

	m.mu.Lock()
	s := m.sessions[sessionKey{imsi, id}]
	if s != nil {
		// Mirror the same local-state mutations Manager.AddPDR/AddFAR/
		// AddQER/AddURR/RemovePDR/RemoveFAR/RemoveQER/RemoveURR perform,
		// so /api/upf/sessions reflects the post-batch view even when
		// the bridge is still mid-flight on the wire.
		s.PDRs = append(s.PDRs, batch.CreatePDRs...)
		s.FARs = append(s.FARs, batch.CreateFARs...)
		s.QERs = append(s.QERs, batch.CreateQERs...)
		s.URRs = append(s.URRs, batch.CreateURRs...)
		for _, uf := range batch.UpdateFARs {
			for i := range s.FARs {
				if s.FARs[i].FARID == uf.FARID {
					s.FARs[i].TEID = uf.TEID
					s.FARs[i].PeerAddr = uf.PeerAddr
					s.FARs[i].PeerPort = uf.PeerPort
					if uf.PeerAddr != 0 {
						s.FARs[i].Action = farActionForward
					}
					break
				}
			}
		}
		s.PDRs = filterPDRs(s.PDRs, batch.RemovePDRs)
		s.FARs = filterFARs(s.FARs, batch.RemoveFARs)
		s.QERs = filterQERs(s.QERs, batch.RemoveQERs)
		s.URRs = filterURRs(s.URRs, batch.RemoveURRs)
	}
	m.mu.Unlock()

	if bridge == nil {
		return nil
	}
	return bridge.ApplyModifyBatch(imsi, id, batch)
}

func filterPDRs(in []PDR, drop []uint16) []PDR {
	if len(drop) == 0 {
		return in
	}
	dset := make(map[uint16]struct{}, len(drop))
	for _, id := range drop {
		dset[id] = struct{}{}
	}
	out := in[:0]
	for _, p := range in {
		if _, ok := dset[p.PDRID]; !ok {
			out = append(out, p)
		}
	}
	return out
}

func filterFARs(in []FAR, drop []uint32) []FAR {
	if len(drop) == 0 {
		return in
	}
	dset := make(map[uint32]struct{}, len(drop))
	for _, id := range drop {
		dset[id] = struct{}{}
	}
	out := in[:0]
	for _, f := range in {
		if _, ok := dset[f.FARID]; !ok {
			out = append(out, f)
		}
	}
	return out
}

func filterQERs(in []QER, drop []uint32) []QER {
	if len(drop) == 0 {
		return in
	}
	dset := make(map[uint32]struct{}, len(drop))
	for _, id := range drop {
		dset[id] = struct{}{}
	}
	out := in[:0]
	for _, q := range in {
		if _, ok := dset[q.QERID]; !ok {
			out = append(out, q)
		}
	}
	return out
}

func filterURRs(in []URR, drop []uint32) []URR {
	if len(drop) == 0 {
		return in
	}
	dset := make(map[uint32]struct{}, len(drop))
	for _, id := range drop {
		dset[id] = struct{}{}
	}
	out := in[:0]
	for _, u := range in {
		if _, ok := dset[u.URRID]; !ok {
			out = append(out, u)
		}
	}
	return out
}
