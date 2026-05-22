# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""UE NAS state machine — uses protocol modules for all message handling.

States: DEREGISTERED → REG_INITIATED → AUTH_PENDING → SEC_MODE_PENDING
        → REGISTERED → CONNECTED → DEREGISTERING
"""

import logging
import time
import threading
from binascii import hexlify

from src.protocol.nas import NasBuilder, NasParser
from src.protocol.nas_security import wrap_nas_security, unwrap_nas_security
from src.protocol.crypto import (
    ue_authenticate, get_snn, derive_kamf, derive_nas_keys, derive_kgnb,
)
from src.config import UE_DEFAULTS

log = logging.getLogger("tester.ue")

# NAS states
DEREGISTERED = "DEREGISTERED"
REG_INITIATED = "REG_INITIATED"
AUTH_PENDING = "AUTH_PENDING"
SEC_MODE_PENDING = "SEC_MODE_PENDING"
REGISTERED = "REGISTERED"
CONNECTED = "CONNECTED"
DEREGISTERING = "DEREGISTERING"


class UeStateMachine:
    """Emulated 5G UE — NAS state machine with SIM credentials."""

    def __init__(self, sim, gnb=None):
        self.sim = sim
        self.imsi = sim.imsi
        self.mcc = sim.imsi[:3]
        self.mnc = sim.imsi[3:5]
        self.gnb = gnb

        self.state = DEREGISTERED
        self.ran_ue_ngap_id = None
        self.amf_ue_ngap_id = None

        self.security_ctx = {
            'KAMF': None, 'KSEAF': None,
            'knasenc': None, 'knasint': None, 'kgnb': None,
            'eea': 0, 'eia': 0,
            'ul_nas_count': 0, 'dl_nas_count': 0,
            'ABBA': b'\x00\x00',
        }

        self.pdu_sessions = {}
        self._pti_counter = 1
        self._reg_request_bytes = None
        self.guti = None

        # Identity context (TS 23.003 §2.2, TS 33.501 §6.12)
        # Read from UE Config (sim_db.json)
        self.supi_type = getattr(sim, 'supi_type', 'supi')
        self.supi = f"{self.supi_type}-{self.imsi}"
        self.routing_indicator = getattr(sim, 'routing_indicator', '0000')
        self.protection_scheme = getattr(sim, 'protection_scheme', 0)
        self.home_nw_pub_key_id = getattr(sim, 'home_nw_pub_key_id', 0)
        self.home_nw_pub_key = getattr(sim, 'home_nw_pub_key', '')  # hex string
        self.suci = None      # SUCI built during registration
        self.suci_type = None  # set during registration
        self._5g_guti = None  # 5G-GUTI from Registration Accept

        # Last reject details (TS 24.501 §9.11.3.2 — 5GMMCause). Tests
        # that drive negative paths read these after a register() to
        # assert the AMF returned the expected cause without having to
        # scrape logs.
        self.last_reject_cause = None
        self.last_reject_message = None     # "registration" | "authentication"

        self._state_event = threading.Event()
        self._state_lock = threading.Lock()
        self.log_entries = []

    # ─── State management ───

    def _log(self, msg, *args):
        formatted = msg % args if args else msg
        self.log_entries.append({"time": time.time(), "msg": formatted})
        if len(self.log_entries) > 500:
            self.log_entries = self.log_entries[-300:]
        log.info("[%s] %s", self.imsi, formatted)

    def _set_state(self, new_state):
        with self._state_lock:
            old = self.state
            self.state = new_state
            self._state_event.set()
            self._state_event = threading.Event()
        self._log("State: %s -> %s", old, new_state)

    def wait_for_state(self, target, timeout=30):
        deadline = time.time() + timeout
        while time.time() < deadline:
            with self._state_lock:
                if self.state == target:
                    return True
                # Bail out if the gNB link has died — no point sleeping the
                # full `timeout` for a REGISTERED we'll never reach once the
                # SCTP association is gone.
                if self.gnb is not None and getattr(self.gnb, "state", None) == "ERROR":
                    return False
                evt = self._state_event
            evt.wait(timeout=min(deadline - time.time(), 1.0))
        return self.state == target

    # ─── Internal: send with security ───

    def _send_secured(self, plain_nas, sec_hdr=2):
        """Wrap in NAS security and send via gNB."""
        ctx = self.security_ctx
        secured, new_count = wrap_nas_security(
            plain_nas, ctx['knasenc'], ctx['knasint'],
            ctx['eea'], ctx['eia'], ctx['ul_nas_count'], sec_hdr=sec_hdr)
        ctx['ul_nas_count'] = new_count
        self.gnb.send_uplink_nas_transport(self, secured)

    # ─── Public actions (called by test cases) ───

    def register(self, reg_type=1, ksi_value=7, requested_nssai=None,
                 sec_caps=None, msin_override=None, prot_scheme_id=None,
                 routing_indicator=None):
        """Initiate 5G registration — TS 24.501 §5.5.1.2.

        Builds SUCI (Subscription Concealed Identifier) from IMSI.
        SUCI = SUPI type + MCC/MNC + Routing Indicator + Protection Scheme + MSIN
        With null scheme (ProtSchemeID=0), MSIN is sent in cleartext.

        Optional knobs (all default to standard initial-registration):
          reg_type           — 1=initial (default), 2=mobility, 3=periodic,
                               4=emergency. Drives TC-REG-015/016/018.
          ksi_value          — ngKSI: 7 = no key, forces fresh auth
                               (TC-REG-021). 0..6 = reuse existing context.
          requested_nssai    — override the baseline-bucket-derived NSSAI
                               (TC-REG-011 slice rejection).
          sec_caps           — override UE_DEFAULTS['ue_sec_cap']
                               (TC-REG-013 algorithm-mismatch reject).
          msin_override      — substitute MSIN in SUCI for unknown-subscriber
                               or malformed-SUCI paths (TC-REG-007/020).
          prot_scheme_id     — SUCI protection scheme (TC-REG-020).
          routing_indicator  — SUCI routing indicator override.

        Resets `last_reject_cause` so the test sees only this register's
        outcome.
        """
        if not self.gnb:
            self._log("ERROR: No gNB attached")
            return False
        # Clear stale reject so callers can distinguish "no reject yet"
        # from "rejected with cause N on the prior attempt".
        self.last_reject_cause = None
        self.last_reject_message = None
        self._set_state(REG_INITIATED)

        # Build SUCI from config (TS 23.003 §2.2B, TS 33.501 §6.12)
        msin = self.imsi[len(self.mcc) + len(self.mnc):]
        scheme_names = {0: "null-scheme", 1: "ecies-profile-a", 2: "ecies-profile-b"}
        self.suci = {
            "supi_type": self.supi_type,
            "plmn": f"{self.mcc}{self.mnc}",
            "routing_indicator": self.routing_indicator,
            "protection_scheme": self.protection_scheme,
            "home_network_public_key_id": self.home_nw_pub_key_id,
            "msin": msin,
            "scheme_output": msin if self.protection_scheme == 0 else "concealed",
        }
        self.suci_type = scheme_names.get(self.protection_scheme, f"scheme-{self.protection_scheme}")
        self._log("SUCI: supi=%s type=%s scheme=%s routing=%s MSIN=%s",
                  self.supi, self.supi_type, self.suci_type,
                  self.routing_indicator, msin)

        # Requested-NSSAI: explicit override (negative-path tests) wins;
        # otherwise derive from this UE's baseline bucket so miot-pool /
        # urllc-pool UEs don't accidentally send eMBB's S-NSSAI and get
        # rejected with cause=62 (TS 24.501 §9.11.3.2).
        if requested_nssai is not None:
            requested = requested_nssai
        else:
            try:
                from src import baseline as _bl
                bucket = _bl.bucket_of(self.imsi)
                requested = [
                    {"sst": sst,
                     "sd": int(_bl.slice_by_sst(sst).sd, 16) if _bl.slice_by_sst(sst).sd else None}
                    for sst in bucket.slices
                ]
            except Exception:
                requested = UE_DEFAULTS.get("requested_nssai")

        nas = NasBuilder.registration_request(
            self.imsi, self.mcc, self.mnc,
            sec_caps if sec_caps is not None else UE_DEFAULTS["ue_sec_cap"],
            requested,
            reg_type=reg_type,
            ksi_value=ksi_value,
            routing_indicator=routing_indicator if routing_indicator is not None
                              else self.routing_indicator,
            msin_override=msin_override,
            prot_scheme_id=prot_scheme_id if prot_scheme_id is not None
                              else self.protection_scheme,
        )
        self._reg_request_bytes = nas
        self.gnb.send_initial_ue_message(self, nas)
        return True

    def establish_pdu_session(self, dnn="internet", sst=1, sd=None, pdu_session_id=1):
        """Establish PDU session — TS 24.501 §8.3.1."""
        if self.state not in (REGISTERED, CONNECTED):
            return False
        pti = self._pti_counter
        self._pti_counter = (self._pti_counter % 254) + 1
        plain = NasBuilder.ul_nas_transport_pdu_session(pdu_session_id, pti, dnn, sst, sd)
        self._send_secured(plain)
        self._log("PDU Session Request: PSI=%d DNN=%s", pdu_session_id, dnn)
        return True

    def deregister(self, switch_off=True, re_registration_required=False,
                   access_type=1):
        """UE-initiated deregistration — TS 24.501 §5.5.2.2.

        switch_off:
          True  (default) — power-off / USIM-removal flavour (§5.5.2.2.1);
                            AMF MUST NOT send a DEREGISTRATION ACCEPT.
          False           — "normal de-registration"; AMF SHALL reply with
                            DEREGISTRATION ACCEPT (§5.5.2.2.2).
        re_registration_required: when set, indicates the UE expects the
                                  network to make it re-register (rare on
                                  the UE side; usually a network choice).
        access_type: 1=3GPP (default), 2=non-3GPP, 3=both. Per §5.5.2.1
                     this MUST match the access the UE is registered on.
        """
        if self.state == DEREGISTERED:
            return False
        self._set_state(DEREGISTERING)
        plain = NasBuilder.deregistration_request(
            switch_off=switch_off,
            re_registration_required=re_registration_required,
            access_type=access_type,
        )
        if self.security_ctx.get('knasint'):
            self._send_secured(plain)
        else:
            self.gnb.send_initial_ue_message(self, plain)
        return True

    # ─── NAS PDU entry point (called by gNB FSM) ───

    def on_nas_pdu(self, nas_bytes):
        """Single entry point for all DL NAS PDUs from gNB."""
        msg, err = NasParser.parse(nas_bytes)
        if msg is None:
            self._log("NAS parse error: %s", err)
            return

        # Unwrap security
        if NasParser.is_secured(msg):
            ctx = self.security_ctx
            msg, new_count, mac_ok = unwrap_nas_security(
                msg, ctx['knasenc'], ctx['knasint'],
                ctx['eea'], ctx['eia'], ctx['dl_nas_count'])
            ctx['dl_nas_count'] = new_count
            if not mac_ok:
                self._log("DL NAS MAC FAILED")

        msg_type = NasParser.get_message_type(msg)
        if msg_type is None:
            self._log("Cannot extract NAS message type")
            return

        self._log("RX NAS type=%d (0x%02X)", msg_type, msg_type)

        # Dispatch
        handlers = {
            86: self._handle_auth_request,
            93: self._handle_security_mode_command,
            66: self._handle_registration_accept,
            68: self._handle_registration_reject,
            70: self._handle_deregistration_accept,
            88: self._handle_auth_reject,
            84: self._handle_config_update,
            104: self._handle_dl_nas_transport,
        }
        handler = handlers.get(msg_type)
        if handler:
            handler(msg)
        else:
            self._log("Unhandled NAS type=%d", msg_type)

    # ─── NAS Handlers ───

    def _handle_auth_request(self, msg):
        """Authentication Request — compute RES* + respond, or resync via AUTS.

        TS 33.501 §6.1.3 — 5G-AKA procedure.
        TS 33.501 §6.1.3.3 — Synchronization failure or MAC failure
            (parent clause; §6.1.3.3.1 covers synch-failure handling
            in the USIM, §6.1.3.3.2 covers home-network recovery).
        """
        self._set_state(AUTH_PENDING)
        rand = bytes(msg['RAND']['V'])
        autn = bytes(msg['AUTN']['AUTN'])

        # ngKSI + ABBA observability (TS 24.501 §5.4.1.3.2): the AMF MUST
        # include a key-set identifier ngKSI and the ABBA parameter in
        # AUTHENTICATION REQUEST. Stash both so TCs can assert them.
        # Post-decode pycrate exposes NAS_KSI as {'NAS_KSI': {'TSC', 'Value'}}.
        try:
            ksi_d = msg['NAS_KSI'].get_val_d() or {}
            inner = ksi_d.get('NAS_KSI', ksi_d)
            if isinstance(inner, dict) and 'Value' in inner:
                self.security_ctx['ngksi'] = int(inner['Value']) & 0x07
                self.security_ctx['tsc'] = int(inner.get('TSC', 0)) & 0x01
        except Exception:
            pass
        try:
            abba = bytes(msg['ABBA']['V'].get_val())
            if abba:
                self.security_ctx['ABBA'] = abba
        except Exception:
            pass

        self._log("Auth Request: RAND=%s...", hexlify(rand).decode()[:16])

        result = ue_authenticate(self.sim, rand, autn, get_snn(self.mcc, self.mnc))
        if result is None:
            self._log("ERROR: MAC verification failed (unrecoverable)")
            nas = NasBuilder.authentication_failure(cause=20)  # MAC failure
            self.gnb.send_uplink_nas_transport(self, nas)
            self._log("TX Auth Failure (MAC failure)")
            return

        if result.get('sync_failure'):
            self._log("SQN out of sync — sending Auth Failure with AUTS for resync")
            nas = NasBuilder.authentication_failure(cause=21, auts=result['AUTS'])
            self.gnb.send_uplink_nas_transport(self, nas)
            self._log("TX Auth Failure (synch failure, AUTS=%s)", hexlify(result['AUTS']).decode())
            return

        self.security_ctx['KSEAF'] = result['KSEAF']
        self.security_ctx['KAMF'] = derive_kamf(result['KSEAF'], self.imsi)

        nas = NasBuilder.authentication_response(result['RESstar'])
        self._log("TX Auth Response")
        self.gnb.send_uplink_nas_transport(self, nas)

    def _handle_security_mode_command(self, msg):
        """Security Mode Command — derive keys + respond secured."""
        self._set_state(SEC_MODE_PENDING)
        algo_bytes = bytes(msg['NASSecAlgo']['NASSecAlgo'])
        ciph_algo = (algo_bytes[0] >> 4) & 0x0F
        integ_algo = algo_bytes[0] & 0x0F
        self._log("SMC: EEA%d / EIA%d", ciph_algo, integ_algo)

        ctx = self.security_ctx
        ctx['eea'] = ciph_algo
        ctx['eia'] = integ_algo
        knasenc, knasint = derive_nas_keys(ctx['KAMF'], ciph_algo, integ_algo)
        ctx['knasenc'] = knasenc
        ctx['knasint'] = knasint
        ctx['ul_nas_count'] = 0
        ctx['dl_nas_count'] = 0

        plain = NasBuilder.security_mode_complete(self._reg_request_bytes)
        self._send_secured(plain, sec_hdr=4)

        smc_count = (ctx['ul_nas_count'] - 1) & 0xFFFFFFFF
        ctx['kgnb'] = derive_kgnb(ctx['KAMF'], smc_count)
        self._log("TX Security Mode Complete")

    def _handle_registration_accept(self, msg):
        """Registration Accept — send Complete + transition REGISTERED."""
        self._log("Registration Accept received")
        # Parse the 5G-GUTI assigned by the AMF (TS 24.501 §8.2.7.2 +
        # §9.11.3.4 figure 9.11.3.4.1 "5G-GUTI" type-of-identity=010).
        # pycrate post-decode exposes the IE as msg['GUTI']['5GSID']
        # with Type / PLMN / AMFRegionID / AMFSetID / AMFPtr / 5GTMSI
        # sub-fields (not 'V'); type==2 → 5G-GUTI.
        try:
            if not msg['GUTI'].get_trans():
                guti = msg['GUTI']['5GSID']
                if int(guti['Type'].get_val()) == 2:
                    self.guti = True
                    self._5g_guti = {
                        "type": "5G-GUTI",
                        "plmn": bytes(guti['PLMN'].get_val()).hex(),
                        "amf_region_id": int(guti['AMFRegionID'].get_val()),
                        "amf_set_id": int(guti['AMFSetID'].get_val()),
                        "amf_pointer": int(guti['AMFPtr'].get_val()),
                        "tmsi_5g": f"0x{int(guti['5GTMSI'].get_val()):08X}",
                    }
                    self._log("5G-GUTI assigned: TMSI=%s set=%d ptr=%d region=0x%02x",
                              self._5g_guti['tmsi_5g'],
                              self._5g_guti['amf_set_id'],
                              self._5g_guti['amf_pointer'],
                              self._5g_guti['amf_region_id'])
        except Exception as e:
            self._log("5G-GUTI parse error: %s", e)

        plain = NasBuilder.registration_complete()
        self._send_secured(plain)
        self._log("TX Registration Complete")
        self._set_state(REGISTERED)

    def _handle_registration_reject(self, msg):
        try:
            cause = msg['5GMMCause']['5GMMCause'].get_val()
            self.last_reject_cause = int(cause)
            self.last_reject_message = "registration"
            self._log("Registration REJECTED cause=%s", cause)
        except Exception:
            self.last_reject_cause = -1
            self.last_reject_message = "registration"
            self._log("Registration REJECTED (cause unparseable)")
        self._set_state(DEREGISTERED)

    def _handle_auth_reject(self, msg):
        # Authentication Reject (TS 24.501 §8.2.5) carries no cause IE;
        # surface a sentinel so callers can still see "auth-reject path
        # taken" without having to scrape state transitions.
        self.last_reject_cause = 3  # ill UE / authentication failure (informational)
        self.last_reject_message = "authentication"
        self._log("Authentication REJECTED")
        self._set_state(DEREGISTERED)

    def _handle_deregistration_accept(self, msg):
        self._log("Deregistration Accept")
        self._set_state(DEREGISTERED)

    def _handle_config_update(self, msg):
        self._log("Config Update (ignored)")

    def _handle_dl_nas_transport(self, msg):
        """DL NAS Transport — contains GSM PDU (PDU session accept/reject).

        pycrate may return the PayloadContainer as raw bytes or as a
        pre-decoded nested list structure depending on the version/platform.
        We handle both cases.
        """
        try:
            pc = msg['PayloadContainer']
            raw = None
            try:
                raw = pc['V'].get_val()
            except Exception:
                try:
                    raw = pc[1].get_val()
                except Exception:
                    raw = pc.get_val()

            if isinstance(raw, (bytes, bytearray)):
                # Raw bytes — parse as 5GSM
                inner, _ = NasParser.parse_inner(raw)
                if inner is None:
                    return
                try:
                    msg_type = inner["5GSMHeader"]["Type"].get_val()
                    psi = inner["5GSMHeader"]["PDUSessID"].get_val()
                except Exception:
                    return
                self._process_gsm(msg_type, psi, inner)

            elif isinstance(raw, list) and raw:
                # pycrate pre-decoded: [[EPD, PSI, PTI, MsgType], ...IEs...]
                hdr = raw[0] if isinstance(raw[0], list) else raw
                if not isinstance(hdr, list) or len(hdr) < 4:
                    self._log("DL NAS Transport: unexpected format")
                    return
                psi = hdr[1]
                msg_type = hdr[3]
                # Extract IP and PCO from IEs
                ip = "unknown"
                pco = {}
                for ie in raw[1:]:
                    if isinstance(ie, list) and len(ie) >= 3:
                        # PDUAddress IE tag=0x29 (41)
                        if ie[0] == 41 or ie[0] == 0x29:
                            addr_data = ie[2] if len(ie) > 2 else ie[1]
                            if isinstance(addr_data, list) and len(addr_data) >= 4:
                                ip_part = addr_data[3] if len(addr_data) > 3 else addr_data[-1]
                                if isinstance(ip_part, bytes) and len(ip_part) >= 4:
                                    ip = f"{ip_part[0]}.{ip_part[1]}.{ip_part[2]}.{ip_part[3]}"
                    # ExtProtConfig IE tag=0x7B (123)
                    if isinstance(ie, list) and len(ie) >= 2:
                        if ie[0] in (0x7B, 123):
                            pco = self._parse_pco_list(ie)
                self._process_gsm_decoded(msg_type, psi, ip, pco, raw)
            else:
                self._log("DL NAS Transport: unexpected payload type %s", type(raw).__name__)
        except Exception as e:
            self._log("DL NAS Transport parse error: %s", e)

    def _process_gsm(self, msg_type, psi, inner):
        """Process parsed 5GSM message (pycrate object)."""
        if msg_type in (0xC1, 0xC2):  # PDU Session Establishment Accept
            ip = "unknown"
            pco = {}
            try:
                addr = bytes(inner['PDUAddress']['V'])
                if len(addr) >= 5:
                    ip = f"{addr[1]}.{addr[2]}.{addr[3]}.{addr[4]}"
            except Exception:
                pass
            # Extract PCO (ExtendedProtocolConfigurationOptions)
            try:
                pco_val = inner['ExtProtConfig']['V'].get_val()
                if isinstance(pco_val, (bytes, bytearray)):
                    pco = self._parse_pco_bytes(pco_val)
                elif isinstance(pco_val, list):
                    pco = self._parse_pco_list([0x7B] + [pco_val])
            except Exception:
                pass
            # Parse Authorized QoS rules (includes default QFI=1 match-all rule)
            qos_info = self._parse_qos_rules(inner)
            session = {"state": "active", "ip": ip, "qos_flows": qos_info}
            if pco:
                session.update(pco)
            self.pdu_sessions[psi] = session
            self._log("PDU Session %d: IP=%s QoS=%d rules%s", psi, ip, len(qos_info),
                      f" P-CSCF={pco['pcscf']}" if pco.get('pcscf') else "")
            if self.gnb and hasattr(self.gnb, '_on_pdu_session_accepted'):
                self.gnb._on_pdu_session_accepted(self, psi, ip)
        elif msg_type in (0xCB, 203):  # PDU Session Modification Command
            qos_info = self._parse_qos_rules(inner)
            self._log("PDU Session %d: Modification Command (dedicated bearer) QoS=%s", psi, qos_info)
            if psi in self.pdu_sessions:
                existing = self.pdu_sessions[psi].get('qos_flows', [])
                self.pdu_sessions[psi]['qos_flows'] = existing + qos_info if qos_info else existing
            self._send_modification_complete(psi)
        elif msg_type == 0xC6:  # PDU Session Release Command
            self.pdu_sessions.pop(psi, None)
            self._log("PDU Session %d released", psi)

    def _process_gsm_decoded(self, msg_type, psi, ip="unknown", pco=None, raw=None):
        """Process pre-decoded 5GSM message (list format)."""
        if msg_type in (0xC1, 0xC2, 193, 194):  # PDU Session Establishment Accept
            # Parse Authorized QoS rules from establishment accept
            self._log("PDU Session Accept raw structure: %s", repr(raw)[:500])
            qos_info = self._parse_qos_rules_from_list(raw) if raw else []
            session = {"state": "active", "ip": ip, "qos_flows": qos_info}
            if pco:
                session.update(pco)
            self.pdu_sessions[psi] = session
            self._log("PDU Session %d: IP=%s QoS=%d rules%s", psi, ip, len(qos_info),
                      f" P-CSCF={pco['pcscf']}" if pco and pco.get('pcscf') else "")
            if self.gnb and hasattr(self.gnb, '_on_pdu_session_accepted'):
                self.gnb._on_pdu_session_accepted(self, psi, ip)
        elif msg_type in (0xCB, 203):  # PDU Session Modification Command
            qos_info = self._parse_qos_rules_from_list(raw)
            self._log("PDU Session %d: Modification Command — %d QoS rules (QFIs: %s)",
                      psi, len(qos_info), ', '.join(str(r['qfi']) for r in qos_info))
            if psi in self.pdu_sessions:
                existing = self.pdu_sessions[psi].get('qos_flows', [])
                # Merge new rules with existing (don't replace default)
                if qos_info:
                    self.pdu_sessions[psi]['qos_flows'] = existing + qos_info
                elif not existing:
                    self.pdu_sessions[psi]['qos_flows'] = []
            self._send_modification_complete(psi)
        elif msg_type in (0xC6, 198):  # PDU Session Release Command
            self.pdu_sessions.pop(psi, None)
            self._log("PDU Session %d released", psi)

    def _parse_qos_rules_from_list(self, raw):
        """Parse QoS rules from pycrate pre-decoded list format.

        pycrate decodes 5GSM Modification Command as:
        [header, [122, len, [rules...]], [121, len, [flow_desc...]]]

        IE 122 (0x7A) = QoS Rules
        IE 121 (0x79) = QoS Flow Descriptions

        Each rule: [rule_id, op_dqr_npf, ...filters..., precedence, [spare, spare, qfi]]
        Filter components decoded as nested lists:
          [48, 17] = protocol 17 (UDP)
          [65, [min, max]] = local port range
          [81, [min, max]] = remote port range
        """
        flows = []
        if not isinstance(raw, list) or len(raw) < 2:
            return flows

        for ie in raw[1:]:
            if not isinstance(ie, list) or len(ie) < 2:
                continue
            ie_tag = ie[0]
            # QoS Rules IE: tag 122 (0x7A) in Modification Command,
            # tag 9 in Establishment Accept (mandatory IE, different encoding)
            if ie_tag not in (9, 122, 0x7A):
                continue

            rules_list = ie[2] if len(ie) > 2 else ie[1]
            if not isinstance(rules_list, list):
                continue
            # rules_list may be a single rule or list of rules
            if rules_list and not isinstance(rules_list[0], list):
                rules_list = [rules_list]  # single rule

            for rule in rules_list:
                if not isinstance(rule, list) or len(rule) < 3:
                    continue
                flow = self._parse_decoded_qos_rule(rule)
                if flow:
                    flows.append(flow)

        return flows

    def _parse_decoded_qos_rule(self, rule):
        """Parse a single decoded QoS rule list.

        Format: [rule_id, op_dqr_npf, ...filter_data..., precedence, qfi_data]
        """
        try:
            rule_id = rule[0]
            # Find QFI — it's typically the last element, can be [0, 0, qfi] or just qfi
            qfi_data = rule[-1]
            if isinstance(qfi_data, list):
                qfi = qfi_data[-1] if qfi_data else 1
            else:
                qfi = int(qfi_data) & 0x3F

            # Find precedence — second to last if last is qfi_data
            precedence = 255
            if len(rule) >= 3:
                prec_candidate = rule[-2]
                if isinstance(prec_candidate, int):
                    precedence = prec_candidate

            # Parse packet filters — find the nested list with filter components
            filters = []
            for elem in rule:
                if isinstance(elem, list) and elem:
                    # Check if this is a list of packet filters [[pf1], [pf2], ...]
                    if isinstance(elem[0], list):
                        for pf_data in elem:
                            pf = self._parse_decoded_filter(pf_data)
                            if pf:
                                filters.append(pf)
                        break

            # Check DQR bit from op_dqr_npf
            op_dqr_npf = rule[1] if len(rule) > 1 else 0
            dqr = (op_dqr_npf >> 4) & 0x01 if isinstance(op_dqr_npf, int) else 0

            return {
                'rule_id': rule_id, 'qfi': qfi, 'dqr': dqr,
                'precedence': precedence, 'filters': filters,
            }
        except Exception as e:
            self._log("QoS rule parse error: %s (rule=%s)", e, repr(rule)[:100])
            return None

    def _parse_decoded_filter(self, pf_data):
        """Parse a decoded packet filter from list format.

        Filter: [pf_id, dir, ...components_or_len..., components_list]
        Components: [[48, 17], [65, [min, max]], [81, [min, max]]]
          48 = protocol, 65 = local port range, 81 = remote port range
        """
        content = {}
        # Find the components list — nested list of [type, value] pairs
        components = None
        for elem in pf_data:
            if isinstance(elem, list) and elem:
                if isinstance(elem[0], list):
                    components = elem
                elif isinstance(elem[0], int) and elem[0] in (1, 16, 17, 48, 64, 65, 80, 81):
                    components = [elem]  # single component
                elif isinstance(elem, list) and len(elem) >= 2:
                    # Could be a nested components list
                    for sub in elem:
                        if isinstance(sub, list) and len(sub) >= 2 and isinstance(sub[0], int):
                            if components is None:
                                components = []
                            components.append(sub)

        if not components:
            # Try the whole pf_data for components
            for elem in pf_data:
                if isinstance(elem, list) and len(elem) == 2 and isinstance(elem[0], int):
                    if components is None:
                        components = []
                    components.append(elem)

        if not components:
            content['match_all'] = True
            return {'content': content}

        for comp in components:
            if not isinstance(comp, list) or len(comp) < 2:
                continue
            comp_type = comp[0]
            comp_val = comp[1]
            if comp_type == 1:  # Match-all
                content['match_all'] = True
            elif comp_type == 48 or comp_type == 0x30:  # Protocol
                content['protocol'] = comp_val
            elif comp_type == 64 or comp_type == 0x40:  # Single local port
                content['local_port'] = comp_val
            elif comp_type == 65 or comp_type == 0x41:  # Local port range
                if isinstance(comp_val, list) and len(comp_val) >= 2:
                    content['local_port_min'] = comp_val[0]
                    content['local_port_max'] = comp_val[1]
            elif comp_type == 80 or comp_type == 0x50:  # Single remote port
                content['remote_port'] = comp_val
            elif comp_type == 81 or comp_type == 0x51:  # Remote port range
                if isinstance(comp_val, list) and len(comp_val) >= 2:
                    content['remote_port_min'] = comp_val[0]
                    content['remote_port_max'] = comp_val[1]

        return {'content': content}

    def _parse_qos_rules(self, inner):
        """Parse QoS rules from PDU Session Accept/Modification (pycrate object).

        Extracts QFI, packet filter info for each QoS rule.
        TS 24.501 §9.11.4.13 — QoS rules IE.
        """
        flows = []
        try:
            qos_ie = inner['QoSRules']
        except (KeyError, IndexError):
            self._log("QoS rules IE not found in message")
            return flows

        try:
            # Check if the IE is transparent (optional, not sent by network)
            if hasattr(qos_ie, 'get_trans') and qos_ie.get_trans():
                self._log("QoS rules IE transparent (not included by network)")
                return flows
            qos_rules_val = qos_ie['V'].get_val()
            self._log("QoS rules raw: type=%s len=%s hex=%s",
                      type(qos_rules_val).__name__,
                      len(qos_rules_val) if hasattr(qos_rules_val, '__len__') else '?',
                      qos_rules_val.hex() if isinstance(qos_rules_val, (bytes, bytearray)) else '?')
            if isinstance(qos_rules_val, (bytes, bytearray)):
                flows = self._decode_qos_rule_bytes(qos_rules_val)
                self._log("QoS rules decoded: %d flows — QFIs=%s filters=%s",
                          len(flows), [f.get('qfi') for f in flows],
                          [len(f.get('filters', [])) for f in flows])
            elif isinstance(qos_rules_val, list):
                for rule in qos_rules_val:
                    if isinstance(rule, list):
                        flow = self._parse_decoded_qos_rule(rule)
                        if flow:
                            flows.append(flow)
                self._log("QoS rules (list): %d flows — QFIs=%s",
                          len(flows), [f.get('qfi') for f in flows])
        except Exception as e:
            self._log("QoS rules parse error: %s", e)
            import traceback
            self._log("%s", traceback.format_exc())
        return flows

    def _decode_qos_rule_bytes(self, data):
        """Decode concatenated QoS rule bytes per TS 24.501 §9.11.4.13."""
        flows = []
        offset = 0
        while offset + 3 <= len(data):
            rule_id = data[offset]
            rule_len = (data[offset + 1] << 8) | data[offset + 2]
            offset += 3
            if offset + rule_len > len(data):
                break
            rule_data = data[offset:offset + rule_len]
            offset += rule_len

            if len(rule_data) < 3:
                continue

            op_dqr_npf = rule_data[0]
            npf = op_dqr_npf & 0x0F
            dqr = (op_dqr_npf >> 4) & 0x01
            op = (op_dqr_npf >> 5) & 0x07

            # Skip packet filter bytes
            pf_offset = 1
            filters = []
            for _ in range(npf):
                if pf_offset >= len(rule_data):
                    break
                pf_dir_id = rule_data[pf_offset]
                pf_id = pf_dir_id & 0x0F
                pf_dir = (pf_dir_id >> 4) & 0x03
                pf_offset += 1
                if pf_offset >= len(rule_data):
                    break
                pf_len = rule_data[pf_offset]
                pf_offset += 1
                pf_content = rule_data[pf_offset:pf_offset + pf_len]
                pf_offset += pf_len
                filters.append({
                    'pf_id': pf_id,
                    'direction': ['dl', 'ul', 'bidir', 'bidir'][pf_dir],
                    'content_hex': pf_content.hex(),
                    'content': self._parse_pf_components(pf_content),
                })

            # Precedence + QFI
            precedence = rule_data[pf_offset] if pf_offset < len(rule_data) else 255
            qfi = rule_data[pf_offset + 1] & 0x3F if pf_offset + 1 < len(rule_data) else 0

            flows.append({
                'rule_id': rule_id, 'qfi': qfi, 'dqr': dqr,
                'precedence': precedence, 'filters': filters,
            })
        return flows

    @staticmethod
    def _parse_pf_components(pf_content):
        """Parse packet filter component types (TS 24.501 §9.11.4.13)."""
        info = {}
        i = 0
        while i < len(pf_content):
            comp_type = pf_content[i]
            i += 1
            if comp_type == 0x01:  # Match-all
                info['match_all'] = True
            elif comp_type == 0x10 and i + 4 <= len(pf_content):  # IPv4 remote
                info['remote_ip'] = f"{pf_content[i]}.{pf_content[i+1]}.{pf_content[i+2]}.{pf_content[i+3]}"
                i += 4
                if i + 4 <= len(pf_content):
                    info['remote_mask'] = f"{pf_content[i]}.{pf_content[i+1]}.{pf_content[i+2]}.{pf_content[i+3]}"
                    i += 4
            elif comp_type == 0x11 and i + 4 <= len(pf_content):  # IPv4 local
                info['local_ip'] = f"{pf_content[i]}.{pf_content[i+1]}.{pf_content[i+2]}.{pf_content[i+3]}"
                i += 4
                if i + 4 <= len(pf_content):
                    info['local_mask'] = f"{pf_content[i]}.{pf_content[i+1]}.{pf_content[i+2]}.{pf_content[i+3]}"
                    i += 4
            elif comp_type == 0x30 and i + 1 <= len(pf_content):  # Protocol/Next header
                info['protocol'] = pf_content[i]  # 6=TCP, 17=UDP
                i += 1
            elif comp_type == 0x40 and i + 2 <= len(pf_content):  # Single local port
                info['local_port'] = (pf_content[i] << 8) | pf_content[i+1]
                i += 2
            elif comp_type == 0x41 and i + 4 <= len(pf_content):  # Local port range
                info['local_port_min'] = (pf_content[i] << 8) | pf_content[i+1]
                info['local_port_max'] = (pf_content[i+2] << 8) | pf_content[i+3]
                i += 4
            elif comp_type == 0x50 and i + 2 <= len(pf_content):  # Single remote port
                info['remote_port'] = (pf_content[i] << 8) | pf_content[i+1]
                i += 2
            elif comp_type == 0x51 and i + 4 <= len(pf_content):  # Remote port range
                info['remote_port_min'] = (pf_content[i] << 8) | pf_content[i+1]
                info['remote_port_max'] = (pf_content[i+2] << 8) | pf_content[i+3]
                i += 4
            else:
                break  # Unknown component
        return info

    def _send_modification_complete(self, psi):
        """Send PDU Session Modification Complete (NAS type 0xCC/204) via secured UL NAS.

        TS 24.501 §8.3.9 — UE acknowledges dedicated bearer activation.
        """
        # Build minimal 5GSM PDU Session Modification Complete
        # EPD=0x2E, PSI, PTI=0, Type=0xCC
        gsm_bytes = bytes([0x2E, psi, 0x00, 0xCC])
        # Wrap in UL NAS Transport
        plain = NasBuilder.ul_nas_transport_raw(psi, gsm_bytes)
        self._send_secured(plain)
        self._log("TX PDU Session Modification Complete (PSI=%d)", psi)

    def _parse_pco_bytes(self, pco_bytes):
        """Parse raw PCO bytes to extract P-CSCF and DNS addresses."""
        result = {}
        if not pco_bytes or len(pco_bytes) < 1:
            return result
        # Skip config protocol byte
        offset = 1 if pco_bytes[0] & 0x80 else 0
        while offset + 3 <= len(pco_bytes):
            container_id = (pco_bytes[offset] << 8) | pco_bytes[offset + 1]
            length = pco_bytes[offset + 2]
            offset += 3
            if offset + length > len(pco_bytes):
                break
            data = pco_bytes[offset:offset + length]
            if container_id == 0x000C and length >= 4:  # P-CSCF IPv4
                result['pcscf'] = f"{data[0]}.{data[1]}.{data[2]}.{data[3]}"
            elif container_id == 0x000D and length >= 4:  # DNS IPv4
                result['dns'] = f"{data[0]}.{data[1]}.{data[2]}.{data[3]}"
            offset += length
        return result

    def _parse_pco_list(self, ie):
        """Parse PCO from pycrate pre-decoded list format."""
        result = {}
        try:
            # Walk nested list looking for bytes that contain PCO containers
            def _find_bytes(obj):
                if isinstance(obj, bytes) and len(obj) >= 4:
                    return obj
                if isinstance(obj, list):
                    for item in obj:
                        found = _find_bytes(item)
                        if found:
                            return found
                return None

            raw = _find_bytes(ie)
            if raw:
                return self._parse_pco_bytes(raw)

            # Alternative: look for container tuples [id, length, data]
            if isinstance(ie, list):
                for item in ie:
                    if isinstance(item, list) and len(item) >= 3:
                        cid = item[0]
                        data = item[-1]
                        if cid == 0x000C and isinstance(data, bytes) and len(data) >= 4:
                            result['pcscf'] = f"{data[0]}.{data[1]}.{data[2]}.{data[3]}"
                        elif cid == 0x000D and isinstance(data, bytes) and len(data) >= 4:
                            result['dns'] = f"{data[0]}.{data[1]}.{data[2]}.{data[3]}"
        except Exception:
            pass
        return result

    # ─── Serialization ───

    def to_dict(self):
        return {
            "imsi": self.imsi, "state": self.state,
            "ran_ue_ngap_id": self.ran_ue_ngap_id,
            "amf_ue_ngap_id": self.amf_ue_ngap_id,
            "gnb": self.gnb.gnb_name if self.gnb else None,
            "pdu_sessions": {str(k): v for k, v in self.pdu_sessions.items()},
            "security": {
                "eea": self.security_ctx.get('eea', 0),
                "eia": self.security_ctx.get('eia', 0),
                "has_keys": self.security_ctx.get('knasint') is not None,
            },
        }
