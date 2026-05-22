# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Licensed under the Apache License, Version 2.0
#
# sa_crypto/aes.py — AES-ECB and AES-CTR wrappers over `cryptography`

__all__ = ['AES_ECB', 'AES_CTR']

from struct import pack
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes


class AES_ECB:
    """AES in ECB mode (single-block encrypt for Milenage / CMAC)."""

    block_size = 16

    def __init__(self, key):
        self._encryptor = Cipher(algorithms.AES(key), modes.ECB()).encryptor()

    def encrypt(self, data):
        return self._encryptor.update(data)


class AES_CTR:
    """AES in CTR mode (stream cipher for EEA2 / ECIES).

    Args:
        key:   16-byte AES key.
        nonce: 8-byte nonce (most-significant half of 128-bit IV).
        cnt:   uint64 initial counter (least-significant half), default 0.
    """

    block_size = 16

    def __init__(self, key, nonce, cnt=0):
        iv = nonce + pack('>Q', cnt)
        self._cipher = Cipher(algorithms.AES(key), modes.CTR(iv)).encryptor()

    def encrypt(self, data):
        return self._cipher.update(data)

    decrypt = encrypt
