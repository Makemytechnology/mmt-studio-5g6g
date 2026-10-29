# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Supplementary Services codec — tester-side mirror.

Mirrors the Go core's services/supplementary/{mmi,codec}.go with
verbatim §-clause citations into the locally-pinned 3GPP PDFs:

  * TS 22.030 §6.5     — UE-side MMI procedures and Annex B
                         Service Codes (Activation, Registration,
                         Deactivation, Interrogation, Erasure forms).
  * TS 24.080 §3.6     — Facility IE component framing
                         (Invoke / ReturnResult / ReturnError / Reject).
  * TS 24.080 §4.5     — SS-Operation set (imports MAP-SS / SS-Ops
                         from TS 29.002 §17.6.4 "Supplementary
                         service operations").
  * TS 22.030 Annex B Table B.1 — Service Code → name mapping.
  * TS 22.030 Annex C  — Basic Service Group SIB encoding (validation
                         is TODO; values pass through here).

The on-air XCAP / SIP signalling that *invokes* these procedures
lives in the per-service spec — TS 24.604 (CDIV), TS 24.611 (ACR/CB),
TS 24.615 (CW), TS 24.607 (OIP/OIR), TS 24.608 (TIP/TIR),
TS 24.610 (HOLD), TS 24.629 (ECT). This module is the layer-3 codec
shared with CS fall-back tests; the IMS-anchored 5GS path is driven
by HTTP via the supplementary REST API and does not exercise the
Facility-IE encoder for the happy path.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import IntEnum
from typing import Optional


# ─── TS 22.030 §6.5.2 — five MMI procedure forms ────────────────
class Procedure(IntEnum):
    """One of the five UE-side MMI procedures from TS 22.030 §6.5.2."""

    UNKNOWN = 0
    ACTIVATION = 1       # *SC*SI#
    DEACTIVATION = 2     # #SC*SI#
    INTERROGATION = 3    # *#SC*SI#
    REGISTRATION = 4     # *SC*SI# (with DN) / **SC*SI#
    ERASURE = 5          # ##SC*SI#

    def __str__(self) -> str:
        return self.name.lower()


# ─── TS 22.030 Annex B Table B.1 — Service Code → service name ──
# Verbatim from Table B.1 (Release 19). Adding entries requires
# citing the row in the table, not a recollection.
SS_CODE_NAME: dict[str, str] = {
    # — Originating identification — TS 22.081 / TS 24.607
    "30": "CLIP",   # §22.081
    "31": "CLIR",   # §22.081
    "76": "COLP",   # §22.081
    "77": "COLR",   # §22.081
    # — Call forwarding — TS 22.082 / TS 24.604
    "21":  "CFU",                # §22.082
    "67":  "CFB",                # §22.082
    "61":  "CFNRy",              # §22.082 (CF No Reply)
    "62":  "CFNRc",              # §22.082 (CF Not Reachable)
    "002": "CFAll",
    "004": "CFAllConditional",
    # — Call waiting — TS 22.083 / TS 24.615
    "43": "CW",                  # §22.083
    # — Call barring — TS 22.088 / TS 24.611
    "33":  "BAOC",
    "331": "BAOIC",
    "332": "BAOICexHC",
    "35":  "BAIC",
    "351": "BAICRoaming",
    "330": "BAAll",
    "333": "BAAllOutgoing",
    "353": "BAAllIncoming",
    # — ECT — TS 22.091 / TS 24.629
    "96": "ECT",
    # — CCBS — TS 22.093
    "37": "CCBS",
    # — CNAP — TS 22.096
    "300": "CNAP",
}

# Service-name groupings for SIA-slot interpretation per TS 22.030
# Annex B Table B.1 column conventions (DN, PW columns).
_CALL_FORWARDING = frozenset({
    "CFU", "CFB", "CFNRy", "CFNRc",
    "CFAll", "CFAllConditional",
})
_BARRING = frozenset({
    "BAOC", "BAOIC", "BAIC",
    "BAOICexHC", "BAICRoaming",
    "BAAll", "BAAllOutgoing", "BAAllIncoming",
})


@dataclass
class MMIRequest:
    """Parsed UE-side MMI procedure invocation per TS 22.030 §6.5.2."""

    procedure: Procedure
    service_code: str
    service_name: str
    sia: str = ""
    sib: str = ""
    sic: str = ""
    raw: str = ""

    def no_reply_timer(self) -> Optional[int]:
        """Return SIC parsed as the No Reply Condition Timer per
        TS 22.030 §6.5 (footnote in Annex B Table B.1: 5..30 s).
        Returns None if SIC is empty or out-of-range; callers should
        fall back to the spec default (20 s) from TS 24.604 §4.5.1."""
        if not self.sic:
            return None
        try:
            t = int(self.sic)
        except ValueError:
            return None
        if not 5 <= t <= 30:
            return None
        return t

    def barring_password(self) -> Optional[str]:
        """SIA if the procedure targets a barring service (TS 22.030
        Annex B "PW" column for §22.088 rows)."""
        if self.service_name not in _BARRING:
            return None
        return self.sia or None

    def forwarded_number(self) -> Optional[str]:
        """SIA if the procedure targets a call-forwarding service
        (TS 22.030 Annex B "DN" column for §22.082 rows)."""
        if self.service_name not in _CALL_FORWARDING:
            return None
        return self.sia or None


def parse_mmi(s: str) -> MMIRequest:
    """Parse a UE-entered MMI string per TS 22.030 §6.5.2.

    Raises ValueError for syntactic problems and for service codes not
    in TS 22.030 Annex B Table B.1 (per §6.5.2 "spare codes shall be
    reserved for future use" — surfaces as TS 24.080 §4.3 "operation
    not provided" upstream).
    """
    if not s:
        raise ValueError("empty MMI string")
    raw = s
    # §6.5.2: every procedure ends with '#'.
    if not s.endswith("#"):
        raise ValueError("MMI: missing terminating '#'")
    s = s[:-1]

    if s.startswith("*#"):
        proc = Procedure.INTERROGATION
        s = s[2:]
    elif s.startswith("##"):
        proc = Procedure.ERASURE
        s = s[2:]
    elif s.startswith("**"):
        proc = Procedure.REGISTRATION
        s = s[2:]
    elif s.startswith("*"):
        # §6.5.2 disambiguation: bare *SC# = Activation; *SC*<DN># for
        # CF-style services = Registration. Promotion happens after
        # SIA is parsed.
        proc = Procedure.ACTIVATION
        s = s[1:]
    elif s.startswith("#"):
        proc = Procedure.DEACTIVATION
        s = s[1:]
    else:
        raise ValueError(f"MMI: bad procedure prefix in {raw!r}")

    parts = s.split("*")
    if not parts or parts[0] == "":
        raise ValueError(f"MMI: missing service code in {raw!r}")
    sc = parts[0]
    if not sc.isdigit():
        raise ValueError(f"MMI: non-digit service code {sc!r}")
    # §6.5.2: "Service Code, SC (2 or 3 digits)".
    if not 2 <= len(sc) <= 3:
        raise ValueError(f"MMI: SC length {len(sc)} (want 2 or 3)")

    if sc not in SS_CODE_NAME:
        raise ValueError(
            f"MMI: unknown service code {sc!r} (TS 22.030 Annex B)"
        )

    req = MMIRequest(
        procedure=proc,
        service_code=sc,
        service_name=SS_CODE_NAME[sc],
        raw=raw,
    )
    # §6.5.2 specifies SI may have absent slots represented by an
    # empty position between '*'s — split() preserves these as ''.
    if len(parts) > 1:
        req.sia = parts[1]
    if len(parts) > 2:
        req.sib = parts[2]
    if len(parts) > 3:
        req.sic = parts[3]

    # §6.5.2 disambiguation: "*21*<DN>#" → registration; bare "*21#"
    # stays as activation (network applies last-registered DN).
    if proc == Procedure.ACTIVATION and req.sia and req.service_name in _CALL_FORWARDING:
        req.procedure = Procedure.REGISTRATION

    return req


# ─── TS 24.080 §4.5 — Operation codes (numeric values from TS 29.002
# §17.6.4 "Supplementary service operations"). The op-code column of
# the imported MAP-SupplementaryServiceOperations module is the
# authoritative source; values below are the well-known assignments
# the tester relies on. ─────────────────────────────────────────
class Op(IntEnum):
    REGISTER_SS = 10
    ERASE_SS = 11
    ACTIVATE_SS = 12
    DEACTIVATE_SS = 13
    INTERROGATE_SS = 14
    NOTIFY_SS = 16                       # network → MS, TS 24.080 §4.5
    REGISTER_PASSWORD = 17
    GET_PASSWORD = 18
    PROCESS_USSD_DATA = 19               # legacy USSD (deprecated)
    FORWARD_CHECK_SS_INDICATION = 38
    PROCESS_USSD_REQ = 59                # network-initiated USSD (TS 22.090)
    USSD_REQUEST = 60                    # user-initiated USSD (TS 22.090)
    USSD_NOTIFY = 61
    BUILD_MPTY = 30                      # TS 22.084 / §4.5
    HOLD_MPTY = 31
    RETRIEVE_MPTY = 32
    SPLIT_MPTY = 33
    EXPLICIT_CT = 53                     # TS 22.091 / TS 24.629
    CALL_DEFLECTION = 117


# ─── TS 24.080 §3.6.2 Table 3.7 — Facility component tags ───────
COMPONENT_INVOKE = 0xA1         # Invoke
COMPONENT_RETURN_RESULT = 0xA2  # ReturnResult
COMPONENT_RETURN_ERROR = 0xA3   # ReturnError
COMPONENT_REJECT = 0xA4         # Reject

# IEI for the Facility IE when carried in a TS 24.008 layer-3
# message (e.g. FACILITY / REGISTER). See TS 24.008 §10.5.4.15.
IEI_FACILITY = 0x1C


def encode_invoke_frame(invoke_id: int, op: Op, params: bytes = b"") -> bytes:
    """Build a minimal Invoke component for an SS-Operation per
    TS 24.080 §3.6.2 Table 3.7. Layout:

        0xA1                     -- Invoke tag (§3.6.2)
        len_invoke               -- short or long form per X.690
        0x02 0x01 invoke_id      -- Invoke ID  (§3.6.3, INTEGER)
        0x02 0x01 op_code        -- Operation Code (§3.6.4, INTEGER)
        [parameter SEQUENCE]     -- §3.6.5, omitted here

    Multi-byte invoke IDs / op codes (negative INTEGERs in particular)
    are not yet handled — the Op values we exercise today fit in a
    single byte.
    """
    if not 0 <= invoke_id <= 0xFF:
        raise ValueError("invoke_id must fit in one byte")
    if not 0 <= int(op) <= 0xFF:
        raise ValueError("op code must fit in one byte")
    body = bytes([
        0x02, 0x01, invoke_id,    # §3.6.3
        0x02, 0x01, int(op),      # §3.6.4
    ]) + params
    if len(body) > 0x7F:
        # X.690 long-form length not exercised yet — multi-octet body
        # lengths use 0x81/0x82 prefix.
        raise ValueError("body > 127 bytes; long-form length not implemented")
    return bytes([COMPONENT_INVOKE, len(body)]) + body


# TODO(spec: TS 24.080 §3.6.1): full Component decoder. We encode
# Invoke frames above but don't yet parse incoming
# ReturnResult / ReturnError / Reject — needed for the negative path
# of network-initiated SS operations (forwardCheckSS, etc.).
#
# TODO(spec: TS 24.080 §3.6.5): ASN.1 Sequence/Set parameter encoding.
# Each operation in §4.5 brings its own ARGUMENT type (TS 29.002
# §17.6.4) — RegisterSS-Arg carries SS-Code + DN + basicService etc.
# Not synthesised here; the tester drives the IMS REST API instead.
#
# TODO(spec: TS 24.080 §4.3): error responses (systemFailure(34),
# illegalSS-Operation(16), SS-Incompatibility(20),
# unknownAlphabet(71)) need a typed Error struct + encode_error frame
# builder for the negative path.
#
# TODO(spec: TS 24.008 §10.5.4.15): Facility IE container that wraps
# the Invoke/ReturnResult components in a CS layer-3 FACILITY /
# REGISTER message. Not needed for IMS-anchored flows.
#
# TODO(spec: TS 24.090 / TS 24.080 §2.5): unstructured SS data (USSD)
# request/response framing — processUnstructuredSS-Request (op 59) /
# unstructuredSS-Request (op 60). USSD over IMS (TS 24.390) flows via
# SIP MESSAGE today; the legacy facility-encoded form is not built.
#
# TODO(spec: TS 22.030 §6.5.4): Registration of new password
# (`**03*ZZ*OLD*NEW*NEW#`) is not yet parsed by parse_mmi.
#
# TODO(spec: TS 22.030 §6.5.5): legacy in-call control procedures
# (HOLD / MPTY / ECT via single-digit shortcodes) are SIP-layer
# events not parsed here.
#
# TODO(spec: TS 22.030 §6.5.6): Roaming-state evaluation that gates
# `*351*PW#` (BAIC roaming) is a network-side check, not wired here.
#
# TODO(spec: TS 22.030 Annex C): SIB encoding (e.g. "11" = telephony,
# "13" = fax G3) currently passes through as a free-form string.
