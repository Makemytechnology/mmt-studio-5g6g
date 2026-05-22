# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""NAS message build + parse — pure functions, no state machine knowledge.

TS 24.501 — 5GS NAS protocol message formats.
"""

from pycrate_mobile.TS24501_FGMM import (
    FGMMRegistrationRequest, FGMMAuthenticationResponse, FGMMAuthenticationFailure,
    FGMMSecurityModeComplete, FGMMRegistrationComplete,
    FGMMMODeregistrationRequest, FGMMULNASTransport, FGMMSecProtNASMessage,
    FGMMServiceRequest,
)
from pycrate_mobile.TS24501_FGSM import FGSMPDUSessionEstabRequest
from pycrate_mobile.TS24501_IE import FGSID, FGSIDTYPE, FGSIDSTMSI, NSSAI
from pycrate_mobile.TS24301_IE import NAS_KSI
from pycrate_mobile.NAS import parse_NAS_MT
from pycrate_mobile.NAS5G import parse_NAS5G


def _psi_bitmap(psis) -> bytes:
    """Encode a PSI set as the 2-octet bitmap of TS 24.501 §9.11.3.44
    (PDU session status) / §9.11.3.57 (Uplink data status).

    Octet 1 bits 8..1 = PSI_7..PSI_0 (PSI_0 is spare, always 0).
    Octet 2 bits 8..1 = PSI_15..PSI_8.
    """
    octet1 = 0
    octet2 = 0
    for psi in psis:
        if 1 <= psi <= 7:
            octet1 |= 1 << psi
        elif 8 <= psi <= 15:
            octet2 |= 1 << (psi - 8)
    return bytes([octet1, octet2])


def _encode_stmsi(tmsi) -> bytes:
    """Encode a 5G-S-TMSI mobile identity per TS 24.501 §9.11.3.4.

    `tmsi` may be a dict with optional keys {AMFSetID, AMFPtr, 5GTMSI}
    — missing keys default to zero, which is a legal (if synthetic)
    encoding. Returns the LV value-part bytes the outer 5GSID Type6LVE
    will frame with its length.
    """
    sid = FGSIDSTMSI()
    sid.set_val({
        'ind': 0xF,
        'spare': 0,
        'Type': FGSIDTYPE.STMSI,
        'AMFSetID': (tmsi or {}).get('AMFSetID', 0),
        'AMFPtr':   (tmsi or {}).get('AMFPtr', 0),
        '5GTMSI':   (tmsi or {}).get('5GTMSI', 0),
    })
    return sid.to_bytes()


class NasBuilder:
    """NAS message construction — returns plain bytes."""

    @staticmethod
    def registration_request(imsi, mcc, mnc, ue_sec_cap, requested_nssai=None,
                             reg_type=1, ksi_value=7, routing_indicator='0000',
                             msin_override=None, prot_scheme_id=0):
        """TS 24.501 §8.2.6 — Registration Request (type 0x41).

        Extra knobs (all keep current default behaviour when omitted):
          reg_type        — 5GS Registration Type Value (TS 24.501 §9.11.3.7):
                            1=initial (default), 2=mobility,
                            3=periodic, 4=emergency, 5=sno.
          ksi_value       — ngKSI Value field (0..6 = key set ID, 7 = no
                            key available, forces fresh auth, §9.11.3.32).
          routing_indicator — SUCI Routing Indicator (TS 23.003 §2.2B);
                              malformed values exercise NSSF / AMF routing
                              error paths.
          msin_override   — substitute the MSIN portion of SUCI to drive
                            unknown-subscriber or malformed-SUCI paths
                            without rewriting the IMSI on the SIM.
          prot_scheme_id  — SUCI Protection Scheme (0=null/cleartext,
                            1=Profile A, 2=Profile B); used to send a
                            "concealed" SUCI the AMF can't decrypt without
                            the home-network private key.
        """
        msg = FGMMRegistrationRequest()
        msg['5GSRegType'].set_val([{'FOR': 1, 'Value': int(reg_type)}])

        ksi = NAS_KSI()
        ksi['TSC'].set_val(0)
        ksi['Value'].set_val(int(ksi_value))
        msg['NAS_KSI'] = [ksi.get_val()]

        # SUCI — protection scheme + routing indicator + MSIN (or override).
        msin = msin_override if msin_override is not None else imsi[len(mcc) + len(mnc):]
        fgsid = FGSID()
        fgsid.set_val({'Type': FGSIDTYPE.SUPI, 'Fmt': 0,
                        'Value': {'PLMN': mcc + mnc,
                                  'RoutingInd': routing_indicator,
                                  'ProtSchemeID': int(prot_scheme_id),
                                  'HNPKID': 0, 'Output': msin}})
        msg['5GSID']['V'].set_val(fgsid.to_bytes())

        msg['UESecCap']['V'].set_val(ue_sec_cap)
        msg['UESecCap'].set_trans(False)

        if requested_nssai:
            nssai_val = []
            for s in requested_nssai:
                sd = s.get("sd")
                nssai_val.append({'Len': 4 if sd else 1,
                                  'SNSSAI': (s["sst"], sd) if sd else (s["sst"],)})
            ie = NSSAI()
            ie.set_val(nssai_val)
            msg['NSSAI']['V'].set_val(ie.to_bytes())
            msg['NSSAI'].set_trans(False)

        return msg.to_bytes()

    @staticmethod
    def authentication_response(res_star):
        """TS 24.501 §8.2.2 — Authentication Response (type 0x57)."""
        msg = FGMMAuthenticationResponse()
        msg['RES']['V'].set_val(res_star)
        msg['RES'].set_trans(False)
        return msg.to_bytes()

    @staticmethod
    def authentication_failure(cause, auts=None):
        """TS 24.501 §8.2.4 — Authentication Failure (type 0x59).

        cause=21 (0x15): synch failure — includes AUTS for SQN resync.
        cause=20 (0x14): MAC failure.
        """
        msg = FGMMAuthenticationFailure()
        msg['5GMMCause']['5GMMCause'].set_val(cause)
        if auts and cause == 21:
            msg['AUTS']['V'].set_val(auts)
            msg['AUTS'].set_trans(False)
        return msg.to_bytes()

    @staticmethod
    def security_mode_complete(reg_request_bytes=None):
        """TS 24.501 §8.2.26 — Security Mode Complete (type 0x5E)."""
        msg = FGMMSecurityModeComplete()

        imeisv = FGSID()
        imeisv.set_val({'Type': FGSIDTYPE.IMEISV, 'Digits': '3578280100152100'})
        msg['IMEISV']['V'].set_val(imeisv.to_bytes())
        msg['IMEISV'].set_trans(False)

        if reg_request_bytes:
            msg['NASContainer']['V'].set_val(reg_request_bytes)
            msg['NASContainer'].set_trans(False)

        return msg.to_bytes()

    @staticmethod
    def registration_complete():
        """TS 24.501 §8.2.8 — Registration Complete (type 0x43)."""
        return FGMMRegistrationComplete().to_bytes()

    @staticmethod
    def service_request(service_type=1, tmsi=None,
                        pdu_session_status=None,
                        uplink_data_status=None,
                        allowed_pdu_session_status=None,
                        nas_message_container=None,
                        ksi=0):
        """TS 24.501 v19.6.2 §8.2.16 — Service Request (type 0x4C).

        Cleartext IEs per §4.4.6 line 4814-4834: EPD/SHT/Spare/MsgType
        + ngKSI + Service type + 5G-S-TMSI. Every other IE is non-
        cleartext and lives only inside the NAS message container per
        §4.4.6 case-(b.1) when the UE has a valid 5G NAS security
        context.

        Args:
          service_type: per §9.11.3.50 Table 9.11.3.50.1 —
            0=signalling, 1=data, 2=mobile-terminated services,
            3=emergency services, 4=emergency services fallback,
            5=high priority access, 6=elevated signalling.
          tmsi: 5G-S-TMSI dict {'AMFSetID', 'AMFPtr', '5GTMSI'} per
            §9.11.3.4 / TS24501_IE.FGSIDSTMSI. None ⇒ a zero-filled
            5G-S-TMSI is sent (legal: §8.2.16.1 marks 5G-S-TMSI as
            mandatory but the value is opaque to the codec).
          pdu_session_status: iterable of PSIs (1..15) the UE
            considers "not in 5GSM state PDU SESSION INACTIVE" per
            §9.11.3.44. Bit per PSI; PSI 0 spare. Non-cleartext —
            place inside `nas_message_container` per §4.4.6.
          uplink_data_status: iterable of PSIs (1..15) the UE wants
            re-activated per §9.11.3.57 ("List Of PDU Sessions To Be
            Activated", §4.2.3.2 step 1). Non-cleartext.
          allowed_pdu_session_status: iterable of PSIs per §9.11.3.13
            (paging-response with non-3GPP). Non-cleartext.
          nas_message_container: raw bytes of an inner SR per §4.4.6
            case-(b.1). When set, the outer SR carries only cleartext
            IEs + this container. Caller is responsible for cipher
            (NEA0 ⇒ plaintext bytes pass through).
          ksi: NAS key set identifier (0..6); 7 means "no key".
        """
        msg = FGMMServiceRequest()
        # ServiceType is a Type1V (single-nibble in upper half octet);
        # pycrate exposes the value at ['V'], not ['Value'].
        msg['ServiceType'].set_val({'V': service_type})
        # ngKSI — TSC=0 (native context), key set id from caller.
        msg['NAS_KSI'].set_val({'V': ksi & 0xF})

        # 5G-S-TMSI mobile identity (§9.11.3.4 / FGSIDSTMSI). When the
        # UE has no allocated TMSI yet (pre-registration), the spec
        # still mandates the IE; emit a zero-filled STMSI.
        if tmsi:
            msg['5GSID']['V'].set_val(_encode_stmsi(tmsi))
        else:
            msg['5GSID']['V'].set_val(_encode_stmsi({}))

        if pdu_session_status is not None:
            msg['PDUSessStat']['V'].set_val(_psi_bitmap(pdu_session_status))
            msg['PDUSessStat'].set_trans(False)

        if uplink_data_status is not None:
            msg['ULDataStat']['V'].set_val(_psi_bitmap(uplink_data_status))
            msg['ULDataStat'].set_trans(False)

        if allowed_pdu_session_status is not None:
            msg['AllowedPDUSessStat']['V'].set_val(
                _psi_bitmap(allowed_pdu_session_status))
            msg['AllowedPDUSessStat'].set_trans(False)

        if nas_message_container is not None:
            msg['NASContainer']['V'].set_val(bytes(nas_message_container))
            msg['NASContainer'].set_trans(False)

        return msg.to_bytes()

    @staticmethod
    def deregistration_request(switch_off=True, re_registration_required=False,
                                access_type=1):
        """TS 24.501 §8.2.11 — Deregistration Request MO (type 0x45).

        Per §9.11.3.20 Table 9.11.3.20.1 the De-registration type IE is a
        4-bit Type1V packed as:
          bit 4 (msb)   : Switch off          (0 = normal, 1 = switch off)
          bit 3         : Re-registration req (0 = no, 1 = yes)
          bits 2-1 (lsb): Access type         (01 = 3GPP, 10 = non-3GPP,
                                               11 = both)
        switch_off=True covers UE shutdown / USIM removal (§5.5.2.2.1);
        AMF MUST NOT reply with DEREGISTRATION ACCEPT in that case
        (§5.5.2.2.2). switch_off=False is "normal de-registration" and
        the AMF SHALL send a DEREGISTRATION ACCEPT back.
        """
        msg = FGMMMODeregistrationRequest()
        dereg_val = ((1 if switch_off else 0) << 3
                     | (1 if re_registration_required else 0) << 2
                     | (access_type & 0x03))
        msg['DeregistrationType']['V'].set_val(dereg_val)
        ksi = NAS_KSI()
        ksi['TSC'].set_val(0)
        ksi['Value'].set_val(0)
        msg['NAS_KSI'] = [ksi.get_val()]
        return msg.to_bytes()

    @staticmethod
    def ul_nas_transport_pdu_session(pdu_session_id, pti, dnn, sst, sd=None):
        """TS 24.501 §8.2.10 — UL NAS Transport with PDU Session Estab Request."""
        pdu_req = FGSMPDUSessionEstabRequest()
        pdu_req['5GSMHeader']['PDUSessID'].set_val(pdu_session_id)
        pdu_req['5GSMHeader']['PTI'].set_val(pti)
        pdu_req['PDUSessType']['V'].set_val(1)  # IPv4
        pdu_req['IntegrityProtMaxDataRate']['V'].set_val(b'\xff\xff')
        gsm_pdu = pdu_req.to_bytes()

        msg = FGMMULNASTransport()
        msg['PayloadContainerType']['V'].set_val(1)
        msg['PayloadContainer']['V'].set_val(gsm_pdu)
        msg['PDUSessID']['V'].set_val(bytes([pdu_session_id]))
        msg['PDUSessID'].set_trans(False)
        msg['RequestType']['V'].set_val(1)
        msg['RequestType'].set_trans(False)

        # TS 24.501 §9.11.2.8: S-NSSAI value = SST (1 byte) [+ SD (3 bytes)]
        # pycrate Type4TLV handles the T+L envelope; V is just the content
        snssai = (bytes([sst]) + sd.to_bytes(3, 'big')) if sd else bytes([sst])
        msg['SNSSAI']['V'].set_val(snssai)
        msg['SNSSAI'].set_trans(False)

        if dnn:
            msg['DNN']['V'].set_val(bytes([len(dnn)]) + dnn.encode('ascii'))
            msg['DNN'].set_trans(False)

        return msg.to_bytes()

    @staticmethod
    def ul_nas_transport_raw(pdu_session_id, gsm_pdu_bytes):
        """TS 24.501 §8.2.10 — UL NAS Transport with raw 5GSM payload.

        Used for PDU Session Modification Complete and other 5GSM messages
        that don't need to be built from pycrate objects.
        """
        msg = FGMMULNASTransport()
        msg['PayloadContainerType']['V'].set_val(1)  # N1 SM
        msg['PayloadContainer']['V'].set_val(gsm_pdu_bytes)
        msg['PDUSessID']['V'].set_val(bytes([pdu_session_id]))
        msg['PDUSessID'].set_trans(False)
        return msg.to_bytes()


class NasParser:
    """NAS message parsing — returns parsed objects."""

    @staticmethod
    def parse(nas_pdu_bytes):
        """Parse NAS PDU from network. Returns (msg, err)."""
        return parse_NAS_MT(nas_pdu_bytes)

    @staticmethod
    def parse_inner(nas_bytes):
        """Parse inner NAS after security unwrap. Returns (msg, err)."""
        return parse_NAS5G(nas_bytes, inner=True, sec_hdr=False)

    @staticmethod
    def is_secured(msg):
        """Check if message is security-protected."""
        if not isinstance(msg, FGMMSecProtNASMessage):
            return False
        try:
            return msg['5GMMHeaderSec']['SecHdr'].get_val() in (1, 2, 3, 4)
        except Exception:
            return False

    @staticmethod
    def get_message_type(msg):
        """Extract 5GMM or 5GSM message type. Returns int or None."""
        try:
            return msg["5GMMHeader"]["Type"].get_val()
        except Exception:
            pass
        try:
            return msg["5GSMHeader"]["Type"].get_val()
        except Exception:
            return None
