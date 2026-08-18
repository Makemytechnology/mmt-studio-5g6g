// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ctx — SMF global context per TS 23.501 §6.2.2.
//
// Holds APN/DNN configuration and subscriber service bindings loaded from DB
// at boot. Only UDR should access the DB directly per 3GPP architecture;
// the SMF reads its context once at startup and serves requests from memory.
package ctx

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// APNConfig holds one APN/DNN row from apn_config + apn_ip_pools.
type APNConfig struct {
	APNName        string
	PDUSessionType string
	AMBRDLKbps     int64
	AMBRULKbps     int64
	DNSPrimary     string
	DNSSecondary   string
	// PCSCFAddress is the P-CSCF IPv4 entry point for IMS DNNs.
	// Carried to the UE in EPCO container 000CH (TS 24.008
	// §10.5.6.3) so the UE can register SIP with the right IMS
	// edge. Empty for non-IMS DNNs.
	PCSCFAddress string
	MTU          int64
	V4Pools      []string // CIDR strings, e.g. "10.45.0.0/16"
	V6Pools      []string
}

// ServiceBinding maps (IMSI, DNN) to the default service's QoS profile:
// 5QI + per-flow MBR/GBR from the services catalog. These are the values
// that populate the default bearer's QER on PDU session establishment
// (TS 23.501 §5.7.2: MBR/GBR are per-flow, NOT APN-AMBR).
//
// The MBR/GBR fields are pointers so NULL in DB stays NULL — no hidden
// default / seed fallback. 0/nil here means "not configured" and the
// caller should treat that as "unlimited for this flow".
type ServiceBinding struct {
	ServiceName string
	FiveQI      uint8
	MBRULKbps   *int
	MBRDLKbps   *int
	GBRULKbps   *int
	GBRDLKbps   *int
}

// SMF is the process-wide SMF context. Populated at startup via Initialize,
// then read-only for the lifetime of the process.
type SMF struct {
	mu            sync.RWMutex
	initialized   bool
	apns          map[string]*APNConfig      // keyed by apn_name (DNN)
	defaultFiveQI map[string]*ServiceBinding // keyed by "imsi:dnn"
}

// Default is the process-wide singleton.
var Default = &SMF{}

// Initialize loads the context from pre-fetched data. Called once at startup.
func (s *SMF) Initialize(apns map[string]*APNConfig, bindings map[string]*ServiceBinding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apns = apns
	s.defaultFiveQI = bindings
	s.initialized = true

	log := logger.Get("smf.ctx")
	log.Infof("SMF context: %d APN(s), %d service binding(s)",
		len(s.apns), len(s.defaultFiveQI))
}

// ReplaceAPNs swaps the APN map atomically. Used by ReloadAPNs after a
// GUI/REST edit. Existing PDU sessions keep whatever AMBR was applied
// at their establishment time — new sessions use the fresh values.
func (s *SMF) ReplaceAPNs(apns map[string]*APNConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apns = apns
	logger.Get("smf.ctx").Infof("SMF APNs reloaded: %d entry/entries", len(apns))
}

// ReplaceServiceBindings swaps the (IMSI,DNN)→default-5QI map atomically.
// Same "new sessions only" semantics as ReplaceAPNs.
func (s *SMF) ReplaceServiceBindings(bindings map[string]*ServiceBinding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultFiveQI = bindings
	logger.Get("smf.ctx").Infof("SMF service bindings reloaded: %d entry/entries", len(bindings))
}

// LookupAPN returns the cached APN config for a DNN, or nil if not found.
func (s *SMF) LookupAPN(dnn string) *APNConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if apn, ok := s.apns[dnn]; ok {
		cp := *apn
		return &cp
	}
	return nil
}

// LookupDefault5QI returns the default 5QI for the given IMSI + DNN pair.
// Returns (0, false) when no binding exists.
func (s *SMF) LookupDefault5QI(imsi, dnn string) (uint8, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := imsi + ":" + dnn
	if b, ok := s.defaultFiveQI[key]; ok {
		return b.FiveQI, true
	}
	return 0, false
}

// LookupDefaultService returns the full default service binding (name,
// 5QI, MBR, GBR) for the given IMSI + DNN, or nil if none is configured.
// Callers use MBR/GBR to populate the default bearer's QER.
func (s *SMF) LookupDefaultService(imsi, dnn string) *ServiceBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.defaultFiveQI[imsi+":"+dnn]; ok {
		cp := *b
		return &cp
	}
	return nil
}

// Initialized reports whether Initialize has been called.
func (s *SMF) Initialized() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.initialized
}

// InitContextFromDB loads the SMF context from the database. Called once
// at startup, before the process accepts any session requests.
func InitContextFromDB() error {
	log := logger.Get("smf.init")

	db, err := engine.Open()
	if err != nil {
		return fmt.Errorf("smf: cannot open DB: %w", err)
	}

	apns, err := loadAPNConfigs(db, log)
	if err != nil {
		return fmt.Errorf("smf: load APN configs: %w", err)
	}

	bindings, err := loadServiceBindings(db, log)
	if err != nil {
		return fmt.Errorf("smf: load service bindings: %w", err)
	}

	Default.Initialize(apns, bindings)
	return nil
}

// ReloadAPNs re-reads apn_config + apn_ip_pools and swaps the cache.
// Call this from any route that mutates APN data (AMBR, DNS, pools)
// so new PDU sessions see the updated values without a restart.
func ReloadAPNs() error {
	log := logger.Get("smf.init")
	db, err := engine.Open()
	if err != nil {
		return fmt.Errorf("smf: reload apns: %w", err)
	}
	apns, err := loadAPNConfigs(db, log)
	if err != nil {
		return fmt.Errorf("smf: reload apns: %w", err)
	}
	Default.ReplaceAPNs(apns)
	return nil
}

// ReloadServiceBindings re-reads the (IMSI,DNN) default-5QI map. Call
// this from every route that mutates service_bindings or the services
// catalog, so new PDU sessions pick up the new default 5QI.
func ReloadServiceBindings() error {
	log := logger.Get("smf.init")
	db, err := engine.Open()
	if err != nil {
		return fmt.Errorf("smf: reload bindings: %w", err)
	}
	bindings, err := loadServiceBindings(db, log)
	if err != nil {
		return fmt.Errorf("smf: reload bindings: %w", err)
	}
	Default.ReplaceServiceBindings(bindings)
	return nil
}

// loadAPNConfigs reads apn_config + apn_ip_pools into a map keyed by DNN.
func loadAPNConfigs(db *sql.DB, log *logger.Logger) (map[string]*APNConfig, error) {
	apns := make(map[string]*APNConfig)

	rows, err := db.Query(`SELECT apn_name, pdu_session_type, ambr_dl_kbps, ambr_ul_kbps,
		dns_primary, dns_secondary, pcscf_address, mtu FROM apn_config`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var a APNConfig
		var dns1, dns2, pcscf sql.NullString
		if err := rows.Scan(&a.APNName, &a.PDUSessionType, &a.AMBRDLKbps, &a.AMBRULKbps,
			&dns1, &dns2, &pcscf, &a.MTU); err != nil {
			log.Warnf("apn_config row scan: %v", err)
			continue
		}
		a.DNSPrimary = dns1.String
		a.DNSSecondary = dns2.String
		a.PCSCFAddress = pcscf.String
		apns[a.APNName] = &a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load IP pools and attach to APNs
	poolRows, err := db.Query(`SELECT a.apn_name, p.cidr, p.ip_version
		FROM apn_ip_pools p JOIN apn_config a ON a.id = p.apn_id`)
	if err != nil {
		return nil, err
	}
	defer poolRows.Close()

	for poolRows.Next() {
		var name, cidr string
		var version int
		if err := poolRows.Scan(&name, &cidr, &version); err != nil {
			log.Warnf("apn_ip_pools row scan: %v", err)
			continue
		}
		apn, ok := apns[name]
		if !ok {
			continue
		}
		if version == 4 {
			apn.V4Pools = append(apn.V4Pools, cidr)
		} else {
			apn.V6Pools = append(apn.V6Pools, cidr)
		}
	}

	log.Infof("Loaded %d APN config(s) from DB", len(apns))
	return apns, poolRows.Err()
}

// loadServiceBindings reads every (IMSI, DNN) → default service QoS.
// MBR/GBR come straight from the services row — NULLs stay NULLs.
func loadServiceBindings(db *sql.DB, log *logger.Logger) (map[string]*ServiceBinding, error) {
	bindings := make(map[string]*ServiceBinding)

	rows, err := db.Query(`
		SELECT ue.imsi, usd.dnn, s.name, s.fiveqi,
		       s.mbr_ul_kbps, s.mbr_dl_kbps, s.gbr_ul_kbps, s.gbr_dl_kbps
		FROM service_bindings sb
		JOIN ue_slice_dnn usd ON usd.id = sb.slice_dnn_id
		JOIN ue_subscribed_nssai usn ON usn.id = usd.subscribed_nssai_id
		JOIN ue ON ue.id = usn.ue_id
		JOIN services s ON s.name = sb.service_name
		WHERE sb.is_default = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	toPtr := func(v sql.NullInt64) *int {
		if !v.Valid {
			return nil
		}
		i := int(v.Int64)
		return &i
	}

	for rows.Next() {
		var imsi, dnn, svcName string
		var fiveqi int64
		var mbrUL, mbrDL, gbrUL, gbrDL sql.NullInt64
		if err := rows.Scan(&imsi, &dnn, &svcName, &fiveqi,
			&mbrUL, &mbrDL, &gbrUL, &gbrDL); err != nil {
			log.Warnf("service_bindings row scan: %v", err)
			continue
		}
		bindings[imsi+":"+dnn] = &ServiceBinding{
			ServiceName: svcName,
			FiveQI:      uint8(fiveqi),
			MBRULKbps:   toPtr(mbrUL),
			MBRDLKbps:   toPtr(mbrDL),
			GBRULKbps:   toPtr(gbrUL),
			GBRDLKbps:   toPtr(gbrDL),
		}
	}

	log.Infof("Loaded %d default service binding(s) from DB", len(bindings))
	return bindings, rows.Err()
}
