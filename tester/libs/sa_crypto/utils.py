# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Licensed under the Apache License, Version 2.0
#
# sa_crypto/utils.py — shared helpers

__all__ = ['xor_buf', 'CMException', 'MAX_UINT32', 'MAX_UINT64',
           'int_from_bytes', 'bytes_from_int']

MAX_UINT32 = 1 << 32
MAX_UINT64 = 1 << 64


def xor_buf(b1, b2):
    """XOR two byte buffers (truncated to shorter length)."""
    return bytes(a ^ b for a, b in zip(b1, b2))


def int_from_bytes(b):
    return int.from_bytes(b, 'big')


def bytes_from_int(i, length):
    return i.to_bytes(length, 'big')


class CMException(Exception):
    """SA-crypto specific exception (API-compatible with CryptoMobile)."""
    pass
