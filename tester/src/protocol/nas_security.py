# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""NAS security — wrap/unwrap, MAC, cipher. Pure functions.

TS 24.501 §4.4 — NAS security.
TS 33.501 §6.4 — NAS integrity and ciphering.
"""

from pycrate_mobile.TS24501_FGMM import FGMMSecProtNASMessage
from pycrate_mobile.NAS5G import parse_NAS5G


def wrap_nas_security(plain_nas, knasenc, knasint, ciph_algo, integ_algo,
                      count, direction=0, bearer=1, sec_hdr=4):
    """Wrap plain NAS PDU in security-protected envelope.

    Args:
        plain_nas: Plain NAS PDU bytes.
        knasenc, knasint: 16-byte NAS keys.
        ciph_algo, integ_algo: Algorithm IDs (0-3).
        count: Current NAS COUNT.
        direction: 0=UL, 1=DL.
        bearer: 1 for NAS signalling.
        sec_hdr: Security header type (2=int+ciph, 4=int+ciph new ctx).

    Returns: (secured_bytes, new_count)
    """
    seqn = count & 0xFF
    seqnoff = count & 0xFFFFFF00

    sec_msg = FGMMSecProtNASMessage()
    sec_msg['5GMMHeaderSec']['EPD'].set_val(126)
    sec_msg['5GMMHeaderSec']['spare'].set_val(0)
    sec_msg['5GMMHeaderSec']['SecHdr'].set_val(sec_hdr)
    sec_msg['Seqn'].set_val(seqn)
    sec_msg['NASMessage'].set_val(plain_nas)

    if knasenc and ciph_algo > 0:
        sec_msg.encrypt(key=knasenc, dir=direction, fgea=ciph_algo,
                        seqnoff=seqnoff, bearer=bearer)

    if knasint and integ_algo > 0:
        sec_msg.mac_compute(key=knasint, dir=direction, fgia=integ_algo,
                            seqnoff=seqnoff, bearer=bearer)

    return sec_msg.to_bytes(), (count + 1) & 0xFFFFFFFF


def unwrap_nas_security(msg, knasenc, knasint, ciph_algo, integ_algo,
                        dl_count, direction=1, bearer=1):
    """Unwrap security-protected NAS message.

    Args:
        msg: Parsed FGMMSecProtNASMessage object.
        knasenc, knasint: 16-byte NAS keys (None if not yet derived).
        ciph_algo, integ_algo: Algorithm IDs.
        dl_count: Current DL NAS COUNT.
        direction: 1=DL (from UE perspective).

    Returns: (inner_msg, new_dl_count, mac_ok)
    """
    sec_hdr = msg['5GMMHeaderSec']['SecHdr'].get_val()
    seqn = msg['Seqn'].get_val()
    seqnoff = dl_count & 0xFFFFFF00

    # Reconcile SQN with overflow counter
    seqn_expected = dl_count & 0xFF
    if seqn != seqn_expected:
        if seqn > seqn_expected:
            dl_count = seqnoff | seqn
        else:
            dl_count = (seqnoff + 0x100) | seqn
        seqnoff = dl_count & 0xFFFFFF00

    # Verify MAC
    mac_ok = True
    if knasint and integ_algo > 0:
        mac_ok = msg.mac_verify(key=knasint, dir=direction, fgia=integ_algo,
                                seqnoff=seqnoff, bearer=bearer)

    # Decrypt
    if sec_hdr in (2, 4) and knasenc and ciph_algo > 0:
        msg.decrypt(key=knasenc, dir=direction, fgea=ciph_algo,
                    seqnoff=seqnoff, bearer=bearer)

    new_count = (dl_count + 1) & 0xFFFFFFFF

    # Extract inner NAS
    inner = msg[-1]
    if hasattr(inner, '_name') and not isinstance(inner, bytes):
        try:
            _ = inner["5GMMHeader"]["Type"].get_val()
            return inner, new_count, mac_ok
        except Exception:
            pass

    inner_bytes = inner.to_bytes()
    inner_msg, _ = parse_NAS5G(inner_bytes, inner=True, sec_hdr=False)
    return inner_msg, new_count, mac_ok
