# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: ProSe Test Coverage Suite for Sidelink (TCS).

TS 23.304 — PC5 sidelink procedures exercised at the node level
(transport heartbeat, CRDT DB sync, mesh routing discovery, message
authentication, resource pool allocation). These are PC5-only flows
with no 5GC reference points, so the Python stubs currently mark the
procedure as pending and defer to the Robot catalog entries in
robot/suites/other/31_tcs_sidelink.robot for the step-by-step
procedure under test.

Companion to tc_prose.py (TC-PROSE-*), which covers the 5GC-side ProSe
control plane (NEF/AF app registration, UDM authorisation, AMF-driven
discovery / communication / relay flows).
"""

import logging

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_prose_sidelink")


# ─── TC-TCS-001 ──────────────────────────────────────────────────────


class TcsSidelinkHeartbeat(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TCS-001",
        title="Sidelink Transport Heartbeat — PC5 peer discovery via heartbeats",
        spec="TS 23.304 §5.3",
        domain=Domain.PROSE,
        nfs=(NF.AMF,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "sidelink", "heartbeat",
              "peer-discovery", "priority-1"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.304 §5.3 covers PC5 sidelink procedures. This test\n"
            "  pins the transport-layer heartbeat: two TCS nodes must\n"
            "  discover each other purely over PC5 (no 5GC involvement)\n"
            "  by exchanging heartbeat messages at a 5 s cadence and\n"
            "  populating their local sync_peers tables.\n"
            "\n"
            "Procedure (TS 23.304 §5.3 PC5 transport)\n"
            "  1. (Robot-only) Start TCS sidelink transport on Node A and\n"
            "     Node B.\n"
            "  2. Wait for two 5 s heartbeat intervals.\n"
            "  3. On Node A inspect sync_peers; assert Node B's identity is\n"
            "     present.\n"
            "  4. On Node B inspect sync_peers; assert Node A's identity is\n"
            "     present.\n"
            "  5. Python stub: immediately fail_test with a pointer to the\n"
            "     robot suite (PC5 has no 5GC REST endpoint to drive from\n"
            "     here).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure stub.\n"
            "\n"
            "Pass criteria\n"
            "  Sync_peers on both nodes contain the peer identity.\n"
            "  Python stub deliberately fail_test until PC5 driver lands.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — stub fails before recording metrics).\n"
            "\n"
            "Known constraints\n"
            "  No 5GC endpoint, hence the deliberate stub. Full procedure\n"
            "  lives in robot/suites/other/31_tcs_sidelink.robot::TC-TCS-001."
        ),
    )
    tc_id = "TC-TCS-001"
    name  = "tcs_sidelink_heartbeat"

    def run(self):
        self.fail_test(
            "Python implementation pending — see "
            "robot/suites/other/31_tcs_sidelink.robot::TC-TCS-001 for "
            "the procedure (PC5 sidelink heartbeat, no 5GC endpoint)."
        )
        return self.result


# ─── TC-TCS-002 ──────────────────────────────────────────────────────


class TcsCrdtDatabaseSync(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TCS-002",
        title="CRDT Database Sync — UE location replication over sidelink",
        spec="TS 23.304 §5.3",
        domain=Domain.PROSE,
        nfs=(NF.AMF, NF.UDM),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "sidelink", "crdt",
              "db-sync", "replication", "priority-1"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Above the PC5 transport, TS 23.304 §5.3 leaves replication\n"
            "  semantics open. This product uses CRDTs to gossip UE-context\n"
            "  changes between mobile-network nodes over the sidelink. The\n"
            "  test pins that a write on Node A propagates to Node B's DB\n"
            "  via the CRDT changeset channel.\n"
            "\n"
            "Procedure (TS 23.304 §5.3 + CRDT replication)\n"
            "  1. (Robot-only) Bring up two TCS nodes with PC5 sidelink up.\n"
            "  2. On Node A, register a UE — creates a ue_location row.\n"
            "  3. Wait for the CRDT changeset to be broadcast on the\n"
            "     sidelink channel.\n"
            "  4. On Node B, query ue_location for the same imsi.\n"
            "  5. Assert the row appears on Node B with matching imsi /\n"
            "     last_known fields.\n"
            "  6. Python stub: immediately fail_test pointing at the robot\n"
            "     suite (no 5GC REST endpoint can drive PC5 from here).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure stub.\n"
            "\n"
            "Pass criteria\n"
            "  Node B ue_location contains Node A's freshly registered UE.\n"
            "  Python stub deliberately fail_test until PC5 driver lands.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — stub fails before recording metrics).\n"
            "\n"
            "Known constraints\n"
            "  PC5-only flow with no 5GC REST surface. Implementation in\n"
            "  robot/suites/other/31_tcs_sidelink.robot::TC-TCS-002."
        ),
    )
    tc_id = "TC-TCS-002"
    name  = "tcs_crdt_database_sync"

    def run(self):
        self.fail_test(
            "Python implementation pending — see "
            "robot/suites/other/31_tcs_sidelink.robot::TC-TCS-002 for "
            "the procedure (PC5 sidelink CRDT sync, no 5GC endpoint)."
        )
        return self.result


# ─── TC-TCS-003 ──────────────────────────────────────────────────────


class TcsMeshRoutingDiscovery(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TCS-003",
        title="Mesh Routing Discovery — neighbour discovery via MESH_HELLO",
        spec="TS 23.304 §5.3",
        domain=Domain.PROSE,
        nfs=(NF.AMF,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "sidelink", "mesh",
              "routing", "discovery", "priority-2"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.304 §5.3 covers sidelink procedures but leaves multi-\n"
            "  hop routing to implementation. This product runs a mesh\n"
            "  routing layer that exchanges MESH_HELLO messages on PC5 to\n"
            "  build a local routing table. The test pins that neighbour\n"
            "  discovery converges and the routing table populates.\n"
            "\n"
            "Procedure (TS 23.304 §5.3 + mesh routing overlay)\n"
            "  1. (Robot-only) Bring up mesh routing on >= 2 TCS nodes\n"
            "     within sidelink range.\n"
            "  2. Wait for one or two MESH_HELLO cycles.\n"
            "  3. Capture MESH_HELLO frames on the sidelink; assert at\n"
            "     least one is seen.\n"
            "  4. Dump each node's routing table; assert it contains the\n"
            "     other node's identity as a neighbour.\n"
            "  5. Python stub: fail_test pointing at the robot suite.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure stub.\n"
            "\n"
            "Pass criteria\n"
            "  Routing tables on both nodes carry each other as neighbours.\n"
            "  Python stub deliberately fail_test until PC5 driver lands.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — stub fails before recording metrics).\n"
            "\n"
            "Known constraints\n"
            "  PC5-only flow. Full implementation in\n"
            "  robot/suites/other/31_tcs_sidelink.robot::TC-TCS-003."
        ),
    )
    tc_id = "TC-TCS-003"
    name  = "tcs_mesh_routing_discovery"

    def run(self):
        self.fail_test(
            "Python implementation pending — see "
            "robot/suites/other/31_tcs_sidelink.robot::TC-TCS-003 for "
            "the procedure (PC5 mesh routing, no 5GC endpoint)."
        )
        return self.result


# ─── TC-TCS-010 ──────────────────────────────────────────────────────


class TcsSidelinkMessageAuth(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TCS-010",
        title="Sidelink Message Authentication — HMAC-SHA256 on PC5",
        spec="TS 23.304 §5.3",
        domain=Domain.PROSE,
        nfs=(NF.AMF, NF.UDM),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "sidelink", "security",
              "hmac", "authentication", "negative", "priority-1"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Security gate above TS 23.304 §5.3 transport: every sidelink\n"
            "  payload is HMAC-SHA256 signed with a pre-shared key, and\n"
            "  the receiver MUST reject any frame whose HMAC doesn't\n"
            "  verify. This negative-case test pins both the happy path\n"
            "  (clean frame accepted) and the tamper path (modified frame\n"
            "  dropped).\n"
            "\n"
            "Procedure (TS 23.304 §5.3 + HMAC integrity layer)\n"
            "  1. (Robot-only) Bring up two TCS nodes sharing an HMAC key.\n"
            "  2. From Node A send a clean sidelink message to Node B.\n"
            "  3. Assert Node B logs HMAC verification success and accepts\n"
            "     the payload.\n"
            "  4. Inject a bit-flipped frame from a tamper helper.\n"
            "  5. Assert Node B logs HMAC verification failure and drops\n"
            "     the payload.\n"
            "  6. Python stub: fail_test pointing at the robot suite.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure stub.\n"
            "\n"
            "Pass criteria\n"
            "  Clean frame accepted on Node B AND tampered frame rejected\n"
            "  on Node B. Python stub deliberately fail_test until PC5\n"
            "  driver lands.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — stub fails before recording metrics).\n"
            "\n"
            "Known constraints\n"
            "  PC5-only flow with no 5GC endpoint. Full implementation in\n"
            "  robot/suites/other/31_tcs_sidelink.robot::TC-TCS-010."
        ),
    )
    tc_id = "TC-TCS-010"
    name  = "tcs_sidelink_message_auth"

    def run(self):
        self.fail_test(
            "Python implementation pending — see "
            "robot/suites/other/31_tcs_sidelink.robot::TC-TCS-010 for "
            "the procedure (PC5 sidelink HMAC, no 5GC endpoint)."
        )
        return self.result


# ─── TC-TCS-011 ──────────────────────────────────────────────────────


class TcsSidelinkResourcePools(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TCS-011",
        title="Sidelink Resource Pools — V2X traffic-type isolation",
        spec="TS 23.304 §5.3",
        domain=Domain.PROSE,
        nfs=(NF.AMF,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "sidelink", "resource-pool",
              "qos", "priority-2"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  V2X over PC5 (rooted in TS 23.304 §5.3) requires QoS-aware\n"
            "  resource pools so signalling, real-time vehicular data and\n"
            "  background DB sync don't trample each other. This product\n"
            "  splits the sidelink bandwidth into three pools (Signaling\n"
            "  20%, RealTime 50%, DBSync 30%) bound to distinct UDP ports.\n"
            "  The test pins pool config + correct steering.\n"
            "\n"
            "Procedure (TS 23.304 §5.3 V2X resource pools)\n"
            "  1. (Robot-only) Read sidelink resource-pool config; assert\n"
            "     three pools exist with 20% / 50% / 30% shares.\n"
            "  2. Send a Signaling test frame; assert it hits the Signaling\n"
            "     pool port.\n"
            "  3. Send a RealTime frame; assert it hits the RealTime pool\n"
            "     port.\n"
            "  4. Send a DBSync frame; assert it hits the DBSync pool port.\n"
            "  5. Python stub: fail_test pointing at the robot suite.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure stub.\n"
            "\n"
            "Pass criteria\n"
            "  All three pools configured AND traffic routed to the\n"
            "  expected pool port. Python stub deliberately fail_test\n"
            "  until PC5 driver lands.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — stub fails before recording metrics).\n"
            "\n"
            "Known constraints\n"
            "  PC5-only flow. Full implementation in\n"
            "  robot/suites/other/31_tcs_sidelink.robot::TC-TCS-011."
        ),
    )
    tc_id = "TC-TCS-011"
    name  = "tcs_sidelink_resource_pools"

    def run(self):
        self.fail_test(
            "Python implementation pending — see "
            "robot/suites/other/31_tcs_sidelink.robot::TC-TCS-011 for "
            "the procedure (PC5 V2X resource pools, no 5GC endpoint)."
        )
        return self.result


ALL_TCS_SIDELINK_TCS = [
    TcsSidelinkHeartbeat,
    TcsCrdtDatabaseSync,
    TcsMeshRoutingDiscovery,
    TcsSidelinkMessageAuth,
    TcsSidelinkResourcePools,
]
