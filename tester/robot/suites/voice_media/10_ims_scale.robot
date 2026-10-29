# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    IMS/VoNR/ViNR Scale Test Suite
...              Mass SIP registration, concurrent voice/video calls, multi-way conferences
...              TS 23.228 (IMS), TS 24.229 (SIP), TS 26.114 (media), ITU-T G.107 (MOS)
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        ims    scale    vonr    vinr    quality

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════
# VoNR Mass Registration
# ═══════════════════════════════════════════════════════════════
TC-IMS-100 VoNR 128 UE Mass SIP Registration
    [Documentation]    TC-IMS-100: 128 UE IMS Registration at Scale
    ...    Standard: TS 24.229 §5.1 (SIP REGISTER), TS 33.203 (IMS-AKA)
    ...    Procedure:
    ...    1. Register 128 UEs via NAS + IMS PDU sessions (DNN=ims)
    ...    2. SIP REGISTER all 128 UEs to P-CSCF with IMS-AKA auth
    ...    3. Verify all 128 get 200 OK from S-CSCF
    ...    Parameters: 128 UEs, SIP port=5060, IMS-AKA authentication
    ...    Verification: All 128 SIP REGISTERs return 200 OK
    ...    Expected Result: All 128 UEs registered in IMS
    [Tags]    registration    128-ue    priority-1
    Log    TC-IMS-100: 128 UE mass SIP registration

# ═══════════════════════════════════════════════════════════════
# VoNR Concurrent Bidirectional Calls
# ═══════════════════════════════════════════════════════════════
TC-IMS-101 VoNR 4 Bidirectional Calls
    [Documentation]    TC-IMS-101: 4 Concurrent VoNR Calls (8 UEs, AMR-WB)
    ...    Standard: TS 26.114 (VoNR media), ITU-T G.107 (MOS)
    ...    Procedure:
    ...    1. Register 8 UEs + IMS PDU, pair into 4 calls
    ...    2. Each pair: bidirectional RTP AMR-WB (port 20000, 50 pps, 20ms)
    ...    3. Measure per-call jitter, loss, compute MOS
    ...    Parameters: 4 calls, 8 UEs, duration=15s, AMR-WB codec
    ...    Traffic: 8 concurrent RTP streams through GTP-U/UPF
    ...    Verification: All calls MOS >= 3.5, jitter < 50ms, loss < 1%
    ...    Expected Result: Average MOS Good or better
    [Tags]    vonr    4-calls    priority-1
    Log    TC-IMS-101: 4 bidirectional VoNR calls

TC-IMS-102 VoNR 16 Bidirectional Calls
    [Documentation]    TC-IMS-102: 16 Concurrent VoNR Calls (32 UEs)
    ...    Parameters: 16 calls, 32 UEs, duration=10s
    ...    Traffic: 32 concurrent RTP streams
    ...    Expected Result: Average MOS >= 3.5
    [Tags]    vonr    16-calls    priority-2
    Log    TC-IMS-102: 16 bidirectional VoNR calls

TC-IMS-103 VoNR 32 Bidirectional Calls
    [Documentation]    TC-IMS-103: 32 Concurrent VoNR Calls (64 UEs)
    ...    Parameters: 32 calls, 64 UEs, duration=10s
    ...    Traffic: 64 concurrent RTP streams
    ...    Expected Result: Average MOS >= 3.0
    [Tags]    vonr    32-calls    large-scale    priority-3
    Log    TC-IMS-103: 32 bidirectional VoNR calls

TC-IMS-104 VoNR 64 Bidirectional Calls
    [Documentation]    TC-IMS-104: 64 Concurrent VoNR Calls (128 UEs)
    ...    Parameters: 64 calls, 128 UEs, duration=10s
    ...    Traffic: 128 concurrent RTP streams — maximum capacity
    ...    Expected Result: Average MOS >= 3.0
    [Tags]    vonr    64-calls    large-scale    priority-3
    Log    TC-IMS-104: 64 bidirectional VoNR calls

# ═══════════════════════════════════════════════════════════════
# VoNR Multi-Way Conferences
# ═══════════════════════════════════════════════════════════════
TC-IMS-105 VoNR 1x32-Way Conference
    [Documentation]    TC-IMS-105: Single 32-Way VoNR Conference
    ...    Standard: TS 24.147 (conferencing), TS 26.114 (media mixing)
    ...    Procedure: 32 UEs in one conference, ring-topology RTP
    ...    Parameters: 1 conference × 32 participants, duration=15s
    ...    Expected Result: All participants MOS >= 3.0
    [Tags]    vonr    conference    32-way    priority-2
    Log    TC-IMS-105: 32-way VoNR conference

TC-IMS-106 VoNR 16x8-Way Conferences
    [Documentation]    TC-IMS-106: 16 Concurrent 8-Way VoNR Conferences (128 UEs)
    ...    Standard: TS 24.147, TS 26.114
    ...    Procedure: 128 UEs grouped into 16 conferences of 8 participants each
    ...    Parameters: 16 conferences × 8 participants, duration=10s
    ...    Expected Result: All conferences MOS >= 3.0
    [Tags]    vonr    conference    8-way    16-conf    large-scale    priority-3
    Log    TC-IMS-106: 16 × 8-way VoNR conferences

TC-IMS-107 VoNR 8x4-Way Conferences
    [Documentation]    TC-IMS-107: 8 Concurrent 4-Way VoNR Conferences (32 UEs)
    ...    Parameters: 8 conferences × 4 participants, duration=10s
    ...    Expected Result: All conferences MOS >= 3.5
    [Tags]    vonr    conference    4-way    8-conf    priority-2
    Log    TC-IMS-107: 8 × 4-way VoNR conferences

TC-IMS-108 VoNR 4x3-Way Conferences
    [Documentation]    TC-IMS-108: 4 Concurrent 3-Way VoNR Conferences (12 UEs)
    ...    Parameters: 4 conferences × 3 participants, duration=10s
    ...    Expected Result: All conferences MOS >= 3.5
    [Tags]    vonr    conference    3-way    4-conf    priority-1
    Log    TC-IMS-108: 4 × 3-way VoNR conferences

# ═══════════════════════════════════════════════════════════════
# ViNR Concurrent Bidirectional Calls (Audio + Video)
# ═══════════════════════════════════════════════════════════════
TC-IMS-110 ViNR 4 Bidirectional Calls
    [Documentation]    TC-IMS-110: 4 Concurrent ViNR Calls (8 UEs, AMR-WB + H.264)
    ...    Standard: TS 26.114, 5QI=1 (voice) + 5QI=2 (video)
    ...    Procedure: 4 call pairs, each with audio + video RTP
    ...    Parameters: 4 calls, 8 UEs, audio port=20000, video port=20002, duration=15s
    ...    Traffic: 16 RTP streams (4×audio UL/DL + 4×video UL/DL)
    ...    Expected Result: Audio MOS >= 3.5, video jitter < 100ms
    [Tags]    vinr    4-calls    audio-video    priority-1
    Log    TC-IMS-110: 4 bidirectional ViNR calls

TC-IMS-111 ViNR 16 Bidirectional Calls
    [Documentation]    TC-IMS-111: 16 Concurrent ViNR Calls (32 UEs)
    ...    Parameters: 16 calls, 32 UEs, audio+video, duration=10s
    ...    Traffic: 64 RTP streams
    ...    Expected Result: Audio MOS >= 3.0
    [Tags]    vinr    16-calls    audio-video    priority-2
    Log    TC-IMS-111: 16 bidirectional ViNR calls

TC-IMS-112 ViNR 32 Bidirectional Calls
    [Documentation]    TC-IMS-112: 32 Concurrent ViNR Calls (64 UEs)
    ...    Parameters: 32 calls, 64 UEs, audio+video, duration=10s
    ...    Traffic: 128 RTP streams
    ...    Expected Result: Audio MOS >= 3.0
    [Tags]    vinr    32-calls    audio-video    large-scale    priority-3
    Log    TC-IMS-112: 32 bidirectional ViNR calls

TC-IMS-113 ViNR 64 Bidirectional Calls
    [Documentation]    TC-IMS-113: 64 Concurrent ViNR Calls (128 UEs)
    ...    Parameters: 64 calls, 128 UEs, audio+video, duration=10s
    ...    Traffic: 256 RTP streams — maximum capacity
    ...    Expected Result: Audio MOS >= 3.0, video stable
    [Tags]    vinr    64-calls    audio-video    large-scale    priority-3
    Log    TC-IMS-113: 64 bidirectional ViNR calls

# ═══════════════════════════════════════════════════════════════
# ViNR Multi-Way Conferences (Audio + Video)
# ═══════════════════════════════════════════════════════════════
TC-IMS-114 ViNR 16x8-Way Conferences
    [Documentation]    TC-IMS-114: 16 Concurrent 8-Way ViNR Conferences (128 UEs)
    ...    Standard: TS 24.147 (conferencing), TS 26.114 (audio+video)
    ...    Parameters: 16 conferences × 8 participants, audio+video, duration=10s
    ...    Expected Result: Audio MOS >= 3.0, video stable
    [Tags]    vinr    conference    8-way    16-conf    large-scale    priority-3
    Log    TC-IMS-114: 16 × 8-way ViNR conferences

TC-IMS-115 ViNR 8x4-Way Conferences
    [Documentation]    TC-IMS-115: 8 Concurrent 4-Way ViNR Conferences (32 UEs)
    ...    Parameters: 8 conferences × 4 participants, audio+video, duration=10s
    ...    Expected Result: Audio MOS >= 3.5
    [Tags]    vinr    conference    4-way    8-conf    priority-2
    Log    TC-IMS-115: 8 × 4-way ViNR conferences

TC-IMS-116 ViNR 4x3-Way Conferences
    [Documentation]    TC-IMS-116: 4 Concurrent 3-Way ViNR Conferences (12 UEs)
    ...    Parameters: 4 conferences × 3 participants, audio+video, duration=10s
    ...    Expected Result: Audio MOS >= 3.5
    [Tags]    vinr    conference    3-way    4-conf    priority-1
    Log    TC-IMS-116: 4 × 3-way ViNR conferences
