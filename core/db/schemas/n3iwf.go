// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/n3iwf.go — N3IWF Configuration DDL (TS 23.501 §6.2.9)
package schemas

var N3IWFDDL = []string{
	`CREATE TABLE IF NOT EXISTS n3iwf_config (
      id              INTEGER PRIMARY KEY CHECK (id = 1),
      enabled         INTEGER NOT NULL DEFAULT 0,
      n3iwf_ip        TEXT NOT NULL DEFAULT '0.0.0.0',
      ike_port        INTEGER NOT NULL DEFAULT 500,
      ike_nat_port    INTEGER NOT NULL DEFAULT 4500,
      inner_ip_pool   TEXT NOT NULL DEFAULT '10.60.0.0/24',
      ipsec_enc_algo  TEXT NOT NULL DEFAULT 'aes-cbc-128',
      ipsec_int_algo  TEXT NOT NULL DEFAULT 'hmac-sha256',
      dh_group        INTEGER NOT NULL DEFAULT 14,
      supported_dnns  TEXT NOT NULL DEFAULT 'internet',
      supported_nssai TEXT NOT NULL DEFAULT '01',
      amf_addr        TEXT NOT NULL DEFAULT '',
      plmn_id         TEXT NOT NULL DEFAULT '',
      n3iwf_id        INTEGER NOT NULL DEFAULT 1,
      tac             TEXT NOT NULL DEFAULT '000001'
    )`,
}

// N3IWFAlter holds ALTER TABLE migrations for existing DBs that
// pre-date the AMF connectivity columns. Empty defaults mean
// "AMF connection disabled" — Start() reads amf_addr=="" as a
// signal to keep running IKE without bridging to AMF.
var N3IWFAlter = []struct {
	Column string
	DDL    string
}{
	{"amf_addr", "amf_addr TEXT NOT NULL DEFAULT ''"},
	{"plmn_id", "plmn_id TEXT NOT NULL DEFAULT ''"},
	{"n3iwf_id", "n3iwf_id INTEGER NOT NULL DEFAULT 1"},
	{"tac", "tac TEXT NOT NULL DEFAULT '000001'"},
}
