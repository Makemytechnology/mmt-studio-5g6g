# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Licensed under the Apache License, Version 2.0
#
# sa_crypto/milenage.py — Milenage authentication algorithm
#
# Clean-room implementation per 3GPP TS 35.205, 35.206, 35.207.
# AES-based authentication for 3G/4G/5G USIM.

__all__ = ['Milenage', 'make_OPc']

from struct import pack, unpack
from .utils import xor_buf
from .aes import AES_ECB


def _rot16(b, r):
    """Rotate a 16-byte buffer left by r bits."""
    ro, rb = r >> 3, r % 8
    br = b[ro:] + b[:ro]
    if rb:
        b0, b1 = unpack('>QQ', br)
        br = pack('>QQ',
                  ((b0 << rb) & 0xffffffffffffffff) | (b1 >> (64 - rb)),
                  ((b1 << rb) & 0xffffffffffffffff) | (b0 >> (64 - rb)))
    return br


def make_OPc(K, OP):
    """Derive OPc from K and OP: OPc = AES_K(OP) XOR OP."""
    return xor_buf(AES_ECB(K).encrypt(OP), OP)


class Milenage:
    """Milenage cryptographic functions (TS 35.205).

    Operator constants c1–c5, r1–r5 use the recommended defaults.
    """

    c1 = b'\x00' * 16
    c2 = b'\x00' * 15 + b'\x01'
    c3 = b'\x00' * 15 + b'\x02'
    c4 = b'\x00' * 15 + b'\x04'
    c5 = b'\x00' * 15 + b'\x08'

    r1 = 0x40
    r2 = 0x00
    r3 = 0x20
    r4 = 0x40
    r5 = 0x60

    def __init__(self, OP):
        self.OP = OP
        self.OPc = None

    def set_opc(self, OPc):
        """Pre-set OPc to save AES rounds across multiple vectors."""
        self.OPc = OPc

    def unset_opc(self):
        self.OPc = None

    def _get_opc(self, K, OP):
        if self.OPc is not None:
            return self.OPc
        return make_OPc(K, OP if OP is not None else self.OP)

    def f1(self, K, RAND, SQN, AMF, OP=None):
        """Compute MAC-A (8 bytes) — network authentication. Returns None on error."""
        if len(K) != 16 or len(RAND) != 16 or len(SQN) != 6 or len(AMF) != 2:
            return None
        OPc = self._get_opc(K, OP)
        inp = SQN + AMF + SQN + AMF
        cipher = AES_ECB(K)
        tmp = cipher.encrypt(xor_buf(RAND, OPc))
        out1 = xor_buf(
            cipher.encrypt(xor_buf(xor_buf(_rot16(xor_buf(inp, OPc), self.r1), self.c1), tmp)),
            OPc)
        return out1[0:8]

    def f1star(self, K, RAND, SQN, AMF, OP=None):
        """Compute MAC-S (8 bytes) — re-sync authentication. Returns None on error."""
        if len(K) != 16 or len(RAND) != 16 or len(SQN) != 6 or len(AMF) != 2:
            return None
        OPc = self._get_opc(K, OP)
        inp = SQN + AMF + SQN + AMF
        cipher = AES_ECB(K)
        tmp = cipher.encrypt(xor_buf(RAND, OPc))
        out1 = xor_buf(
            cipher.encrypt(xor_buf(xor_buf(_rot16(xor_buf(inp, OPc), self.r1), self.c1), tmp)),
            OPc)
        return out1[8:16]

    def f2345(self, K, RAND, OP=None):
        """Compute (RES[8], CK[16], IK[16], AK[6]). Returns None on error."""
        if len(K) != 16 or len(RAND) != 16:
            return None
        OPc = self._get_opc(K, OP)
        cipher = AES_ECB(K)
        tmp = xor_buf(cipher.encrypt(xor_buf(OPc, RAND)), OPc)

        out2 = xor_buf(OPc, cipher.encrypt(xor_buf(_rot16(tmp, self.r2), self.c2)))
        out3 = xor_buf(OPc, cipher.encrypt(xor_buf(_rot16(tmp, self.r3), self.c3)))
        out4 = xor_buf(OPc, cipher.encrypt(xor_buf(_rot16(tmp, self.r4), self.c4)))

        return out2[8:16], out3, out4, out2[:6]

    def f5star(self, K, RAND, OP=None):
        """Compute AK for re-sync (6 bytes). Returns None on error."""
        if len(K) != 16 or len(RAND) != 16:
            return None
        OPc = self._get_opc(K, OP)
        cipher = AES_ECB(K)
        tmp = xor_buf(cipher.encrypt(xor_buf(OPc, RAND)), OPc)
        out5 = xor_buf(OPc, cipher.encrypt(xor_buf(_rot16(tmp, self.r5), self.c5)))
        return out5[:6]
