# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Licensed under the Apache License, Version 2.0
#
# sa_crypto/ec.py — Elliptic curve helpers for ECIES (TS 33.501 §6.12)
#
# X25519 (Profile A) and NIST secp256r1 (Profile B) key exchange,
# plus ANSI X9.63 KDF for shared-key derivation.

__all__ = ['X25519', 'ECDH_SECP256R1', 'KDF']

from cryptography.hazmat.primitives import serialization, hashes
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
from cryptography.hazmat.primitives.kdf.x963kdf import X963KDF
from .utils import int_from_bytes, bytes_from_int


class X25519:
    """Curve25519 ECDH (ECIES Profile A)."""

    def __init__(self, loc_privkey=None):
        if loc_privkey:
            self.PrivKey = X25519PrivateKey.from_private_bytes(loc_privkey)
        else:
            self.generate_keypair()

    def generate_keypair(self):
        self.PrivKey = X25519PrivateKey.generate()

    def get_pubkey(self):
        return self.PrivKey.public_key().public_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PublicFormat.Raw)

    def get_privkey(self, encoding=serialization.Encoding.Raw,
                    format=serialization.PrivateFormat.Raw):
        return self.PrivKey.private_bytes(
            encoding=encoding, format=format,
            encryption_algorithm=serialization.NoEncryption())

    def generate_sharedkey(self, ext_pubkey):
        return self.PrivKey.exchange(X25519PublicKey.from_public_bytes(ext_pubkey))


class ECDH_SECP256R1:
    """NIST P-256 ECDH (ECIES Profile B)."""

    def __init__(self, loc_privkey=None):
        if loc_privkey:
            self.PrivKey = ec.derive_private_key(
                int_from_bytes(loc_privkey), ec.SECP256R1())
        else:
            self.generate_keypair()

    def generate_keypair(self):
        self.PrivKey = ec.generate_private_key(curve=ec.SECP256R1())

    def get_pubkey(self):
        return self.PrivKey.public_key().public_bytes(
            format=serialization.PublicFormat.CompressedPoint,
            encoding=serialization.Encoding.X962)

    def get_privkey(self):
        return bytes_from_int(self.PrivKey.private_numbers().private_value, 32)

    def generate_sharedkey(self, ext_pubkey):
        ext = ec.EllipticCurvePublicKey.from_encoded_point(
            curve=ec.SECP256R1(), data=ext_pubkey)
        return self.PrivKey.exchange(ec.ECDH(), ext)


def KDF(sharedinfo, sharedkey):
    """ANSI X9.63 KDF — derive 64 bytes (AES key + IV + HMAC key) for ECIES."""
    return X963KDF(
        algorithm=hashes.SHA256(),
        length=64,
        sharedinfo=sharedinfo,
    ).derive(sharedkey)
