# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Licensed under the Apache License, Version 2.0
#
# sa_crypto — 3GPP cryptographic toolkit for SA Core
#
# Clean-room implementation of 3GPP authentication, key derivation,
# and ECIES algorithms using the permissive-licensed `cryptography` library.
# Replaces CryptoMobile (GPLv2) for Phase 1 functions.
#
# Specifications implemented:
#   TS 35.205/206/207  — Milenage (AES-based 3G/4G/5G authentication)
#   TS 33.501 Annex A  — 5G key derivation functions (A2–A23)
#   TS 33.501 §6.12    — ECIES for SUCI concealment (Profile A/B)
#   TS 33.501 §5.3     — NAS ciphering NEA2 and integrity NIA2 (AES-based)
