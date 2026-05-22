# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    MCX (Mission Critical Communications) Test Suite
...              TS 23.379 — MCPTT (Mission Critical Push-to-Talk)
...              TS 23.281 — MCVideo (Mission Critical Video)
...              TS 23.282 — MCData (Mission Critical Data)
...              TS 24.379 — MCPTT floor control (Stage 3)
...              Covers: MCPTT group/emergency calls, floor control, MCVideo calls,
...              MCData SDS/FD, user profiles, group management, priority/preemption
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        mcx    mission-critical

*** Test Cases ***
# ===============================================================
# MCPTT Group Call Setup
# TS 23.379 §10.6.2.3.1.1.2 — Pre-arranged group call setup
# TS 23.379 §10.6.2.3.1.2.2 — Chat group call setup
# ===============================================================
TC-MCX-001 MCPTT Pre-Arranged Group Call Setup
    [Documentation]    TC-MCX-001: Initiate a pre-arranged MCPTT group call
    ...    Standard: TS 23.379 §10.6.2.3.1.1.2 (Pre-arranged group call setup)
    ...    Procedure:
    ...    1. Create MCX group (type=normal) with 3 members via REST API
    ...    2. Create MCX user profiles for all 3 UEs (linked by IMSI)
    ...    3. Originator initiates group call via POST /api/mcx/calls
    ...    4. MCX server notifies all group members via WebSocket
    ...    5. Floor control FSM initialized in idle state
    ...    6. RTP ports allocated for media relay
    ...    Verification:
    ...    - Call record created with call_type=group, state=active
    ...    - All group members listed as participants
    ...    - Floor controller initialized for call_id
    ...    Expected Result: MCPTT group call active with all members notified
    [Tags]    mcptt    group-call    setup    priority-1
    Log    TC-MCX-001: MCPTT Pre-Arranged Group Call Setup

TC-MCX-002 MCPTT Emergency Group Call Setup
    [Documentation]    TC-MCX-002: Initiate an emergency MCPTT group call
    ...    Standard: TS 23.379 §10.6.2.6.1 (MCPTT emergency group call)
    ...    Procedure:
    ...    1. MCX group with 3 members configured
    ...    2. Originator initiates emergency group call (is_emergency=true)
    ...    3. Call created with priority=1 (EMERGENCY level)
    ...    4. All group members receive emergency notification
    ...    5. Emergency indicator set on call and floor control
    ...    Verification:
    ...    - Call record has call_type=emergency, priority=1
    ...    - Emergency pre-empts any existing normal call
    ...    - WebSocket notification includes emergency flag
    ...    Expected Result: Emergency group call active with highest priority
    [Tags]    mcptt    group-call    emergency    priority-1
    Log    TC-MCX-002: MCPTT Emergency Group Call Setup

TC-MCX-003 MCPTT Group Call Teardown
    [Documentation]    TC-MCX-003: End an active MCPTT group call
    ...    Standard: TS 23.379 §10.6.2.3.1.1.3 (Release pre-arranged group call)
    ...    Procedure:
    ...    1. Active MCPTT group call with 3 participants
    ...    2. Call initiator ends call via DELETE /api/mcx/calls/{call_id}
    ...    3. All participants notified of call end
    ...    4. Floor control FSM destroyed
    ...    5. RTP ports released
    ...    Verification:
    ...    - Call state set to ended in DB
    ...    - All participants removed
    ...    - Floor controller cleaned up
    ...    Expected Result: Call ended, all resources released
    [Tags]    mcptt    group-call    teardown    priority-1
    Log    TC-MCX-003: MCPTT Group Call Teardown

# ===============================================================
# MCPTT Floor Control
# TS 24.380 §6.2.4 — Floor participant FSM, on-network basic operation
# States: idle → taken → (released → idle)
# (Floor control protocol lives in TS 24.380, not TS 24.379.)
# ===============================================================
TC-MCX-010 MCPTT Floor Request And Grant
    [Documentation]    TC-MCX-010: Request and grant PTT floor
    ...    Standard: TS 24.380 §6.2.4.3 (Floor Request — 'U: has no permission' state)
    ...    Procedure:
    ...    1. Active group call with floor in idle state
    ...    2. Participant A sends Floor Request
    ...    3. Floor controller transitions idle → taken
    ...    4. Floor Granted sent to Participant A
    ...    5. Floor Taken notification sent to all other participants
    ...    Verification:
    ...    - Floor state = taken
    ...    - Floor holder = Participant A
    ...    - Floor event recorded in DB
    ...    Expected Result: Floor granted to requester, all participants notified
    [Tags]    mcptt    floor-control    request    grant    priority-1
    Log    TC-MCX-010: MCPTT Floor Request And Grant

TC-MCX-011 MCPTT Floor Release
    [Documentation]    TC-MCX-011: Release PTT floor after speaking
    ...    Standard: TS 24.380 §6.2.4.5 (Floor Release — 'U: has permission' state)
    ...    Procedure:
    ...    1. Active group call, Participant A holds floor
    ...    2. Participant A sends Floor Release
    ...    3. Floor controller transitions taken → idle
    ...    4. Floor Idle notification sent to all participants
    ...    5. If queued requests exist, next highest priority is granted
    ...    Verification:
    ...    - Floor state = idle (or taken by next in queue)
    ...    - Floor holder = None (or next requester)
    ...    Expected Result: Floor released, available for next request
    [Tags]    mcptt    floor-control    release    priority-1
    Log    TC-MCX-011: MCPTT Floor Release

TC-MCX-012 MCPTT Floor Preemption By Higher Priority
    [Documentation]    TC-MCX-012: Higher priority user pre-empts floor holder
    ...    Standard: TS 24.380 §6.2.4.5.4 (Receive Floor Revoke message)
    ...    Procedure:
    ...    1. Active group call, Participant A (priority=5/normal) holds floor
    ...    2. Participant B (priority=1/emergency) sends Floor Request
    ...    3. Floor controller evaluates pre-emption (can_preempt returns true)
    ...    4. Floor Revoked sent to Participant A
    ...    5. Floor Granted sent to Participant B
    ...    6. Floor Taken notification to all other participants
    ...    Verification:
    ...    - Participant A's floor revoked
    ...    - Floor holder = Participant B
    ...    - Pre-emption event recorded
    ...    Expected Result: Emergency user pre-empts normal floor holder
    [Tags]    mcptt    floor-control    preemption    emergency    priority-1
    Log    TC-MCX-012: MCPTT Floor Preemption

# ===============================================================
# MCVideo Call Setup
# TS 23.281 §7.1 — Group call (MCVideo)
# TS 23.281 §7.7 — Transmission control
# ===============================================================
TC-MCX-020 MCVideo Group Call Setup
    [Documentation]    TC-MCX-020: Initiate an MCVideo group call
    ...    Standard: TS 23.281 §7.1.2.3.1.1 (Pre-arranged MCVideo group call)
    ...    Procedure:
    ...    1. MCX group with 3 members configured
    ...    2. Originator initiates video group call via POST /api/mcx/video/calls
    ...    3. Audio + video RTP ports allocated (4 ports per session)
    ...    4. All group members notified via WebSocket
    ...    5. Transmission control FSM initialized
    ...    Verification:
    ...    - Call record created with service=mcvideo, state=active
    ...    - RTP ports allocated for both audio and video
    ...    - Transmission controller initialized for call_id
    ...    Expected Result: MCVideo group call active with A/V media paths
    [Tags]    mcvideo    group-call    setup    priority-1
    Log    TC-MCX-020: MCVideo Group Call Setup

TC-MCX-021 MCVideo Transmission Control Grant And Revoke
    [Documentation]    TC-MCX-021: MCVideo transmission control (grant/revoke)
    ...    Standard: TS 23.281 §7.7.1 (Transmission control for on-network MCVideo)
    ...    Procedure:
    ...    1. Active MCVideo group call
    ...    2. Participant A requests transmission control
    ...    3. Transmission granted — video uplink enabled for Participant A
    ...    4. Participant A releases transmission
    ...    5. Transmission idle — available for next request
    ...    Verification:
    ...    - Transmission state transitions: idle → granted → idle
    ...    - Only granted participant can send video
    ...    Expected Result: Transmission control arbitrates video uplink
    [Tags]    mcvideo    transmission-control    priority-1
    Log    TC-MCX-021: MCVideo Transmission Control

# ===============================================================
# MCData Short Data Service (SDS)
# TS 23.282 §7.4.2.2 — One-to-one SDS using signalling control plane
# TS 23.282 §7.4.2.5 — Group SDS using signalling control plane
# TS 23.282 §7.4.2.1.2 — Data disposition notification (delivery)
# ===============================================================
TC-MCX-030 MCData SDS Private Message Send And Receive
    [Documentation]    TC-MCX-030: Send a private SDS text message
    ...    Standard: TS 23.282 §7.4.2.2 (One-to-one standalone SDS — signalling control plane)
    ...    Procedure:
    ...    1. MCX user profiles for sender and recipient exist
    ...    2. Sender sends SDS message via POST /api/mcx/data/sds
    ...    3. Message stored in mcx_messages table (msg_type=sds)
    ...    4. Recipient notified via WebSocket (message_received event)
    ...    Verification:
    ...    - Message record created with sender, recipient, content
    ...    - WebSocket notification delivered to recipient
    ...    - Message retrievable via GET /api/mcx/data/messages
    ...    Expected Result: Private SDS message delivered and stored
    [Tags]    mcdata    sds    private    message    priority-1
    Log    TC-MCX-030: MCData SDS Private Message

TC-MCX-031 MCData SDS Group Message
    [Documentation]    TC-MCX-031: Send a group SDS text message
    ...    Standard: TS 23.282 §7.4.2.5 (Group standalone SDS — signalling control plane)
    ...    Procedure:
    ...    1. MCX group with 3 members configured
    ...    2. Sender sends group SDS message (group_id specified)
    ...    3. Message stored with group association
    ...    4. All group members notified via WebSocket
    ...    Verification:
    ...    - Message record includes group_id
    ...    - All group members receive WebSocket notification
    ...    Expected Result: Group SDS message delivered to all members
    [Tags]    mcdata    sds    group    message    priority-1
    Log    TC-MCX-031: MCData SDS Group Message

TC-MCX-032 MCData SDS Delivery Confirmation
    [Documentation]    TC-MCX-032: Verify SDS delivery status tracking
    ...    Standard: TS 23.282 §7.4.2.1.2 (MCData data disposition notification)
    ...    Procedure:
    ...    1. Send private SDS message
    ...    2. Recipient marks message as delivered via PUT /api/mcx/data/messages/{id}
    ...    3. Delivery status updated in DB (mark_delivered)
    ...    4. Sender receives delivery confirmation via WebSocket
    ...    Verification:
    ...    - Message delivered_at timestamp set
    ...    - Sender notified of delivery
    ...    Expected Result: Delivery confirmation tracked and reported
    [Tags]    mcdata    sds    delivery    confirmation    priority-2
    Log    TC-MCX-032: MCData SDS Delivery Confirmation

# ===============================================================
# MCX User Profile & Group Management
# TS 23.379 §A.3 — MCPTT user profile configuration data
# TS 23.379 §A.4 — MCPTT related group configuration data
# ===============================================================
TC-MCX-040 MCX User Profile Creation From IMSI
    [Documentation]    TC-MCX-040: Create MCX user profile from registered UE IMSI
    ...    Standard: TS 23.379 §A.3 (MCPTT user profile configuration data)
    ...    Procedure:
    ...    1. UE registered with valid IMSI in ue table
    ...    2. Call get_or_create_mcx_user(imsi) via REST API
    ...    3. MCPTT ID derived from IMSI (imsi_to_mcptt_id mapping)
    ...    4. Profile created in mcx_user_profiles with default priority=5
    ...    Verification:
    ...    - User profile exists with mcptt_id, display_name, priority
    ...    - Profile linked to ue_id in DB
    ...    - Duplicate call returns existing profile (idempotent)
    ...    Expected Result: MCX user profile created and retrievable
    [Tags]    user-profile    creation    imsi    priority-1
    Log    TC-MCX-040: MCX User Profile Creation

TC-MCX-041 MCX Group Creation And Membership
    [Documentation]    TC-MCX-041: Create MCX group and manage membership
    ...    Standard: TS 23.379 §A.4 (MCPTT related group configuration data)
    ...    Procedure:
    ...    1. Create MCX group (name, type=normal, max_members=50, priority=5)
    ...    2. Add 3 MCX users to group via join_group()
    ...    3. Verify membership list contains all 3 members
    ...    4. Remove 1 member via leave_group()
    ...    5. Verify membership updated to 2 members
    ...    Verification:
    ...    - Group record created with correct attributes
    ...    - Members added/removed correctly
    ...    - Capacity enforced (max_members limit)
    ...    Expected Result: Group CRUD and membership operations work
    [Tags]    group    creation    membership    priority-1
    Log    TC-MCX-041: MCX Group Creation And Membership

TC-MCX-042 MCX Group Capacity Limit Enforced
    [Documentation]    TC-MCX-042: Group rejects member when at max capacity
    ...    Standard: TS 23.379 §A.4 (MCPTT related group configuration data — capacity is a deployment policy applied on top of §A.4 group config)
    ...    Procedure:
    ...    1. Create MCX group with max_members=2
    ...    2. Add 2 members — both succeed
    ...    3. Attempt to add 3rd member — rejected with "Group full"
    ...    Verification:
    ...    - 3rd join_group() returns (False, "Group full")
    ...    - Group membership count remains 2
    ...    Expected Result: Group capacity limit enforced
    [Tags]    group    capacity    negative    priority-2
    Log    TC-MCX-042: MCX Group Capacity Limit

# ===============================================================
# MCX Priority & Preemption
# TS 24.380 §6.2.4.5 — 'U: has permission' state (Floor Revoke handling)
# Priority levels: 1=emergency, 2=imminent_peril, 3=high, 5=normal, 7=low, 9=background
# ===============================================================
TC-MCX-050 MCX Emergency Call Preempts Normal Call
    [Documentation]    TC-MCX-050: Emergency call pre-empts active normal group call
    ...    Standard: TS 23.379 §10.6.2.6.1.2 (MCPTT group call upgraded to an MCPTT emergency group call — i.e. the on-the-fly emergency upgrade that pre-empts the existing call)
    ...    TS 24.380 §6.2.4.5.4 (Receive Floor Revoke message — pre-emption mechanism on the floor participant FSM)
    ...    Procedure:
    ...    1. Active normal group call (priority=5) on group A
    ...    2. User initiates emergency group call (priority=1) on same group
    ...    3. Emergency call pre-empts — existing call resources reallocated
    ...    4. All participants notified of emergency escalation
    ...    Verification:
    ...    - Emergency call active with priority=1
    ...    - Pre-emption logic (can_preempt) evaluated correctly
    ...    - Floor holder of normal call revoked
    ...    Expected Result: Emergency call takes precedence over normal call
    [Tags]    priority    preemption    emergency    priority-1
    Log    TC-MCX-050: MCX Emergency Preempts Normal

TC-MCX-051 MCX Priority Floor Queue Ordering
    [Documentation]    TC-MCX-051: Floor queue orders requests by priority level
    ...    Standard: TS 24.380 §6.2.4.9 (State: 'U: queued' — floor request queueing on the floor participant FSM)
    ...    Procedure:
    ...    1. Active group call, Participant A (priority=5) holds floor
    ...    2. Participant B (priority=7/low) requests floor — queued
    ...    3. Participant C (priority=3/high) requests floor — queued
    ...    4. Participant A releases floor
    ...    5. Participant C (priority=3) granted floor before B (priority=7)
    ...    Verification:
    ...    - Floor queue sorted by priority (lower number = higher priority)
    ...    - Queue max size enforced (MAX_FLOOR_QUEUE config)
    ...    - After A releases, C gets floor (not B)
    ...    Expected Result: Floor granted in priority order from queue
    [Tags]    priority    floor-queue    ordering    priority-1
    Log    TC-MCX-051: MCX Priority Floor Queue Ordering
