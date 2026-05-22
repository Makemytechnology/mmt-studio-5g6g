# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    TCS (Tactical Communication System) Sidelink Test Suite
...              TS 23.287 §5.2 — V2X sidelink architecture
...              TS 38.300 §16.7 — NR Sidelink
...              Covers: PC5 sidelink transport, mesh routing, CRDT sync
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        tcs    sidelink    pc5    mesh

*** Test Cases ***
TC-TCS-001 Sidelink Transport Heartbeat
    [Documentation]    TC-TCS-001: PC5 sidelink heartbeat between nodes
    ...    Procedure:
    ...    1. Start TCS sidelink transport on two nodes
    ...    2. Verify heartbeat messages exchanged (5-second interval)
    ...    3. Verify peer detected in sync_peers table
    ...    Expected Result: Sidelink peers discover each other
    [Tags]    heartbeat    peer-discovery    priority-1
    Log    TC-TCS-001: Sidelink Heartbeat

TC-TCS-002 CRDT Database Sync
    [Documentation]    TC-TCS-002: CRDT database synchronization via sidelink
    ...    Procedure:
    ...    1. Two TCS nodes with sidelink connectivity
    ...    2. Node A registers a UE (creates ue_location entry)
    ...    3. CRDT changeset broadcast to Node B
    ...    4. Verify Node B has the UE location entry
    ...    Expected Result: UE location replicated across nodes
    [Tags]    crdt    db-sync    replication    priority-1
    Log    TC-TCS-002: CRDT Sync

TC-TCS-003 Mesh Routing Discovery
    [Documentation]    TC-TCS-003: Mesh routing neighbor discovery
    ...    Procedure:
    ...    1. Start mesh routing on multiple nodes
    ...    2. Verify MESH_HELLO messages exchanged
    ...    3. Verify routing table populated with neighbors
    ...    Expected Result: Mesh topology discovered
    [Tags]    mesh    routing    discovery    priority-2
    Log    TC-TCS-003: Mesh Routing

TC-TCS-010 Sidelink Message Authentication
    [Documentation]    TC-TCS-010: HMAC-SHA256 authentication on sidelink
    ...    Procedure:
    ...    1. Send message between two TCS nodes
    ...    2. Verify HMAC authentication on receipt
    ...    3. Inject tampered message, verify rejection
    ...    Expected Result: Authenticated messages accepted, tampered rejected
    [Tags]    security    hmac    authentication    priority-1
    Log    TC-TCS-010: Sidelink Authentication

TC-TCS-011 Sidelink Resource Pools
    [Documentation]    TC-TCS-011: V2X resource pool allocation
    ...    Procedure:
    ...    1. Verify 3 resource pools configured (Signaling 20%, RealTime 50%, DBSync 30%)
    ...    2. Send traffic on each pool
    ...    3. Verify traffic routed to correct pool port
    ...    Expected Result: Resource pools isolate traffic types
    [Tags]    resource-pool    qos    priority-2
    Log    TC-TCS-011: Resource Pools
