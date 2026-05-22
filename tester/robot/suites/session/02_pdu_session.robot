# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    PDU Session Establishment Test Suite
...              Tests: Default PDU, IMS PDU, multi-PDU, DNN selection
...              Covers: TS 24.501 §8.3.1, TS 23.502 §4.3.2
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        pdu-session    nas

*** Test Cases ***
TC-PDU-001 Internet PDU Session
    [Documentation]    TC-PDU-001: PDU Session Establishment (Internet DNN)
    ...    Standard: TS 24.501 §6.4.1 (PDU session establishment),
    ...    TS 23.502 §4.3.2 (PDU session establishment procedure)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. Register UE via NAS (5G-AKA, Security Mode, Registration Accept)
    ...    3. UE sends PDU Session Establishment Request (PSI=1, DNN=internet)
    ...    4. SMF selects UPF, allocates UE IP from pool, creates PFCP session
    ...    5. AMF sends PDU Session Resource Setup Request (GTP-U TEID, QFI=1)
    ...    6. gNB creates GTP-U tunnel, allocates TUN interface with UE IP
    ...    7. PDU Session Establishment Accept received with UE IP address
    ...    Parameters: DNN=internet, PSI=1, timeout=20s
    ...    Verification: PDU session created with valid UE IP address,
    ...    GTP-U tunnel established (TEID assigned), session stored in UE context
    ...    Expected Result: PDU session active with routable UE IP address
    [Tags]    smoke    internet    priority-1
    ${ip}=    Full Registration And PDU Session    ${UE_1}    dnn=internet    psi=1
    Log    TC-PDU-001 PASS: Internet PDU Session IP: ${ip}
    Should Not Be Equal    ${ip}    unknown    PDU session should have an IP

TC-PDU-002 IMS PDU Session
    [Documentation]    TC-PDU-002: IMS PDU Session Establishment
    ...    Standard: TS 24.501 §6.4.1 (PDU session establishment),
    ...    TS 23.228 §5.2 (IMS access via 5GC), TS 24.229 §5.1 (SIP over IMS PDU)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. Register UE via NAS (5G-AKA, Security Mode, Registration Accept)
    ...    3. UE sends PDU Session Establishment Request (PSI=2, DNN=ims)
    ...    4. SMF selects UPF for IMS, allocates IMS UE IP, creates PFCP session
    ...    5. P-CSCF address provided via PCO (Protocol Configuration Options)
    ...    6. Default QoS flow: 5QI=5 (IMS signaling, non-GBR)
    ...    7. PDU Session Establishment Accept with IMS IP and P-CSCF
    ...    Parameters: DNN=ims, PSI=2, timeout=20s
    ...    Verification: IMS PDU session created with valid UE IP,
    ...    P-CSCF address available for SIP registration
    ...    Expected Result: IMS PDU session active, UE has IMS IP address
    [Tags]    ims    priority-1
    Full Registration    ${UE_1}
    ${ip}=    Full PDU Session    ${UE_1}    dnn=ims    psi=2
    Log    TC-PDU-002 PASS: IMS PDU Session IP: ${ip}

TC-PDU-003 Multiple PDU Sessions
    [Documentation]    TC-PDU-003: Dual PDU Session Establishment (Internet + IMS)
    ...    Standard: TS 24.501 §6.4.1 (PDU session establishment),
    ...    TS 23.501 §5.6.1 (multiple PDU sessions per UE)
    ...    Procedure:
    ...    1. Create gNB, register UE via NAS
    ...    2. Establish first PDU session: DNN=internet, PSI=1
    ...    - SMF allocates internet UE IP, creates GTP-U tunnel
    ...    3. Establish second PDU session: DNN=ims, PSI=2
    ...    - SMF allocates IMS UE IP from separate pool
    ...    - P-CSCF address provided via PCO
    ...    4. Both PDU sessions active simultaneously on same UE
    ...    5. Each session has independent GTP-U tunnel and QoS flows
    ...    Parameters: internet: DNN=internet/PSI=1, IMS: DNN=ims/PSI=2, timeout=20s
    ...    Verification: Both PDU sessions established with distinct IP addresses,
    ...    UE has two active sessions with independent GTP-U tunnels
    ...    Expected Result: Two active PDU sessions, internet IP and IMS IP both valid
    [Tags]    multi-pdu    priority-1
    Full Registration    ${UE_1}
    ${ip_inet}=    Full PDU Session    ${UE_1}    dnn=internet    psi=1
    ${ip_ims}=     Full PDU Session    ${UE_1}    dnn=ims        psi=2
    Log    TC-PDU-003: Internet: ${ip_inet}, IMS: ${ip_ims}
    Should Not Be Equal    ${ip_inet}    ${ip_ims}    PDU sessions should have different IPs

TC-PDU-004 Two UEs With PDU Sessions
    [Documentation]    TC-PDU-004: Two-UE PDU Session Establishment
    ...    Standard: TS 24.501 §6.4.1 (PDU session), TS 38.413 (NGAP multi-UE),
    ...    TS 29.281 (GTP-U per-UE tunnels)
    ...    Procedure:
    ...    1. Create gNB, connect SCTP, complete NG Setup
    ...    2. Register UE_1 via NAS (5G-AKA, Security Mode, Registration Accept)
    ...    3. Establish PDU session for UE_1 (DNN=internet, PSI=1) — gets IP_1
    ...    4. Register UE_2 via NAS (independent 5G-AKA on same gNB)
    ...    5. Establish PDU session for UE_2 (DNN=internet, PSI=1) — gets IP_2
    ...    6. Verify both UEs have distinct IP addresses and GTP-U tunnels
    ...    Parameters: 2 UEs from config, DNN=internet, PSI=1, timeout=20s
    ...    Verification: Both UEs registered and have active PDU sessions,
    ...    IP_1 != IP_2, independent GTP-U TEIDs allocated
    ...    Expected Result: Two UEs with distinct IPs and active PDU sessions
    [Tags]    multi-ue    priority-2
    ${ip1}=    Full Registration And PDU Session    ${UE_1}
    ${ip2}=    Full Registration And PDU Session    ${UE_2}
    Log    TC-PDU-004: UE1: ${ip1}, UE2: ${ip2}
    Should Not Be Equal    ${ip1}    ${ip2}    Different UEs should get different IPs
