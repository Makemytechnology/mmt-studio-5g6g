# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Licensed under the Apache License, Version 2.0
#
# sa_crypto/conv.py — 3GPP key derivation / conversion functions
#
# Clean-room implementation per:
#   TS 33.102 Annex C   — 2G/3G conversion (C2–C5)
#   TS 33.401 Annex A   — LTE key derivation (A2–A7)
#   TS 33.501 Annex A   — 5G key derivation (A2–A23)

__all__ = [
    'KDF',
    'conv_102_C2', 'conv_102_C3', 'conv_102_C4', 'conv_102_C5',
    'conv_401_A2', 'conv_401_A3', 'conv_401_A4', 'conv_401_A7',
    'conv_501_A2', 'conv_501_A3', 'conv_501_A4', 'conv_501_A5',
    'conv_501_A6', 'conv_501_A7', 'conv_501_A8', 'conv_501_A9',
    'conv_501_A10', 'conv_501_A11', 'conv_501_A12', 'conv_501_A13',
    'conv_501_A141', 'conv_501_A142', 'conv_501_A151', 'conv_501_A152',
    'conv_501_A16', 'conv_501_A17', 'conv_501_A18',
    'conv_501_A19', 'conv_501_A20', 'conv_501_A21',
    'conv_501_A22', 'conv_501_A23',
]

import hmac
from hashlib import sha256
from struct import pack
from .utils import xor_buf, CMException


# TS 33.220: Generic KDF — HMAC-SHA-256
def KDF(K, S):
    """3GPP Key Derivation Function (TS 33.220)."""
    return hmac.new(K, S, sha256).digest()


# --------------------------------------------------------------------------- #
# 2G / 3G conversion functions (TS 33.102, §6.8.1.2, Annex C)
# --------------------------------------------------------------------------- #

def conv_102_C2(XRES):
    """SRES from XRES (4 bytes)."""
    if not 4 <= len(XRES) <= 16:
        raise CMException('conv_C2: invalid args')
    x = XRES + b'\x00' * (16 - len(XRES)) if len(XRES) < 16 else XRES
    return xor_buf(xor_buf(xor_buf(x[:4], x[4:8]), x[8:12]), x[12:16])


def conv_102_C3(CK, IK):
    """Kc from CK, IK (8 bytes)."""
    if len(CK) != 16 or len(IK) != 16:
        raise CMException('conv_C3: invalid args')
    return xor_buf(xor_buf(xor_buf(CK[:8], CK[8:]), IK[:8]), IK[8:])


def conv_102_C4(Kc):
    """CK from Kc (16 bytes)."""
    if len(Kc) != 8:
        raise CMException('conv_C4: invalid args')
    return Kc + Kc


def conv_102_C5(Kc):
    """IK from Kc (16 bytes)."""
    if len(Kc) != 8:
        raise CMException('conv_C5: invalid args')
    x = xor_buf(Kc[:4], Kc[4:])
    return x + Kc + x


# --------------------------------------------------------------------------- #
# LTE key derivation (TS 33.401, Annex A)
# --------------------------------------------------------------------------- #

def conv_401_A2(CK, IK, sn_id, sqn_x_ak):
    """KASME from CK, IK, SN-ID, SQN^AK (32 bytes)."""
    if len(CK) != 16 or len(IK) != 16 or len(sn_id) != 3 or len(sqn_x_ak) != 6:
        raise CMException('conv_401_A2: invalid args')
    return KDF(CK + IK, b'\x10' + sn_id + b'\x00\x03' + sqn_x_ak + b'\x00\x06')


def conv_401_A3(Kasme, ul_nas_cnt):
    """KeNB from KASME and UL NAS count (32 bytes)."""
    if len(Kasme) != 32 or not 0 <= ul_nas_cnt < 16777216:
        raise CMException('conv_401_A3: invalid args')
    return KDF(Kasme, b'\x11' + pack('>IH', ul_nas_cnt, 4))


def conv_401_A4(Kasme, SYNC):
    """NH from KASME and SYNC (32 bytes)."""
    if len(Kasme) != 32 or len(SYNC) != 32:
        raise CMException('conv_401_A4: invalid args')
    return KDF(Kasme, b'\x12' + SYNC + b'\x00\x20')


def conv_401_A7(KEY, alg_dist=0, alg_id=0):
    """NAS/RRC/UP key from KASME or KeNB (32 bytes)."""
    if len(KEY) != 32 or not 0 <= alg_dist < 256 or not 0 <= alg_id < 256:
        raise CMException('conv_401_A7: invalid args')
    return KDF(KEY, b'\x15' + pack('>BHBH', alg_dist, 1, alg_id, 1))


# --------------------------------------------------------------------------- #
# 5G key derivation (TS 33.501, Annex A)
# --------------------------------------------------------------------------- #

def conv_501_A2(CK, IK, sn_name, sqn_x_ak):
    """KAUSF from CK, IK, serving network name, SQN^AK (32 bytes)."""
    if len(CK) != 16 or len(IK) != 16 or not 32 <= len(sn_name) <= 255 or len(sqn_x_ak) != 6:
        raise CMException('conv_501_A2: invalid args')
    return KDF(CK + IK, b'\x6a' + sn_name + pack('>H', len(sn_name)) +
               sqn_x_ak + b'\x00\x06')


def conv_501_A3(CK, IK, an_id, sqn_x_ak):
    """CK', IK' from CK, IK, access network ID, SQN^AK (16+16 bytes)."""
    if len(CK) != 16 or len(IK) != 16 or not 6 <= len(an_id) <= 255 or len(sqn_x_ak) != 6:
        raise CMException('conv_501_A3: invalid args')
    buf = KDF(CK + IK, b'\x20' + an_id + pack('>H', len(an_id)) +
              sqn_x_ak + b'\x00\x06')
    return buf[:16], buf[16:]


def conv_501_A4(CK, IK, sn_name, rand, res):
    """RES* from CK, IK, SN name, RAND, RES (16 bytes)."""
    if len(CK) != 16 or len(IK) != 16 or not 32 <= len(sn_name) <= 255 \
       or len(rand) != 16 or not 4 <= len(res) <= 16:
        raise CMException('conv_501_A4: invalid args')
    return KDF(CK + IK, b'\x6b' + sn_name + pack('>H', len(sn_name)) +
               rand + b'\x00\x10' + res + pack('>H', len(res)))[16:]


def conv_501_A5(rand, res_star):
    """HRES* from RAND, RES* (16 bytes)."""
    if len(rand) != 16 or len(res_star) != 16:
        raise CMException('conv_501_A5: invalid args')
    return sha256(rand + res_star).digest()[16:]


def conv_501_A6(KAUSF, sn_name):
    """K_SEAF from KAUSF, SN name (32 bytes)."""
    if len(KAUSF) != 32 or not 32 <= len(sn_name) <= 255:
        raise CMException('conv_501_A6: invalid args')
    return KDF(KAUSF, b'\x6c' + sn_name + pack('>H', len(sn_name)))


def conv_501_A7(KSEAF, subs_id, abba):
    """K_AMF from K_SEAF, subscriber ID, ABBA (32 bytes)."""
    if len(KSEAF) != 32 or not 12 <= len(subs_id) <= 255 or len(abba) != 2:
        raise CMException('conv_501_A7: invalid args')
    return KDF(KSEAF, b'\x6d' + subs_id + pack('>H', len(subs_id)) +
               abba + b'\x00\x02')


def conv_501_A8(K, alg_type=1, alg_id=1):
    """K_NAS_enc/int, K_RRC_enc/int, K_UP_enc/int from K_AMF or K_gNB (32 bytes)."""
    if len(K) != 32 or not 0 <= alg_type <= 6 or not 0 <= alg_id <= 15:
        raise CMException('conv_501_A8: invalid args')
    return KDF(K, b'\x69' + pack('>BHBH', alg_type, 1, alg_id, 1))


def conv_501_A9(KAMF, ul_nas_cnt=0, acc_type_dist=1):
    """K_gNB / K_N3IWF from K_AMF, UL NAS count, access type (32 bytes)."""
    if len(KAMF) != 32 or not 0 <= ul_nas_cnt <= 4294967295 or not 1 <= acc_type_dist <= 2:
        raise CMException('conv_501_A9: invalid args')
    return KDF(KAMF, b'\x6e' + pack('>IHBH', ul_nas_cnt, 4, acc_type_dist, 1))


def conv_501_A10(KAMF, sync):
    """NH from K_AMF, SYNC (32 bytes)."""
    if len(KAMF) != 32 or len(sync) != 32:
        raise CMException('conv_501_A10: invalid args')
    return KDF(KAMF, b'\x6f' + sync + b'\x00\x20')


def conv_501_A11(K, pci=0, arfcn_dl=0):
    """K_NG-RAN* for target gNB (32 bytes)."""
    if len(K) != 32 or not 0 <= pci <= 65535 or not 0 <= arfcn_dl <= 16777216:
        raise CMException('conv_501_A11: invalid args')
    return KDF(K, b'\x70' + pack('>HH', pci, 2) + pack('>IH', arfcn_dl, 3)[1:])


def conv_501_A12(K, pci=0, earfcn_dl=0):
    """K_NG-RAN* for target ng-eNB (32 bytes)."""
    if len(K) != 32 or not 0 <= pci <= 65535 or not 0 <= earfcn_dl <= 16777216:
        raise CMException('conv_501_A12: invalid args')
    return KDF(K, b'\x71' + pack('>HH', pci, 2) + pack('>IH', earfcn_dl, 3)[1:])


def conv_501_A13(KAMF, dir=1, dl_nas_cnt=0):
    """K_AMF' from K_AMF, direction, DL NAS count (32 bytes)."""
    if len(KAMF) != 32 or dir != 1 or not 0 <= dl_nas_cnt <= 4294967295:
        raise CMException('conv_501_A13: invalid args')
    return KDF(KAMF, b'\x72' + pack('>BHIH', dir, 1, dl_nas_cnt, 4))


def conv_501_A141(KAMF, ul_nas_cnt=0):
    """K_ASME' for 5G-to-EPS idle mobility (32 bytes)."""
    if len(KAMF) != 32 or not 0 <= ul_nas_cnt <= 4294967295:
        raise CMException('conv_501_A141: invalid args')
    return KDF(KAMF, b'\x73' + pack('>IH', ul_nas_cnt, 4))


def conv_501_A142(KAMF, dl_nas_cnt=0):
    """K_ASME' for 5G-to-EPS handover (32 bytes)."""
    if len(KAMF) != 32 or not 0 <= dl_nas_cnt <= 4294967295:
        raise CMException('conv_501_A142: invalid args')
    return KDF(KAMF, b'\x74' + pack('>IH', dl_nas_cnt, 4))


def conv_501_A151(KASME, ul_nas_cnt=0):
    """K_AMF' for EPS-to-5G idle mobility (32 bytes)."""
    if len(KASME) != 32 or not 0 <= ul_nas_cnt <= 4294967295:
        raise CMException('conv_501_A151: invalid args')
    return KDF(KASME, b'\x75' + pack('>IH', ul_nas_cnt, 4))


def conv_501_A152(KASME, nh):
    """K_AMF' for EPS-to-5G handover (32 bytes)."""
    if len(KASME) != 32 or len(nh) != 32:
        raise CMException('conv_501_A152: invalid args')
    return KDF(KASME, b'\x76' + nh + b'\x00\x20')


def conv_501_A16(K, sn_cnt=0):
    """K_SN from master node key, SN count (32 bytes)."""
    if len(K) != 32 or not 0 <= sn_cnt <= 65535:
        raise CMException('conv_501_A16: invalid args')
    return KDF(K, b'\x79' + pack('>HH', sn_cnt, 2))


def conv_501_A17(KAUSF, sor_hdr, sor_cnt=0, pref_plmn=None):
    """SoR-MAC-I_AUSF (16 bytes)."""
    if len(KAUSF) != 32 or not 0 <= len(sor_hdr) <= 65535 or not 0 <= sor_cnt <= 65535:
        raise CMException('conv_501_A17: invalid args')
    S = b'\x77' + sor_hdr + pack('>H', len(sor_hdr)) + pack('>HH', sor_cnt, 2)
    if pref_plmn is not None:
        S += pref_plmn + pack('>H', len(pref_plmn))
    return KDF(KAUSF, S)


def conv_501_A18(KAUSF, sor_ack=1, sor_cnt=0):
    """SoR-MAC-I_UE (16 bytes)."""
    if len(KAUSF) != 32 or sor_ack != 1 or not 0 <= sor_cnt <= 65535:
        raise CMException('conv_501_A18: invalid args')
    return KDF(KAUSF, b'\x78' + pack('>BHHH', sor_ack, 2, sor_cnt, 2))


def conv_501_A19(KAUSF, upu_data, upu_cnt=0):
    """UPU-MAC-I_AUSF (16 bytes)."""
    if len(KAUSF) != 32 or not 0 <= len(upu_data) <= 65535 or not 0 <= upu_cnt <= 65535:
        raise CMException('conv_501_A19: invalid args')
    return KDF(KAUSF, b'\x7b' + upu_data + pack('>HHH', 2, upu_cnt, 2))


def conv_501_A20(KAUSF, upu_ack=1, upu_cnt=0):
    """UPU-MAC-I_UE (16 bytes)."""
    if len(KAUSF) != 32 or upu_ack != 1 or not 0 <= upu_cnt <= 65535:
        raise CMException('conv_501_A20: invalid args')
    return KDF(KAUSF, b'\x7c' + pack('>BHHH', upu_ack, 2, upu_cnt, 2))


def conv_501_A21(KAMF, dl_nas_cnt=0):
    """K_ASME_SRVCC from K_AMF, DL NAS count (32 bytes)."""
    if len(KAMF) != 32 or not 0 <= dl_nas_cnt <= 4294967295:
        raise CMException('conv_501_A21: invalid args')
    return KDF(KAMF, b'\x7d' + pack('>IH', dl_nas_cnt, 4))


def conv_501_A22(KTNGF, use_type_dist=1):
    """K_TIPsec or K_TNAP from K_TNGF (32 bytes). FC not yet defined in TS 33.501."""
    if len(KTNGF) != 32 or not 1 <= use_type_dist <= 2:
        raise CMException('conv_501_A22: invalid args')
    return KDF(KTNGF, b'' + pack('>BH', use_type_dist, 1))


def conv_501_A23(KGNB, cu_ip_addr, du_ip_addr):
    """K_IAB_PSK from K_gNB, CU IP, DU IP (32 bytes)."""
    if len(KGNB) != 32 or not 0 <= len(cu_ip_addr) <= 32 or not 0 <= len(du_ip_addr) <= 32:
        raise CMException('conv_501_A23: invalid args')
    return KDF(KGNB, b'\x83' + cu_ip_addr + pack('>H', len(cu_ip_addr)) +
               du_ip_addr + pack('>H', len(du_ip_addr)))
