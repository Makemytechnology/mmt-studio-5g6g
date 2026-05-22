# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: PIN (Personal IoT Network).

TS 23.542 -- Architecture enhancements for Personal IoT Networks (PIN).
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src import baseline

log = logging.getLogger("tester.tc_pin")


def _pin_api(path, method="GET", body=None):
    """Call SA Core PIN REST API."""
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode()), resp.status
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


def _create_network(owner_imsi=None):
    """Helper: create a PIN network, return (result, status, network_id).

    owner_imsi is stored as a key in /api/pin/networks; no NGAP/UPF flow
    is exercised, so defaulting to a baseline UE keeps the relationship
    intact without inventing off-roster identifiers.
    """
    if owner_imsi is None:
        owner_imsi = baseline.imsi("embb-bulk", 0)
    result, status = _pin_api("/api/pin/networks", "POST", {
        "owner_imsi": owner_imsi,
        "name": "test-pin-network",
    })
    net_id = result.get("id") or result.get("network_id")
    return result, status, net_id


class PinCreateNetwork(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PIN-001",
        title="Create and delete a Personal IoT Network",
        spec="TS 23.542 §5",
        domain=Domain.IOT,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the Personal IoT Network registry.\n"
            "  TS 23.542 §5 defines a PIN as a set of PIN elements + a\n"
            "  PIN gateway anchored on a 5GS subscriber (the PIN owner).\n"
            "  Creation/deletion is the most basic CRUD invariant for the\n"
            "  Rel-18 PIN feature.\n"
            "\n"
            "Procedure (TS 23.542 §5.2.2)\n"
            "  1. _create_network() — POST /api/pin/networks with\n"
            "     owner_imsi = baseline.imsi('embb-bulk', 0) and name\n"
            "     'test-pin-network'.\n"
            "  2. fail_test if status not in (200, 201).\n"
            "  3. Read network_id from response (id or network_id).\n"
            "  4. finally: DELETE /api/pin/networks/{network_id} so the\n"
            "     registry is left clean for the next test.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — owner IMSI is fixed to the first baseline eMBB UE.\n"
            "\n"
            "Pass criteria\n"
            "  POST returns HTTP 200/201 with a parseable network_id.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  network_id, network (full envelope).\n"
            "\n"
            "Known constraints\n"
            "  No NGAP/NAS attach is exercised — owner_imsi is a foreign\n"
            "  key into UDM, the actual UE need not be registered.\n"
            "  Owner IMSI must exist as a UDM row but no actual NAS attach is\n"
            "  required. Network row is reaped on finally so the registry stays\n"
            "  tidy across runs."
        ),
    )

    def run(self):
        net_id = None
        try:
            result, status, net_id = _create_network()
            if status not in (200, 201):
                self.fail_test(f"Create network failed: {status} {result}")
                return self.result

            log.info("PIN network created: id=%s", net_id)
            self.pass_test(network_id=net_id, network=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if net_id:
                _pin_api(f"/api/pin/networks/{net_id}", "DELETE")
        return self.result


class PinAddElement(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PIN-002",
        title="Add a sensor element to a PIN network",
        spec="TS 23.542 §5",
        domain=Domain.IOT,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the PIN element attach path: TS 23.542 §5.3 says\n"
            "  a PIN element joins the PIN via a non-3GPP local-radio link\n"
            "  (BLE, WiFi, Zigbee, etc.). The registry must accept the\n"
            "  element under its network.\n"
            "\n"
            "Procedure (TS 23.542 §5.3.2)\n"
            "  1. _create_network() — same as TC-PIN-001.\n"
            "  2. POST /api/pin/networks/{network_id}/elements with\n"
            "     element_id='SENSOR-001', element_type='sensor',\n"
            "     protocol='BLE', name='Temperature Sensor'.\n"
            "  3. fail_test if element POST not in (200, 201).\n"
            "  4. finally: DELETE the network to reclaim the rows.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — element shape is pinned.\n"
            "\n"
            "Pass criteria\n"
            "  Element POST returns HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  network_id, element (the create envelope).\n"
            "\n"
            "Known constraints\n"
            "  BLE link itself is not modelled — protocol is a registry\n"
            "  label, no actual L2 simulation runs.\n"
            "  Element type / protocol are registry labels; no L2 simulation\n"
            "  runs. Multi-protocol behaviour is left to TC-PIN-004 (relay)\n"
            "  and operator policy.\n"
            "  Operator should ensure the SA Core has BLE / WiFi / Zigbee\n"
            "  type tags enabled before running."
        ),
    )

    def run(self):
        net_id = None
        try:
            result, status, net_id = _create_network()
            if status not in (200, 201):
                self.fail_test(f"Create network failed: {status} {result}")
                return self.result

            elem_result, elem_status = _pin_api(
                f"/api/pin/networks/{net_id}/elements", "POST", {
                    "element_id": "SENSOR-001",
                    "element_type": "sensor",
                    "protocol": "BLE",
                    "name": "Temperature Sensor",
                })
            if elem_status not in (200, 201):
                self.fail_test(f"Add element failed: {elem_status} {elem_result}")
                return self.result

            log.info("PIN element added to network %s", net_id)
            self.pass_test(network_id=net_id, element=elem_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if net_id:
                _pin_api(f"/api/pin/networks/{net_id}", "DELETE")
        return self.result


class PinSetGateway(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PIN-003",
        title="Bind a gateway UE (IMSI) to a PIN network",
        spec="TS 23.542 §5",
        domain=Domain.IOT,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins the gateway binding step: TS 23.542 §5.2 says one UE\n"
            "  acts as the PIN Gateway (PEGC), bridging the PIN's local\n"
            "  network into 5GS. Without this binding the PIN cannot relay\n"
            "  traffic out to the DN.\n"
            "\n"
            "Procedure (TS 23.542 §5.4.2)\n"
            "  1. _create_network() to obtain network_id.\n"
            "  2. gateway_imsi = baseline.imsi('embb-bulk', 1).\n"
            "  3. POST /api/pin/networks/{network_id}/gateway with the\n"
            "     gateway_imsi.\n"
            "  4. fail_test if status not in (200, 201).\n"
            "  5. GET /api/pin/networks/{network_id}; pull gateway_imsi\n"
            "     (or gateway.imsi nested) and compare to expected.\n"
            "  6. fail_test on mismatch.\n"
            "  7. finally: DELETE the network.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — gateway IMSI = baseline eMBB UE #1.\n"
            "\n"
            "Pass criteria\n"
            "  gateway POST 200/201 AND readback gateway_imsi == expected.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  network_id, gateway_imsi.\n"
            "\n"
            "Known constraints\n"
            "  No PDU session is established on the gateway UE — only the\n"
            "  registry binding is exercised.\n"
            "  No PDU session is established on the gateway UE — the binding\n"
            "  is purely an UDM/AF-level foreign key. Cleanup deletes the\n"
            "  network, which cascades to the gateway row."
        ),
    )

    def run(self):
        net_id = None
        gateway_imsi = baseline.imsi("embb-bulk", 1)
        try:
            result, status, net_id = _create_network()
            if status not in (200, 201):
                self.fail_test(f"Create network failed: {status} {result}")
                return self.result

            gw_result, gw_status = _pin_api(
                f"/api/pin/networks/{net_id}/gateway", "POST", {
                    "gateway_imsi": gateway_imsi,
                })
            if gw_status not in (200, 201):
                self.fail_test(f"Set gateway failed: {gw_status} {gw_result}")
                return self.result

            # Verify gateway set
            net_result, net_status = _pin_api(f"/api/pin/networks/{net_id}")
            if net_status != 200:
                self.fail_test(f"GET network failed: {net_status}")
                return self.result

            gw = net_result.get("gateway_imsi") or net_result.get("gateway", {}).get("imsi")
            if gw != gateway_imsi:
                self.fail_test(f"Gateway mismatch: expected {gateway_imsi}, got {gw}",
                               network=net_result)
                return self.result

            log.info("PIN gateway set: network=%s gateway=%s", net_id, gateway_imsi)
            self.pass_test(network_id=net_id, gateway_imsi=gateway_imsi)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if net_id:
                _pin_api(f"/api/pin/networks/{net_id}", "DELETE")
        return self.result


class PinRelayData(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PIN-004",
        title="Relay data through a PIN network and verify the log",
        spec="TS 23.542 §5",
        domain=Domain.IOT,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  End-to-end PIN data-plane smoke: builds the full PIN graph\n"
            "  (network + element + gateway), relays a small DL frame, and\n"
            "  verifies the data log records the event. Pins the §5.5\n"
            "  relay role of the PEGC.\n"
            "\n"
            "Procedure (TS 23.542 §5.5)\n"
            "  1. _create_network() — owner=baseline eMBB UE #0.\n"
            "  2. POST element SENSOR-002 (sensor, BLE).\n"
            "  3. POST gateway = baseline.imsi('embb-bulk', 1).\n"
            "  4. POST /api/pin/networks/{id}/relay with element_id=\n"
            "     'SENSOR-002', data_hex='48656C6C6F' ('Hello'),\n"
            "     direction='DL'.\n"
            "  5. fail_test if relay status not in (200, 201).\n"
            "  6. GET /api/pin/networks/{id}/data-log.\n"
            "  7. Extract entries (entries or data_log key) and fail_test\n"
            "     if empty.\n"
            "  8. finally: DELETE network.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — payload pinned to 0x48656C6C6F.\n"
            "\n"
            "Pass criteria\n"
            "  relay POST 200/201 AND data-log has at least one entry.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  network_id, data_entries (count).\n"
            "\n"
            "Known constraints\n"
            "  Element / gateway POSTs ignore status — only the relay path\n"
            "  must succeed. No real BLE / UPF traversal."
        ),
    )

    def run(self):
        net_id = None
        try:
            result, status, net_id = _create_network()
            if status not in (200, 201):
                self.fail_test(f"Create network failed: {status} {result}")
                return self.result

            # Add element
            _pin_api(f"/api/pin/networks/{net_id}/elements", "POST", {
                "element_id": "SENSOR-002",
                "element_type": "sensor",
                "protocol": "BLE",
                "name": "Relay Test Sensor",
            })

            # Set gateway
            _pin_api(f"/api/pin/networks/{net_id}/gateway", "POST", {
                "gateway_imsi": baseline.imsi("embb-bulk", 1),
            })

            # Relay data
            relay_result, relay_status = _pin_api(
                f"/api/pin/networks/{net_id}/relay", "POST", {
                    "element_id": "SENSOR-002",
                    "data_hex": "48656C6C6F",
                    "direction": "DL",
                })
            if relay_status not in (200, 201):
                self.fail_test(f"Relay data failed: {relay_status} {relay_result}")
                return self.result

            # Verify data log
            dl_result, dl_status = _pin_api(
                f"/api/pin/networks/{net_id}/data-log")
            if dl_status != 200:
                self.fail_test(f"GET data-log failed: {dl_status}")
                return self.result

            entries = dl_result.get("entries", dl_result.get("data_log", []))
            if not entries:
                self.fail_test("Data log is empty after relay")
                return self.result

            log.info("PIN data relayed: %d log entries", len(entries))
            self.pass_test(network_id=net_id, data_entries=len(entries))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if net_id:
                _pin_api(f"/api/pin/networks/{net_id}", "DELETE")
        return self.result


class PinRemoveElement(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PIN-005",
        title="Remove a PIN element and verify it is gone",
        spec="TS 23.542 §5",
        domain=Domain.IOT,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Negative-CRUD invariant: deleting a PIN element must remove\n"
            "  it from the network's elements list. Without this the PIN\n"
            "  topology cannot be edited operationally.\n"
            "\n"
            "Procedure (TS 23.542 §5.3.3)\n"
            "  1. _create_network() to obtain network_id.\n"
            "  2. POST element 'SENSOR-003' (sensor, BLE) — status ignored.\n"
            "  3. DELETE /api/pin/networks/{id}/elements/SENSOR-003.\n"
            "  4. fail_test if delete status not in (200, 204).\n"
            "  5. GET /api/pin/networks/{id} and pull elements list.\n"
            "  6. fail_test if 'SENSOR-003' still appears.\n"
            "  7. finally: DELETE network.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Element DELETE returns 200/204 AND element_id is absent\n"
            "  from the post-delete network readback.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  network_id, removed_element (SENSOR-003).\n"
            "\n"
            "Known constraints\n"
            "  No cross-element relationships are tested (PIN inter-element\n"
            "  links per §5.3.4 are out of scope here).\n"
            "  No cross-element relationships are tested — PIN inter-element\n"
            "  links per §5.3.4 are out of scope here. Final delete reclaims\n"
            "  the network and all remaining child rows."
        ),
    )

    def run(self):
        net_id = None
        elem_id = "SENSOR-003"
        try:
            result, status, net_id = _create_network()
            if status not in (200, 201):
                self.fail_test(f"Create network failed: {status} {result}")
                return self.result

            # Add element
            _pin_api(f"/api/pin/networks/{net_id}/elements", "POST", {
                "element_id": elem_id,
                "element_type": "sensor",
                "protocol": "BLE",
                "name": "Remove Test Sensor",
            })

            # Remove element
            del_result, del_status = _pin_api(
                f"/api/pin/networks/{net_id}/elements/{elem_id}", "DELETE")
            if del_status not in (200, 204):
                self.fail_test(f"Delete element failed: {del_status} {del_result}")
                return self.result

            # Verify removed
            net_result, net_status = _pin_api(f"/api/pin/networks/{net_id}")
            if net_status != 200:
                self.fail_test(f"GET network failed: {net_status}")
                return self.result

            elements = net_result.get("elements", [])
            elem_ids = [e.get("element_id") for e in elements]
            if elem_id in elem_ids:
                self.fail_test("Element still present after delete",
                               elements=elements)
                return self.result

            log.info("PIN element removed: network=%s element=%s", net_id, elem_id)
            self.pass_test(network_id=net_id, removed_element=elem_id)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if net_id:
                _pin_api(f"/api/pin/networks/{net_id}", "DELETE")
        return self.result


ALL_PIN_TCS = [
    PinCreateNetwork,
    PinAddElement,
    PinSetGateway,
    PinRelayData,
    PinRemoveElement,
]
