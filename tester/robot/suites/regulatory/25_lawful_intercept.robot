# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    LI (Lawful Intercept) Test Suite
...              TS 33.127 — Lawful Intercept architecture and functions
...              TS 33.128 — Lawful Intercept provisioning and handover interfaces
...              Covers: IRI event generation, CC session activation,
...              warrant-based target provisioning, audit logging, LI deactivation
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        li    lawful-intercept    security

*** Test Cases ***
# ===============================================================
# IRI (Intercept Related Information) Events
# TS 33.128 §6.2 — IRI generation for 5GS events
# ===============================================================
TC-LI-001 IRI Event Generation On Registration
    [Documentation]    TC-LI-001: Verify IRI event generated on UE registration
    ...    Standard: TS 33.128 §6.2.2 (Registration IRI)
    ...    Procedure:
    ...    1. Provision LI target for UE via warrant API (TC-LI-010 precondition)
    ...    2. Register target UE (standard registration flow)
    ...    3. AMF generates xIRI (registration event) to LIPF
    ...    4. IRI contains: SUPI, timestamp, registration type, serving PLMN
    ...    Verification:
    ...    - IRI event logged with correct event type (registration)
    ...    - IRI payload contains SUPI matching target identity
    ...    - Timestamp within acceptable tolerance
    ...    Expected Result: Registration IRI delivered to LIPF
    [Tags]    iri    registration    priority-1
    Log    TC-LI-001: IRI Event on Registration

TC-LI-002 IRI Event Generation On PDU Session
    [Documentation]    TC-LI-002: Verify IRI event generated on PDU session establishment
    ...    Standard: TS 33.128 §6.2.3 (PDU Session IRI)
    ...    Procedure:
    ...    1. Target UE registered with active LI warrant
    ...    2. UE establishes PDU session (DNN=internet)
    ...    3. SMF generates xIRI (PDU session event) to LIPF
    ...    4. IRI contains: SUPI, PSI, DNN, allocated UE IP, QoS parameters
    ...    Verification:
    ...    - IRI event logged with correct event type (pdu-session-establishment)
    ...    - IRI payload contains DNN, UE IP address, session ID
    ...    Expected Result: PDU Session IRI delivered to LIPF
    [Tags]    iri    pdu-session    priority-1
    Log    TC-LI-002: IRI Event on PDU Session

TC-LI-003 CC Session Activation
    [Documentation]    TC-LI-003: Activate CC (Call Content) interception for target UE
    ...    Standard: TS 33.128 §7 (CC delivery), TS 33.127 §6.3 (CC activation)
    ...    Procedure:
    ...    1. Target UE registered with active LI warrant (IRI-only initially)
    ...    2. Upgrade warrant to include CC via provisioning API
    ...    3. UE establishes PDU session and sends user-plane data
    ...    4. UPF duplicates CC packets to LI mediation function (MF)
    ...    5. CC packets delivered via X3 interface
    ...    Verification:
    ...    - CC activation acknowledged by UPF
    ...    - User-plane packets duplicated to MF
    ...    - CC stream correlates with IRI session ID
    ...    Expected Result: CC interception active, content mirrored to MF
    [Tags]    cc    content    user-plane    priority-1
    Log    TC-LI-003: CC Session Activation

# ===============================================================
# LI Provisioning & Management
# TS 33.127 §5 — LI provisioning architecture
# TS 33.128 §5.2 — Target provisioning via X1 interface
# ===============================================================
TC-LI-010 Warrant-Based Target Provisioning Via API
    [Documentation]    TC-LI-010: Provision LI target via REST API (warrant-based)
    ...    Standard: TS 33.127 §5.3 (X1 provisioning interface)
    ...    Procedure:
    ...    1. POST /api/li/targets with warrant details:
    ...       - target_identity (SUPI or GPSI)
    ...       - warrant_id, warrant_type (IRI-only or IRI+CC)
    ...       - activation_time, deactivation_time
    ...    2. Verify target provisioned in LI subsystem
    ...    3. GET /api/li/targets and confirm target listed
    ...    Verification:
    ...    - API returns 201 Created with target ID
    ...    - Target appears in active target list
    ...    - Warrant metadata stored (ID, validity period)
    ...    Expected Result: LI target provisioned and active
    [Tags]    api    provisioning    warrant    priority-1
    Log    TC-LI-010: Warrant-Based Target Provisioning

TC-LI-011 LI Audit Log Generation
    [Documentation]    TC-LI-011: Verify LI audit trail for all provisioning actions
    ...    Standard: TS 33.127 §8 (Security and audit requirements)
    ...    Procedure:
    ...    1. Perform LI provisioning action (add/modify/delete target)
    ...    2. Query /api/li/audit-log
    ...    3. Verify audit entry contains:
    ...       - Action type (create/update/delete)
    ...       - Operator identity
    ...       - Timestamp (UTC)
    ...       - Target identity (hashed for privacy)
    ...       - Warrant reference
    ...    Verification:
    ...    - Audit log entry present for every provisioning action
    ...    - Entries are immutable (append-only)
    ...    - Timestamps monotonically increasing
    ...    Expected Result: Complete audit trail for LI operations
    [Tags]    audit    logging    compliance    priority-1
    Log    TC-LI-011: LI Audit Log Generation

TC-LI-012 LI Deactivation Cleanup
    [Documentation]    TC-LI-012: Deactivate LI target and verify full cleanup
    ...    Standard: TS 33.127 §5.3.3 (Target deactivation)
    ...    Procedure:
    ...    1. Active LI target with IRI+CC interception running
    ...    2. DELETE /api/li/targets/{target_id} (or warrant expiry)
    ...    3. Verify IRI event generation stops for target UE
    ...    4. Verify CC duplication stopped at UPF
    ...    5. Verify audit log records deactivation
    ...    Verification:
    ...    - No further IRI events for deactivated target
    ...    - UPF CC duplication rule removed
    ...    - Target removed from active target list
    ...    - Audit log contains deactivation entry
    ...    Expected Result: LI fully deactivated, no residual interception
    [Tags]    deactivation    cleanup    priority-1
    Log    TC-LI-012: LI Deactivation Cleanup

# ===============================================================
# Validation + OAM
# ===============================================================
TC-LI-013 Warrant With Bogus Scope Rejected
    [Documentation]    TC-LI-013: ADMF rejects warrants with invalid scope
    ...    Standard: TS 33.127 §5.3 (scope ∈ {iri, cc, iri+cc})
    ...    Verification: POST returns 4xx with "scope" in error message
    [Tags]    api    validation    priority-2
    Log    TC-LI-013: Bogus scope rejected

TC-LI-014 Warrant Missing Required Fields Rejected
    [Documentation]    TC-LI-014: ADMF rejects warrants missing target_imsi
    ...    Standard: TS 33.127 §5.3 (warrant requires target identity)
    ...    Verification: POST without target_imsi returns 4xx
    [Tags]    api    validation    priority-2
    Log    TC-LI-014: Missing required fields rejected

TC-LI-015 Stats Counter Tracks Active Warrants
    [Documentation]    TC-LI-015: /api/li/stats reports active warrant count
    ...    Procedure: snapshot baseline, create 2 warrants, verify +2;
    ...    revoke 1, verify -1
    [Tags]    api    oam    priority-2
    Log    TC-LI-015: Stats counter

TC-LI-016 Mark Delivered Flips Pending IRI Rows
    [Documentation]    TC-LI-016: MDF-ack pipeline marks IRI rows delivered
    ...    Standard: TS 33.127 X2 (deferred wire) — pending → delivered
    ...    Procedure: register UE under iri-warrant, observe pending>0,
    ...    POST /api/li/warrant/{id}/mark-delivered, verify pending drops
    [Tags]    api    mdf    priority-2
    Log    TC-LI-016: Mark delivered

TC-LI-017 CC-Only Warrant Skips IRI Capture
    [Documentation]    TC-LI-017: scope=cc must NOT capture IRI rows
    ...    Standard: TS 33.127 §7 (IRI / CC handover separation)
    ...    Verification: register + PDU on cc-only warrant; IRI count=0,
    ...    CC session row exists
    [Tags]    scope    separation    priority-1
    Log    TC-LI-017: CC-only skips IRI

# ===============================================================
# X1 / X2 / X3 reference points (TS 33.127 §6)
# ===============================================================
TC-LI-018 X1 Provision Creates Warrant
    [Documentation]    TC-LI-018: ADMF→POI provisioning via X1 surface
    ...    Standard: TS 33.127 §6.2 (X1 LI provisioning)
    ...    Procedure: POST /api/li/x1/provision; verify warrant active
    ...    + audit log carries x1_provision row
    [Tags]    x1    provisioning    priority-1
    Log    TC-LI-018: X1 provision creates warrant

TC-LI-019 X1 Deactivate Revokes Warrant
    [Documentation]    TC-LI-019: X1 deactivate stops interception
    ...    Standard: TS 33.127 §6.2 (X1 deactivate verb)
    ...    Verification: status flips to revoked; cache drops entry
    [Tags]    x1    deactivation    priority-1
    Log    TC-LI-019: X1 deactivate

TC-LI-020 X1 Modify Updates Warrant Fields
    [Documentation]    TC-LI-020: X1 modify changes scope / end_time / mdf
    ...    Standard: TS 33.127 §6.2 (X1 modify verb)
    [Tags]    x1    modify    priority-2
    Log    TC-LI-020: X1 modify

TC-LI-021 X2 IRI Delivery To MDF
    [Documentation]    TC-LI-021: POI POSTs IRI to warrant's mdf_endpoint
    ...    Standard: TS 33.127 §6.3 (X2 IRI delivery)
    ...    Procedure: warrant points at tester's mock MDF; register UE;
    ...    verify REGISTER event POSTed to /x2/iri
    [Tags]    x2    iri-delivery    priority-1
    Log    TC-LI-021: X2 delivery

TC-LI-022 X2 Retries On Transient MDF Failure
    [Documentation]    TC-LI-022: failed delivery does not lose rows
    ...    Standard: TS 33.127 §6.3 (queue is the buffer)
    ...    Procedure: inject one 500; verify retry succeeds
    [Tags]    x2    retry    priority-1
    Log    TC-LI-022: X2 retry

TC-LI-023 X2 Disabled Does Not Push
    [Documentation]    TC-LI-023: li_x2_enabled=0 keeps rows pending
    ...    Defensive: a deployment without configured MDFs must not
    ...    accidentally exfiltrate
    [Tags]    x2    safety    priority-1
    Log    TC-LI-023: X2 disabled

TC-LI-024 X3 CC Delivery To MDF
    [Documentation]    TC-LI-024: OPENED + CLOSED CC events to MDF
    ...    Standard: TS 33.127 §6.4 (X3 CC delivery)
    ...    Procedure: iri+cc warrant; register + PDU + dereg; verify
    ...    both phases land at /x3/cc on the mock
    [Tags]    x3    cc-delivery    priority-1
    Log    TC-LI-024: X3 CC delivery
