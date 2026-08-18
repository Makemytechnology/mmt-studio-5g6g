# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""OAM and Analytics primitives — tester-side mirror.

Mirrors the Go core's
    oam/{pm,fm,trace}                  — performance counters, alarms, trace
    nf/nwdaf{,/analytics,/exposure}    — NWDAF analytics + exposure
packages. Pure dataclasses + in-memory stores; no live core, no DB, no
network. The goal is to give pytest a black-box surface that exercises
the same enumerations (severities, depths, analytics IDs, exposure-type
mapping) as the Go side, so a contract drift on either side is caught.

Spec anchors (verifiable against local PDFs):

  Analytics — TS 23.288 §6.1, §6.2, §6.3, §6.5, §6.7, §6.9
  ----------
  * TS 23.288 §6.1.1   Analytics Subscribe/Unsubscribe.
  * TS 23.288 §6.1.1.2 Analytics subscribe/unsubscribe by AFs via NEF.
  * TS 23.288 §6.1.2   Analytics Request.
  * TS 23.288 §6.1.2.2 Analytics request by AFs via NEF.
  * TS 23.288 §6.1.3   Contents of Analytics Exposure.
  * TS 23.288 §6.2.2   Data Collection from NFs.
  * TS 23.288 §6.3     Slice load level analytics.
  * TS 23.288 §6.5     NF load analytics.
  * TS 23.288 §6.7.2   UE Mobility analytics.
  * TS 23.288 §6.7.3   UE Communication analytics.
  * TS 23.288 §6.7.5   Abnormal behaviour analytics.
  * TS 23.288 §6.9     QoS Sustainability analytics.
  * TS 29.522 §4.4     Northbound APIs at the NEF (exposure shape).

  Fault Management — TS 28.532 §11.2a
  ----------------
  * TS 28.532 §11.2a   Generic fault supervision management service —
                       defers to TS 28.111 (not loaded; see TODOs).

Deferred (PDFs not loaded; TODO(spec:) prose only):

  * TODO(spec: TS 28.111)  Generic fault-supervision Stage 2/3
                           (subscribe / getAlarmList / acknowledgeAlarm
                           / clearAlarm / notifyNewAlarm).
  * TODO(spec: ITU-T X.733) Alarm reporting function — perceived
                           severities + probable causes vocabulary.
  * TODO(spec: TS 28.552)  5G performance measurements catalogue.
  * TODO(spec: TS 28.554)  5G end-to-end KPIs.
  * TODO(spec: TS 32.422)  Subscriber and equipment trace control.
  * TODO(spec: TS 29.520)  Stage-3 Nnwdaf services (JSON schemas).
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Optional


# ════════════════════════════════════════════════════════════════════
# Analytics — TS 23.288
# ════════════════════════════════════════════════════════════════════

# Analytics IDs — must mirror nf/nwdaf/analytics/analytics.go constants
# 1:1. Each is anchored to a §6.x clause (verified against TS 23.288).
ANALYTICS_NF_LOAD = "NF_LOAD"                  # TS 23.288 §6.5
ANALYTICS_UE_MOBILITY = "UE_MOBILITY"          # TS 23.288 §6.7.2
ANALYTICS_UE_COMMUNICATION = "UE_COMMUNICATION"  # TS 23.288 §6.7.3
ANALYTICS_QOS_SUSTAINABILITY = "QOS_SUSTAINABILITY"  # TS 23.288 §6.9
ANALYTICS_ABNORMAL_BEHAVIOUR = "ABNORMAL_BEHAVIOUR"  # TS 23.288 §6.7.5
ANALYTICS_PDU_SESSION = "PDU_SESSION"          # TS 23.288 §6.4 (deferred)
ANALYTICS_SLICE_LOAD = "SLICE_LOAD"            # TS 23.288 §6.3

VALID_ANALYTICS_IDS = frozenset({
    ANALYTICS_NF_LOAD,
    ANALYTICS_UE_MOBILITY,
    ANALYTICS_UE_COMMUNICATION,
    ANALYTICS_QOS_SUSTAINABILITY,
    ANALYTICS_ABNORMAL_BEHAVIOUR,
    ANALYTICS_PDU_SESSION,
    ANALYTICS_SLICE_LOAD,
})

# External exposure-type → internal analytics-ID map.
# Mirror of nf/nwdaf/exposure/exposure.go ExposureTypes.
EXPOSURE_TYPES = {
    "ue_mobility": ANALYTICS_UE_MOBILITY,
    "ue_communication": ANALYTICS_UE_COMMUNICATION,
    "nf_load": ANALYTICS_NF_LOAD,
    "network_performance": ANALYTICS_QOS_SUSTAINABILITY,
    "abnormal_behaviour": ANALYTICS_ABNORMAL_BEHAVIOUR,
    "qos_sustainability": ANALYTICS_QOS_SUSTAINABILITY,
    "pdu_session": ANALYTICS_PDU_SESSION,
    "slice_load": ANALYTICS_SLICE_LOAD,
}


@dataclass
class DataPoint:
    """Mirror of analytics.DataPoint."""

    source_nf: str
    analytics_id: str
    data: dict = field(default_factory=dict)
    imsi: str = ""
    dnn: str = ""
    collected_at: Optional[datetime] = None


@dataclass
class AnalyticsResult:
    """Mirror of analytics.AnalyticsResult."""

    analytics_id: str
    result: dict = field(default_factory=dict)
    confidence: float = 0.0
    message: str = ""
    data_points_used: int = 0
    time_window_sec: int = 300


def compute_analytics(analytics_id: str, points: list[DataPoint], time_window_sec: int = 300, *, now: Optional[datetime] = None) -> AnalyticsResult:
    """Pure-Python mirror of analytics.ComputeAnalytics.

    Implements the §6.x dispatch shape. Per-ID logic is intentionally
    simpler than the Go side — the goal is to verify enum + envelope
    parity, not to reproduce the entire engine.
    """
    if analytics_id not in VALID_ANALYTICS_IDS:
        return AnalyticsResult(analytics_id=analytics_id, message=f"Unknown analytics ID: {analytics_id}")
    now = now or datetime.now(timezone.utc)
    cutoff = now - timedelta(seconds=time_window_sec)
    recent = [p for p in points if (p.collected_at or now) > cutoff]
    if not recent:
        return AnalyticsResult(analytics_id=analytics_id, confidence=0.0, message="No data in time window")

    if analytics_id == ANALYTICS_UE_MOBILITY:
        # 'current' tracks the LAST element (mirrors Go behaviour — no
        # CollectedAt sort).
        latest = recent[-1].data
        peak = max(int(p.data.get("total_ues", 0)) for p in recent)
        return AnalyticsResult(
            analytics_id=analytics_id,
            result={
                "current_ues": int(latest.get("total_ues", 0)),
                "current_registered": int(latest.get("registered", 0)),
                "current_connected": int(latest.get("connected", 0)),
                "peak_ues": peak,
                "samples": len(recent),
            },
            confidence=min(0.5 + len(recent) * 0.05, 0.9),
            data_points_used=len(recent),
            time_window_sec=time_window_sec,
        )

    if analytics_id == ANALYTICS_ABNORMAL_BEHAVIOUR:
        alerts = []
        for p in recent:
            counters = p.data.get("pm_counters") or {}
            auth_att = float(counters.get("AUTH.Att", 0))
            auth_fail = float(counters.get("AUTH.Fail", 0))
            if auth_att > 0 and auth_fail / auth_att > 0.3:
                alerts.append({"type": "AUTH_FAILURE_SPIKE", "severity": "high"})
            if float(counters.get("AUTH.FailMAC", 0)) > 5:
                alerts.append({"type": "MAC_VERIFICATION_FAILURES", "severity": "critical"})
        return AnalyticsResult(
            analytics_id=analytics_id,
            result={"anomaly_detected": bool(alerts), "alerts": alerts, "alert_count": len(alerts)},
            confidence=0.7 if alerts else 0.5,
            data_points_used=len(recent),
            time_window_sec=time_window_sec,
        )

    if analytics_id == ANALYTICS_QOS_SUSTAINABILITY:
        upf = [p for p in recent if p.source_nf == "UPF"]
        if not upf:
            return AnalyticsResult(analytics_id=analytics_id, result={"qos_status": "no_data"}, confidence=0.1,
                                   data_points_used=len(recent), time_window_sec=time_window_sec)
        latest = upf[-1].data
        rx = float(latest.get("rx_pkts", 0))
        tx = float(latest.get("tx_pkts", 0))
        dropped = float(latest.get("dropped", 0))
        denom = max(rx + tx, 1)
        drop_rate = dropped / denom
        status = "sustainable"
        if drop_rate > 0.05:
            status = "degraded"
        elif drop_rate > 0.01:
            status = "at_risk"
        return AnalyticsResult(
            analytics_id=analytics_id,
            result={"qos_status": status, "drop_rate": drop_rate},
            confidence=min(0.6 + len(upf) * 0.05, 0.9),
            data_points_used=len(recent),
            time_window_sec=time_window_sec,
        )

    # Default — populate envelope with no analytics-specific result.
    return AnalyticsResult(
        analytics_id=analytics_id,
        result={},
        confidence=0.5,
        data_points_used=len(recent),
        time_window_sec=time_window_sec,
    )


# ════════════════════════════════════════════════════════════════════
# Exposure consumer + subscription mirror — TS 23.288 §6.1.1.2
# ════════════════════════════════════════════════════════════════════

# target_type mirror of nwdaf_exposure_subscriptions.target_type CHECK.
TARGET_TYPES = ("imsi", "slice", "network")
QUERY_TYPES = ("subscription", "one_shot")


@dataclass
class ExposureConsumer:
    """Mirror of nwdaf_exposure_consumers row."""

    name: str
    callback_url: str = ""
    api_key: str = ""
    allowed_analytics: list[str] = field(default_factory=list)
    active: bool = True


def check_analytics_permission(consumer: ExposureConsumer, analytics_type: str) -> bool:
    """Mirror of exposure.CheckAnalyticsPermission."""
    if not consumer.allowed_analytics:
        return True
    return analytics_type in consumer.allowed_analytics


@dataclass
class ExposureSubscription:
    """Mirror of nwdaf_exposure_subscriptions row."""

    consumer_id: int
    analytics_type: str
    target_type: str
    target_id: str = ""
    interval_s: int = 60
    callback_url: str = ""
    active: bool = True


def validate_subscription(s: ExposureSubscription) -> Optional[str]:
    if s.target_type not in TARGET_TYPES:
        return f"invalid target_type {s.target_type!r}"
    if s.analytics_type not in VALID_ANALYTICS_IDS:
        return f"invalid analytics_type {s.analytics_type!r}"
    if s.interval_s <= 0:
        return "interval_s must be > 0"
    return None


# ════════════════════════════════════════════════════════════════════
# Performance Counters — TODO(spec: TS 28.552)
# ════════════════════════════════════════════════════════════════════
#
# Counter-name constants mirror nf/oam/pm/perf_counters.go. Tests check
# parity so a rename on either side trips the test on this side too.

# AMF — Registration Management
RM_REG_ATT = "RM.RegAtt"
RM_REG_SUCC = "RM.RegSucc"
RM_REG_FAIL = "RM.RegFail"
RM_DEREG_ATT = "RM.DeregAtt"
RM_DEREG_SUCC = "RM.DeregSucc"

# AMF — Authentication
AUTH_ATT = "AUTH.Att"
AUTH_SUCC = "AUTH.Succ"
AUTH_FAIL = "AUTH.Fail"
AUTH_FAIL_MAC = "AUTH.FailMAC"
AUTH_FAIL_SQN = "AUTH.FailSQN"

# SMF — PDU Session Management
SM_SESS_ATT = "SM.SessAtt"
SM_SESS_SUCC = "SM.SessSucc"
SM_SESS_FAIL = "SM.SessFail"

# UPF — PFCP report types (TS 29.244 §7.5.8.x)
UPF_REPORT_DLDR = "UPF.ReportDLDR"
UPF_REPORT_USAGE = "UPF.ReportUsage"
UPF_REPORT_ERR_IND = "UPF.ReportErrInd"


def success_rate(succ: int, fail: int) -> float:
    """Mirror of pm.SuccessRate — returns -1 when there are no attempts."""
    total = succ + fail
    if total == 0:
        return -1.0
    return succ / total * 100.0


# ════════════════════════════════════════════════════════════════════
# Fault Management — TS 28.532 §11.2a
# ════════════════════════════════════════════════════════════════════
#
# X.733 vocabulary — perceivedSeverity, alarmType, probableCause.
# Mirror of oam/fm/fault_manager.go constants.

ALARM_TYPES = ("Communications", "Processing", "Environmental", "QoS", "Equipment")
ALARM_SEVERITIES = ("Critical", "Major", "Minor", "Warning", "Indeterminate", "Cleared")

# Severity ordering (lower = more severe) — used by ActiveAlarms sort.
SEVERITY_ORDER = {
    "Critical": 0, "Major": 1, "Minor": 2, "Warning": 3,
    "Indeterminate": 4, "Cleared": 5,
}


@dataclass
class Alarm:
    """Mirror of fm.Alarm."""

    alarm_id: int
    managed_object: str
    alarm_type: str
    probable_cause: str
    perceived_severity: str
    specific_problem: str
    additional_text: str = ""
    additional_info: str = ""
    raise_count: int = 1
    ack_state: str = "Unacknowledged"
    ack_user: str = ""
    event_time: Optional[datetime] = None
    last_raised: Optional[datetime] = None
    clear_time: Optional[datetime] = None
    ack_time: Optional[datetime] = None


def correlation_key(managed_object: str, probable_cause: str, specific_problem: str) -> str:
    """Mirror of fm.correlationKey — same separator + order."""
    return f"{managed_object}::{probable_cause}::{specific_problem}"


class FaultManager:
    """Pure-Python mirror of the fm.Manager surface.

    Same correlation contract: repeated Raise() with the same
    (managed_object, probable_cause, specific_problem) tuple bumps
    raise_count rather than creating a new alarm row.
    """

    def __init__(self) -> None:
        self._seq = 0
        self._active: dict[str, Alarm] = {}

    def raise_alarm(self, *, managed_object: str, alarm_type: str, probable_cause: str,
                    perceived_severity: str, specific_problem: str,
                    additional_text: str = "", additional_info: Optional[dict] = None,
                    now: Optional[datetime] = None) -> int:
        if perceived_severity == "Cleared":
            raise ValueError("use clear() — Raise must not be called with Cleared")
        now = now or datetime.now(timezone.utc)
        info = json.dumps(additional_info) if additional_info else ""
        key = correlation_key(managed_object, probable_cause, specific_problem)
        existing = self._active.get(key)
        if existing:
            existing.perceived_severity = perceived_severity
            existing.additional_text = additional_text
            existing.additional_info = info
            existing.last_raised = now
            existing.raise_count += 1
            return existing.alarm_id
        self._seq += 1
        a = Alarm(
            alarm_id=self._seq,
            managed_object=managed_object,
            alarm_type=alarm_type,
            probable_cause=probable_cause,
            perceived_severity=perceived_severity,
            specific_problem=specific_problem,
            additional_text=additional_text,
            additional_info=info,
            event_time=now,
            last_raised=now,
        )
        self._active[key] = a
        return a.alarm_id

    def clear(self, managed_object: str, probable_cause: str, specific_problem: str, *, now: Optional[datetime] = None) -> int:
        now = now or datetime.now(timezone.utc)
        key = correlation_key(managed_object, probable_cause, specific_problem)
        a = self._active.pop(key, None)
        if a is None:
            return 0
        a.perceived_severity = "Cleared"
        a.clear_time = now
        return a.alarm_id

    def ack(self, alarm_id: int, user: str = "operator", *, now: Optional[datetime] = None) -> bool:
        now = now or datetime.now(timezone.utc)
        for a in self._active.values():
            if a.alarm_id == alarm_id:
                a.ack_state = "Acknowledged"
                a.ack_user = user
                a.ack_time = now
                return True
        return False

    def active_alarms(self) -> list[Alarm]:
        out = list(self._active.values())
        out.sort(key=lambda a: (SEVERITY_ORDER.get(a.perceived_severity, 99),
                                -(a.last_raised.timestamp() if a.last_raised else 0)))
        return out

    def counts(self) -> dict[str, int]:
        out = {s: 0 for s in ("Critical", "Major", "Minor", "Warning", "Indeterminate")}
        for a in self._active.values():
            if a.perceived_severity in out:
                out[a.perceived_severity] += 1
        out["total"] = sum(out.values())
        return out


# ════════════════════════════════════════════════════════════════════
# Trace — TODO(spec: TS 32.422 / TS 32.423)
# ════════════════════════════════════════════════════════════════════

# Mirror of trace_sessions.depth + status CHECK constraints.
TRACE_DEPTHS = ("minimum", "medium", "maximum")
TRACE_STATUSES = ("active", "completed", "stopped")


@dataclass
class TraceSession:
    """Mirror of trace_sessions row."""

    trace_ref: str
    imsi: str = ""
    gnb_ip: str = ""
    depth: str = "medium"
    interfaces: str = "N1,N2"
    duration_sec: int = 600
    status: str = "active"
    record_count: int = 0


def validate_trace_session(s: TraceSession) -> Optional[str]:
    if s.depth not in TRACE_DEPTHS:
        return f"invalid depth {s.depth!r}"
    if s.status not in TRACE_STATUSES:
        return f"invalid status {s.status!r}"
    if s.duration_sec <= 0:
        return "duration_sec must be > 0"
    return None


@dataclass
class TraceRecord:
    """Mirror of trace_records row."""

    trace_ref: str
    interface: str
    direction: str
    msg_type: str
    msg_code: int = 0
    imsi: str = ""
    summary: str = ""
    hex_dump: str = ""
    latency_us: Optional[int] = None
