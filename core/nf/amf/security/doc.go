// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package security is the AMF's single-owner 5G NAS security manager.
//
// Every inbound and outbound 5GMM NAS PDU must flow through this package
// so that integrity, ciphering, NAS COUNT management, key derivation,
// and 5G NAS security context lifecycle are owned in exactly one place.
// Handlers (gmm/*, gsm/*) receive plaintext inner + a metadata struct
// and never touch keys or counters directly.
//
// This file is package documentation only. It captures the invariants
// and the spec basis before any code lands, so the refactor from the
// scattered model (dispatch.stripSecurity + per-handler re-verifies +
// three separate K_gNB derivation sites) can be validated against it.
//
// # Why a single owner
//
// The two bugs fixed in commits 170af94 (stale K_gNB on Service Request)
// and 8d7ffa4 (double MAC verify in §4.4 reuse) were both symptoms of
// diffuse ownership: keys/counts lived on the per-UE security struct
// and anyone could read, advance, or re-derive them. Under this design
// the per-UE security state is package-private to security/ and mutated
// only by the entry points defined below; nobody can double-verify or
// read a stale cached K_gNB by construction.
//
// # Specification basis (all citations resolve against local PDFs)
//
//	TS 24.501 v19.6.2 — nf/codecs/tlv-3gpp-nas/... + /tmp/ts24501.txt:
//	  §4.4       "NAS security"                (general principles)
//	  §4.4.2.1   Establishment / take-into-use of 5G NAS security ctx
//	  §4.4.3.1   NAS COUNT structure + storage semantics
//	  §4.4.3.2   Replay protection
//	  §4.4.3.3   Integrity protection and verification
//	  §4.4.3.4   Ciphering and deciphering
//	  §4.4.3.5   NAS COUNT wrap-around
//	  §4.4.4.1   Integrity protection — general
//	  §4.4.4.3   Integrity checking in the AMF
//	  §4.4.5     Ciphering of NAS signalling messages
//	  §4.4.6     Protection of initial NAS signalling messages
//	  §5.4.2.2   Security mode control initiation by the network
//	  §9.3       Security header type (Table 9.3.1)
//
//	TS 33.501 v19.6.0 — specs/3gpp/ts_133501v190600p.pdf + /tmp/ts133501.txt:
//	  §6.4       NAS security (overview)
//	  §6.7.2     NAS Security Mode Command procedure
//	  §6.8.1.2.2 Establishment of keys for cryptographically protected
//	             radio bearers in 3GPP access (K_gNB on CM-IDLE→CM-CONNECTED)
//	  §6.9.3     Key handling in mobility registration update
//	  §A.8       Algorithm key derivation functions
//	  §A.9       KgNB KDF (FC=0x6E, P0=UL NAS COUNT, P1=access-type=0x01)
//
// # The Security Header Type table — authoritative decision tree
//
// TS 24.501 §9.3 Table 9.3.1 (verbatim) dictates how every 5GMM PDU is
// wrapped or unwrapped. This table is the full set of cases; the
// manager handles each explicitly.
//
//	SHT  Meaning                                             Our role
//	---- --------------------------------------------------- -----------------------------------
//	0b0000  Plain 5GS NAS message, not security protected    RX: return inner as-is, no count
//	                                                         TX: emit plain, no MAC, no count bump
//	                                                         Used for: Initial RR (no ctx), Identity
//	                                                         Request/Response pre-SMC, Auth Request,
//	                                                         Auth Response, Auth Failure, SMC Reject
//	                                                         (when no prior ctx exists).
//	0b0001  Integrity protected                              RX: verify MAC, advance UL count
//	                                                         TX: attach MAC (no cipher), bump DL count
//	                                                         Used for: DL messages when ciphering is
//	                                                         off (NEA=0) or during the window between
//	                                                         SMC send and SMC Complete receipt.
//	0b0010  Integrity protected and ciphered                 RX: verify MAC, decipher, advance UL count
//	                                                         TX: cipher, attach MAC, bump DL count
//	                                                         Used for: the normal post-SMC steady
//	                                                         state when NEA!=0.
//	0b0011  Integrity protected with new 5G NAS security ctx RX: n/a (UE→AMF only for SMC Complete
//	        (Table 9.3.1 NOTE 1: SECURITY MODE COMMAND only)  pre-cipher window — see below)
//	                                                         TX: integrity-only with NEW ngKSI /
//	                                                         KNASInt, DL count is reset to 0 first
//	                                                         (TS 33.501 §6.7.2 "integrity protected
//	                                                         (but not ciphered) with NAS integrity key
//	                                                         based on the KAMF indicated by the ngKSI
//	                                                         in the NAS Security Mode Command
//	                                                         message").
//	0b0100  Integrity protected and ciphered with new 5G NAS RX: verify + decipher with new ctx,
//	        security context                                  advance UL count (count IS reset by
//	        (Table 9.3.1 NOTE 2: SECURITY MODE COMPLETE only) the UE per §6.7.2 step 2b)
//	                                                         TX: n/a (AMF never sends this)
//
// All other SHT values are reserved per §9.3. The manager rejects them
// with a decode error at the entry point.
//
// # Invariants the manager guarantees
//
// I1. The per-UE security context (KAMF, KNASInt, KNASEnc, EEA, EIA,
//     ULCount, DLCount, NGKSI, NGKSIAssigned, ABBA, UESecCap, AuthDone)
//     is package-private. External code holds an opaque *Context handle
//     and mutates state only via the methods in §"Public API". This is
//     how Bug 8d7ffa4 (double verify) becomes un-expressible — handlers
//     have no Unwrap/Wrap primitive to call twice.
//
// I2. Per TS 24.501 §4.4.3.1:
//
//       "The value of the uplink NAS COUNT stored in the AMF is the
//        largest uplink NAS COUNT used in a successfully integrity
//        checked NAS message."
//
//     ⇒ UL count advances ONLY on a successful integrity check in RxNAS.
//     MAC-fail returns error without advancing. This is already the
//     semantic of secureUnwrap today; centralizing makes it an invariant
//     instead of a per-caller assumption.
//
//       "The value of the downlink NAS COUNT stored in the AMF is the
//        value that shall be used in the next NAS message."
//
//     ⇒ DL count advances AFTER the outbound message is encoded, by
//     exactly +1 per SECURITY PROTECTED message emitted (§4.4.3.1
//     para 6 "After each new or retransmitted outbound SECURITY
//     PROTECTED 5GS NAS MESSAGE message, the sender shall increase
//     the NAS COUNT number by one"). Plain (SHT=0) messages do NOT
//     bump DL count.
//
// I3. The "count that protected an inbound PDU" is returned to the
//     caller as metadata (RxMeta.ULCount), not reconstructed by
//     subtracting 1 from a post-advance field. This removes the `-1`
//     compensation pattern currently duplicated in smc.go:330,
//     registration.go:1198, and service.go (commit 170af94).
//
// I4. K_gNB is NOT cached as state. K_gNB.Derive(ctx) is a method
//     called just-in-time by the ICS sender; it reads the current
//     context + UL count and returns a fresh 32-byte value. There is
//     no "stale K_gNB" failure mode because there is nothing to stale
//     (this prevents regressions of commit 170af94).
//
// I5. Integrity verification and ciphering are atomic from the
//     caller's perspective. RxNAS either returns (plain, meta, nil) or
//     (nil, nil, err) — never a partial result. On replay (a received
//     count that equals or trails the stored largest-verified-count
//     for that direction), RxNAS returns ErrReplay and does NOT
//     advance (per §4.4.3.2 "a given NAS COUNT value shall be
//     accepted at most one time and only if message integrity verifies
//     correctly").
//
// I6. The SMC first DL message is the single asymmetry: DL count is
//     reset to 0 before encoding, SHT=3 is used, cipher is skipped
//     even if NEA!=0 is selected (TS 33.501 §6.7.2 step 1b:
//     "integrity protected (but not ciphered) with NAS integrity key
//     based on the KAMF indicated by the ngKSI"). This is expressed
//     as a dedicated TxSMC method rather than a TxNAS flag — the call
//     sites are few (gmm/smc.go) and naming the method after the
//     procedure makes the asymmetry self-documenting.
//
// I7. §4.4 cached-context reuse (same-UE / cross-UE migration) is a
//     method on the manager, not a handler concern. The registration
//     path asks the manager "Reuse(existing, incoming) → (ok, ngKSI)"
//     and the manager owns the ngKSI-match + MAC-verify + count
//     handoff. Double-verify (commit 8d7ffa4 root cause) has no entry
//     point under this API.
//
// # Public API (planned — names and signatures are final)
//
// All of the below are methods on *Manager or free functions taking a
// *Context; the split lives in manager.go / context.go when code lands.
//
//	// RxNAS verifies and unwraps a 5GMM PDU received from NGAP.
//	// The returned plaintext bytes start at the EPD (0x7E) and include
//	// the inner SHT (always 0) + message type + body, so callers can
//	// pass them unchanged to nasgen's DecodeNASMessage.
//	//
//	// On success, UL count has advanced per I2. Meta.ULCount is the
//	// count that protected this PDU (pre-advance), for the narrow
//	// set of callers that need it (ICS K_gNB freshness).
//	RxNAS(ctx *Context, pdu []byte) (plain []byte, meta RxMeta, err error)
//
//	type RxMeta struct {
//	    SHT      uint8  // original SHT from the wire
//	    ULCount  uint32 // the 32-bit count that protected this PDU
//	    NGKSI    uint8  // which ngKSI the sender claimed
//	    Plain    bool   // SHT==0; keys untouched
//	}
//
//	// TxNAS wraps an outbound 5GMM inner (plain bytes starting with
//	// EPD+SHT=0+msgType+body) with the currently-active security
//	// context and returns the wire PDU. DL count advances per I2.
//	//
//	// For the SMC special case use TxSMC instead — TxNAS refuses to
//	// emit SHT=3 or SHT=4.
//	TxNAS(ctx *Context, inner []byte) (pdu []byte, err error)
//
//	// TxPlain emits a SHT=0 outbound PDU (no MAC, no cipher, no
//	// count advance). Used for Auth Request, Identity Request, any
//	// DL that precedes SMC activation.
//	TxPlain(ctx *Context, inner []byte) (pdu []byte, err error)
//
//	// TxSMC encodes a SECURITY MODE COMMAND per TS 33.501 §6.7.2 +
//	// TS 24.501 §5.4.2.2: DL count reset to 0, SHT=3 (integrity with
//	// new ctx, not ciphered), MAC computed with the new KNASInt that
//	// the caller just installed via ActivateCtx. Advances DL count to
//	// 1 after emission.
//	TxSMC(ctx *Context, inner []byte) (pdu []byte, err error)
//
//	// ActivateCtx takes a primary-authentication outcome (KAMF +
//	// NGKSI + selected EEA/EIA) and promotes it to the "current" 5G
//	// NAS security context for this UE, resetting UL/DL counts to 0
//	// per TS 33.501 §6.7.2 + §6.9.3 "K_AMF_change_flag" semantics.
//	// Next TxSMC will emit the first DL message under this context.
//	ActivateCtx(ctx *Context, kamf [32]byte, ngksi uint8, eea, eia uint8) error
//
//	// DeriveKgNB returns a freshly computed K_gNB for the current
//	// UL count, per TS 33.501 §A.9 (FC=0x6E, access=0x01).
//	// Callers (initialctxsetup.Send only) must invoke this at the
//	// moment of encoding the ICS Request — no caching allowed.
//	//
//	// Per TS 33.501 §6.8.1.2.2:
//	//   - If this CM-IDLE→CM-CONNECTED transition did NOT include a
//	//     NAS SMC, freshness = the UL count of the NAS message that
//	//     triggered the transition (ServiceRequest / RR with
//	//     "PDU session(s) to be re-activated"). The manager tracks
//	//     this as "most-recent-verified inbound UL count".
//	//   - If the transition DID include a NAS SMC on this path,
//	//     freshness = the UL count of the SMC Complete.
//	// The manager resolves which clause applies from its internal
//	// SMC-since-last-transition flag — callers don't choose.
//	DeriveKgNB(ctx *Context) (kgnb []byte, err error)
//
//	// Reuse implements TS 24.501 §4.4 cached-context skip-auth: if the
//	// incoming RR's ngKSI matches an existing *Context, verify the MAC
//	// on the RR (if SHT!=0), migrate state onto the target handle, and
//	// return ok. Handlers never call the MAC primitive; this is the
//	// single entry point that §4.4 requires.
//	Reuse(existing, incoming *Context, rrPDU []byte) (ok bool, err error)
//
//	// ResetOnHandover applies the §4.4.3.1 N1→N1 handover count reset
//	// rules (new KAMF: both counts to 0; same KAMF: signal DL LSB, bump
//	// DL). Exposed so the handover path doesn't reach into fields.
//	ResetOnHandover(ctx *Context, newKAMF bool) error
//
// # Receive path — flow
//
// (TS 24.501 §4.4.3.1 + §4.4.3.3 + §4.4.4.3, handled by RxNAS)
//
//  1. Parse EPD + SHT. If SHT==0, return (pdu, {Plain: true}, nil) with
//     no key operation. Callers see the plaintext directly; Auth Request,
//     Identity Request, Reg Reject, Auth Reject, and the very first RR
//     of a fresh attach all land here.
//
//  2. For SHT ∈ {1,2,3,4}: read 4-byte MAC + 1-byte SQN + body.
//     Reconstruct the 32-bit UL count per §4.4.3.1 para 7-9:
//
//       "The sequence number part of the estimated NAS COUNT value
//        shall be equal to the sequence number in the received NAS
//        message; and if the receiver can guarantee that this NAS
//        message was not previously accepted, then the receiver may
//        select the estimated NAS overflow counter so that the
//        estimated NAS COUNT value is lower than the stored NAS
//        COUNT value; otherwise, the receiver selects the estimated
//        NAS overflow counter so that the estimated NAS COUNT value
//        is higher than the stored NAS COUNT value."
//
//     Our policy: use highest-seen-SQN for that direction; if the
//     received SQN < stored low-byte, bump overflow by 1 (today's
//     nas_security.go:173-178 algorithm, preserved).
//
//  3. Compute MAC(KNASInt, EIA, count, bearer=1, direction=UL, SQN||body).
//     If mismatch, return ErrMACVerify. UL count is NOT advanced (I2).
//
//  4. If SHT ∈ {2,4} and EEA != 0: decipher body with KNASEnc.
//
//  5. Enforce replay (I5): if count <= stored-last-verified-UL-count,
//     return ErrReplay without advancing.
//
//  6. Advance stored UL count to max(stored, count). Stamp RxMeta.
//     Return (plain-with-inner-SHT-0-prepended, meta, nil).
//
//  7. Inner expansion per §4.4.6 (NAS message container): this is a
//     PROTOCOL-LAYER concern (the inner is a second 5GMM PDU), not a
//     SECURITY concern. The security manager returns the deciphered
//     outer bytes; gmm/dispatch.go's §4.4.6 re-dispatch stays where
//     it is, just feeding RxNAS output into itself.
//
// # Send path — flow
//
// TxNAS (SHT=1 or SHT=2 path, post-SMC steady state):
//
//  1. Check that context is "activated" (I6) i.e. ActivateCtx has run
//     and counts / keys are loaded. If not, return ErrNotActivated;
//     caller must use TxPlain.
//  2. Read inner (EPD+SHT=0+msgType+body). Pick SHT: if EEA!=0 use 2,
//     else 1 (§4.4.5 "Ciphering of NAS signalling messages").
//  3. Cipher body with KNASEnc (skip when SHT=1). Build MAC input =
//     SQN(low byte of DL count) || ciphered-body. Compute MAC with
//     KNASInt/EIA/count/bearer=1/direction=DL.
//  4. Emit EPD || SHT || MAC(4) || SQN(1) || body.
//  5. DL count += 1 per §4.4.3.1.
//
// TxSMC (SHT=3 path, TS 33.501 §6.7.2 step 1b):
//
//  1. DL count ← 0 (§6.7.2 does not spell this out directly but
//     §6.7.2 step 2b + §4.4.3.1 "value that shall be used in the next
//     NAS message" together mandate a fresh count for the new ctx;
//     implementations universally reset to 0 here).
//  2. SHT = 3, MAC computed with NEW KNASInt over SQN||body. No cipher.
//  3. Emit. DL count ← 1.
//
// TxPlain (SHT=0): emit EPD || 0 || msgType || body unchanged. No MAC.
// Count unchanged. Used for Auth Request + Identity Request.
//
// # Handler-visible rule set (single sentence each)
//
//  R1. To receive NAS: call security.RxNAS(ctx, pdu) once; use the
//      plaintext + meta it returns. Never call again.
//  R2. To send DL NAS post-SMC: call security.TxNAS(ctx, inner).
//  R3. To send DL NAS pre-SMC: call security.TxPlain(ctx, inner).
//  R4. To send SMC: call security.TxSMC(ctx, inner) after ActivateCtx.
//  R5. To attach K_gNB to an ICS Request: call security.DeriveKgNB(ctx)
//      at encode time. Do not stash it anywhere.
//  R6. To reuse a cached context per §4.4: call security.Reuse(…).
//      The return value is authoritative; no re-verification allowed.
//
// # Concurrency
//
// Per-UE: the *Context carries an internal sync.Mutex. RxNAS, TxNAS,
// TxSMC, TxPlain, ActivateCtx, DeriveKgNB, Reuse, ResetOnHandover all
// take it. NGAP and GMM today run on the same goroutine per UE so
// contention is nil in practice, but the lock guarantees that a future
// Paging-concurrent-with-UL-NAS race can't corrupt count state.
//
// Cross-UE: no global state. The manager is a thin holder of a lookup
// map (IMSI → *Context) with an RWMutex; lookups are read-only and
// mutations go through Manager.Insert / Manager.Remove.
//
// # Migration from the scattered model — completed (this section
//   is kept for historical reference; all steps are landed on main)
//
// Step 1. Golden-trace tests — done as security/primitives_test.go
//         (wrap↔unwrap round-trips for SHT 1-4, MAC-tamper rejection,
//         plain passthrough, plus kgnb_test.go golden-vector check
//         against sacrypto.ConvA9 for DeriveKgNB).
//
// Step 2+3. Move primitives + introduce RxNAS / TxDL / TxSMC / TxPlain
//         + flip dispatch.go to RxNAS — commit e6d2b0e. Also added
//         security.Reuse (step 5) and security.DecipherContainer (§4.4.6)
//         in the same commit. nf/amf/gmm/nas_security.go deleted there.
//
// Step 4. K_gNB derivation moved to security.DeriveKgNB called from
//         initialctxsetup.Send — commit f9e3d67. The three scattered
//         sites (smc.go, registration.go §4.4, service.go) are gone
//         along with the (ULNasCount-1) compensation pattern.
//
// Step 5. §4.4 cached-context reuse lives behind security.Reuse —
//         commit e6d2b0e wired case-i from registration.go via
//         ue.Security.LastRxNASPDU; commit fff6a77 replaced that
//         with the Handler signature's outerPDU plumbing.
//
// Step 6. ue.Security.LastRxNASPDU + ue.Security.KgNB removed —
//         commit fff6a77. Handler signature grew an outerPDU param
//         so dispatch threads the secured wire PDU through §4.4.6
//         re-dispatch to the registration handler.
//
// Step 7. security.ActivateCtx centralises the SMC key install
//         (K_NASEnc / K_NASInt via §A.8, NGKSI / EEA / EIA stamp,
//         UL/DL count reset per §4.4.3.1 + §6.7.2) — commit 4a7e080.
//         gmm/smc.go no longer touches ue.Security.K* or count fields.
//
// # Out of scope
//
//  - User-plane security (UP integrity / UP confidentiality). Different
//    key hierarchy, negotiated per QoS flow at PDU Session setup.
//  - Non-3GPP access (N3IWF / TNGF / TWIF / WAGF). TS 33.501 §A.9
//    access-type distinguisher switches to 0x02, and TS 24.501 §4.4.3.1
//    requires a separate NAS COUNT pair per access. Scaffolded for but
//    not implemented in the first cut.
//  - 5GSM security. §4.4 para 1 verbatim: "5GSM messages are security
//    protected indirectly by being piggybacked by the security
//    protected 5GMM messages" — no per-5GSM operation needed.
//  - Mapped 5G NAS security context (EPS→5G inter-system change,
//    §4.4.2.1). Out of scope for the first cut but ActivateCtx's
//    signature accommodates the future mapped-ctx path.
package security
