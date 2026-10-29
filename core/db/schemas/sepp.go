// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// db/schemas/sepp.go — Schema for the security/sepp_policy package
// (peer-PLMN allow-list, topology-hiding rules, and the N32 audit
// log). The infra/roaming/sepp transparent proxy is plumbing; this
// is the operator policy layer it consults at admission time.
//
// Spec anchors:
//
//   - TS 29.573 §5.2  N32-c control-plane (peer capability negotiation,
//                     TLS handshake) — peer fingerprints come from here.
//   - TS 29.573 §5.3  N32-f forwarding plane (HTTP reverse proxy with
//                     message filtering) — topology-hiding rules
//                     applied per request.
//   - TS 33.501 §13.1 5GC SBI security at the PLMN border — TLS
//                     mutual-auth requirement; peer cert SAN is the
//                     identity used by the allow-list.
package schemas

func init() {
	Register("sepp_policy", SEPPPolicyDDL)
}

var SEPPPolicyDDL = []string{
	`CREATE TABLE IF NOT EXISTS sepp_peers (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      plmn_id         TEXT NOT NULL UNIQUE,
      fqdn            TEXT NOT NULL,
      public_san      TEXT,
      allowed_paths   TEXT NOT NULL DEFAULT '',
      status          TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','inactive','blocked')),
      description     TEXT NOT NULL DEFAULT '',
      created_at      TEXT NOT NULL DEFAULT (datetime('now')),
      updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS sepp_topology_hiding (
      id                  INTEGER PRIMARY KEY AUTOINCREMENT,
      peer_id             INTEGER NOT NULL UNIQUE,
      hide_internal_fqdn  INTEGER NOT NULL DEFAULT 1,
      hide_callbacks      INTEGER NOT NULL DEFAULT 1,
      replace_fqdn        TEXT NOT NULL DEFAULT '',
      strip_headers       TEXT NOT NULL DEFAULT '',
      updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
      FOREIGN KEY (peer_id) REFERENCES sepp_peers(id) ON DELETE CASCADE
    )`,

	`CREATE TABLE IF NOT EXISTS sepp_n32_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      peer_plmn       TEXT NOT NULL DEFAULT '',
      direction       TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
      path            TEXT NOT NULL,
      method          TEXT NOT NULL DEFAULT '',
      status_code     INTEGER NOT NULL DEFAULT 0,
      latency_ms      INTEGER NOT NULL DEFAULT 0,
      action          TEXT NOT NULL DEFAULT 'forwarded'
                      CHECK (action IN ('forwarded','rejected','rewritten')),
      reason          TEXT NOT NULL DEFAULT '',
      created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE INDEX IF NOT EXISTS idx_sepp_peers_plmn ON sepp_peers(plmn_id)`,
	`CREATE INDEX IF NOT EXISTS idx_sepp_peers_status ON sepp_peers(status)`,
	`CREATE INDEX IF NOT EXISTS idx_sepp_log_peer ON sepp_n32_log(peer_plmn, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_sepp_log_action ON sepp_n32_log(action, created_at)`,
}
