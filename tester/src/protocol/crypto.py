# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""UE-side 5G-AKA cryptography — pure functions, no state.

TS 33.501 §6.1.3 — 5G-AKA, TS 33.102 §6.3 — Milenage, TS 33.501 Annex A — KDF.
"""

from sa_crypto.milenage import Milenage
from sa_crypto.utils import xor_buf
from sa_crypto.conv import (
    conv_501_A2, conv_501_A4, conv_501_A6, conv_501_A7, conv_501_A8, conv_501_A9,
)


def get_snn(mcc, mnc):
    """Serving Network Name — TS 33.501 §6.1.1."""
    return f"5G:mnc{mnc.zfill(3)}.mcc{mcc}.3gppnetwork.org".encode("utf-8")


def ue_authenticate(sim, rand, autn, sn_name):
    """UE-side 5G-AKA. Returns dict with RESstar/keys on success,
    or dict with 'sync_failure' + 'AUTS' on SQN mismatch,
    or None on unrecoverable MAC failure.

    TS 33.501 §6.1.3, TS 33.102 §6.3.3 (SQN resync via AUTS).
    """
    K, OPc, op_type = sim.k, sim.opc, sim.op_type
    mil = Milenage(None)
    if op_type == "OPC":
        mil.set_opc(OPc)
        XRES, CK, IK, AK = mil.f2345(K, rand)
    else:
        XRES, CK, IK, AK = mil.f2345(K, rand, OPc)

    sqn_xor_ak = autn[:6]
    amf_field = autn[6:8]
    mac_a = autn[8:16]
    sqn_bytes = xor_buf(sqn_xor_ak, AK)

    if op_type == "OPC":
        mil.set_opc(OPc)
        expected_mac = mil.f1(K, rand, sqn_bytes, amf_field)
    else:
        expected_mac = mil.f1(K, rand, sqn_bytes, amf_field, OPc)

    if mac_a != expected_mac:
        # MAC failed — compute AUTS for SQN resync (TS 33.102 §6.3.3)
        auts = compute_auts(sim, rand)
        if auts:
            return {"sync_failure": True, "AUTS": auts}
        return None

    return {
        "RESstar": conv_501_A4(CK, IK, sn_name, rand, XRES),
        "KAUSF": conv_501_A2(CK, IK, sn_name, sqn_xor_ak),
        "KSEAF": conv_501_A6(conv_501_A2(CK, IK, sn_name, sqn_xor_ak), sn_name),
        "CK": CK, "IK": IK,
    }


def compute_auts(sim, rand):
    """Compute AUTS for SQN resynchronization (TS 33.102 §6.3.3).

    AUTS = SQN_MS xor AK || MAC-S
    where MAC-S = f1*(K, SQN_MS, RAND, AMF=0x0000)
    """
    K, OPc, op_type = sim.k, sim.opc, sim.op_type
    sqn_val = sim.sqn if isinstance(sim.sqn, int) else int.from_bytes(sim.sqn, 'big')
    sqn_ms = sqn_val.to_bytes(6, 'big')

    mil = Milenage(None)
    if op_type == "OPC":
        mil.set_opc(OPc)
        _, _, _, AK = mil.f2345(K, rand)
        mac_s = mil.f1star(K, rand, sqn_ms, b'\x00\x00')
    else:
        _, _, _, AK = mil.f2345(K, rand, OPc)
        mac_s = mil.f1star(K, rand, sqn_ms, b'\x00\x00', OPc)

    auts = xor_buf(sqn_ms, AK) + mac_s
    return auts


def derive_kamf(kseaf, supi, abba=b"\x00\x00"):
    return conv_501_A7(kseaf, supi.encode("utf-8"), abba)


def derive_nas_keys(kamf, ciph_algo_id, integ_algo_id):
    return (
        conv_501_A8(kamf, alg_type=1, alg_id=ciph_algo_id)[-16:],
        conv_501_A8(kamf, alg_type=2, alg_id=integ_algo_id)[-16:],
    )


def derive_kgnb(kamf, ul_nas_count):
    return conv_501_A9(kamf, ul_nas_cnt=ul_nas_count, acc_type_dist=1)
