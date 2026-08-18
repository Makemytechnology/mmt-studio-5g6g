# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Core Provisioner — manage SA Core subscribers via REST API.

Provisions UE credentials, subscriptions, slices, DNN bindings, and
SUCI keys on the sa_core via its webservice endpoints.

All test cases can run without manual core-side configuration.
"""

import json
import logging

from src.core.api import core_api as _core_api

log = logging.getLogger("tester.provisioner")


# ── Subscriber Provisioning ──

def provision_ue_auth(imsi, k, opc, op_type="OPC", amf="8000", sqn=0,
                      msisdn=None, suci_profile=None, hn_private_key=None):
    """Provision UE auth credentials on core (POST /ue/auth).

    Args:
        suci_profile: "A" or "B" for ECIES, None for null scheme
        hn_private_key: 64 hex chars (32 bytes) — HN private key for SUCI decryption
    """
    body = {
        "imsi": imsi,
        "k": k,
        "op": opc,
        "op_type": op_type,
        "amf": amf,
        "sqn": sqn,
    }
    if msisdn:
        body["msisdn"] = msisdn
    if suci_profile:
        body["suci_profile"] = suci_profile
    if hn_private_key:
        body["hn_private_key"] = hn_private_key

    result = _core_api("/api/ue/auth", "POST", body)
    if result and result.get("ok"):
        log.info("Provisioned UE auth: %s (suci=%s)", imsi, suci_profile or "null")
    else:
        log.warning("Failed to provision UE auth: %s — %s", imsi, result)
    return result


def provision_subscription(imsi, ambr_dl_kbps=1000000, ambr_ul_kbps=1000000):
    """Set UE subscription AMBR on core (POST /api/ue/subscription)."""
    body = {
        "imsi": imsi,
        "ambr": {"downlink_kbps": ambr_dl_kbps, "uplink_kbps": ambr_ul_kbps},
    }
    result = _core_api("/api/ue/subscription", "POST", body)
    if result and result.get("ok"):
        log.info("Provisioned subscription: %s (AMBR DL=%d UL=%d kbps)",
                 imsi, ambr_dl_kbps, ambr_ul_kbps)
    return result


def provision_subscription_tree(imsi, slices):
    """Provision a UE's subscription tree (NSSAI + DNN bindings + services)
    in one call.

    Replaces the deprecated /ue/subscribed-nssai + /ue/slice-dnn pair. Core
    exposes POST /api/subscriber/{imsi}/subscription which atomically writes
    the entries to ue_subscribed_nssai, ue_slice_dnn, and service_bindings.

    Args:
        imsi: UE IMSI
        slices: list of dicts with keys:
            sst (int)
            sd (str hex, optional)
            dnns (list[str], optional) — list of DNN names this slice allows
    """
    payload_slices = []
    for idx, s in enumerate(slices):
        item = {
            "sst": int(s["sst"]),
            "is_default": idx == 0,
        }
        sd = s.get("sd")
        if sd is not None:
            item["sd"] = f"{int(sd):06x}" if isinstance(sd, int) else str(sd)
        if s.get("dnns"):
            dnn_items = []
            default_dnn = s.get("default_dnn") or s["dnns"][0]
            for d in s["dnns"]:
                services = []
                for svc_name, is_default in DNN_DEFAULT_SERVICES.get(d, []):
                    services.append({
                        "service_name": svc_name,
                        "is_default": bool(is_default),
                    })
                dnn_items.append({
                    "dnn": d,
                    "is_default": d == default_dnn,
                    "services": services,
                })
            item["dnns"] = dnn_items
        payload_slices.append(item)

    result = _core_api(
        f"/api/subscriber/{imsi}/subscription", "POST",
        {"imsi": imsi, "slices": payload_slices},
    )
    if result and result.get("ok"):
        log.info("Provisioned subscription tree: %s (%d slices, %d bindings)",
                 imsi, len(payload_slices), result.get("bindings", 0))
    return result


# Per-DNN default service bindings. Mirrors db/seed/ue.go's `bindings`
# slice in mmt-studio-core-go so a tester-provisioned UE gets the same
# starter QoS profile set the core seed used to plant (the marker for
# this is "default_data" on the internet DNN — UPF metering picks it up
# as 5QI=9 NonGBR).
DNN_DEFAULT_SERVICES = {
    "internet": [("default_data", 1)],
    "ims":      [("ims_signalling", 1), ("conv_voice", 0), ("conv_video", 0)],
    "mcx":      [("mcx_signalling", 1), ("mcptt_voice", 0), ("mcvideo", 0), ("mcdata", 0)],
    "iot":      [("default_data", 1)],
}


# Standardized QoS service catalog (TS 23.501 §5.7.4 + TS 23.280 MCX +
# §5.7.4 URLLC). Mirrors db/seed/services.go::StandardQoSProfiles so a
# tester-pushed DB carries the exact same services table the core seed
# used to plant. Without this, service_bindings rows reference names
# that don't exist in `services` → bindings count = 0 per UE.
#
# Format: (name, fiveqi, resource_type, arp_pri, arp_pcap, arp_pvuln,
#          gbr_ul_kbps, gbr_dl_kbps, mbr_ul_kbps, mbr_dl_kbps, status)
_STANDARD_SERVICES = [
    # GBR (Conversational / Streaming)
    ("conv_voice",        1, "GBR",     1, 1, 0,   64,   64,   128,   128, "INACTIVE (event gated)"),
    ("conv_video",        2, "GBR",     2, 1, 0, 1000, 1000,  4000,  4000, "INACTIVE (event gated)"),
    ("realtime_gaming",   3, "GBR",     3, 0, 1,  500,  500,  2000,  2000, "INACTIVE (event gated)"),
    ("non_conv_video",    4, "GBR",     5, 0, 1,  500,  500,  4000,  4000, "INACTIVE (event gated)"),
    # NonGBR (Interactive / Background)
    ("ims_signalling",    5, "NonGBR",  1, 1, 0, None, None, None, None, "ACTIVE"),
    ("tcp_interactive",   6, "NonGBR",  6, 0, 1, None, None, None, None, "ACTIVE"),
    ("voice_video_gaming",7, "NonGBR",  7, 0, 1, None, None, None, None, "ACTIVE"),
    ("video_streaming",   8, "NonGBR",  8, 0, 1, None, None, None, None, "ACTIVE"),
    ("default_data",      9, "NonGBR",  9, 0, 1, None, None, None, None, "ACTIVE"),
    # MCX
    ("mcptt_voice",      65, "GBR",     1, 1, 0,   24,   24,    64,    64, "INACTIVE (event gated)"),
    ("mcvideo",          67, "GBR",     2, 1, 0,  500,  500,  2000,  2000, "INACTIVE (event gated)"),
    ("mcdata",           69, "NonGBR",  3, 1, 0, None, None, None, None, "INACTIVE (event gated)"),
    ("mcx_signalling",   70, "NonGBR",  1, 1, 0, None, None, None, None, "INACTIVE (event gated)"),
    # URLLC
    ("urllc_discrete_auto",   80, "GBR", 1, 1, 0, 100, 100, 1000, 1000, "INACTIVE (event gated)"),
    ("urllc_discrete_auto_lo",82, "GBR", 1, 1, 0, 100, 100, 1000, 1000, "INACTIVE (event gated)"),
    ("urllc_electricity",     83, "GBR", 2, 1, 0,  50,  50,  500,  500, "INACTIVE (event gated)"),
    ("urllc_process_auto",    84, "GBR", 3, 1, 0, 100, 100, 1000, 1000, "INACTIVE (event gated)"),
    ("urllc_process_mon",     85, "GBR", 4, 0, 1,  50,  50,  500,  500, "INACTIVE (event gated)"),
]


def provision_services_catalog():
    """Upsert the standard QoS service catalog (POST /api/services).

    Must run BEFORE provision_subscription_tree — service_bindings rows
    FK into `services.name`, so missing services → silently dropped
    bindings (binding count = 0 per UE).

    Returns the number of services successfully provisioned.
    """
    ok = 0
    for (name, fiveqi, rtype, arp_pri, arp_pcap, arp_pvuln,
         gbr_ul, gbr_dl, mbr_ul, mbr_dl, status) in _STANDARD_SERVICES:
        body = {
            "name": name, "fiveqi": fiveqi, "resource_type": rtype,
            "arp_priority": arp_pri, "arp_pcap": arp_pcap, "arp_pvuln": arp_pvuln,
            "status": status,
            "flow_rules": [],
        }
        if gbr_ul is not None: body["gbr_ul_kbps"] = gbr_ul
        if gbr_dl is not None: body["gbr_dl_kbps"] = gbr_dl
        if mbr_ul is not None: body["mbr_ul_kbps"] = mbr_ul
        if mbr_dl is not None: body["mbr_dl_kbps"] = mbr_dl
        # /api/services returns {"name": "<name>"} on success — no `ok` field.
        res = _core_api("/api/services", "POST", body)
        if res and res.get("name") == name:
            ok += 1
        else:
            log.warning("Failed to provision service %s: %s", name, res)
    log.info("Provisioned %d/%d standard services", ok, len(_STANDARD_SERVICES))
    return ok


def delete_ue(imsi):
    """Delete UE and all associated data from core."""
    result = _core_api(f"/api/ue/auth/{imsi}", "DELETE")
    log.info("Deleted UE: %s — %s", imsi, result)
    return result


def get_ue_auth(imsi):
    """Get UE auth data from core."""
    return _core_api(f"/api/ue/auth/{imsi}")


def clone_ue(source_imsi, target_imsi, target_msisdn=None):
    """Clone a UE on the core (POST /api/ue/clone)."""
    body = {"source_imsi": source_imsi, "new_imsi": target_imsi}
    if target_msisdn:
        body["new_msisdn"] = target_msisdn
    return _core_api("/api/ue/clone", "POST", body)


# ── Bulk Provisioning ──

def _bucket_template(b):
    """Build the per-bucket UEBulkTemplate body from a baseline.Bucket.

    K / OPc are derived server-side from IMSI + kdf_version, so the
    template carries the same KDF settings the tester uses (must match
    db/seed/manifest.go::DeriveK byte-for-byte — both sides hash
    sha256(imsi || 'MMT-K-' || kdf_version)[:16]).
    """
    from src import baseline as _bl
    slice_items = []
    for idx, sst in enumerate(b.slices):
        sl = _bl.slice_by_sst(sst)
        dnn_items = []
        for d in b.dnns:
            services = [
                {"service_name": svc, "is_default": bool(is_default)}
                for svc, is_default in DNN_DEFAULT_SERVICES.get(d, [])
            ]
            dnn_items.append({
                "dnn": d,
                "is_default": d == b.default_dnn,
                "services": services,
            })
        slice_items.append({
            "sst": sl.sst,
            "sd": sl.sd,
            "is_default": idx == 0,
            "dnns": dnn_items,
        })
    return {
        "kdf_version": _bl.kdf_version(),
        "op_type": "OPC",
        "amf": _bl.amf(),
        "initial_sqn": _bl.initial_sqn(),
        "ambr_dl_kbps": b.ue_ambr_dl_kbps or 1000000,
        "ambr_ul_kbps": b.ue_ambr_ul_kbps or 1000000,
        "subscription_tree": slice_items,
    }


def sync_all_ues_bulk():
    """Provision every baseline UE bucket via /api/ue/bulk-provision.

    One POST per bucket → 4 round-trips for the default 128-UE roster
    instead of 384 (was: auth + tree + subscription per UE). The core
    runs each bucket in a single transaction and refreshes UDM / SMF
    caches once per bucket.

    Returns (provisioned_count, failed_count) — same shape as the
    legacy sync_all_ues() so callers don't need to change.
    """
    from src import baseline as _bl
    total_prov = 0
    total_fail = 0
    for bucket_name in _bl.bucket_names():
        b = _bl.bucket(bucket_name)
        body = {
            "template": _bucket_template(b),
            "range": {
                "imsi_start": b.imsis[0],
                "msisdn_start": b.msisdns[0],
                "count": b.count,
            },
        }
        res = _core_api("/api/ue/bulk-provision", "POST", body, timeout=120)
        if res and res.get("ok"):
            log.info("Bulk-provisioned bucket %s: %d UEs, %d auth, %d slices, %d dnns, %d bindings",
                     bucket_name, res.get("ues_total", b.count),
                     res.get("auth_rows", 0), res.get("slices", 0),
                     res.get("dnns", 0), res.get("bindings", 0))
            total_prov += res.get("ues_total", b.count)
        else:
            log.error("Bulk provisioning failed for bucket %s: %s", bucket_name, res)
            total_fail += b.count
    log.info("Bulk sync complete: %d provisioned, %d failed", total_prov, total_fail)
    return (total_prov, total_fail)


def sync_all_ues():
    """Provision all baseline + in-memory delta UEs onto sa_core.

    Reads from src.protocol.sim_db.sim_db_list() — the in-memory store
    that lazy-inits from config/baseline.yaml and includes any operator
    GUI deltas. For each UE:
      1. POST /ue/auth (credentials + SUCI key if configured)
      2. POST /ue/subscription (default AMBR)
      3. POST /ue/subscribed-nssai (default slice)

    Returns (provisioned_count, failed_count).
    """
    from src.protocol.sim_db import sim_db_list
    ues = sim_db_list()
    if not ues:
        log.warning("sim_db is empty — no UEs to provision")
        return (0, 0)

    provisioned = 0
    failed = 0

    for ue in ues:
        imsi = ue["imsi"]
        try:
            # SUCI profile
            suci_profile = None
            hn_private_key = None
            if ue.get("supi_type") == "suci" and ue.get("protection_scheme", 0) > 0:
                scheme = ue["protection_scheme"]
                suci_profile = "A" if scheme == 1 else "B" if scheme == 2 else None
                # Private key is NOT in sim_db.json (only public key)
                # It needs to be provided separately or derived
                # For now, log a warning
                if suci_profile:
                    log.info("UE %s uses SUCI %s — HN private key must be provisioned separately",
                             imsi, suci_profile)

            # Provision auth
            result = provision_ue_auth(
                imsi=imsi,
                k=ue["k"],
                opc=ue["opc"],
                op_type=ue.get("op_type", "OPC"),
                amf=ue.get("amf", "8000"),
                sqn=ue.get("sqn", 0),
                msisdn=ue.get("msisdn"),
                suci_profile=suci_profile,
                hn_private_key=hn_private_key,
            )

            if result and result.get("ok"):
                # Provision AMBR subscription
                ambr_dl = 1000000
                ambr_ul = 1000000
                try:
                    from src import baseline as _bl
                    b = _bl.bucket_of(imsi)
                    if b.ue_ambr_dl_kbps:
                        ambr_dl = b.ue_ambr_dl_kbps
                    if b.ue_ambr_ul_kbps:
                        ambr_ul = b.ue_ambr_ul_kbps
                    # Provision subscription tree (NSSAI + DNN bindings)
                    # so the AMF accepts the UE's requested S-NSSAI with the
                    # correct SD. Without this every UE falls back to the
                    # plmn_nssai default (SST=1 SD=000001) and bucket 2/3
                    # registrations fail with 5GMM cause 62.
                    slice_items = []
                    for sst in b.slices:
                        sl = _bl.slice_by_sst(sst)
                        slice_items.append({
                            "sst": sl.sst,
                            "sd": sl.sd,
                            "dnns": list(b.dnns),
                            "default_dnn": b.default_dnn,
                        })
                    provision_subscription_tree(imsi, slice_items)
                except Exception as e:
                    log.debug("Per-UE bucket lookup for %s failed: %s", imsi, e)

                provision_subscription(imsi, ambr_dl_kbps=ambr_dl, ambr_ul_kbps=ambr_ul)
                provisioned += 1
            else:
                failed += 1
        except Exception as e:
            log.warning("Failed to provision %s: %s", imsi, e)
            failed += 1

    log.info("Sync complete: %d provisioned, %d failed (total %d)", provisioned, failed, len(ues))
    return (provisioned, failed)


def provision_suci_keys(imsi, hn_private_key, suci_profile="A"):
    """Provision SUCI HN private key on core for a specific UE.

    Called separately from sync_all_ues since the private key
    is not stored in the tester's sim_db.json (only public key is).

    Args:
        hn_private_key: 64 hex chars (32 bytes X25519 for Profile A)
        suci_profile: "A" or "B"
    """
    # Get existing auth data
    auth = get_ue_auth(imsi)
    if not auth:
        log.warning("Cannot provision SUCI keys — UE %s not found on core", imsi)
        return None

    # Update with SUCI fields
    body = {
        "imsi": imsi,
        "k": auth.get("k_hex", ""),
        "op": auth.get("op_hex", ""),
        "op_type": auth.get("op_type", "OPC"),
        "amf": auth.get("amf_hex", "8000"),
        "sqn": auth.get("sqn", 0),
        "suci_profile": suci_profile,
        "hn_private_key": hn_private_key,
    }
    result = _core_api("/api/ue/auth", "POST", body)
    if result and result.get("ok"):
        log.info("SUCI keys provisioned: %s profile=%s", imsi, suci_profile)
    return result


# ── Network-config provisioning (PLMN / NSSAI / TAC / APN) ──
# These functions feed core's REST surface from tester/config/baseline.yaml
# so the tester owns the runtime DB state. Core's seed (db/seed/*.go) is
# still wired for the operator's standalone-GUI cold-boot — the tester
# wipes core's DB on test entry (POST /api/admin/drop-db-data) and then calls
# sync_all() to push its own configuration.

def _ok(result):
    """True if a core POST returned a JSON body with ok=True (or 200 with
    no ok field — e.g. /api/tac/tracking-areas just returns {ok, tac})."""
    return bool(result) and (result.get("ok") is True or "deleted" in result or "tac" in result or "apn_name" in result)


def provision_plmn(mcc, mnc, name="MMT-CORE", plmn_type="home",
                   region_id=1, set_id=1, pointer=0, priority=1):
    """Insert a supported PLMN (POST /api/plmn/supported)."""
    body = {
        "mcc": mcc, "mnc": mnc, "name": name, "plmn_type": plmn_type,
        "priority": priority,
        "amf_region_id": region_id, "amf_set_id": set_id, "amf_pointer": pointer,
    }
    result = _core_api("/api/plmn/supported", "POST", body)
    if _ok(result):
        log.info("Provisioned PLMN: %s-%s (%s)", mcc, mnc, name)
    else:
        log.warning("Failed to provision PLMN %s-%s: %s", mcc, mnc, result)
    return result


def provision_plmn_nssai(plmn_id, sst, sd=None):
    """Attach an S-NSSAI to a PLMN (POST /api/plmn/supported/{id}/nssai)."""
    body = {"sst": int(sst)}
    if sd is not None:
        body["sd"] = sd
    result = _core_api(f"/api/plmn/supported/{plmn_id}/nssai", "POST", body)
    if _ok(result):
        log.info("Provisioned PLMN NSSAI: %s sst=%d sd=%s", plmn_id, sst, sd or "-")
    else:
        log.warning("Failed PLMN NSSAI %s sst=%d: %s", plmn_id, sst, result)
    return result


def provision_nssai_catalog(sst, sd, name):
    """Add an entry to the NSSAI catalog (POST /api/catalog/nssai)."""
    body = {"sst": int(sst), "sd": sd or "", "name": name}
    result = _core_api("/api/catalog/nssai", "POST", body)
    # Returns the inserted row dict, not {ok: true}; presence is success.
    if result:
        log.info("Provisioned NSSAI catalog entry: sst=%d sd=%s name=%s", sst, sd or "-", name)
    else:
        log.warning("Failed NSSAI catalog sst=%d sd=%s: %s", sst, sd, result)
    return result


def provision_tac(tac, mcc, mnc, name="Default", paging_priority=5):
    """Create a tracking area (POST /api/tac/tracking-areas)."""
    body = {
        "tac": f"{int(tac):04d}" if not isinstance(tac, str) else tac,
        "plmn_mcc": mcc, "plmn_mnc": mnc, "name": name,
        "paging_priority": paging_priority,
    }
    result = _core_api("/api/tac/tracking-areas", "POST", body)
    if _ok(result):
        log.info("Provisioned TAC: %s (%s-%s)", body["tac"], mcc, mnc)
    else:
        log.warning("Failed TAC %s: %s", body["tac"], result)
    return result


def provision_apn(name, pool_cidr, pdu_type="IPv4", ssc_mode=1,
                  ambr_dl_kbps=1000000, ambr_ul_kbps=1000000,
                  dns_primary="8.8.8.8", dns_secondary="8.8.4.4",
                  pcscf_address="", mtu=1500):
    """Create an APN + bind a single IP pool.

    Two-step: POST /api/apn (the APN row) then POST /api/apn/{name}/pools.
    """
    apn_body = {
        "apn_name": name,
        "ambr_dl_kbps": ambr_dl_kbps, "ambr_ul_kbps": ambr_ul_kbps,
        "pdu_session_type": pdu_type, "ssc_mode": ssc_mode,
        "dns_primary": dns_primary, "dns_secondary": dns_secondary,
        "pcscf_address": pcscf_address, "mtu": mtu,
    }
    result = _core_api("/api/apn", "POST", apn_body)
    if not _ok(result):
        log.warning("Failed APN %s: %s", name, result)
        return result

    pool_body = {"cidr": pool_cidr, "ip_version": 6 if pdu_type == "IPv6" else 4}
    pool_result = _core_api(f"/api/apn/{name}/pools", "POST", pool_body)
    if _ok(pool_result):
        log.info("Provisioned APN: %s (%s, %s)", name, pool_cidr, pdu_type)
    else:
        log.warning("APN %s created but pool %s failed: %s", name, pool_cidr, pool_result)
    return pool_result


# ── UPF instance bulk provisioning ──

def provision_upfs(upfs: list) -> tuple:
    """Push baseline.yaml's `upfs:` block into core via the bulk endpoint.

    After /api/admin/drop-db-data wipes `upf_instances`, the integrated
    upfloop does NOT re-register (its smfupf.Register call only fires
    at boot — nf/upf/upfloop/upfloop.go:340). Without rows in the
    table, SMF Select() returns "no UPFs registered" and every PDU
    session establishment fails. This pushes the same rows that
    upfloop would have written, restoring TS 23.501 §6.3.3
    DNN+SST-based UPF selection.

    Returns (count_ok, failures).
    """
    if not upfs:
        return 0, []
    # u.address is the PFCP/N4 endpoint; u.n3_address is the GTP-U/N3
    # endpoint advertised to the gNB (TS 38.413 §9.3.2.2 UL-NGU-UP-TNL).
    # These collapse to the same IP only when SMF/UPF/gNB are on the same
    # host — in this lab the gNB lives in a separate netns, so the N3 IP
    # must be the externally-reachable mmtnet address. n6_ip mirrors n3
    # (DN-side host route handled by the integrated UPF datapath).
    body = {
        "instances": [
            {
                "upf_id":         u.id,
                "upf_ip":         u.address,
                "n3_ip":          u.n3_address,
                "n6_ip":          u.n3_address,
                "pfcp_port":      u.port,
                "supported_dnns": list(u.supported_dnns),
                "supported_sst":  [str(s) for s in u.supported_sst],
            }
            for u in upfs
        ]
    }
    res = _core_api("/api/admin/upf-instances", "POST", body)
    if not (res and res.get("ok")):
        log.warning("provision_upfs: bulk POST failed: %s", res)
        return 0, [u.id for u in upfs]
    registered = res.get("registered", []) or []
    failed = [f.get("upf_id", "?") for f in (res.get("failed", []) or [])]
    log.info("Provisioned %d UPF instance(s): %s", len(registered), registered)
    return len(registered), failed


# ── Bulk provisioning from tester baseline.yaml ──

def sync_network_config():
    """Push PLMN / NSSAI catalog / per-PLMN NSSAI / TAC / APNs from
    tester/config/baseline.yaml into core.

    Order matters:
      1. PLMN              — supported_plmns row (creates the PLMN id used below)
      2. NSSAI catalog     — global SST/SD/name dropdown source for GUI/SMF
      3. PLMN NSSAI        — what the AMF advertises in NGSetupResponse / TAI
      4. TAC               — tracking areas (per PLMN)
      5. APN               — apn_config + apn_ip_pools

    Returns dict: counts per section, with failures listed.
    """
    from src import baseline as _bl

    counts = {"plmn": 0, "nssai_catalog": 0, "plmn_nssai": 0, "tac": 0,
              "apn": 0, "services": 0, "upf": 0}
    failures: list = []

    # 0. QoS services catalog — service_bindings inserted later FK into
    # `services.name`, so this MUST run before any per-UE provisioning.
    counts["services"] = provision_services_catalog()

    p = _bl.plmn()
    g = _bl.guami()
    plmn_id = _bl.plmn_id()

    # 1. PLMN
    res = provision_plmn(p.mcc, p.mnc, name="MMT-CORE", plmn_type="home",
                         region_id=g.region_id, set_id=g.set_id, pointer=g.pointer,
                         priority=1)
    if _ok(res):
        counts["plmn"] += 1
    else:
        failures.append(f"plmn:{plmn_id}")

    # 2 + 3. Slices — catalog + per-PLMN attach
    for s in _bl.all_slices():
        if provision_nssai_catalog(s.sst, s.sd, s.name):
            counts["nssai_catalog"] += 1
        else:
            failures.append(f"nssai_catalog:sst={s.sst}")
        if _ok(provision_plmn_nssai(plmn_id, s.sst, s.sd)):
            counts["plmn_nssai"] += 1
        else:
            failures.append(f"plmn_nssai:{plmn_id}:sst={s.sst}")

    # 4. TACs (per AMF served_tai)
    for t in _bl.served_tacs():
        if _ok(provision_tac(t.tac, t.mcc, t.mnc, name=f"TAC-{t.tac}")):
            counts["tac"] += 1
        else:
            failures.append(f"tac:{t.tac}")

    # 5. APNs
    for d in _bl.all_dnns():
        pdu = "IPv4"
        if _ok(provision_apn(
            name=d.name, pool_cidr=d.pool, pdu_type=pdu, ssc_mode=1,
            ambr_dl_kbps=max(d.session_ambr_dl_kbps, 1000000),
            ambr_ul_kbps=max(d.session_ambr_ul_kbps, 1000000),
        )):
            counts["apn"] += 1
        else:
            failures.append(f"apn:{d.name}")

    # 6. UPF instances — repopulate upf_instances so SMF Select()
    # has something to route §6.4.1.3 establishments at.
    upf_ok, upf_failed = provision_upfs(list(_bl.upfs()))
    counts["upf"] = upf_ok
    for fid in upf_failed:
        failures.append(f"upf:{fid}")

    if failures:
        log.warning("sync_network_config: %d failures: %s", len(failures), failures)

    # Force the AMF to re-read PLMN / GUAMI / per-PLMN NSSAI from the
    # DB. Without this the AMF keeps the in-memory snapshot it took at
    # boot (when SeedAll's buggy SD=000001 rows for SST 2/3 existed)
    # and NSSAI selection rejects every UE in bucket 2/3 with 5GMM
    # cause 62.
    rel = _core_api("/api/admin/reload-amf-context", "POST")
    if rel and rel.get("ok"):
        log.info("AMF context reloaded — new PLMN/NSSAI in effect")
    else:
        log.warning("AMF context reload failed: %s — registrations may fail with cause=62", rel)

    log.info("sync_network_config done: %s", counts)
    return {"counts": counts, "failures": failures}


def sync_all():
    """Full provisioning push: network config + all UEs.

    This is the tester-owned single source of truth. After
    POST /api/admin/drop-db-data wipes core's tables, this function rebuilds
    the runtime state from tester/config/baseline.yaml.

    Returns a summary dict.
    """
    log.info("sync_all: pushing tester baseline.yaml to core")
    net = sync_network_config()
    # One POST per bucket via /api/ue/bulk-provision — replaces the
    # 3-round-trips × 128 UEs of the legacy per-UE loop. No fallback
    # to the per-UE path: if bulk fails the test must fail loudly so
    # the underlying cause gets fixed (matches the "no silent
    # fallbacks" project convention).
    prov, fail = sync_all_ues_bulk()
    summary = {
        "network": net["counts"],
        "network_failures": net["failures"],
        "ues_provisioned": prov,
        "ues_failed": fail,
    }
    log.info("sync_all complete: %s", summary)
    return summary
