# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Licensed under the Apache License, Version 2.0
#
# sa_crypto/ecies.py — ECIES for SUCI concealment / de-concealment
#
# Clean-room implementation per TS 33.501, §6.12 and Annex C.
#
# Profile A: Curve25519 / X25519
# Profile B: NIST secp256r1
# KDF: ANSI X9.63 with SHA-256
# Encryption: AES-128-CTR
# MAC: HMAC-SHA-256 (truncated to 8 bytes)

__all__ = ['ECIES_UE', 'ECIES_HN']

import hashlib
import hmac
from struct import unpack

from .aes import AES_CTR
from .ec import X25519, ECDH_SECP256R1, KDF
from .utils import CMException


class ECIES_UE:
    """UE-side ECIES: conceal SUPI into SUCI."""

    def __init__(self, profile='A'):
        if profile == 'A':
            self.EC = X25519()
        elif profile == 'B':
            self.EC = ECDH_SECP256R1()
        else:
            raise CMException('unknown ECIES profile %s' % profile)

    def generate_sharedkey(self, hn_pub_key, fresh=True):
        """Generate shared keystream from UE ephemeral key + HN public key."""
        if fresh:
            self.EC.generate_keypair()
        self.EK = self.EC.get_pubkey()
        self.SK = KDF(self.EK, self.EC.generate_sharedkey(hn_pub_key))

    def protect(self, plaintext):
        """Encrypt plaintext MSIN. Returns (ue_pubkey, ciphertext, mac_tag)."""
        aes_key = self.SK[:16]
        aes_nonce = self.SK[16:24]
        aes_cnt = unpack('>Q', self.SK[24:32])[0]
        mac_key = self.SK[32:64]

        ciphertext = AES_CTR(aes_key, aes_nonce, aes_cnt).encrypt(plaintext)
        mac = hmac.new(mac_key, ciphertext, hashlib.sha256).digest()
        return self.EK, ciphertext, mac[:8]


class ECIES_HN:
    """Home-network-side ECIES: de-conceal SUCI to recover SUPI."""

    def __init__(self, hn_priv_key, profile='A'):
        if profile == 'A':
            self.EC = X25519(loc_privkey=hn_priv_key)
        elif profile == 'B':
            self.EC = ECDH_SECP256R1(loc_privkey=hn_priv_key)
        else:
            raise CMException('unknown ECIES profile %s' % profile)

    def unprotect(self, ue_pubkey, ciphertext, mac):
        """Decrypt SUCI. Returns cleartext bytes or None if MAC fails."""
        SK = KDF(ue_pubkey, self.EC.generate_sharedkey(ue_pubkey))
        aes_key = SK[:16]
        aes_nonce = SK[16:24]
        aes_cnt = unpack('>Q', SK[24:32])[0]
        mac_key = SK[32:64]

        mac_hn = hmac.new(mac_key, ciphertext, hashlib.sha256).digest()
        if not hmac.compare_digest(mac_hn[:8], mac):
            return None
        return AES_CTR(aes_key, aes_nonce, aes_cnt).decrypt(ciphertext)
