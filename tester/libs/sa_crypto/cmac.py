# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Licensed under the Apache License, Version 2.0
#
# sa_crypto/cmac.py — AES-CMAC for 3GPP integrity (EIA2 / NIA2)
#
# Uses the `cryptography` library's CMAC implementation directly,
# but also provides a compatible class interface for callers that
# pass arbitrary block ciphers (legacy CM.py EIA2 path).

__all__ = ['CMAC']

from struct import pack, unpack
from .utils import xor_buf, CMException, MAX_UINT64


class CMAC:
    """CMAC mode of operation (NIST SP 800-38B).

    API-compatible with CryptoMobile.CMAC for the EIA2 integrity path.

    Args:
        key:       AES key bytes.
        ciphermod: Block cipher class with block_size attr and encrypt() method.
        Tlen:      Requested MAC length in bits (default: full block).
    """

    def __init__(self, key, ciphermod, Tlen=None):
        self.key = key
        self._blocksize = ciphermod.block_size
        self._cipher = ciphermod(key)
        self._encrypt = self._cipher.encrypt
        self._keyschedule()
        if Tlen is None:
            self.Tlen = 8 * self._blocksize
        elif not 0 < Tlen <= 8 * self._blocksize:
            raise CMException('invalid Tlen')
        else:
            self.Tlen = Tlen

    def _keyschedule(self):
        L = self._encrypt(b'\x00' * 16)
        Lh, Ll = unpack('>QQ', L)
        K1 = (((Lh << 64) + Ll) << 1) & 0xffffffffffffffffffffffffffffffff
        if Lh & 0x8000000000000000:
            K1 ^= 0x87
        K2 = (K1 << 1) & 0xffffffffffffffffffffffffffffffff
        if K1 & 0x80000000000000000000000000000000:
            K2 ^= 0x87
        self.K1 = pack('>QQ', K1 >> 64, K1 % MAX_UINT64)
        self.K2 = pack('>QQ', K2 >> 64, K2 % MAX_UINT64)

    def cmac(self, data_in, data_len=None):
        """Compute AES-CMAC over data_in.

        Args:
            data_in:  Input bytes.
            data_len: Length in bits (default: 8*len(data_in)).
        """
        len_data_in = 8 * len(data_in)
        if data_len is None:
            data_len = len_data_in
            lastbits = 0
        elif not 0 < data_len <= len_data_in:
            raise CMException('invalid args')
        elif data_len < len_data_in:
            olen = data_len >> 3
            lastbits = (8 - (data_len % 8)) % 8
            if lastbits:
                data_in = data_in[:olen] + bytes([data_in[olen] & (0x100 - (1 << lastbits))])
            else:
                data_in = data_in[:olen]
        else:
            lastbits = 0

        M = [data_in[i:i + self._blocksize] for i in range(0, len(data_in), self._blocksize)]
        if M:
            Mn = M.pop()
            Mnlen = data_len % (8 * self._blocksize)
            if Mnlen:
                if lastbits:
                    Mn = Mn[:-1] + bytes([Mn[-1] + (1 << (lastbits - 1))])
                else:
                    Mn += b'\x80'
                Mn += (16 - 1 - (Mnlen >> 3)) * b'\x00'
                Mn = xor_buf(Mn, self.K2)
            else:
                Mn = xor_buf(Mn, self.K1)
        else:
            Mn = xor_buf(b'\x80' + b'\x00' * 15, self.K2)
        M.append(Mn)

        C = self._blocksize * b'\x00'
        for Mi in M:
            C = self._encrypt(xor_buf(C, Mi))

        if self.Tlen == 8 * self._blocksize:
            return C
        olen = self.Tlen >> 3
        T = C[:olen]
        if self.Tlen % 8:
            lastbits = (8 - (self.Tlen % 8)) % 8
            return T + bytes([C[olen] & (0x100 - (1 << lastbits))])
        return T
