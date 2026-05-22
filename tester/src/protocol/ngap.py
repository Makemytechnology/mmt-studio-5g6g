# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""NGAP APER encode/decode — thread-safe pycrate wrapper. TS 38.413."""

import threading
import struct
import socket as _socket

from pycrate_asn1dir import NGAP
from libs.gpp_utils import encode_plmn_id

_lock = threading.Lock()


class NgapCodec:
    """Thread-safe NGAP APER encode/decode."""

    @staticmethod
    def encode(val):
        with _lock:
            pdu = NGAP.NGAP_PDU_Descriptions.NGAP_PDU
            pdu.set_val(val)
            return pdu.to_aper()

    @staticmethod
    def decode(buf):
        """Returns (category, proc_code, ies_dict)."""
        with _lock:
            pdu = NGAP.NGAP_PDU_Descriptions.NGAP_PDU
            pdu.from_aper(buf)
            pdu_val = pdu.get_val()
        category, data = pdu_val
        proc_code = data['procedureCode']
        msg_value = data['value']
        msg_data = msg_value[1] if isinstance(msg_value, tuple) else msg_value
        ies = NgapCodec.extract_ies(msg_data) if isinstance(msg_data, dict) else {}
        return category, proc_code, ies

    @staticmethod
    def extract_ies(msg_data):
        ies = {}
        for ie in msg_data.get('protocolIEs', []):
            val = ie['value']
            ies[ie['id']] = val[1] if isinstance(val, tuple) and len(val) == 2 else val
        return ies

    @staticmethod
    def build_ng_setup_request(gnb_id, gnb_name, mcc, mnc, tac, slices, paging_drx="v128"):
        plmn = encode_plmn_id(mcc, mnc)
        tac_bytes = bytes.fromhex(tac.zfill(6))
        slice_list = []
        for s in slices:
            snssai = {'sST': bytes([s["sst"]])}
            if s.get("sd") is not None:
                snssai['sD'] = s["sd"].to_bytes(3, 'big')
            slice_list.append({'s-NSSAI': snssai})
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 21, "criticality": "reject",
            "value": ("NGSetupRequest", {"protocolIEs": [
                {"id": 21, "criticality": "ignore", "value": ("PagingDRX", paging_drx)},
                {"id": 27, "criticality": "reject", "value": ("GlobalRANNodeID",
                    ("globalGNB-ID", {"pLMNIdentity": plmn, "gNB-ID": ("gNB-ID", (gnb_id, 32))}))},
                {"id": 82, "criticality": "ignore", "value": ("RANNodeName", gnb_name)},
                {"id": 102, "criticality": "reject", "value": ("SupportedTAList", [{
                    "tAC": tac_bytes,
                    "broadcastPLMNList": [{"pLMNIdentity": plmn, "tAISliceSupportList": slice_list}],
                }])},
            ]}),
        }))

    @staticmethod
    def build_initial_ue_message(ran_ue_ngap_id, nas_pdu, mcc, mnc, tac, gnb_id):
        plmn = encode_plmn_id(mcc, mnc)
        tac_bytes = bytes.fromhex(tac.zfill(6))
        nr_cell_id = (gnb_id << 4) | 0x0
        loc = ("userLocationInformationNR", {
            "nR-CGI": {"pLMNIdentity": plmn, "nRCellIdentity": (nr_cell_id, 36)},
            "tAI": {"pLMNIdentity": plmn, "tAC": tac_bytes},
        })
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 15, "criticality": "ignore",
            "value": ("InitialUEMessage", {"protocolIEs": [
                {"id": 38, "criticality": "reject", "value": ("NAS-PDU", nas_pdu)},
                {"id": 85, "criticality": "reject", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 90, "criticality": "ignore", "value": ("RRCEstablishmentCause", "mo-Signalling")},
                {"id": 112, "criticality": "ignore", "value": ("UEContextRequest", "requested")},
                {"id": 121, "criticality": "reject", "value": ("UserLocationInformation", loc)},
            ]}),
        }))

    @staticmethod
    def build_uplink_nas_transport(amf_ue_ngap_id, ran_ue_ngap_id, nas_pdu, mcc, mnc, tac, gnb_id):
        plmn = encode_plmn_id(mcc, mnc)
        tac_bytes = bytes.fromhex(tac.zfill(6))
        nr_cell_id = (gnb_id << 4) | 0x0
        loc = ("userLocationInformationNR", {
            "nR-CGI": {"pLMNIdentity": plmn, "nRCellIdentity": (nr_cell_id, 36)},
            "tAI": {"pLMNIdentity": plmn, "tAC": tac_bytes},
        })
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 46, "criticality": "ignore",
            "value": ("UplinkNASTransport", {"protocolIEs": [
                {"id": 10, "criticality": "reject", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 38, "criticality": "reject", "value": ("NAS-PDU", nas_pdu)},
                {"id": 85, "criticality": "reject", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 121, "criticality": "ignore", "value": ("UserLocationInformation", loc)},
            ]}),
        }))

    @staticmethod
    def build_initial_context_setup_response(amf_ue_ngap_id, ran_ue_ngap_id):
        return NgapCodec.encode(('successfulOutcome', {
            "procedureCode": 14, "criticality": "reject",
            "value": ("InitialContextSetupResponse", {"protocolIEs": [
                {"id": 10, "criticality": "ignore", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 85, "criticality": "ignore", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
            ]}),
        }))

    @staticmethod
    def build_pdu_session_resource_setup_response(amf_ue_ngap_id, ran_ue_ngap_id,
                                                    pdu_session_id, gnb_gtp_teid, gnb_gtp_ip):
        ip_bytes = _socket.inet_aton(gnb_gtp_ip)
        with _lock:
            xfer = NGAP.NGAP_IEs.PDUSessionResourceSetupResponseTransfer
            xfer.set_val({
                'dLQosFlowPerTNLInformation': {
                    'uPTransportLayerInformation': ('gTPTunnel', {
                        'transportLayerAddress': (int.from_bytes(ip_bytes, 'big'), 32),
                        'gTP-TEID': struct.pack('>I', gnb_gtp_teid),
                    }),
                    'associatedQosFlowList': [{'qosFlowIdentifier': 1}],
                },
            })
            transfer_bytes = xfer.to_aper()
        return NgapCodec.encode(('successfulOutcome', {
            "procedureCode": 29, "criticality": "reject",
            "value": ("PDUSessionResourceSetupResponse", {"protocolIEs": [
                {"id": 10, "criticality": "ignore", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 75, "criticality": "ignore", "value": ("PDUSessionResourceSetupListSURes", [{
                    'pDUSessionID': pdu_session_id,
                    'pDUSessionResourceSetupResponseTransfer': transfer_bytes,
                }])},
                {"id": 85, "criticality": "ignore", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
            ]}),
        }))

    @staticmethod
    def build_pdu_session_resource_setup_response_failed(
            amf_ue_ngap_id, ran_ue_ngap_id, pdu_session_id,
            cause_group="transport", cause_value="transport-resource-unavailable"):
        """Build PDUSessionResourceSetupResponse with FailedToSetupListSURes (IE 76).

        Used when the gNB *cannot* provision the local user-plane resources
        (TUN create, route install, etc.) for the PDU session AMF asked for.
        Sending a Failed-To-Setup item — instead of a fake success — lets
        SMF release the session cleanly via PFCP and the UE gets a proper
        rejection instead of a tunnel that silently drops every packet.

        cause_group / cause_value default to ('transport',
        'transport-resource-unavailable') which matches "we couldn't bring
        up N3 locally". Other useful pairings:
          ('radioNetwork', 'radio-resources-not-available')
          ('misc', 'hardware-failure')
        See TS 38.413 §9.3.1.2 for the full enum.
        """
        with _lock:
            xfer = NGAP.NGAP_IEs.PDUSessionResourceSetupUnsuccessfulTransfer
            xfer.set_val({'cause': (cause_group, cause_value)})
            transfer_bytes = xfer.to_aper()
        return NgapCodec.encode(('successfulOutcome', {
            "procedureCode": 29, "criticality": "reject",
            "value": ("PDUSessionResourceSetupResponse", {"protocolIEs": [
                {"id": 10, "criticality": "ignore", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 76, "criticality": "ignore", "value": ("PDUSessionResourceFailedToSetupListSURes", [{
                    'pDUSessionID': pdu_session_id,
                    'pDUSessionResourceSetupUnsuccessfulTransfer': transfer_bytes,
                }])},
                {"id": 85, "criticality": "ignore", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
            ]}),
        }))

    @staticmethod
    def decode_pdu_session_setup_request_transfer(transfer_bytes):
        """Decode PDUSessionResourceSetupRequestTransfer APER bytes.

        Extracts UL-NGU-UP-TNLInformation which contains gTPTunnel
        with transportLayerAddress (bit string -> IP) and gTP-TEID (4 bytes -> int).

        Returns (upf_ip, upf_teid) or (None, None).

        pycrate's NGAP ASN.1 grammar is the golden standard — diagnostics below
        log the exact reason a peer's encoding fails to align so non-conforming
        core implementations can be identified without touching the FSM.
        """
        import logging
        _ngap_log = logging.getLogger("tester.ngap")

        # Always record the raw bytes we were handed so a peer-encoding problem
        # can be reproduced offline.
        _hex_preview = bytes(transfer_bytes or b"")[:48].hex()
        _total_len = len(transfer_bytes) if transfer_bytes is not None else 0

        try:
            with _lock:
                xfer = NGAP.NGAP_IEs.PDUSessionResourceSetupRequestTransfer
                xfer.from_aper(transfer_bytes)
                val = xfer.get_val()
        except Exception as e:
            _ngap_log.warning(
                "pycrate APER decode FAILED for "
                "PDUSessionResourceSetupRequestTransfer (%d bytes, hex=%s...): %s",
                _total_len, _hex_preview, e)
            return (None, None)

        # Decode succeeded — walk the decoded structure and look for IE 139.
        ies = val.get('protocolIEs', []) if isinstance(val, dict) else []
        ids_seen = [ie.get('id') if isinstance(ie, dict) else None for ie in ies]

        for ie in ies:
            if not isinstance(ie, dict):
                continue
            ie_id = ie.get('id')
            ie_val = ie.get('value')
            # IE 139 = UL-NGU-UP-TNLInformation (UPTransportLayerInformation)
            if ie_id != 139:
                continue
            if not ie_val:
                _ngap_log.warning("IE 139 present but value is empty (%r)", ie_val)
                continue
            # ie_val = ('UPTransportLayerInformation', ('gTPTunnel', {...}))
            inner = ie_val
            if isinstance(inner, tuple) and len(inner) == 2:
                inner = inner[1]
            if isinstance(inner, tuple) and len(inner) == 2:
                tunnel_info = inner[1]
            elif isinstance(inner, dict):
                tunnel_info = inner
            else:
                _ngap_log.warning(
                    "IE 139 unwrap failed: inner type=%s value=%r",
                    type(inner).__name__, str(inner)[:200])
                continue
            # transportLayerAddress is (int_value, bit_length)
            addr_val = tunnel_info.get('transportLayerAddress') if isinstance(tunnel_info, dict) else None
            teid_bytes = tunnel_info.get('gTP-TEID') if isinstance(tunnel_info, dict) else None
            if not addr_val or not teid_bytes:
                _ngap_log.warning(
                    "IE 139 tunnel_info missing fields: keys=%s addr=%r teid=%r",
                    list(tunnel_info.keys()) if isinstance(tunnel_info, dict) else '(not-dict)',
                    addr_val, teid_bytes)
                continue
            try:
                ip_int = addr_val[0] if isinstance(addr_val, tuple) else addr_val
                upf_ip = _socket.inet_ntoa(ip_int.to_bytes(4, 'big'))
                if isinstance(teid_bytes, (bytes, bytearray)):
                    upf_teid = struct.unpack('>I', teid_bytes)[0]
                else:
                    upf_teid = int(teid_bytes)
                return (upf_ip, upf_teid)
            except Exception as e:
                _ngap_log.warning(
                    "IE 139 field conversion failed: addr=%r teid=%r err=%s",
                    addr_val, teid_bytes, e)
                continue

        # Decoded but IE 139 never matched.
        _ngap_log.warning(
            "PDUSessionResourceSetupRequestTransfer decoded but no usable IE 139 "
            "(%d bytes, hex=%s..., ids_seen=%s)",
            _total_len, _hex_preview, ids_seen)
        return (None, None)

    @staticmethod
    def build_ue_context_release_complete(amf_ue_ngap_id, ran_ue_ngap_id):
        return NgapCodec.encode(('successfulOutcome', {
            "procedureCode": 41, "criticality": "reject",
            "value": ("UEContextReleaseComplete", {"protocolIEs": [
                {"id": 10, "criticality": "ignore", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 85, "criticality": "ignore", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
            ]}),
        }))

    # ─── UE Context Release / Error / RLF Messages ───

    @staticmethod
    def build_ue_context_release_request(amf_ue_ngap_id, ran_ue_ngap_id,
                                          cause_group="radioNetwork",
                                          cause_value="radio-connection-with-ue-lost"):
        """UEContextReleaseRequest — gNB → AMF (TS 38.413 §8.3.2.2).

        gNB requests AMF to release UE context.
        Used for RLF, inactivity, AN release.
        """
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 42, "criticality": "ignore",
            "value": ("UEContextReleaseRequest", {"protocolIEs": [
                {"id": 10, "criticality": "reject", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 85, "criticality": "reject", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 15, "criticality": "ignore", "value": ("Cause",
                    (cause_group, cause_value))},
            ]}),
        }))

    @staticmethod
    def build_error_indication(amf_ue_ngap_id=None, ran_ue_ngap_id=None,
                                cause_group="radioNetwork", cause_value="unspecified"):
        """ErrorIndication — gNB → AMF (TS 38.413 §8.7.5.2 Successful Operation).

        Reports protocol errors or abnormal conditions.
        """
        ies = []
        if amf_ue_ngap_id is not None:
            ies.append({"id": 10, "criticality": "ignore",
                        "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)})
        if ran_ue_ngap_id is not None:
            ies.append({"id": 85, "criticality": "ignore",
                        "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)})
        ies.append({"id": 15, "criticality": "ignore",
                    "value": ("Cause", (cause_group, cause_value))})
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 9, "criticality": "ignore",
            "value": ("ErrorIndication", {"protocolIEs": ies}),
        }))

    @staticmethod
    def build_rrc_inactive_transition_report(amf_ue_ngap_id, ran_ue_ngap_id,
                                              rrc_state, mcc, mnc, tac, gnb_id):
        """RRCInactiveTransitionReport — gNB → AMF (TS 38.413 §8.7.4.2).

        Reports UE transition to/from RRC Inactive state.
        """
        plmn = encode_plmn_id(mcc, mnc)
        tac_bytes = bytes.fromhex(tac.zfill(6))
        nr_cell_id = (gnb_id << 4) | 0x0
        loc = ("userLocationInformationNR", {
            "nR-CGI": {"pLMNIdentity": plmn, "nRCellIdentity": (nr_cell_id, 36)},
            "tAI": {"pLMNIdentity": plmn, "tAC": tac_bytes},
        })
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 37, "criticality": "ignore",
            "value": ("RRCInactiveTransitionReport", {"protocolIEs": [
                {"id": 10, "criticality": "reject", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 85, "criticality": "reject", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 92, "criticality": "ignore", "value": ("RRCState", rrc_state)},
                {"id": 121, "criticality": "ignore", "value": ("UserLocationInformation", loc)},
            ]}),
        }))

    # ─── Handover Messages (TS 38.413 §8.4) ───

    @staticmethod
    def build_handover_required(amf_ue_ngap_id, ran_ue_ngap_id,
                                source_gnb_id, target_gnb_id, mcc, mnc, tac,
                                pdu_sessions, cause="handover-desirable-for-radio-reason"):
        """HandoverRequired — source gNB → AMF (TS 38.413 §8.4.1.2).

        Args:
            source_gnb_id: int — source gNB's globalGNB-ID (for uEHistoryInformation)
            target_gnb_id: int — target gNB's globalGNB-ID
            pdu_sessions: list of dict with 'psi' key — PDU sessions to handover
            cause: str — handover cause (radioNetwork category)
        """
        plmn = encode_plmn_id(mcc, mnc)
        tac_bytes = bytes.fromhex(tac.zfill(6))
        target_cell_id = (target_gnb_id << 4) | 0x0
        source_cell_id = (source_gnb_id << 4) | 0x0

        # rRCContainer: valid minimal UPER-encoded HandoverPreparationInformation
        # TS 38.331: criticalExtensions=c1, c1=handoverPreparationInformation,
        # HandoverPreparationInformation-IEs with all optional fields absent
        rrc_container = b'\x00\x00'

        # Source-to-Target Transparent Container (opaque for AMF)
        # AMF forwards this to target gNB without parsing
        # Mandatory: rRCContainer, targetCell-ID, uEHistoryInformation
        with _lock:
            container = NGAP.NGAP_IEs.SourceNGRANNode_ToTargetNGRANNode_TransparentContainer
            container.set_val({
                'rRCContainer': rrc_container,
                'targetCell-ID': ('nR-CGI', {
                    'pLMNIdentity': plmn,
                    'nRCellIdentity': (target_cell_id, 36),
                }),
                'uEHistoryInformation': [{
                    'lastVisitedCellInformation': ('nGRANCell', {
                        'globalCellID': ('nR-CGI', {
                            'pLMNIdentity': plmn,
                            'nRCellIdentity': (source_cell_id, 36),
                        }),
                        'cellType': {'cellSize': 'medium'},
                        'timeUEStayedInCell': 10,
                    }),
                }],
            })
            container_bytes = container.to_aper()

        # PDU Session Resource List for Handover
        pdu_list = []
        for sess in pdu_sessions:
            psi = sess.get('psi', 1)
            # HandoverRequiredTransfer — empty for basic handover
            with _lock:
                xfer = NGAP.NGAP_IEs.HandoverRequiredTransfer
                xfer.set_val({})
                xfer_bytes = xfer.to_aper()
            pdu_list.append({
                'pDUSessionID': psi,
                'handoverRequiredTransfer': xfer_bytes,
            })

        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 12, "criticality": "reject",
            "value": ("HandoverRequired", {"protocolIEs": [
                {"id": 10, "criticality": "reject", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 85, "criticality": "reject", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 29, "criticality": "reject", "value": ("HandoverType", "intra5gs")},
                {"id": 15, "criticality": "ignore", "value": ("Cause",
                    ("radioNetwork", cause))},
                {"id": 105, "criticality": "reject", "value": ("TargetID",
                    ("targetRANNodeID", {
                        "globalRANNodeID": ("globalGNB-ID", {
                            "pLMNIdentity": plmn,
                            "gNB-ID": ("gNB-ID", (target_gnb_id, 32)),
                        }),
                        "selectedTAI": {
                            "pLMNIdentity": plmn,
                            "tAC": tac_bytes,
                        },
                    }))},
                {"id": 61, "criticality": "reject", "value": (
                    "PDUSessionResourceListHORqd", pdu_list)},
                {"id": 101, "criticality": "reject", "value": (
                    "SourceToTarget-TransparentContainer", container_bytes)},
            ]}),
        }))

    @staticmethod
    def build_handover_request_acknowledge(amf_ue_ngap_id, ran_ue_ngap_id,
                                           pdu_sessions, gnb_gtp_ip, gnb_gtp_teids):
        """HandoverRequestAcknowledge — target gNB → AMF (TS 38.413 §8.4.2.2).

        Args:
            pdu_sessions: list of dict with 'psi' key
            gnb_gtp_ip: str — target gNB's GTP-U IP
            gnb_gtp_teids: dict — {psi: teid} — new TEIDs on target gNB
        """
        ip_bytes = _socket.inet_aton(gnb_gtp_ip)

        # Target-to-Source Transparent Container
        with _lock:
            container = NGAP.NGAP_IEs.TargetNGRANNode_ToSourceNGRANNode_TransparentContainer
            container.set_val({
                # Valid minimal UPER-encoded RRC HandoverCommand (TS 38.331)
                # criticalExtensions=c1, c1=handoverCommand, all optional absent
                'rRCContainer': b'\x00\x00',
            })
            container_bytes = container.to_aper()

        # PDU Session Resource Admitted List
        admitted_list = []
        for sess in pdu_sessions:
            psi = sess.get('psi', 1)
            teid = gnb_gtp_teids.get(psi, 0)
            # HandoverRequestAcknowledgeTransfer
            with _lock:
                xfer = NGAP.NGAP_IEs.HandoverRequestAcknowledgeTransfer
                xfer.set_val({
                    'dL-NGU-UP-TNLInformation': ('gTPTunnel', {
                        'transportLayerAddress': (int.from_bytes(ip_bytes, 'big'), 32),
                        'gTP-TEID': struct.pack('>I', teid),
                    }),
                    'qosFlowSetupResponseList': [{'qosFlowIdentifier': 1}],
                })
                xfer_bytes = xfer.to_aper()
            admitted_list.append({
                'pDUSessionID': psi,
                'handoverRequestAcknowledgeTransfer': xfer_bytes,
            })

        return NgapCodec.encode(('successfulOutcome', {
            "procedureCode": 13, "criticality": "reject",
            "value": ("HandoverRequestAcknowledge", {"protocolIEs": [
                {"id": 10, "criticality": "ignore", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 85, "criticality": "ignore", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 53, "criticality": "ignore", "value": (
                    "PDUSessionResourceAdmittedList", admitted_list)},
                {"id": 106, "criticality": "reject", "value": (
                    "TargetToSource-TransparentContainer", container_bytes)},
            ]}),
        }))

    @staticmethod
    def build_handover_notify(amf_ue_ngap_id, ran_ue_ngap_id, mcc, mnc, tac, gnb_id):
        """HandoverNotify — target gNB → AMF (TS 38.413 §8.4.3.2).

        Tells AMF the UE has arrived at the target gNB.
        AMF will then update UPF (N4 path switch) and release source context.
        """
        plmn = encode_plmn_id(mcc, mnc)
        tac_bytes = bytes.fromhex(tac.zfill(6))
        nr_cell_id = (gnb_id << 4) | 0x0
        loc = ("userLocationInformationNR", {
            "nR-CGI": {"pLMNIdentity": plmn, "nRCellIdentity": (nr_cell_id, 36)},
            "tAI": {"pLMNIdentity": plmn, "tAC": tac_bytes},
        })
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 11, "criticality": "ignore",
            "value": ("HandoverNotify", {"protocolIEs": [
                {"id": 10, "criticality": "reject", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 85, "criticality": "reject", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 121, "criticality": "reject", "value": ("UserLocationInformation", loc)},
            ]}),
        }))

    @staticmethod
    def build_path_switch_request(amf_ue_ngap_id, ran_ue_ngap_id,
                                  mcc, mnc, tac, gnb_id,
                                  pdu_sessions, gnb_gtp_ip, gnb_gtp_teids):
        """PathSwitchRequest — target gNB → AMF (TS 38.413 §8.4.4.2).

        Alternative to HandoverNotify — used for Xn handover path switch.
        Includes new GTP-U tunnel info for each PDU session.
        """
        plmn = encode_plmn_id(mcc, mnc)
        tac_bytes = bytes.fromhex(tac.zfill(6))
        nr_cell_id = (gnb_id << 4) | 0x0
        ip_bytes = _socket.inet_aton(gnb_gtp_ip)

        pdu_list = []
        for sess in pdu_sessions:
            psi = sess.get('psi', 1)
            teid = gnb_gtp_teids.get(psi, 0)
            with _lock:
                xfer = NGAP.NGAP_IEs.PathSwitchRequestTransfer
                xfer.set_val({
                    'dL-NGU-UP-TNLInformation': ('gTPTunnel', {
                        'transportLayerAddress': (int.from_bytes(ip_bytes, 'big'), 32),
                        'gTP-TEID': struct.pack('>I', teid),
                    }),
                })
                xfer_bytes = xfer.to_aper()
            pdu_list.append({
                'pDUSessionID': psi,
                'pathSwitchRequestTransfer': xfer_bytes,
            })

        loc = ("userLocationInformationNR", {
            "nR-CGI": {"pLMNIdentity": plmn, "nRCellIdentity": (nr_cell_id, 36)},
            "tAI": {"pLMNIdentity": plmn, "tAC": tac_bytes},
        })

        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 25, "criticality": "reject",
            "value": ("PathSwitchRequest", {"protocolIEs": [
                {"id": 85, "criticality": "reject", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 100, "criticality": "ignore", "value": ("SourceAMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 121, "criticality": "ignore", "value": ("UserLocationInformation", loc)},
                {"id": 76, "criticality": "reject", "value": (
                    "PDUSessionResourceToBeSwitchedDLList", pdu_list)},
            ]}),
        }))

    @staticmethod
    def _ran_status_container(drb_id, ul_sn, ul_hfn, dl_sn, dl_hfn):
        return {
            'dRBsSubjectToStatusTransferList': [{
                'dRB-ID': drb_id,
                'dRBStatusUL': ('dRBStatusUL12', {
                    'uL-COUNTValue': {
                        'pDCP-SN12': ul_sn & 0xFFF,
                        'hFN-PDCP-SN12': ul_hfn & 0xFFFFF,
                    },
                }),
                'dRBStatusDL': ('dRBStatusDL12', {
                    'dL-COUNTValue': {
                        'pDCP-SN12': dl_sn & 0xFFF,
                        'hFN-PDCP-SN12': dl_hfn & 0xFFFFF,
                    },
                }),
            }],
        }

    @staticmethod
    def build_uplink_ran_status_transfer(amf_ue_ngap_id, ran_ue_ngap_id,
                                          drb_id=1, ul_pdcp_sn=0, dl_pdcp_sn=0):
        """UplinkRANStatusTransfer — source gNB → AMF (TS 38.413 §8.4.6.2 / §9.2.3.13).

        Carries PDCP COUNT values per DRB so the target can resume in-sequence.
        Sent by source gNB after HandoverCommand and before HandoverNotify.
        """
        container = NgapCodec._ran_status_container(
            drb_id, ul_pdcp_sn & 0xFFF, (ul_pdcp_sn >> 12) & 0xFFFFF,
            dl_pdcp_sn & 0xFFF, (dl_pdcp_sn >> 12) & 0xFFFFF)
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 49, "criticality": "ignore",
            "value": ("UplinkRANStatusTransfer", {"protocolIEs": [
                {"id": 10, "criticality": "reject", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 85, "criticality": "reject", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 84, "criticality": "reject", "value": (
                    "RANStatusTransfer-TransparentContainer", container)},
            ]}),
        }))

    @staticmethod
    def build_handover_failure(amf_ue_ngap_id, cause_group="radioNetwork",
                                cause_value="ho-failure-in-target-5GC-ngran-node-or-target-system"):
        """HandoverFailure — target gNB → AMF (TS 38.413 §8.4.2.3 / §9.2.3.5).

        Target gNB rejects HandoverRequest (resource alloc failed, security mismatch, ...).
        Note: HandoverResourceAllocation procedure (proc 13), unsuccessful outcome.
        """
        return NgapCodec.encode(('unsuccessfulOutcome', {
            "procedureCode": 13, "criticality": "reject",
            "value": ("HandoverFailure", {"protocolIEs": [
                {"id": 10, "criticality": "ignore", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 15, "criticality": "ignore", "value": ("Cause",
                    (cause_group, cause_value))},
            ]}),
        }))

    @staticmethod
    def build_handover_cancel(amf_ue_ngap_id, ran_ue_ngap_id,
                               cause_group="radioNetwork",
                               cause_value="handover-cancelled"):
        """HandoverCancel — source gNB → AMF (TS 38.413 §8.4.5.2 / §9.2.3.6)."""
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 10, "criticality": "reject",
            "value": ("HandoverCancel", {"protocolIEs": [
                {"id": 10, "criticality": "reject", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 85, "criticality": "reject", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
                {"id": 15, "criticality": "ignore", "value": ("Cause",
                    (cause_group, cause_value))},
            ]}),
        }))

    @staticmethod
    def build_pdu_session_resource_modify_response(amf_ue_ngap_id, ran_ue_ngap_id,
                                                     pdu_session_id):
        """PDUSessionResourceModifyResponse — TS 38.413 §8.2.3.2."""
        # Build transfer IE (PDUSessionResourceModifyResponseTransfer)
        with _lock:
            xfer = NGAP.NGAP_IEs.PDUSessionResourceModifyResponseTransfer
            xfer.set_val({})  # Empty transfer = success acknowledgement
            transfer_bytes = xfer.to_aper()
        return NgapCodec.encode(('successfulOutcome', {
            "procedureCode": 26, "criticality": "reject",
            "value": ("PDUSessionResourceModifyResponse", {"protocolIEs": [
                {"id": 10, "criticality": "ignore", "value": ("AMF-UE-NGAP-ID", amf_ue_ngap_id)},
                {"id": 65, "criticality": "ignore", "value": ("PDUSessionResourceModifyListModRes", [{
                    'pDUSessionID': pdu_session_id,
                    'pDUSessionResourceModifyResponseTransfer': transfer_bytes,
                }])},
                {"id": 85, "criticality": "ignore", "value": ("RAN-UE-NGAP-ID", ran_ue_ngap_id)},
            ]}),
        }))

    # ─── PWS — Public Warning System (TS 38.413 §8.9) ───
    #
    # All four PWS procedures use non-UE-associated signalling
    # (§8.9.1.1, §8.9.2.1, §8.9.3.1, §8.9.4.1) — no AMF-UE-NGAP-ID /
    # RAN-UE-NGAP-ID IEs anywhere. Procedure codes per §9.4:
    #   32 = PWSCancel        (initiating: AMF→gNB, response: gNB→AMF)
    #   33 = PWSFailureInd    (initiating: gNB→AMF, no response)
    #   34 = PWSRestartInd    (initiating: gNB→AMF, no response)
    #   51 = WriteReplaceWarn (initiating: AMF→gNB, response: gNB→AMF)
    # IE IDs per TS 38.413 §9.3.1 (verified against generated headers):
    #   12 BroadcastCancelledAreaList  13 BroadcastCompletedAreaList
    #   14 CancelAllWarningMessages    16 CellIDListForRestart
    #   17 ConcurrentWarningMessageInd 20 DataCodingScheme
    #   23 EmergencyAreaIDListForRestart  27 GlobalRANNodeID
    #   35 MessageIdentifier           47 NumberOfBroadcastsRequested
    #   81 PWSFailedCellIDList         87 RepetitionPeriod
    #   95 SerialNumber                104 TAIListForRestart
    #   122 WarningAreaList            123 WarningMessageContents
    #   125 WarningType

    @staticmethod
    def build_write_replace_warning_request(message_identifier, serial_number,
                                             repetition_period=5,
                                             number_of_broadcasts=1,
                                             warning_type=None,
                                             data_coding_scheme=None,
                                             warning_message_contents=None,
                                             concurrent=False):
        """WriteReplaceWarningRequest — AMF → gNB (TS 38.413 §9.2.8.1).

        message_identifier / serial_number are 16-bit values; pycrate
        BIT STRING(16) encodes as (int_value, 16). Mandatory IEs per
        §9.2.8.1: MessageIdentifier (35), SerialNumber (95),
        RepetitionPeriod (87), NumberOfBroadcastsRequested (47).
        warning_type: bytes(2) | None — §9.3.1.39 OCTET STRING(2).
        data_coding_scheme: int 0..255 | None — §9.3.1.41 BIT STRING(8).
        warning_message_contents: bytes 1..9600 | None — §9.3.1.42.
        concurrent=True ⇒ ConcurrentWarningMessageInd present (only
        defined value is 'true' per §9.3.1.46).
        """
        ies = [
            {"id": 35, "criticality": "reject",
             "value": ("MessageIdentifier", (message_identifier & 0xFFFF, 16))},
            {"id": 95, "criticality": "reject",
             "value": ("SerialNumber", (serial_number & 0xFFFF, 16))},
            {"id": 87, "criticality": "reject",
             "value": ("RepetitionPeriod", repetition_period)},
            {"id": 47, "criticality": "reject",
             "value": ("NumberOfBroadcastsRequested", number_of_broadcasts)},
        ]
        if warning_type is not None:
            ies.append({"id": 125, "criticality": "ignore",
                        "value": ("WarningType", bytes(warning_type))})
        if data_coding_scheme is not None:
            ies.append({"id": 20, "criticality": "ignore",
                        "value": ("DataCodingScheme",
                                  (data_coding_scheme & 0xFF, 8))})
        if warning_message_contents is not None:
            ies.append({"id": 123, "criticality": "ignore",
                        "value": ("WarningMessageContents",
                                  bytes(warning_message_contents))})
        if concurrent:
            ies.append({"id": 17, "criticality": "reject",
                        "value": ("ConcurrentWarningMessageInd", "true")})
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 51, "criticality": "reject",
            "value": ("WriteReplaceWarningRequest", {"protocolIEs": ies}),
        }))

    @staticmethod
    def build_write_replace_warning_response(message_identifier, serial_number,
                                              completed_nr_cells=None):
        """WriteReplaceWarningResponse — gNB → AMF (TS 38.413 §9.2.8.2).

        Per §8.9.1.2 line 7681-7682: omit BroadcastCompletedAreaList ⇒
        AMF treats broadcast as unsuccessful in all cells of the gNB.
        completed_nr_cells: list[(plmn_bytes, nr_cell_id_int)] | None —
        if provided, includes a CellIDBroadcastListNR with one entry per
        item.
        """
        ies = [
            {"id": 35, "criticality": "reject",
             "value": ("MessageIdentifier", (message_identifier & 0xFFFF, 16))},
            {"id": 95, "criticality": "reject",
             "value": ("SerialNumber", (serial_number & 0xFFFF, 16))},
        ]
        if completed_nr_cells:
            cell_list = [{
                'nR-CGI': {'pLMNIdentity': plmn,
                           'nRCellIdentity': (cid & ((1 << 36) - 1), 36)},
            } for plmn, cid in completed_nr_cells]
            ies.append({"id": 13, "criticality": "ignore",
                        "value": ("BroadcastCompletedAreaList",
                                  ("cellIDBroadcastNR", cell_list))})
        return NgapCodec.encode(('successfulOutcome', {
            "procedureCode": 51, "criticality": "reject",
            "value": ("WriteReplaceWarningResponse", {"protocolIEs": ies}),
        }))

    @staticmethod
    def build_pws_cancel_request(message_identifier, serial_number,
                                  cancel_all=False):
        """PWSCancelRequest — AMF → gNB (TS 38.413 §9.2.8.3).

        Mandatory: MessageIdentifier (35), SerialNumber (95). Optional
        CancelAllWarningMessages (14) — only encoded value is 'true'
        per §9.3.1.47; presence ⇒ "stop and discard all warning
        messages for the area" (§8.9.2.2 line 7742-7748).
        """
        ies = [
            {"id": 35, "criticality": "reject",
             "value": ("MessageIdentifier", (message_identifier & 0xFFFF, 16))},
            {"id": 95, "criticality": "reject",
             "value": ("SerialNumber", (serial_number & 0xFFFF, 16))},
        ]
        if cancel_all:
            ies.append({"id": 14, "criticality": "reject",
                        "value": ("CancelAllWarningMessages", "true")})
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 32, "criticality": "reject",
            "value": ("PWSCancelRequest", {"protocolIEs": ies}),
        }))

    @staticmethod
    def build_pws_cancel_response(message_identifier, serial_number,
                                    cancelled_nr_cells=None):
        """PWSCancelResponse — gNB → AMF (TS 38.413 §9.2.8.4).

        Per §8.9.2.2 line 7739-7740: omit BroadcastCancelledAreaList ⇒
        AMF treats "no ongoing broadcast for this msgID/serial".
        """
        ies = [
            {"id": 35, "criticality": "reject",
             "value": ("MessageIdentifier", (message_identifier & 0xFFFF, 16))},
            {"id": 95, "criticality": "reject",
             "value": ("SerialNumber", (serial_number & 0xFFFF, 16))},
        ]
        if cancelled_nr_cells:
            cell_list = [{
                'nR-CGI': {'pLMNIdentity': plmn,
                           'nRCellIdentity': (cid & ((1 << 36) - 1), 36)},
                'numberOfBroadcasts': nbroadcasts,
            } for plmn, cid, nbroadcasts in cancelled_nr_cells]
            ies.append({"id": 12, "criticality": "ignore",
                        "value": ("BroadcastCancelledAreaList",
                                  ("cellIDCancelledNR", cell_list))})
        return NgapCodec.encode(('successfulOutcome', {
            "procedureCode": 32, "criticality": "reject",
            "value": ("PWSCancelResponse", {"protocolIEs": ies}),
        }))

    @staticmethod
    def build_pws_failure_indication(failed_nr_cells, mcc, mnc, gnb_id):
        """PWSFailureIndication — gNB → AMF (TS 38.413 §9.2.8.6).

        Per §8.9.4.2 line 7799-7800: gNB indicates which cells failed
        the ongoing PWS broadcast.
        failed_nr_cells: list[int] of NR cell identities (36-bit).
        mcc/mnc/gnb_id used to build mandatory GlobalRANNodeID.
        """
        plmn = encode_plmn_id(mcc, mnc)
        # nR-CGI-PWSFailedList is SEQUENCE OF NR-CGI per §9.3.1.45 — items
        # are bare NR-CGI structs, not wrapped with numberOfBroadcasts.
        failed_list = [{
            'pLMNIdentity': plmn,
            'nRCellIdentity': (cid & ((1 << 36) - 1), 36),
        } for cid in failed_nr_cells]
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 33, "criticality": "ignore",
            "value": ("PWSFailureIndication", {"protocolIEs": [
                {"id": 81, "criticality": "reject",
                 "value": ("PWSFailedCellIDList",
                           ("nR-CGI-PWSFailedList", failed_list))},
                {"id": 27, "criticality": "reject",
                 "value": ("GlobalRANNodeID",
                           ("globalGNB-ID", {
                               "pLMNIdentity": plmn,
                               "gNB-ID": ("gNB-ID", (gnb_id, 32)),
                           }))},
            ]}),
        }))

    @staticmethod
    def build_pws_restart_indication(restart_nr_cells, mcc, mnc, gnb_id, tac):
        """PWSRestartIndication — gNB → AMF (TS 38.413 §9.2.8.5).

        Per §8.9.3.2: gNB tells AMF to re-broadcast warnings on the
        listed cells/TAIs after a recovery event.
        """
        plmn = encode_plmn_id(mcc, mnc)
        tac_bytes = bytes.fromhex(tac.zfill(6))
        cell_list = [{
            'pLMNIdentity': plmn,
            'nRCellIdentity': (cid & ((1 << 36) - 1), 36),
        } for cid in restart_nr_cells]
        return NgapCodec.encode(('initiatingMessage', {
            "procedureCode": 34, "criticality": "ignore",
            "value": ("PWSRestartIndication", {"protocolIEs": [
                {"id": 16, "criticality": "reject",
                 "value": ("CellIDListForRestart",
                           ("nR-CGIListforRestart", cell_list))},
                {"id": 27, "criticality": "reject",
                 "value": ("GlobalRANNodeID",
                           ("globalGNB-ID", {
                               "pLMNIdentity": plmn,
                               "gNB-ID": ("gNB-ID", (gnb_id, 32)),
                           }))},
                {"id": 104, "criticality": "reject",
                 "value": ("TAIListForRestart", [{
                     'pLMNIdentity': plmn, 'tAC': tac_bytes,
                 }])},
            ]}),
        }))
