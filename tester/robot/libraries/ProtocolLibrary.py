# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Robot Framework keyword library — Protocol decode."""

import os, sys

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
for p in (PROJECT_ROOT, os.path.join(PROJECT_ROOT, "libs")):
    if p not in sys.path:
        sys.path.insert(0, p)

import re
from robot.api.deco import keyword, library
from robot.api import logger


@library(scope='GLOBAL', version='1.0')
class ProtocolLibrary:
    ROBOT_LIBRARY_SCOPE = 'GLOBAL'

    @keyword("Decode NAS Hex")
    def decode_nas_hex(self, hex_string):
        from pycrate_mobile.NAS import parse_NAS_MO, parse_NAS_MT
        from binascii import unhexlify
        hex_clean = re.sub(r'[\s:.-]', '', hex_string)
        nas_bytes = unhexlify(hex_clean)
        msg, err = parse_NAS_MO(nas_bytes)
        if msg is None:
            msg, err = parse_NAS_MT(nas_bytes)
        result = msg.show() if msg else f"Parse error: {err}"
        logger.info(result[:500])
        return result

    @keyword("Decode NGAP Hex")
    def decode_ngap_hex(self, hex_string):
        from pycrate_asn1dir import NGAP
        from binascii import unhexlify
        hex_clean = re.sub(r'[\s:.-]', '', hex_string)
        pdu = NGAP.NGAP_PDU_Descriptions.NGAP_PDU
        pdu.from_aper(unhexlify(hex_clean))
        result = pdu.to_asn1()
        logger.info(result[:500])
        return result

    @keyword("NAS Should Contain Message Type")
    def nas_should_contain_type(self, decoded_text, expected_type):
        if expected_type not in decoded_text:
            raise AssertionError(f"Expected '{expected_type}' not in decoded NAS")

    @keyword("Build NGAP NG Setup Request")
    def build_ng_setup(self, gnb_id=0x500001, gnb_name="test-gnb", mcc="001", mnc="01",
                        tac="0001", sst=1, sd=0x010203):
        from src.protocol.ngap import NgapCodec
        data = NgapCodec.build_ng_setup_request(
            int(gnb_id, 16) if isinstance(gnb_id, str) else int(gnb_id),
            gnb_name, mcc, mnc, tac, [{"sst": int(sst), "sd": int(sd)}])
        return data.hex()

    @keyword("Build NGAP NG Setup Request From Config")
    def build_ng_setup_from_config(self, config_name):
        """Build NG Setup Request using parameters from a gNB config profile."""
        from src.protocol.ngap import NgapCodec
        from src.protocol.gnb_config import gnb_cfg_get
        from src.config import GNB_PROFILES_PATH, GNB_DEFAULTS
        profile = gnb_cfg_get(GNB_PROFILES_PATH, config_name)
        if not profile:
            raise AssertionError(f"gNB config profile not found: {config_name}")
        gnb_id_str = str(profile.get("gnb_id", "0x500000"))
        gnb_id = int(gnb_id_str, 16) if gnb_id_str.startswith("0x") else int(gnb_id_str)
        slices = profile.get("slices", GNB_DEFAULTS["slices"])
        for s in slices:
            if isinstance(s.get("sd"), str) and s["sd"].startswith("0x"):
                s["sd"] = int(s["sd"], 16)
        data = NgapCodec.build_ng_setup_request(
            gnb_id, profile.get("gnb_name", config_name),
            profile.get("mcc", "001"), profile.get("mnc", "01"),
            profile.get("tac", "0001"), slices)
        return data.hex()

    @keyword("Build NAS Registration Request")
    def build_reg_request(self, imsi="001010123456901", mcc="001", mnc="01"):
        from src.protocol.nas import NasBuilder
        from src.config import UE_DEFAULTS
        data = NasBuilder.registration_request(imsi, mcc, mnc, UE_DEFAULTS["ue_sec_cap"],
                                                 UE_DEFAULTS.get("requested_nssai"))
        return data.hex()
