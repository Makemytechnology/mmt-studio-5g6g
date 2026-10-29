<!-- Copyright (c) 2026 MakeMyTechnology. All rights reserved. -->

# TSN AF + TSCTSF — Rel-19 Time Sensitive Communication design

Spec grounding (all Rel-19 texts live in `core/specs/3gpp/`):

| Spec | Version | What we take from it |
|------|---------|----------------------|
| TS 23.501 | 19.7.0 | §5.27 TSC framework (TSCAI §5.27.2, TSC QoS §5.27.3, hold-and-forward §5.27.4, bridge delay §5.27.5, time sync §5.27.1), §5.28 5GS-bridge management |
| TS 23.502 | 19.7.0 | §4.3.2.2/§4.3.3.2 PMIC transfer in PDU session procedures, §5.2.27 Ntsctsf services |
| TS 23.503 | 19.7.0 | §6.1.3.23/23a/23b PCF↔TSN AF / TSCTSF / DetNet responsibilities |
| TS 24.501 | 19.6.2 | §9.11.4.25 DS-TT MAC (IEI 0x6E), §9.11.4.26 UE-DS-TT residence time (IEI 0x6F, IEEE 1588 correctionField encoding), §9.11.4.27 PMIC (IEI 0x74 TLV-E) |
| TS 24.539 | 19.3.0 | Port/user-plane-node management service messages (PMIC/UMIC payloads): MANAGE PORT COMMAND/COMPLETE, PORT MANAGEMENT NOTIFY..., operation codes, parameter IDs |
| TS 29.244 | 19.5.0 | PFCP TSC IEs: Create Bridge Info for TSC (194), DS-TT/NW-TT Port Number (196/197), 5GS User Plane Node ID (198), TSC Management Information (199–201), PMIC (202), Clock Drift (203–210) |
| TS 29.512 | 19.6.0 | PccRule.tscaiInputUl/Dl/tscaiTimeDom/capBatAdaptation, SmPolicyDecision/UpdateContextData TSN containers, trigger `TSN_BRIDGE_INFO` |
| TS 29.514 | 19.6.0 | AppSessionContextReqData TSC attributes, TscaiInputContainer, events `TSN_BRIDGE_INFO`, `BAT_OFFSET_INFO` |
| TS 29.565 | 19.7.0 | Ntsctsf_TimeSynchronization / QoSandTSCAssistance / ASTI resource + data model |

## Architecture on this codebase

The studio uses in-process service calls in place of HTTP SBI (see `docs/design/nf/pcf.md`);
TSN AF and TSCTSF follow the same convention: Go function surface == service operation,
REST routes in the webservice expose the operator/CNC-facing side.

```
      CNC (REST /api/tsn/cnc/*)          AF (REST /api/tsctsf/*)
             │ 802.1Qcc                        │ N85-equivalent
             ▼                                 ▼
   ┌──── core/nf/tsnaf ────┐        ┌──── core/nf/tsctsf ────┐
   │ bridge registry        │        │ TimeSynchronization    │
   │ §5.27.5 delay calc     │        │ QoSandTSCAssistance    │
   │ §5.28.4 QoS mapping    │        │ ASTI                   │
   │ PMIC/UMIC (TS 24.539)  │        │ PTP instance → PMIC    │
   └───────────┬────────────┘        └───────────┬────────────┘
               │  N5 in-process (29.514 shapes)  │
               ▼                                 ▼
        core/nf/pcf  ──  PCC rules + TSCAC + containers (29.512 shapes)
               │ N7 in-process (smpolicy)
               ▼
        core/nf/smf/session ── TSCAI derivation §5.27.2.4
           │            │                │
           │ NAS PMIC   │ NGAP           │ PFCP: bridge info req/resp,
           │ (0x74)     │ TSCTraffic-    │ TSCMI (PMIC/UMIC), clock drift
           ▼            │ Characteristics▼
        UE/DS-TT        ▼             core/nf/upf  (NW-TT: port alloc,
                     gNB (via AMF      UPN ID, UMIC param store,
                     pdusetup)         Ethernet PDI + MAC reporting)
```

## Container travel paths (TS 23.501 §5.28.3)

- **DS-TT→AF**: PMIC in NAS PDU Session Establishment/Modification (IEI 0x74) → SMF →
  `smpolicy.Update` trigger `TSN_BRIDGE_INFO` → PCF → `TSN_BRIDGE_INFO` event → TSN AF/TSCTSF.
- **AF→DS-TT**: `tsnPortManContDstt` in N5 → PCC decision → SMF network-requested PDU Session
  Modification Command carrying PMIC.
- **NW-TT↔AF**: PMIC/UMIC inside PFCP TSC Management Information (IE 199/200/201) on N4 →
  SMF ↔ PCF as above (`tsnPortManContNwtts` / `tsnBridgeManCont`).

## TSCAI derivation (TS 23.501 §5.27.2.4 — implemented in `smf/session/tscai.go`)

- DL BAT = TSCAC BAT (clock-drift corrected when time domain matches a UPF report) + CN PDB.
- UL BAT = TSCAC BAT + UE-DS-TT residence time (from NAS IE 0x6F; TSN AF preconfig fallback).
- Periodicity corrected by cumulative rateRatio when the UPF has reported one (IE 210).
- Survival time in messages → `count × periodicity`.
- NGAP: `TSCTrafficCharacteristics{DL/UL TSCAssistanceInformation{Periodicity, BurstArrivalTime}}`
  attached to the QoS flow (id 196 extension); BAT offset feedback surfaces as the
  `BAT_OFFSET_INFO` AF event (EnTSCAC).

## Bridge delay (TS 23.501 §5.27.5 — implemented in `tsnaf/bridge.go`)

Per port pair per traffic class:
- `independentDelayMin/Max = UE-DS-TT residence time (or configured min/max) + configured CN delay min/max`
- `dependentDelayMin/Max` = per-octet store-and-forward time at port link speed.
- txPropagationDelay arrives via PMIC parameter 0x0001 (ns × 2^16 per TS 24.539).

## 5GS bridge model (TS 23.501 §5.28.1)

- One bridge per UPF per network instance; UPN ID allocated by the UPF (IE 198, uint64).
- DS-TT port number allocated by UPF at PDU session establishment (IE 196 in the
  Created Bridge Info grouped reply); NW-TT ports preconfigured on the UPF.
- TSN AF binds DS-TT MAC ↔ PDU session ↔ port number ↔ bridge and reports capabilities
  (delays, traffic classes, gate params per IEEE 802.1Q) to the CNC surface.

## Database

Rel-19 TSC state is runtime state owned by the TSN AF / TSCTSF registries (bridge
ports, delays, PTP instances, ASTI configs live for the PDU-session / AF-session
lifetime). The existing `tsn_*` tables keep their shape and are populated by the
TSN AF as the live view for the GUI: discovered 5GS bridges land in `tsn_bridges`
(`persistBridge`), CNC-configured streams in `tsn_streams` (`persistStream`).
Everything else is exposed over `/api/tsnaf/*` and `/api/tsctsf/*` directly from
the in-memory registries.

## Codec work

- `nasgen` definitions gain `DSTTEthernetPortMACAddress` (0x6E), `UEDSTTResidenceTime` (0x6F)
  and PMIC (0x74) on PDU SESSION ESTABLISHMENT REQUEST per Table 8.3.1.1.1 (regenerated).
- TS 24.539 PMIC/UMIC codec implemented at `core/edge/tsn/ttmgmt` (pure Go, no generator):
  message types §9.1, operation codes, port parameters (0x0001 txPropagationDelay …
  0x00E9 PTP instance list), user-plane-node parameters (0x0001 address, 0x0003 node ID,
  0x0004 NW-TT port numbers, 0x0012 static filtering entries, 0x0090–0x0092 TSS).
- PFCP grouped TSC IEs composed from existing generated leaf IEs (194, 196–198, 199–202).

## Time sync & DetNet (Rel-19 follow-up)

- `core/edge/tsn/gptp` — IEEE 802.1AS PDU codec + the TS 23.501 §5.27.1.2.2
  ingress/egress translator transforms (TSi suffix TLV across the 5GS, link-delay +
  rateRatio update at ingress, rateRatio-scaled residence time into correctionField at
  egress) and the §5.27.1.7 5GS-grandmaster Sync/Follow_Up generator. NW-TT glue:
  `NWTT.GPTPIngress/GPTPEgress/GenerateGMSyncPair`.
- TSCTSF DetNet intake (`nf/tsctsf/detnet.go`) — RFC 9633 traffic-specification JSON →
  TscQosRequirement per the TS 23.503 §6.1.3.23b mapping table; REST `/api/tsctsf/detnet`.

## Dataplane enforcement (C, libupf_dp)

- `upf_tsc_gate_t` per session + `upf_tsc_gate_is_open()` (IEEE 802.1Qbv cyclic
  schedule walk) enforced in `upf_process_packet` step 2a½: DL packets in a closed
  window are HELD in the session dl_buf and flushed when the gate reopens
  (hold & forward, TS 23.501 §5.27.4); UL closed-window packets are dropped and
  counted (the UL hold buffer is DS-TT-side).
- Programming path: TSN AF PMIC set-parameters → NW-TT param store →
  `pfcp.SetTSCGateSink` (wired by upfloop to the pre-swap cgo bridge) →
  `upf_dp_set_tsc_gate`.
- Ethernet MAC learning: `upf_dp_learn_mac`/`upf_dp_get_macs` session table fed from
  the PDI Ethernet Packet Filter MACs via `NWTT.RegisterDetectedMAC` → MAC sink.
- DPDK 25.11 + libupf_dp.so build once via `make dpdk && make upf` and are reused
  (Makefile skips when `build/lib/librte_hash.so` exists).

## Gap closure round 2 (steering / BMCA / RESTCONF)

- **PTP steering in the fast path** — `dataplane/src/upf_ptp.c`: PTP-over-UDP
  (IEEE 1588 Annex C, ports 319/320) event messages are detected in the UL/DL
  packet path; DL ingress stamps the TSi suffix (5GS-internal TLV shared with the
  Go codec), UL egress folds the residence time into correctionField and strips it
  (§5.27.1.2.2). Enabled per session via `upf_dp_set_ptp_steering`, switched on when
  a PTP instance list PMIC is provisioned (TSCTSF ConfigCreate → NW-TT →
  `pfcp.SetTSCPTPSink` → cgo). Cross-implementation interop is pinned by tests: a
  C-stamped suffix decodes with the Go gptp codec and vice versa. The 802.1AS
  L2-Ethernet transport still terminates at the NW-TT Go surface (the IP data path
  carries no raw Ethernet frames).
- **BMCA** — `edge/tsn/gptp/bmca.go`: IEEE 1588-2019 §9.3.4 dataset comparison and
  the §9.3.3 recommended-state decision (Leader/Follower/Passive), Announce receipt
  timeout re-evaluation (§5.27.1.5), one engine per (g)PTP domain on the NW-TT
  (`ProcessAnnounce`/`PortStates`); port states surface through PMIC
  portDS.portState and gate the §5.27.1.7 GM Sync generation (no Sync sourced while
  an external master wins).
- **RESTCONF** — `webservice/app/routes_restconf.go`: RFC 8040 binding for the
  RFC 9633 DetNet YANG (root discovery, `/restconf/data/ietf-detnet:detnet`
  GET/POST/PUT/DELETE, `application/yang-data+json`, `ietf-restconf:errors`).
  TS 23.501 §5.28.5.3 permits "Netconf/Restconf"; NETCONF over SSH (RFC 6242) is
  intentionally not implemented. The 3GPP §6.1.3.23b extensions ride
  `mmt-3gpp-detnet:*` augmentation names, including target-supi/target-dnn as the
  studio's stand-in for source-IP → UE resolution.

## PFCP wire coverage (complete)

Every TSC exchange crosses real PFCP messages (E2E-tested over UDP loopback in
`nf/smf/upfclient/pfcp_tsc_e2e_test.go`):

| Exchange | Wire | Direction |
|---|---|---|
| Bridge info request/report | IEs 194 / 195 (196-198 inside) | SMF ↔ UPF, Establishment |
| PMIC / UMIC | TSC Management Information 199 / 200, PMIC 202, UMIC 266 | SMF ↔ UPF, Modification |
| Clock drift arming | Clock Drift Control Information 203 (RRTO\|RRCR 204) | SMF → UPF, Association Setup |
| Clock drift report | Node Report Request (CKDR) → Clock Drift Report 205 (206/209/210) | UPF → SMF |
| Ethernet stream filter | PDI Ethernet Packet Filter 132 (MAC 133 DEST + C-TAG 134 VID) | SMF → UPF, Modification |
| MAC addresses detected | Session Report Usage Report → Ethernet Traffic Information 143 (§8.2.98) | UPF → SMF |

Downstream of the wire: clock drift reports feed `ApplyClockDriftReport` →
§5.27.2.4 TSCAI; detected MACs fire the UE_MAC_CH trigger (TS 29.512 §5.6.3.6)
toward the PCF and bind to the DS-TT port at the TSN AF.
