# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""eSIM primitives — tester-side mirror.

Mirrors the Go core's services/esim packages (esim, profile, smdp).
Pure functions + dataclasses — no I/O, no live core state — suitable
for round-trip fixtures alongside the SM-DP+ ES9+ behaviour tests.

Spec anchors that ARE §-checked by speccheck (PDFs under specs/common/):

  * TS 23.003 §2.2       IMSI structure.
  * TS 31.102 §4.2       USIM ADF EF contents (umbrella).
  * TS 31.102 §4.2.2     EF_IMSI on the USIM ADF.
  * TS 31.102 §4.2.18    EF_AD (Administrative Data) — encodes MNC
                         length in byte 4.
  * TS 33.501 §6.1.3     5G AKA — the K / OPc carried in the
                         profile feed this procedure.

Non-3GPP references (NOT §-checked by speccheck — GSMA/ITU-T are
not in the DOC_MAP regex):

  * GSMA SGP.22 §4.1     Activation Code format
                         ("LPA:1$<smdp>$<matchingID>").
  * GSMA SGP.22 §2.5.3   BPP envelope (IV ‖ ciphertext ‖ MAC).
  * GSMA SGP.22 §3.1.2   ES9+ Initiate Authentication.
  * GSMA SGP.22 §3.1.3   ES9+ Authenticate Client.
  * GSMA SGP.22 §3.3.x   ES9+ Get Bound Profile Package.
  * GSMA SGP.22 §3.5     ES9+ Handle Notification.
  * ITU-T E.118          ICCID structure + Luhn checksum.

Profile state vocabulary (mirrors GSMA SGP.22 lifecycle):
  available → reserved → downloaded → installed → enabled / disabled / deleted.

Deferred / TODO:

  * GSMA SGP.22 §5.7  — full ASN.1 BPP wire codec (today: structural
                        envelope only).
  * GSMA SGP.22 §3.4  — Cancel Session / error path.
  * GSMA SGP.22 §6    — ECKA(ECDSA) eUICC PKI signature path.
  * GSMA SGP.32       — IoT eSIM (eIM, IPA).
  * TS 31.102 §5.2.1  — Milenage parameter blob layout (this module
                        carries K + OPc as hex; SQN delta + AMF
                        aren't part of the profile blob yet).
"""

from __future__ import annotations

import hashlib
import hmac
import os
from dataclasses import dataclass, field
from typing import Optional


# ─── Profile state machine (GSMA SGP.22 lifecycle) ───────────────


PROFILE_STATES = frozenset({
    "available", "reserved", "downloaded",
    "installed", "enabled", "disabled", "deleted",
})

PROFILE_TYPES = frozenset({"test", "operational", "provisioning"})

# GSMA SGP.22 §3.5 notification event types (mirrors the
# esim_notifications.event_type CHECK in the DB schema).
NOTIFICATION_EVENT_TYPES = frozenset({
    "install", "enable", "disable", "delete", "download",
})


# ─── ICCID — ITU-T E.118 (informative; not §-checked) ────────────


def luhn_checksum(digits: str) -> int:
    """ITU-T E.118 Luhn check digit for the given numeric body.

    ITU-T E.118 specifies decimal Luhn modulo-10; this helper
    matches the reference algorithm bit-for-bit so a Go-side and
    Python-side ICCID round-trip identically.
    """
    if not digits or not digits.isdigit():
        raise ValueError("digits must be non-empty numeric string")
    total = 0
    for i, ch in enumerate(reversed(digits)):
        n = int(ch)
        if i % 2 == 0:
            n *= 2
            if n > 9:
                n -= 9
        total += n
    return (10 - (total % 10)) % 10


def validate_iccid(iccid: str) -> bool:
    """ITU-T E.118 — industry identifier '89' + Luhn check digit.
    Matches the Go core's ValidateICCID predicate exactly."""
    if not iccid or len(iccid) < 18 or len(iccid) > 20:
        return False
    if not iccid.isdigit():
        return False
    if iccid[:2] != "89":
        return False
    return luhn_checksum(iccid[:-1]) == int(iccid[-1])


def make_iccid(issuer_id: str, sequence: int) -> str:
    """Build a 19-digit ICCID body and append the Luhn check digit.

    ITU-T E.118 industry identifier "89" + 4-digit issuer + 12-digit
    sequence + Luhn check.
    """
    if len(issuer_id) != 4 or not issuer_id.isdigit():
        raise ValueError("issuer_id must be 4 digits")
    body = f"89{issuer_id}{sequence:012d}"
    return f"{body}{luhn_checksum(body)}"


# ─── Activation Code (GSMA SGP.22 §4.1, informative) ─────────────


def generate_activation_code(smdp_address: str, matching_id: str) -> str:
    """GSMA SGP.22 §4.1 Activation Code: LPA:1$<smdp>$<matchingID>."""
    if not smdp_address:
        raise ValueError("smdp_address is required")
    if not matching_id:
        raise ValueError("matching_id is required")
    return f"LPA:1${smdp_address}${matching_id}"


def parse_activation_code(ac: str) -> Optional[dict]:
    """Parse a GSMA SGP.22 §4.1 Activation Code. Returns None on
    malformed input (no exception, mirroring Go's ParseActivationCode)."""
    if not ac or not ac.startswith("LPA:1$"):
        return None
    parts = ac[len("LPA:1$"):].split("$")
    if len(parts) < 2:
        return None
    return {"smdp_address": parts[0], "matching_id": parts[1]}


def generate_matching_id() -> str:
    """16-byte random hex (32 hex chars) — informative; SGP.22 §4.1
    requires uniqueness only."""
    return os.urandom(16).hex()


# ─── BPP Envelope (GSMA SGP.22 §2.5.3, informative) ──────────────


@dataclass
class SessionKeys:
    enc_key: bytes
    mac_key: bytes
    dek: bytes


def generate_session_keys() -> SessionKeys:
    """Three independent 16-byte AES-128 keys."""
    return SessionKeys(
        enc_key=os.urandom(16),
        mac_key=os.urandom(16),
        dek=os.urandom(16),
    )


def _pkcs7_pad(data: bytes, block: int = 16) -> bytes:
    pad = block - (len(data) % block)
    return data + bytes([pad] * pad)


def _pkcs7_unpad(data: bytes, block: int = 16) -> bytes:
    if not data:
        return data
    pad = data[-1]
    if pad < 1 or pad > block:
        return data
    return data[:-pad]


def encrypt_profile(plain: bytes, keys: SessionKeys) -> dict:
    """AES-128-CBC + HMAC-SHA-256 (encrypt-then-MAC).

    Spec context: GSMA SGP.22 §2.5.3 BPP encryption envelope.
    Wire-format here is the structural (IV ‖ ciphertext ‖ MAC)
    triple as a hex-encoded dict — the spec mandates ASN.1
    encoding (TODO at module level).
    """
    # Use cryptography lib via stdlib hashlib for HMAC; AES via
    # ctypes-free pure-Python is not available in stdlib, so reach
    # for the cryptography backend if present, else error.
    try:
        from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
    except ImportError as e:
        raise RuntimeError("cryptography package required for BPP envelope") from e

    iv = os.urandom(16)
    padded = _pkcs7_pad(plain)
    encryptor = Cipher(algorithms.AES(keys.enc_key), modes.CBC(iv)).encryptor()
    ct = encryptor.update(padded) + encryptor.finalize()
    mac = hmac.new(keys.mac_key, iv + ct, hashlib.sha256).digest()
    return {"iv": iv.hex(), "ciphertext": ct.hex(), "mac": mac.hex()}


def decrypt_profile(envelope: dict, keys: SessionKeys) -> Optional[bytes]:
    """Reverse of encrypt_profile. Returns None on MAC mismatch."""
    try:
        from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
    except ImportError as e:
        raise RuntimeError("cryptography package required for BPP envelope") from e

    iv = bytes.fromhex(envelope["iv"])
    ct = bytes.fromhex(envelope["ciphertext"])
    expected = bytes.fromhex(envelope["mac"])
    actual = hmac.new(keys.mac_key, iv + ct, hashlib.sha256).digest()
    if not hmac.compare_digest(actual, expected):
        return None
    decryptor = Cipher(algorithms.AES(keys.enc_key), modes.CBC(iv)).decryptor()
    padded = decryptor.update(ct) + decryptor.finalize()
    return _pkcs7_unpad(padded)


# ─── USIM Profile Builder (TS 31.102 §4.2) ───────────────────────


def build_usim_profile(imsi: str, k_hex: str, opc_hex: str,
                       iccid: str, mcc: str, mnc: str,
                       op_type: str = "milenage") -> dict:
    """Build the on-card USIM profile shape. EF_AD encoding per
    TS 31.102 §4.2.18 (byte 4 = MNC length)."""
    mnc_len = 3 if len(mnc) == 3 else 2
    return {
        "version": "2.3.1",
        "iccid": iccid,
        "imsi": imsi,
        "mcc": mcc,
        "mnc": mnc,
        "op_type": op_type,
        "k": k_hex,
        "opc": opc_hex,
        # TS 31.102 §4.2.18 — three reserved bytes then MNC length.
        "ef_ad": f"000000{mnc_len:02x}",
        "algorithm": "milenage",
        "access_rules": {
            "rat_list": ["e-utran", "nr"],
            "plmn_list": [{"mcc": mcc, "mnc": mnc}],
        },
    }


# ─── Profile + eUICC + notification persistence shapes ───────────


@dataclass
class Profile:
    """eSIM profile row — mirrors esim_profiles. State vocabulary
    matches GSMA SGP.22 lifecycle."""

    iccid: str
    imsi: str
    profile_state: str = "available"
    eid: Optional[str] = None
    activation_code: Optional[str] = None
    matching_id: Optional[str] = None
    smdp_address: Optional[str] = None
    profile_name: str = "SA Core"
    profile_type: str = "operational"
    profile_class: str = "operational"


def new_profile(iccid: str, imsi: str, *,
                profile_name: str = "",
                profile_type: str = "",
                profile_class: str = "",
                activation_code: Optional[str] = None,
                matching_id: Optional[str] = None,
                smdp_address: Optional[str] = None) -> Profile:
    if not validate_iccid(iccid):
        raise ValueError(f"invalid iccid: {iccid}")
    if not imsi:
        raise ValueError("imsi is required")
    if not profile_name:
        profile_name = "SA Core"
    if not profile_type:
        profile_type = "operational"
    if profile_type not in PROFILE_TYPES:
        raise ValueError(f"profile_type must be one of {PROFILE_TYPES}")
    if not profile_class:
        profile_class = "operational"
    return Profile(
        iccid=iccid, imsi=imsi, profile_name=profile_name,
        profile_type=profile_type, profile_class=profile_class,
        activation_code=activation_code, matching_id=matching_id,
        smdp_address=smdp_address,
    )


def transition_state(p: Profile, new_state: str) -> Profile:
    """Move a profile to a new SGP.22 lifecycle state. Rejects
    unknown states; otherwise applies in-place."""
    if new_state not in PROFILE_STATES:
        raise ValueError(f"invalid profile_state: {new_state!r}")
    p.profile_state = new_state
    return p


@dataclass
class EUICC:
    eid: str
    device_info: Optional[str] = None
    lpa_version: Optional[str] = None


@dataclass
class Notification:
    iccid: str
    event_type: str
    eid: Optional[str] = None
    seq_number: int = 0
    result_code: int = 0


def new_notification(iccid: str, event_type: str, *,
                     eid: Optional[str] = None,
                     seq_number: int = 0,
                     result_code: int = 0) -> Notification:
    if event_type not in NOTIFICATION_EVENT_TYPES:
        raise ValueError(f"event_type must be one of {NOTIFICATION_EVENT_TYPES}")
    return Notification(iccid=iccid, event_type=event_type,
                        eid=eid, seq_number=seq_number, result_code=result_code)
