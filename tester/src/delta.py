# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Delta UEs — provision off-baseline UEs at test start; cleanup at end.

Some tests need IMSIs that are NOT in db/seed/baseline.yaml's roster:
NPN admission tests need a specific UE that's deny-listed; N26
inter-system tests need a UE the AMF will not find; ranging/PIN tests
need sidelink pairs that don't exist in the eMBB / mIoT / URLLC
buckets. Putting those into baseline.yaml would pollute the canonical
manifest with test-only data.

Instead each such test uses delta:

    from src import delta

    with delta.ue("001019999900201") as imsi:
        # core now knows this IMSI (cloned from embb-bulk[0]'s profile);
        # registration, PDU sessions, traffic all work normally
        ue.run_test_flow(imsi)
    # cleanup runs whether the test passed, failed, or raised

Implementation: POST /api/ue/clone on sa_core deep-copies a template
UE's auth (K/OPc), subscription (AMBR), subscribed-NSSAI, slice-DNN
authorisations, and service bindings under the new IMSI. Reset-to-
baseline wipes the new IMSI on the next test anyway (under
pretest_mode=baseline/full), so explicit DELETE on context exit is
defensive — required only under pretest_mode=delta where reset is
skipped between tests.

By default the template is `embb-bulk[0]` (slice eMBB, DNNs internet
+ ims). Override via `template_imsi=` or `bucket=`+`template_idx=`
when the off-roster UE needs different slice/DNN authorisations,
e.g.:

    with delta.ue("001019999900201",
                  bucket="urllc-pool", template_idx=0) as imsi:
        ...  # imsi is URLLC-authorised, like the urllc-pool template

The provisioning is best-effort: a network round-trip can fail under
load. The context manager raises DeltaError on add failure so the
test reports ERROR (not FAIL) with a clear root cause.
"""

from __future__ import annotations

import logging
from contextlib import contextmanager

from src import baseline
from src.core.api import core_api as _core_api

log = logging.getLogger("tester.delta")


class DeltaError(Exception):
    """Raised when a delta UE add/remove round-trip fails."""


def _msisdn_from_imsi(imsi: str) -> str:
    """Strip MCC(3)+MNC(2) → 10-digit subscriber number, matching the
    seeder convention (baseline MSISDNs are imsi[5:])."""
    if len(imsi) < 5:
        raise DeltaError(f"IMSI {imsi!r} too short to derive MSISDN")
    return imsi[5:]


def add(
    new_imsi: str,
    *,
    template_imsi: str | None = None,
    bucket: str = "embb-bulk",
    template_idx: int = 0,
    new_msisdn: str | None = None,
) -> str:
    """Clone a baseline UE under `new_imsi`. Returns the new IMSI.

    Raises DeltaError if the IMSI is already in the baseline roster
    (caller should be intentional about delta vs baseline IMSIs), or
    if the core POST /api/ue/clone round-trip fails.
    """
    if baseline.is_baseline_imsi(new_imsi):
        raise DeltaError(
            f"{new_imsi!r} is already in the baseline roster "
            f"(bucket={baseline.bucket_of(new_imsi).name}); "
            "use baseline.imsi(...) directly, not delta.add()"
        )
    if template_imsi is None:
        template_imsi = baseline.imsi(bucket, template_idx)
    if new_msisdn is None:
        new_msisdn = _msisdn_from_imsi(new_imsi)

    log.info("delta: cloning %s -> %s (msisdn=%s)",
             template_imsi, new_imsi, new_msisdn)
    resp = _core_api("/api/ue/clone", "POST", {
        "source_imsi": template_imsi,
        "new_imsi":    new_imsi,
        "new_msisdn":  new_msisdn,
    })
    if not resp or not resp.get("ok"):
        raise DeltaError(
            f"clone failed: source={template_imsi} new={new_imsi} resp={resp!r}"
        )
    return new_imsi


def remove(imsi: str) -> bool:
    """Delete a delta UE. Idempotent: returns False if the IMSI wasn't
    present, True if a row was deleted. Never raises — cleanup must
    not mask the test's own exception.

    Refuses to delete baseline IMSIs to avoid corrupting the roster
    for the next test. (Reset-to-baseline would restore them, but a
    pretest_mode=delta run wouldn't.)
    """
    if baseline.is_baseline_imsi(imsi):
        log.warning("delta.remove refused: %r is a baseline IMSI", imsi)
        return False
    try:
        resp = _core_api("/api/ue", "DELETE", {"imsi": imsi})
    except Exception as e:
        log.warning("delta.remove(%s) raised: %s", imsi, e)
        return False
    deleted = bool(resp and resp.get("deleted"))
    log.info("delta: removed %s (deleted=%s)", imsi, deleted)
    return deleted


@contextmanager
def ue(
    new_imsi: str,
    *,
    template_imsi: str | None = None,
    bucket: str = "embb-bulk",
    template_idx: int = 0,
    new_msisdn: str | None = None,
):
    """Provision a delta UE for the duration of the with-block.

    On entry: clones from `template_imsi` (or bucket[idx]) under
    `new_imsi`. On exit: deletes the new IMSI. Cleanup runs even if
    the body raises.

    Yields the new IMSI string so the body can write:

        with delta.ue("001019999900201") as imsi:
            ...
    """
    add(new_imsi,
        template_imsi=template_imsi,
        bucket=bucket,
        template_idx=template_idx,
        new_msisdn=new_msisdn)
    try:
        yield new_imsi
    finally:
        remove(new_imsi)


@contextmanager
def ues(*imsis_and_kwargs):
    """Provision multiple delta UEs. Each positional arg is either a
    bare IMSI string or a (imsi, kwargs_dict) tuple.

        with delta.ues("001019999900201", "001019999900202") as imsis:
            ...
        with delta.ues(
            ("001019999900201", {"bucket": "urllc-pool"}),
            ("001019999900202", {"template_imsi": "001011234560050"}),
        ) as (a, b):
            ...

    All adds run before the body; if any fails, already-added UEs
    are removed before the exception propagates. Cleanup on normal
    exit walks them in reverse-add order (LIFO).
    """
    added: list[str] = []
    out: list[str] = []
    try:
        for spec in imsis_and_kwargs:
            if isinstance(spec, tuple):
                imsi, kw = spec
            else:
                imsi, kw = spec, {}
            add(imsi, **kw)
            added.append(imsi)
            out.append(imsi)
        yield tuple(out)
    except Exception:
        # Surface the original error, but still try to clean up
        # whatever we did add. LIFO removal so dependencies (if any)
        # are torn down in the right order.
        for imsi in reversed(added):
            remove(imsi)
        raise
    else:
        for imsi in reversed(added):
            remove(imsi)
