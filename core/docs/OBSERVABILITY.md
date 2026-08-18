# Operator Observability

This document is for operators running MMT Studio Core in production or
staging — what signals are available, how to pipe them into ELK /
Prometheus / OpenTelemetry, and which panels map to which signal.

There are four independent channels; you can use any subset.

---

## 1. Structured logs → ELK / Loki / Splunk

The core emits one log line per event. Format:

- **Default** — human-readable text with severity, timestamp (3GPP
  `YYYY-MM-DD HH:MM:SS:mmm` format), module, optional IMSI tag:

      2026-04-19 11:04:22:531 INFO  [sacore.amf.gmm.fsm] [IMSI:001011234560001] FSM AUTHENTICATION → SECURITY_MODE on AuthenticationResponse

- **JSON** — set `SACORE_LOG_JSON=1` to switch every sink (stdout +
  rotating file) to one JSON object per record:

      {"ts":"2026-04-19T11:04:22.531+05:30","level":"INFO",
       "module":"sacore.amf.gmm.fsm","imsi":"001011234560001",
       "msg":"FSM AUTHENTICATION → SECURITY_MODE on AuthenticationResponse"}

  Keys are stable across releases. ELK ingestion with Filebeat:

      filebeat.inputs:
        - type: log
          paths: [/var/log/sacore/sacore.log]
          json.keys_under_root: true
          json.add_error_key: true
      output.elasticsearch:
        hosts: ["elk:9200"]
        index: "sacore-%{+yyyy.MM.dd}"

  Grafana Loki (promtail):

      scrape_configs:
        - job_name: sacore
          static_configs:
            - targets: [localhost]
              labels: {job: sacore, __path__: /var/log/sacore/*.log}
          pipeline_stages:
            - json:
                expressions: {level: level, module: module, imsi: imsi}
            - labels: {level: '', module: '', imsi: ''}

## Suggested queries

- **All FSM transitions for one UE** (diagnose stuck registrations):
  `imsi:001011234560001 AND module:*fsm`
- **Procedure collisions** (spec violations):
  `msg:"procedure collision"`
- **Timer expiries** (unhappy paths):
  `msg:"expired"`
- **gNB disconnects**:
  `module:"sacore.amf.ngap.server" AND msg:"recv"`

---

## 2. Prometheus metrics → Grafana

`GET /metrics` on the web-service port (default `:5000`) exposes every
pm counter and every FSM state count in Prometheus text format 0.0.4.

Scrape config:

    scrape_configs:
      - job_name: sacore
        static_configs: [targets: ['amf:5000']]
        metrics_path: /metrics
        scrape_interval: 15s

Key metric families emitted:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `sacore_auth_*` | counter | `family=AUTH` | Authentication attempts / successes / MAC failures / SQN syncs |
| `sacore_rm_*` | counter | `family=RM` | Registration Management counters |
| `sacore_mm_*` | counter | `family=MM` | Mobility Management counters |
| `sacore_ngap_*` | counter | `family=NGAP` | NG Setup / paging / context-release counters |
| `sacore_sm_*` | counter | `family=SM` | PDU Session Mgmt establish / modify / release |
| `sacore_n3_*` | counter | `family=N3` | UPF / N3 tunnel counters |
| `sacore_fsm_state` | gauge | `fsm`, `state` | Count of live FSM instances in each state |
| `sacore_pti_active` | gauge | `kind` | In-flight 5GSM PTI transactions by procedure kind |

## Suggested alerts

    # UEs stuck mid-registration
    ALERT SacoreUEStuckInAuth
      IF sacore_fsm_state{fsm="gmm",state="AUTHENTICATION"} > 0
      FOR 30s
      LABELS {severity=warning}

    # PFCP delete hanging
    ALERT SacorePFCPDeleteHanging
      IF sacore_fsm_state{fsm="pfcp",state="DELETE_IN_PROGRESS"} > 0
      FOR 60s

    # Auth failure rate
    ALERT SacoreAuthFailureSpike
      IF rate(sacore_auth_fail[5m]) > 0.1

    # NGAP collision detected (someone is misbehaving)
    ALERT SacoreNGAPCollision
      IF increase(sacore_ngap_collision[5m]) > 0

---

## 3. OpenTelemetry → Tempo / Jaeger / OTLP collectors

OTEL scaffolding in `oam/otel` reads `otel_enabled` / `otel_exporter` /
`otel_endpoint` from `infra_config`. Three exporters supported:

- `prometheus` — scraped at the port from `otel_prometheus_port` (default 9464)
- `otlp` — pushed via gRPC to `otel_endpoint`
- `console` — dumped to stdout (dev only)

When OTLP exporter is enabled each 5GMM / 5GSM / NGAP procedure becomes
one span with the following attributes (planned; SDK wiring is the
next stage):

| Span name | Attributes |
|---|---|
| `gmm.register` | `imsi`, `amfUeID`, `gnbIP`, `duration_ms` |
| `gmm.authenticate` | `imsi`, `result`, `retries` |
| `gmm.securitymode` | `imsi`, `eea`, `eia` |
| `ngap.initialctx` | `amfUeID`, `gnbIP` |
| `ngap.pdusetup` | `amfUeID`, `pduSessionID`, `upfID` |
| `ngap.uectx.release` | `amfUeID`, `cause` |
| `pfcp.session` | `imsi`, `seid`, `upf_node` |
| `5gsm.establish` | `imsi`, `pduSessionID`, `dnn`, `sst` |

---

## 4. REST snapshots → troubleshooting UIs

When you don't need historical data, the four JSON endpoints below
return the *current* FSM state of every subject:

| Endpoint | Returns |
|---|---|
| `GET /api/amf/fsm/gmm` | one row per UE: IMSI, AMF-UE-NGAP-ID, GMM state |
| `GET /api/amf/fsm/ngap` | one row per UE association: gNB, AMF-UE-NGAP-ID, NGAP state, pending PDU-setup forks |
| `GET /api/smf/sessions` | one row per PDU session: IMSI, PDU-session-id, DNN, 5GSM state, IP, UPF |
| `GET /api/smf/pfcp` | one row per PFCP session: UPF, IMSI, state, SEID |
| `GET /api/smf/pti` | in-flight 5GSM PTI transactions: IMSI, PTI, kind, started-at |

These are lightweight enough for a per-second refresh from the Live
Sessions HTML panel.

---

## Log-level + IMSI filtering

- `LOG_LEVEL=DEBUG` — dials the global slog level up (runtime-changeable
  via `/api/logger/level`).
- `SACORE_LOG_IMSI=001011234560001,001011234560002` — comma-separated
  allow-list; only these IMSIs land in the log. Infrastructure logs
  with no IMSI still pass through.
- `NO_COLOR=1` — disable ANSI color on non-TTY sinks.
- `SACORE_LOG_JSON=1` — switch to JSON output (ELK-friendly).

---

## Trace capture (signalling trace, TS 32.422)

Orthogonal to the live FSM panels — `oam/trace` persists raw NAS / NGAP
/ PFCP PDUs into `trace_records` when an operator has activated a
trace session via the Traces GUI panel. Useful for post-mortem on a
specific UE: activate the trace, reproduce, retrieve via
`GET /api/trace/records?imsi=…`.
