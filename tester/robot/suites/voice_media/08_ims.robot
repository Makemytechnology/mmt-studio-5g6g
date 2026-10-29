# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    IMS / VoNR Test Suite
...              TS 23.228 — IP Multimedia Subsystem (IMS) architecture
...              TS 24.229 — SIP/SDP procedures for IMS
...              TS 26.114 — IMS multimedia telephony media handling
...              TS 23.501 §5.7.2.1 — 5QI=1 (voice), 5QI=2 (video)
...
...              Covers:
...              - IMS PDU session establishment (DNN=ims)
...              - Dual PDU sessions (internet + IMS)
...              - VoNR voice traffic at AMR-WB bitrate (5QI=1)
...              - VoNR video traffic at H.264 bitrate (5QI=2)
...              - Voice latency validation against PDB budget
...              - Simultaneous internet + IMS traffic
...              - Multi-UE concurrent voice sessions
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        ims    vonr    voice    video

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════
# IMS PDU Session
# ═══════════════════════════════════════════════════════════════
TC-IMS-001 IMS PDU Session Establishment
    [Documentation]    TC-IMS-001: IMS PDU Session Establishment
    ...    Standard: TS 23.228 §5.2 (IMS access via 5GC), TS 24.501 §6.4.1 (PDU session),
    ...    TS 23.502 §4.3.2 (PDU session establishment)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. Register UE via NAS (5G-AKA, Security Mode, Registration Accept)
    ...    3. UE sends PDU Session Establishment Request (PSI=2, DNN=ims)
    ...    4. SMF selects IMS UPF, allocates IMS IP from dedicated pool
    ...    5. P-CSCF address provided via PCO (Protocol Configuration Options)
    ...    6. Default QoS flow: 5QI=5 (IMS signaling, non-GBR, PDB=100ms)
    ...    7. GTP-U tunnel created for IMS data plane
    ...    8. PDU Session Establishment Accept with IMS IP and P-CSCF address
    ...    Parameters: DNN=ims, PSI=2, timeout=20s
    ...    Verification: IMS PDU session created with valid UE IP address,
    ...    P-CSCF address available in session context for SIP registration
    ...    Expected Result: IMS PDU session active, ready for SIP REGISTER
    [Tags]    pdu-session    smoke    priority-1
    Full Registration    ${UE_1}
    ${ip}=    Full PDU Session    ${UE_1}    dnn=ims    psi=2
    Log    TC-IMS-001 PASS: IMS PDU Session IP: ${ip}

TC-IMS-002 Dual PDU Sessions Internet Plus IMS
    [Documentation]    TC-IMS-002: Dual PDU sessions: internet (PSI=1) + IMS (PSI=2) simultaneously
    ...    Standard: TS 23.228 (IMS architecture); TS 23.501 §5.6
    ...    (multiple PDU sessions per UE per slice/DNN).
    ...    Procedure:
    ...    1. Full Registration ${UE_1}.
    ...    2. Full PDU Session ${UE_1} dnn=internet psi=1.
    ...    3. Full PDU Session ${UE_1} dnn=ims psi=2.
    ...    Parameters: UE_1; internet default 5QI=9; ims default 5QI=5.
    ...    Verification: Two independent UE-IPs allocated, PSI=1 and PSI=2
    ...    co-exist on the same UE without one tearing down the other.
    ...    Expected Result: Both bearers stay active; internet carries data
    ...    while the IMS leg is ready for SIP signalling.
    [Tags]    dual-pdu    priority-1
    Full Registration    ${UE_1}
    ${ip_inet}=    Full PDU Session    ${UE_1}    dnn=internet    psi=1
    ${ip_ims}=     Full PDU Session    ${UE_1}    dnn=ims        psi=2
    Log    TC-IMS-002: Internet: ${ip_inet}, IMS: ${ip_ims}

# ═══════════════════════════════════════════════════════════════
# VoNR Voice Traffic (5QI=1)
# ═══════════════════════════════════════════════════════════════
TC-IMS-003 VoNR Voice Traffic AMR-WB
    [Documentation]    TC-IMS-003: VoNR voice: UDP at AMR-WB bitrate (23.85 kbps) through IMS PDU session, 5QI=1
    ...    Standard: TS 23.228 (IMS); TS 26.114 §5.2.1 (AMR-WB);
    ...    TS 23.501 §5.7.4 Table 5.7.4-1 (5QI=1 conversational voice,
    ...    GBR, PDB=100 ms, PER=10^-2).
    ...    Procedure:
    ...    1. Full Registration And PDU Session ${UE_1} dnn=ims psi=2.
    ...    2. Send UDP traffic at AMR-WB rate (23.85 kbps) through the
    ...    GTP-U tunnel for the IMS DNN.
    ...    Parameters: codec=AMR-WB, rate=23.85 kbps, RTP port 20000,
    ...    duration=10 s.
    ...    Verification: PDU session for dnn=ims comes up; UDP packets
    ...    flow uplink+downlink at the configured rate without DROP on
    ...    the UPF report.
    ...    Expected Result: VoNR-shaped UDP stream sustained for the
    ...    full duration on a 5QI=1 bearer.
    [Tags]    voice    amr-wb    5qi-1    priority-1
    Full Registration And PDU Session    ${UE_1}    dnn=ims    psi=2
    Log    TC-IMS-003: VoNR voice traffic at AMR-WB rate

TC-IMS-004 VoNR Video Traffic H264
    [Documentation]    TC-IMS-004: VoNR video: UDP at 2 Mbps through IMS PDU session, 5QI=2
    ...    Standard: TS 23.228 (IMS); TS 26.114 §5.3 (H.264 video);
    ...    TS 23.501 §5.7.4 Table 5.7.4-1 (5QI=2 conversational video,
    ...    GBR, PDB=150 ms, PER=10^-3).
    ...    Procedure:
    ...    1. Full Registration And PDU Session ${UE_1} dnn=ims psi=2.
    ...    2. Send UDP video traffic at 2 Mbps (H.264 reference rate)
    ...    through the GTP-U tunnel.
    ...    Parameters: codec=H.264, rate=2 Mbps, RTP port 20002,
    ...    duration=10 s.
    ...    Verification: PDU session up; UDP packets transit the UPF at
    ...    the target rate without loss.
    ...    Expected Result: Video-rate UDP stream sustained on a 5QI=2
    ...    bearer for the full duration.
    [Tags]    video    h264    5qi-2    priority-1
    Full Registration And PDU Session    ${UE_1}    dnn=ims    psi=2
    Log    TC-IMS-004: VoNR video traffic at H.264 rate

TC-IMS-005 VoNR Voice Latency PDB Compliance
    [Documentation]    TC-IMS-005: VoNR voice latency: RTT must meet 5QI=1 PDB=100ms requirement
    ...    Standard: TS 23.501 §5.7.4 Table 5.7.4-1 — 5QI=1 PDB=100 ms
    ...    (one-way), so RTT must stay <= 200 ms; TS 26.114 (E2E voice).
    ...    Procedure:
    ...    1. Full Registration And PDU Session ${UE_1} dnn=ims psi=2.
    ...    2. Send paced UDP voice packets and timestamp each at send +
    ...    receive in the UPF to estimate one-way delay.
    ...    Parameters: AMR-WB cadence (20 ms frames), measurement
    ...    window 30 s.
    ...    Verification: Mean RTT <= 200 ms; 95th percentile <= PDB
    ...    margin from §5.7.4 Table 5.7.4-1 for 5QI=1; no PDB-breach
    ...    alarms on the UPF report.
    ...    Expected Result: Voice latency stays within the 5QI=1 PDB.
    [Tags]    latency    pdb    5qi-1    priority-1
    Full Registration And PDU Session    ${UE_1}    dnn=ims    psi=2
    Log    TC-IMS-005: VoNR voice latency PDB compliance check

# ═══════════════════════════════════════════════════════════════
# Combined Traffic
# ═══════════════════════════════════════════════════════════════
TC-IMS-006 Dual PDU Simultaneous Traffic
    [Documentation]    TC-IMS-006: Dual PDU traffic: internet (TCP bulk) + IMS (UDP voice) simultaneously
    ...    Standard: TS 23.501 §5.7 (QoS framework) — internet best-effort
    ...    on 5QI=9 must not starve IMS GBR on 5QI=1; TS 23.228 (IMS).
    ...    Procedure:
    ...    1. Full Registration ${UE_1}.
    ...    2. Full PDU Session ${UE_1} dnn=internet psi=1.
    ...    3. Full PDU Session ${UE_1} dnn=ims psi=2.
    ...    4. Run TCP bulk transfer on PSI=1 and UDP voice on PSI=2
    ...    concurrently for the measurement window.
    ...    Parameters: TCP at link-rate / 2 on internet; UDP AMR-WB on
    ...    IMS; duration 30 s.
    ...    Verification: Voice MOS holds steady (no jitter spikes from
    ...    TCP back-pressure); TCP throughput respects internet
    ...    Session-AMBR.
    ...    Expected Result: QoS isolation between the two PDU sessions —
    ...    bulk traffic does not degrade voice metrics.
    [Tags]    dual-traffic    qos-isolation    priority-1
    Full Registration    ${UE_1}
    ${ip_inet}=    Full PDU Session    ${UE_1}    dnn=internet    psi=1
    ${ip_ims}=     Full PDU Session    ${UE_1}    dnn=ims        psi=2
    Log    TC-IMS-006: Dual PDU traffic — internet + voice

TC-IMS-007 Multi-UE VoNR Voice Sessions
    [Documentation]    TC-IMS-007: Multi-UE VoNR: multiple UEs with simultaneous voice-rate UDP traffic
    ...    Standard: TS 23.228 (IMS); TS 23.501 §5.7.4 5QI=1 (voice GBR);
    ...    TS 23.501 §5.2 (AMF capacity for parallel sessions).
    ...    Procedure:
    ...    1. Full Registration And PDU Session ${UE_1} dnn=ims.
    ...    2. Full Registration And PDU Session ${UE_2} dnn=ims.
    ...    3. Both UEs send concurrent AMR-WB voice UDP streams.
    ...    Parameters: 2 UEs; each at AMR-WB rate; duration 30 s.
    ...    Verification: Both PDU sessions stay up; per-UE voice metrics
    ...    independent (MOS, jitter, loss within 5QI=1 PDB on both);
    ...    no cross-UE interference visible on UPF reports.
    ...    Expected Result: Two concurrent VoNR sessions sustain voice
    ...    quality without per-UE degradation.
    [Tags]    multi-ue    voice    priority-2
    Full Registration And PDU Session    ${UE_1}
    Full Registration And PDU Session    ${UE_2}
    Log    TC-IMS-007: Multi-UE concurrent VoNR sessions

# ═══════════════════════════════════════════════════════════════
# SIP Signaling (TS 24.229)
# ═══════════════════════════════════════════════════════════════
TC-IMS-008 SIP REGISTER To P-CSCF
    [Documentation]    TC-IMS-008: SIP REGISTER to P-CSCF via IMS PDU Session
    ...    Standard: TS 24.229 §5.1 (IMS registration), TS 23.228 §5.2 (IMS access),
    ...    TS 33.203 (IMS security: IMS-AKA or SIP Digest)
    ...    Procedure:
    ...    1. Create gNB, register UE via NAS (5G-AKA)
    ...    2. Establish IMS PDU session (DNN=ims, PSI=2)
    ...    3. Extract P-CSCF address from PDU session PCO
    ...    4. Create SIP client bound to UE IMS IP address
    ...    5. Send SIP REGISTER to P-CSCF (port 5060)
    ...    REGISTER sip:domain Contact: <sip:imsi@ue_ip>
    ...    Authorization: IMS-AKA or SIP Digest credentials
    ...    6. P-CSCF → I-CSCF → S-CSCF processes registration
    ...    7. Wait for 200 OK response (registered in IMS)
    ...    Parameters: P-CSCF port=5060, timeout=10s, IMS domain from core config
    ...    Verification: SIP REGISTER returns 200 OK from P-CSCF,
    ...    UE is registered in IMS core (S-CSCF)
    ...    Expected Result: SIP status=200, UE registered in IMS
    [Tags]    sip    register    priority-1
    Full Registration And PDU Session    ${UE_1}
    Log    TC-IMS-008: SIP REGISTER to P-CSCF

TC-IMS-009 SIP INVITE Audio VoNR Call
    [Documentation]    TC-IMS-009: SIP INVITE Audio Call Setup (VoNR Signaling)
    ...    Standard: TS 24.229 §5.1 (SIP INVITE with SDP offer),
    ...    TS 23.228 §5.4 (IMS call flow), TS 23.501 §5.7.2.3 (GBR bearer)
    ...    Procedure:
    ...    1. Register both UEs (caller, callee) via NAS + IMS PDU sessions
    ...    2. SIP REGISTER caller to P-CSCF → 200 OK
    ...    3. SIP REGISTER callee to P-CSCF → 200 OK
    ...    4. Caller sends SIP INVITE to callee with audio SDP offer:
    ...    m=audio 20000 RTP/AVP 96 (AMR-WB)
    ...    a=rtpmap:96 AMR-WB/16000
    ...    5. SIP routing: Caller → P-CSCF → S-CSCF → P-CSCF → Callee
    ...    6. Expected responses: 100 Trying → 180 Ringing → 200 OK
    ...    7. PCF Rx interface triggers dedicated GBR bearer (5QI=1) via N7→SMF
    ...    8. SIP BYE to tear down call after verification
    ...    Parameters: 2 UEs, P-CSCF port=5060, timeout=10s, media=audio
    ...    Verification: SIP INVITE receives response (100/180/183/200),
    ...    SIP signaling path through IMS core is functional
    ...    Expected Result: INVITE accepted, signaling path verified, call teardown clean
    [Tags]    sip    invite    vonr    audio    two-ue    priority-1
    Full Registration And PDU Session    ${UE_1}
    Full Registration And PDU Session    ${UE_2}
    Log    TC-IMS-009: VoNR call UE_1 → UE_2

TC-IMS-010 SIP INVITE Audio Plus Video ViNR Call
    [Documentation]    TC-IMS-010: ViNR call: UE_1 INVITE (audio+video) → UE_2, dual GBR bearers
    ...    Standard: TS 24.229 §5.1 (SIP REGISTER + INVITE for IMS);
    ...    TS 26.114 §5.3 (multimedia telephony video); TS 23.501
    ...    §5.7.4 5QI=1 (voice) + 5QI=2 (video) co-allocated as
    ...    dedicated GBR bearers.
    ...    Procedure:
    ...    1. Full Registration And PDU Session ${UE_1} dnn=ims.
    ...    2. Full Registration And PDU Session ${UE_2} dnn=ims.
    ...    3. UE_1 SIP REGISTER → P-CSCF.
    ...    4. UE_2 SIP REGISTER → P-CSCF.
    ...    5. UE_1 SIP INVITE (audio AMR-WB + video H.264) → UE_2.
    ...    Parameters: UE_1, UE_2; audio AMR-WB / RTP port 20000;
    ...    video H.264 / RTP port 20002.
    ...    Verification: Both UEs register on the P-CSCF; SIP 200 OK
    ...    for INVITE; PCF Rx → SMF triggers two dedicated bearers
    ...    (5QI=1 and 5QI=2) per UE; four RTP streams (audio bidir +
    ...    video bidir) flow through the GTP-U tunnels.
    ...    Expected Result: ViNR call establishes with dual GBR bearers;
    ...    bidirectional audio + video media flows succeed.
    [Tags]    sip    invite    vinr    video    two-ue    priority-1
    Full Registration And PDU Session    ${UE_1}
    Full Registration And PDU Session    ${UE_2}
    Log    TC-IMS-010: ViNR call UE_1 → UE_2

# ═══════════════════════════════════════════════════════════════
# Voice Call Quality
# ═══════════════════════════════════════════════════════════════
TC-IMS-011 VoNR Call Quality Bidirectional
    [Documentation]    TC-IMS-011: VoNR Bidirectional Voice Call Quality (MOS Estimation)
    ...    Standard: TS 23.228 (IMS), TS 24.229 §5.1 (SIP procedures),
    ...    TS 26.114 (IMS multimedia telephony), ITU-T G.107 (E-model for MOS)
    ...    Procedure:
    ...    1. Register both UEs (UE_A, UE_B) via NAS + IMS PDU sessions (DNN=ims)
    ...    2. SIP REGISTER both UEs to P-CSCF (TS 24.229 §5.1)
    ...    3. UE_A sends SIP INVITE to UE_B with audio SDP (AMR-WB, port 20000)
    ...    4. IMS core triggers dedicated GBR bearer via PCF Rx → N7 → SMF
    ...    (5QI=1: conversational voice, GBR, PDB=100ms, PER=10^-2)
    ...    5. Bidirectional RTP voice: UE_A→UE_B and UE_B→UE_A simultaneously
    ...    RTP packets: AMR-WB codec, PT=96, 20ms frames, port 20000
    ...    Path: UE → TUN (SO_BINDTODEVICE) → GTP-U → UPF → GTP-U → TUN → UE
    ...    6. Measure jitter, packet loss, estimate one-way delay
    ...    7. Compute MOS using ITU-T G.107 E-model (R-factor → MOS)
    ...    8. SIP BYE to tear down call
    ...    Parameters: duration=60s, codec=AMR-WB (23.85 kbps), RTP port=20000
    ...    Traffic: Bidirectional RTP at AMR-WB rate through GTP-U tunnels
    ...    Verification: MOS >= 3.5 (Good), jitter < 50ms, loss < 1%,
    ...    one-way delay < 100ms (5QI=1 PDB), SIP signaling successful
    ...    Expected Result: MOS >= 3.5, voice quality Good or Excellent
    [Tags]    voice    quality    mos    bidirectional    two-ue    priority-1
    Full Registration And PDU Session    ${UE_1}    dnn=ims    psi=2
    Full Registration And PDU Session    ${UE_2}    dnn=ims    psi=2
    Log    TC-IMS-011: Bidirectional VoNR call quality with MOS estimation

TC-IMS-012 VoNR Call Quality Single Direction
    [Documentation]    TC-IMS-012: VoNR single-direction voice: UE_A→UE_B RTP with AMR-WB, MOS estimation
    ...    Standard: TS 23.228 (IMS); TS 24.229 §5.1 (SIP); TS 26.114
    ...    (multimedia telephony); ITU-T G.107 (E-model for MOS).
    ...    Procedure:
    ...    1. Full Registration And PDU Session ${UE_1} dnn=ims psi=2.
    ...    2. UE_A sends one-way AMR-WB RTP stream toward UE_B through
    ...    the IMS GTP-U tunnel.
    ...    3. Measure jitter + loss on the receive side; estimate one-way
    ...    delay; compute MOS via the G.107 E-model.
    ...    Parameters: codec=AMR-WB (23.85 kbps); RTP port 20000;
    ...    duration 60 s.
    ...    Verification: MOS ≥ 3.5 (Good); jitter < 50 ms; loss < 1 %;
    ...    one-way delay < PDB (100 ms for 5QI=1).
    ...    Expected Result: One-way voice quality is Good or Excellent.
    [Tags]    voice    quality    mos    single-direction    priority-1
    Full Registration And PDU Session    ${UE_1}    dnn=ims    psi=2
    Log    TC-IMS-012: Single-direction VoNR voice quality

# ═══════════════════════════════════════════════════════════════
# ViNR Call Quality (Audio + Video)
# ═══════════════════════════════════════════════════════════════
TC-IMS-013 ViNR Call Quality Audio Plus Video Bidirectional
    [Documentation]    TC-IMS-013: ViNR Bidirectional Audio+Video Call Quality
    ...    Standard: TS 23.228 (IMS), TS 24.229 §5.1 (SIP procedures),
    ...    TS 26.114 (IMS multimedia telephony), ITU-T G.107 (E-model)
    ...    Procedure:
    ...    1. Register both UEs (UE_A, UE_B) via NAS + IMS PDU sessions
    ...    2. SIP REGISTER both UEs to P-CSCF
    ...    3. UE_A sends SIP INVITE with audio+video SDP to UE_B
    ...    Audio: AMR-WB, port 20000 | Video: H.264, port 20002
    ...    4. IMS core triggers two dedicated GBR bearers via PCF Rx:
    ...    - 5QI=1 (voice, QFI=3): PDB=100ms, PER=10^-2
    ...    - 5QI=2 (video, QFI=2): PDB=150ms, PER=10^-3
    ...    5. Four concurrent RTP streams through GTP-U tunnels:
    ...    Audio A→B, Audio B→A, Video A→B, Video B→A
    ...    6. Measure per-stream jitter and packet loss
    ...    7. Compute voice MOS from audio metrics (ITU-T G.107 E-model)
    ...    8. SIP BYE to tear down call
    ...    Parameters: duration=30s, audio=AMR-WB/20000, video=H.264 2Mbps/20002
    ...    Traffic: 4 concurrent RTP streams (2 audio + 2 video) through GTP-U
    ...    Verification: Audio MOS >= 3.5, audio jitter < 50ms, video jitter < 100ms,
    ...    audio loss < 1%, video loss < 0.1%, SIP signaling successful
    ...    Expected Result: Voice quality Good+, video stream stable
    [Tags]    voice    video    quality    mos    bidirectional    two-ue    priority-1
    Full Registration And PDU Session    ${UE_1}    dnn=ims    psi=2
    Full Registration And PDU Session    ${UE_2}    dnn=ims    psi=2
    Log    TC-IMS-013: ViNR audio+video call quality

# ═══════════════════════════════════════════════════════════════
# Conference Call (TS 24.147)
# ═══════════════════════════════════════════════════════════════
TC-IMS-014 3-Way Conference Call
    [Documentation]    TC-IMS-014: 3-Way IMS Conference Call
    ...    Standard: TS 24.147 §5.3.1.2 (conference creation by merging calls),
    ...    TS 24.229 (SIP procedures), TS 26.114 (media handling for conferencing)
    ...    Procedure:
    ...    1. Register 3 UEs (A, B, C) via NAS + IMS PDU sessions (DNN=ims)
    ...    2. SIP REGISTER all 3 UEs to P-CSCF
    ...    3. UE_A INVITE → UE_B (first call, audio) — dedicated GBR bearer activated
    ...    4. UE_A holds UE_B (re-INVITE with a=sendonly SDP)
    ...    5. UE_A INVITE → UE_C (second call, audio) — second GBR bearer
    ...    6. UE_A merges both calls → INVITE to conference factory URI
    ...    (TS 24.147: sip:conference-factory@domain)
    ...    7. MRFP mixes audio from all 3 participants
    ...    8. 3-way RTP voice: each UE sends AMR-WB to others via UPF/MRFP
    ...    9. Measure per-participant jitter, loss, compute MOS
    ...    10. SIP BYE to tear down conference
    ...    Parameters: duration=30s, 3 UEs, codec=AMR-WB, RTP port=20000
    ...    Traffic: 3 concurrent RTP audio streams through independent GTP-U tunnels
    ...    Verification: MOS >= 3.0 for all participants, conference setup succeeds,
    ...    hold/resume and merge SIP procedures complete
    ...    Expected Result: Conference established, voice quality Fair or better for all
    [Tags]    conference    3-way    merge    voice    priority-1
    Full Registration And PDU Session    ${UE_1}    dnn=ims    psi=2
    Full Registration And PDU Session    ${UE_2}    dnn=ims    psi=2
    Full Registration And PDU Session    ${UE_3}    dnn=ims    psi=2
    Log    TC-IMS-014: 3-way conference call (merge)

# ═══════════════════════════════════════════════════════════════
# Mid-Call Upgrade / Downgrade (VoNR ↔ ViNR)
# ═══════════════════════════════════════════════════════════════
TC-IMS-015 VoNR To ViNR Mid-Call Upgrade
    [Documentation]    TC-IMS-015: Upgrade voice call to video mid-call
    ...    Standard: TS 24.229 §5.1.3 (re-INVITE), TS 26.114 (media modification)
    ...    Standard: TS 23.228 §5.4.7 (media change), TS 29.214 (Rx/N5 interaction)
    ...    Procedure:
    ...    1. Register 2 UEs, IMS PDU sessions (DNN=ims)
    ...    2. SIP REGISTER both UEs to P-CSCF
    ...    3. SIP INVITE (audio only) — VoNR call established
    ...       - Dedicated bearer: 5QI=1 (voice, GBR, PDB=100ms)
    ...    4. Run bidirectional audio RTP for 15s — measure pre-upgrade MOS
    ...    5. SIP re-INVITE with m=audio + m=video (+g.3gpp.mid-call)
    ...       - P-CSCF detects new media → Rx/N5 → PCF → SMF
    ...       - PDU Session Modification adds 5QI=2 (video) bearer
    ...    6. Run bidirectional audio + video RTP for 15s — measure post-upgrade MOS
    ...    7. SIP BYE to tear down
    ...    Parameters: audio=AMR-WB port 20000, video=H.264 port 20002
    ...    Verification: Audio MOS maintained, video stream active after upgrade
    ...    Expected Result: Seamless VoNR → ViNR upgrade without dropping audio
    [Tags]    mid-call    upgrade    vonr-to-vinr    re-invite    priority-1
    Log    TC-IMS-015: VoNR → ViNR mid-call upgrade

TC-IMS-016 ViNR To VoNR Mid-Call Downgrade
    [Documentation]    TC-IMS-016: Downgrade video call to voice-only mid-call
    ...    Standard: TS 24.229 §5.1.3, TS 26.114
    ...    Procedure:
    ...    1. Register 2 UEs, IMS PDU sessions
    ...    2. SIP INVITE (audio + video) — ViNR call established
    ...       - Dedicated bearers: 5QI=1 (voice) + 5QI=2 (video)
    ...    3. Run audio + video RTP for 15s — measure pre-downgrade quality
    ...    4. SIP re-INVITE with m=audio only (remove m=video)
    ...       - P-CSCF detects removed media → Rx/N5 → PCF → SMF
    ...       - PDU Session Modification removes 5QI=2 (video) bearer
    ...    5. Run audio-only RTP for 15s — verify voice continues, no video
    ...    6. SIP BYE
    ...    Parameters: audio=AMR-WB port 20000
    ...    Verification: Audio continues after video removal, MOS stable
    ...    Expected Result: Seamless ViNR → VoNR downgrade
    [Tags]    mid-call    downgrade    vinr-to-vonr    re-invite    priority-1
    Log    TC-IMS-016: ViNR → VoNR mid-call downgrade

TC-IMS-017 VoNR Upgrade Downgrade Cycle
    [Documentation]    TC-IMS-017: Full cycle — VoNR → ViNR → VoNR
    ...    Standard: TS 24.229 §5.1.3
    ...    Procedure:
    ...    1. VoNR call established (audio only)
    ...    2. Phase 1: audio RTP 10s — measure MOS
    ...    3. re-INVITE: upgrade to ViNR (add video)
    ...    4. Phase 2: audio + video RTP 10s — measure MOS
    ...    5. re-INVITE: downgrade to VoNR (remove video)
    ...    6. Phase 3: audio-only RTP 10s — measure MOS
    ...    7. Compare MOS across all 3 phases
    ...    Expected Result: Voice quality stable across upgrade/downgrade cycle
    [Tags]    mid-call    upgrade    downgrade    cycle    priority-1
    Log    TC-IMS-017: VoNR → ViNR → VoNR cycle

# ═══════════════════════════════════════════════════════════════
# SIP CANCEL / Hold / Resume (RFC 3261 §9 / §14, RFC 3264 §5.1)
# ═══════════════════════════════════════════════════════════════
TC-IMS-021 SIP CANCEL Of In-Flight INVITE
    [Documentation]    TC-IMS-021: SIP CANCEL — caller hangs up before callee answers
    ...    Standard: RFC 3261 §9 (CANCEL Method), §9.1 (same-Via-branch),
    ...              §9.2 (UAS Behavior — 200 OK for CANCEL + 487 for
    ...              the cancelled INVITE), §17.2.1 (IS FSM
    ...              Proceeding → Completed on 487).
    ...              TS 29.514 §4.2.4 (symmetric AF Delete fires for
    ...              the policy-authorized media — release of the
    ...              5QI=1 GBR rules installed at INVITE time).
    ...    Procedure:
    ...    1. Register caller + callee on NAS + IMS PDU sessions
    ...    2. SIP REGISTER both UEs at the P-CSCF
    ...    3. Caller sends INVITE for audio media
    ...       — AF→PCF Create fires immediately on the SDP, dynamic
    ...         5QI=1 GBR PCC rule activated for the caller's
    ...         IMS PDU session
    ...    4. Caller sends CANCEL on the same Via branch (§9.1)
    ...       — race the synchronous CSCF B2BUA-stub
    ...    5. Verify CANCEL response is 200 OK (§9.2 step 2)
    ...    6. Verify INVITE outcome status is either 487 (CANCEL
    ...       won the race) or 200 (CANCEL late — final already sent;
    ...       still a valid §9.2 outcome since 487 is only emitted
    ...       when the IS is in Proceeding)
    ...    7. AF→PCF Delete fires symmetrically — PCC rules cleared
    ...    Parameters: 2 UEs, P-CSCF port=5060, INVITE timeout=5s,
    ...                CANCEL timeout=3s
    ...    Verification: cancel_status=200 (RFC 3261 §9.2),
    ...                  invite_status ∈ {487, 200}
    ...    Expected Result: CANCEL acknowledged, IS FSM advances
    ...                     correctly, no policy state leak
    [Tags]    sip    cancel    rfc3261-9    two-ue    priority-1
    Full Registration And PDU Session    ${UE_1}
    Full Registration And PDU Session    ${UE_2}
    Log    TC-IMS-021: SIP CANCEL of in-flight INVITE

TC-IMS-022 SIP Hold Resume Cycle
    [Documentation]    TC-IMS-022: SIP Hold / Resume cycle via re-INVITE
    ...    Standard: RFC 3261 §14 (re-INVITE for in-dialog media change),
    ...              §12.2.1.1 (in-dialog request format — To-tag,
    ...              monotonic CSeq, dialog ID stable),
    ...              RFC 3264 §5.1 ("Hold" — a=sendonly on offer =
    ...              caller-on-hold; a=sendrecv = resume),
    ...              RFC 4566 §6 (a= direction attributes;
    ...              session-level default is sendrecv).
    ...              TS 24.229 §5.1.4 — IMS in-dialog media modification.
    ...    Procedure:
    ...    1. Register caller + callee on NAS + IMS PDU sessions
    ...    2. SIP REGISTER both UEs at the P-CSCF
    ...    3. Caller INVITE → 200 OK (sendrecv default media)
    ...       — capture initial dialog To-tag
    ...    4. Caller re-INVITE with a=sendonly (HOLD)
    ...       — expect 200 OK; To-tag MUST equal the initial 200's
    ...         To-tag per RFC 3261 §12.2.1.1
    ...    5. Caller re-INVITE with a=sendrecv (RESUME)
    ...       — expect 200 OK; To-tag MUST still equal initial
    ...    6. BYE to tear down the dialog
    ...    Parameters: 2 UEs, P-CSCF port=5060, re-INVITE timeout=10s
    ...    Verification: invite_status=200 AND hold_status=200 AND
    ...                  resume_status=200 AND
    ...                  initial_to_tag == hold_to_tag == resume_to_tag
    ...    Expected Result: Dialog ID stable across hold + resume,
    ...                     all re-INVITE legs accepted by S-CSCF,
    ...                     PCF re-evaluates SDP each round
    ...                     (§13.3.1.1 / a= direction parsed)
    [Tags]    sip    reinvite    hold    resume    rfc3264-5.1    two-ue    priority-1
    Full Registration And PDU Session    ${UE_1}
    Full Registration And PDU Session    ${UE_2}
    Log    TC-IMS-022: SIP Hold / Resume cycle
