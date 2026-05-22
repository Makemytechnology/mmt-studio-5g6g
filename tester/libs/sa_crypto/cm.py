# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Licensed under the Apache License, Version 2.0
#
# sa_crypto/cm.py — 3GPP NAS ciphering & integrity algorithms
#
# Phase 1: AES-based algorithms only (NEA2 / NIA2) — fully replaceable
# with the `cryptography` library.
#
# Phase 2 (future): SNOW3G-based NEA1/NIA1 and ZUC-based NEA3/NIA3
# require clean-room C implementations of SNOW3G and ZUC from 3GPP
# specs TS 35.201 and TS 35.221, or optional runtime plugins.
#
# For now, EEA1/EIA1/EEA3/EIA3 fall back to CryptoMobile C extensions
# if available, otherwise raise ImportError at call time.

__all__ = ['EEA2', 'EIA2']

from struct import pack
from .aes import AES_CTR, AES_ECB
from .cmac import CMAC
from .utils import CMException, MAX_UINT32


# --------------------------------------------------------------------------- #
# NEA2 / NIA2 — AES-based (TS 33.501 §5.3, TS 33.401 Annex B)
# --------------------------------------------------------------------------- #

class AES_3GPP:
    """AES-based LTE/5G ciphering (EEA2) and integrity (EIA2)."""

    def EEA2(self, key, count, bearer, dir, data_in, bitlen=None):
        if not 0 <= count < MAX_UINT32 or not 0 <= bearer <= 32:
            raise CMException('EEA2: invalid args')
        if bitlen is None:
            bitlen = 8 * len(data_in)
            lastbits = None
        else:
            lastbits = (8 - (bitlen % 8)) % 8
            blen = bitlen >> 3
            if lastbits:
                blen += 1
            if blen < len(data_in):
                data_in = data_in[:blen]

        nonce = pack('>II', count, (bearer << 27) + (dir << 26))
        enc = AES_CTR(key, nonce).encrypt(data_in)

        if lastbits:
            return enc[:-1] + bytes([enc[-1] & (0x100 - (1 << lastbits))])
        return enc

    def EIA2(self, key, count, bearer, dir, data_in, bitlen=None):
        if not 0 <= count < MAX_UINT32 or not 0 <= bearer <= 32:
            raise CMException('EIA2: invalid args')
        if bitlen is None:
            bitlen = 8 * len(data_in)
        else:
            lastbits = (8 - (bitlen % 8)) % 8
            blen = bitlen >> 3
            if lastbits:
                blen += 1
            if blen < len(data_in):
                data_in = data_in[:blen]

        M = pack('>II', count, (bearer << 27) + (dir << 26)) + data_in
        cmac = CMAC(key, AES_ECB, Tlen=32)
        return cmac.cmac(M, 64 + bitlen)


_A = AES_3GPP()
EEA2 = _A.EEA2
EIA2 = _A.EIA2


# --------------------------------------------------------------------------- #
# NEA1/NIA1 (SNOW3G) and NEA3/NIA3 (ZUC) — optional C extension fallback
# --------------------------------------------------------------------------- #

def _not_available(name):
    def _stub(*args, **kwargs):
        raise ImportError(
            f'{name} requires SNOW3G/ZUC C extensions (Phase 2). '
            'Use NEA2/NIA2 (AES-based) instead.')
    _stub.__name__ = name
    return _stub

EEA1 = _not_available('EEA1')
EIA1 = _not_available('EIA1')
EEA3 = _not_available('EEA3')
EIA3 = _not_available('EIA3')
