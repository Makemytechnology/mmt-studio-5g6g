// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// dns.go — FQDN → EAS resolution surface (TS 23.548 §6.2.3.2.2
// EAS Discovery Procedure with EASDF, RFC 1035 DNS shape).
//
// Spec anchor:
//
//   - TS 23.548 §6.2.3 "EAS Discovery and Re-discovery procedures"
//     — under the Session-Breakout Connectivity Model (§6.2.3.2.2)
//     the UE issues a DNS Query for the EAS FQDN; the EASDF (TS
//     23.548 §5.2.2) maps that FQDN to a concrete EAS address +
//     DNAI and the SMF inserts a ULCL/BP towards the chosen PSA.
//     This file holds the EASDF-side FQDN→EAS table and the
//     resolver helper consumers can call without going through
//     the full DNS protocol stack.
//
//   - TS 23.558 §8.2.4 "EAS Profile" — defines a dedicated FQDN
//     field on the EAS profile. The local row stores the FQDN in
//     a side table (eas_dns_entries) so existing EAS rows don't
//     have to gain a column; a future migration could fold it in.
//
// What this surface is NOT:
//   - A full DNS server. The eas_api consumers call the resolver
//     over REST; a real EASDF would expose port 53 and answer DNS
//     queries with the picked EAS's IPv4 + the carried DNAI.

package eas

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// DNSEntry is one row of eas_dns_entries — a registered FQDN tied
// to a specific EAS row id.
type DNSEntry struct {
	ID        int64  `json:"id"`
	FQDN      string `json:"fqdn"`
	EASID     int64  `json:"eas_id"`
	CreatedAt string `json:"created_at"`
}

// ResolveAnswer is the result of ResolveDNS — the EAS row plus
// the DNAI hint that the SMF would use to insert ULCL/BP under
// TS 23.548 §6.2.3.2.2.
type ResolveAnswer struct {
	FQDN        string `json:"fqdn"`
	EASID       int64  `json:"eas_id"`
	EndpointURL string `json:"endpoint_url"`
	DNAI        string `json:"dnai,omitempty"`
}

// RegisterDNSEntry binds an FQDN to an EAS row. Idempotent on
// duplicate FQDN: the existing row's eas_id is updated and the row
// id is returned. The eas_id must already exist in eas_registry —
// FK CASCADE means deleting the EAS deletes the DNS entry too.
func RegisterDNSEntry(fqdn string, easID int64) (int64, error) {
	fqdn = strings.TrimSpace(strings.ToLower(fqdn))
	if fqdn == "" {
		return 0, fmt.Errorf("fqdn is required")
	}
	if easID <= 0 {
		return 0, fmt.Errorf("eas_id is required")
	}
	// Check the EAS exists so the FK violation surfaces as a
	// readable error rather than a SQLite generic message.
	if e, err := GetEAS(easID); err != nil {
		return 0, err
	} else if e == nil {
		return 0, fmt.Errorf("eas_id %d not found in registry", easID)
	}
	// Upsert by FQDN (UNIQUE in the DDL).
	res, err := engine.Exec(`INSERT INTO eas_dns_entries (fqdn, eas_id) VALUES (?,?)
		ON CONFLICT(fqdn) DO UPDATE SET eas_id=excluded.eas_id`, fqdn, easID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		// ON CONFLICT path didn't INSERT — look up the existing row.
		row := engine.QueryRow(`SELECT id FROM eas_dns_entries WHERE fqdn=?`, fqdn)
		if err := row.Scan(&id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// ListDNSEntries returns every registered FQDN, newest first.
// Used by the OAM panel to show the EASDF table.
func ListDNSEntries() ([]DNSEntry, error) {
	rows, err := engine.Query(`SELECT id, fqdn, eas_id, created_at
		FROM eas_dns_entries ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DNSEntry
	for rows.Next() {
		var e DNSEntry
		if err := rows.Scan(&e.ID, &e.FQDN, &e.EASID, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// DeleteDNSEntry removes a single entry by id. Idempotent on
// missing rows (no error returned when no row matches).
func DeleteDNSEntry(id int64) error {
	_, err := engine.Exec(`DELETE FROM eas_dns_entries WHERE id=?`, id)
	return err
}

// DeleteDNSEntryByFQDN removes by FQDN — handy for OAM scripts that
// don't carry the row id.
func DeleteDNSEntryByFQDN(fqdn string) error {
	_, err := engine.Exec(`DELETE FROM eas_dns_entries WHERE fqdn=?`,
		strings.TrimSpace(strings.ToLower(fqdn)))
	return err
}

// ResolveDNS looks up a single FQDN and returns the bound EAS plus
// its DNAI. Mirrors the EASDF response under the Session-Breakout
// Connectivity Model (TS 23.548 §6.2.3.2.2). Returns nil, nil for
// "no match" so the caller can render a 404 cleanly.
func ResolveDNS(fqdn string) (*ResolveAnswer, error) {
	fqdn = strings.TrimSpace(strings.ToLower(fqdn))
	if fqdn == "" {
		return nil, fmt.Errorf("fqdn is required")
	}
	row := engine.QueryRow(`
		SELECT d.eas_id, e.endpoint_url, COALESCE(e.dnai,'')
		  FROM eas_dns_entries d
		  JOIN eas_registry e ON e.id = d.eas_id
		 WHERE d.fqdn = ?
		   AND e.status = 'active'`, fqdn)
	a := &ResolveAnswer{FQDN: fqdn}
	if err := row.Scan(&a.EASID, &a.EndpointURL, &a.DNAI); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}
