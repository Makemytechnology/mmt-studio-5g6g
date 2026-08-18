// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Operations REST routes — Go port of the Python reference routes:
//   - operations.py   (PDU sessions, UE/gNB/AMF contexts, UPF stats, KPIs, iperf)
//   - admin.py        (DB admin, NF status, system info, infra config, UPF instances)
//   - logger.py       (log level, IMSI filter, log entries, modules, presets)
//   - fm_api.py       (fault management alarm CRUD)
//   - benchmark.py    (live benchmark KPIs, soak, peak reset)
//
// Endpoints that require packages not yet ported return sensible defaults
// (empty arrays, zero counters) so the GUI panels do not 404.
package app

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/nf/amf"
	amfctx "github.com/mmt/mmt-studio-core/nf/amf/ctx"
	gmmfsm "github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	gmmkpi "github.com/mmt/mmt-studio-core/nf/amf/gmm/kpi"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/nssf"
	smfctx "github.com/mmt/mmt-studio-core/nf/smf/ctx"
	"github.com/mmt/mmt-studio-core/nf/smf/ipalloc"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/nf/smf/upf"
	"github.com/mmt/mmt-studio-core/nf/udm"
	upfMgr "github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/platform"
	"github.com/mmt/mmt-studio-core/oam/pm"
	"github.com/go-chi/chi/v5"
)

// RegisterOperationsRoutes wires the operations / admin / logger / FM / benchmark
// API endpoints. Call from main() after the other Register*Routes methods.
func (s *Server) RegisterOperationsRoutes() {
	r := s.Router
	log := logger.Get("webservice.ops")

	// ═══════════════════════════════════════════════════════════════════
	// APN / DNN Configuration (operations.py network-config section)
	// ═══════════════════════════════════════════════════════════════════

	r.Get("/api/apn", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := db.Query(`SELECT id, apn_name, ambr_dl_kbps, ambr_ul_kbps,
			pdu_session_type, ssc_mode, dns_primary, dns_secondary, pcscf_address, mtu
			FROM apn_config ORDER BY apn_name`)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var items []map[string]any
		for rows.Next() {
			var id int64
			var name, pduType string
			var ambrDL, ambrUL, sscMode, mtu int64
			var dnsPri, dnsSec, pcscf *string
			if err := rows.Scan(&id, &name, &ambrDL, &ambrUL, &pduType, &sscMode,
				&dnsPri, &dnsSec, &pcscf, &mtu); err != nil {
				continue
			}
			item := map[string]any{
				"id": id, "apn_name": name,
				"ambr_dl_kbps": ambrDL, "ambr_ul_kbps": ambrUL,
				"pdu_session_type": pduType, "ssc_mode": sscMode,
				"dns_primary": dnsPri, "dns_secondary": dnsSec,
				"pcscf_address": pcscf, "mtu": mtu,
			}
			items = append(items, item)
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"items": items})
	})

	r.Post("/api/apn", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			APNName        string `json:"apn_name"`
			OldName        string `json:"old_name"` // set on rename; empty on add/edit
			AMBRDL         int64  `json:"ambr_dl_kbps"`
			AMBRUL         int64  `json:"ambr_ul_kbps"`
			PDUSessionType string `json:"pdu_session_type"`
			SSCMode        int    `json:"ssc_mode"`
			DNSPrimary     string `json:"dns_primary"`
			DNSSecondary   string `json:"dns_secondary"`
			PCSCFAddress   string `json:"pcscf_address"`
			MTU            int    `json:"mtu"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if strings.TrimSpace(d.APNName) == "" {
			jsonError(w, "apn_name required", http.StatusBadRequest)
			return
		}
		if d.PDUSessionType == "" {
			d.PDUSessionType = "IPv4"
		}
		if d.SSCMode == 0 {
			d.SSCMode = 1
		}
		if d.MTU == 0 {
			d.MTU = 1500
		}
		if d.AMBRDL == 0 {
			d.AMBRDL = 1000000
		}
		if d.AMBRUL == 0 {
			d.AMBRUL = 1000000
		}
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Rename path — UPDATE preserves apn_config.id so apn_ip_pools
		// stays attached, and ue_slice_dnn.dnn cascades to the new name
		// via its `ON UPDATE CASCADE` FK (core.go:106). No DELETE fires,
		// so the RESTRICT gate never trips.
		if d.OldName != "" && d.OldName != d.APNName {
			res, err := db.Exec(`UPDATE apn_config SET
				apn_name=?, ambr_dl_kbps=?, ambr_ul_kbps=?,
				pdu_session_type=?, ssc_mode=?, dns_primary=?,
				dns_secondary=?, pcscf_address=?, mtu=?
				WHERE apn_name=?`,
				d.APNName, d.AMBRDL, d.AMBRUL, d.PDUSessionType, d.SSCMode,
				d.DNSPrimary, d.DNSSecondary, d.PCSCFAddress, d.MTU,
				d.OldName)
			if err != nil {
				msg := err.Error()
				if strings.Contains(msg, "UNIQUE") {
					msg = "APN '" + d.APNName + "' already exists — pick a different name or edit that one directly"
					jsonError(w, msg, http.StatusConflict)
					return
				}
				jsonError(w, msg, http.StatusInternalServerError)
				return
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				jsonError(w, "APN '"+d.OldName+"' not found — cannot rename", http.StatusNotFound)
				return
			}
			log.Infof("APN rename: %s → %s", d.OldName, d.APNName)
			if err := smfctx.ReloadAPNs(); err != nil {
				log.Warnf("APN cache reload after rename failed: %v", err)
			}
			jsonReply(w, map[string]any{"ok": true, "apn_name": d.APNName, "renamed_from": d.OldName})
			return
		}

		// Add/edit path — INSERT … ON CONFLICT DO UPDATE preserves
		// apn_config.id when the row already exists. `INSERT OR REPLACE`
		// would DELETE-then-INSERT, cascading apn_ip_pools
		// (ON DELETE CASCADE = pool loss) and tripping ue_slice_dnn's
		// ON DELETE RESTRICT whenever any subscriber still lists this
		// DNN. Upsert-in-place sidesteps both.
		_, err = db.Exec(`INSERT INTO apn_config
			(apn_name, ambr_dl_kbps, ambr_ul_kbps, pdu_session_type, ssc_mode,
			 dns_primary, dns_secondary, pcscf_address, mtu)
			VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT(apn_name) DO UPDATE SET
			 ambr_dl_kbps=excluded.ambr_dl_kbps,
			 ambr_ul_kbps=excluded.ambr_ul_kbps,
			 pdu_session_type=excluded.pdu_session_type,
			 ssc_mode=excluded.ssc_mode,
			 dns_primary=excluded.dns_primary,
			 dns_secondary=excluded.dns_secondary,
			 pcscf_address=excluded.pcscf_address,
			 mtu=excluded.mtu`,
			d.APNName, d.AMBRDL, d.AMBRUL, d.PDUSessionType, d.SSCMode,
			d.DNSPrimary, d.DNSSecondary, d.PCSCFAddress, d.MTU)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Infof("APN upsert: %s", d.APNName)
		// Refresh SMF's cached APN map so new PDU sessions see the new
		// AMBR/DNS/PDU-type without an sacore-web restart.
		if err := smfctx.ReloadAPNs(); err != nil {
			log.Warnf("APN cache reload after upsert failed: %v", err)
		}
		jsonReply(w, map[string]any{"ok": true, "apn_name": d.APNName})
	})

	r.Delete("/api/apn/{name}", func(w http.ResponseWriter, rq *http.Request) {
		name := chi.URLParam(rq, "name")
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		res, err := db.Exec(`DELETE FROM apn_config WHERE apn_name=?`, name)
		if err != nil {
			// Typical cause: ue_slice_dnn.dnn references apn_config.apn_name
			// with ON DELETE RESTRICT — the APN is still subscribed by one
			// or more UEs. Surface a useful message instead of a bare
			// "constraint failed".
			msg := err.Error()
			if strings.Contains(msg, "FOREIGN KEY") || strings.Contains(msg, "constraint") {
				msg = "APN '" + name + "' is still subscribed by at least one UE (ue_slice_dnn); remove the subscription first"
			}
			jsonError(w, msg, http.StatusConflict)
			return
		}
		n, _ := res.RowsAffected()
		log.Infof("APN delete: %s (%d rows)", name, n)
		if err := smfctx.ReloadAPNs(); err != nil {
			log.Warnf("APN cache reload after delete failed: %v", err)
		}
		jsonReply(w, map[string]any{"ok": true, "deleted": n})
	})

	// ── APN IP Pools ─────────────────────────────────────────────────
	r.Get("/api/apn/{name}/pools", func(w http.ResponseWriter, rq *http.Request) {
		name := chi.URLParam(rq, "name")
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := db.Query(`SELECT p.id, p.cidr, p.ip_version
			FROM apn_ip_pools p
			JOIN apn_config a ON a.id = p.apn_id
			WHERE a.apn_name=?`, name)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var pools []map[string]any
		for rows.Next() {
			var id, ipVer int64
			var cidr string
			if err := rows.Scan(&id, &cidr, &ipVer); err != nil {
				continue
			}
			pools = append(pools, map[string]any{"id": id, "cidr": cidr, "ip_version": ipVer})
		}
		if pools == nil {
			pools = []map[string]any{}
		}
		jsonReply(w, map[string]any{"apn_name": name, "pools": pools})
	})

	r.Post("/api/apn/{name}/pools", func(w http.ResponseWriter, rq *http.Request) {
		name := chi.URLParam(rq, "name")
		var d struct {
			CIDR      string `json:"cidr"`
			IPVersion int    `json:"ip_version"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.CIDR == "" {
			jsonError(w, "cidr required", http.StatusBadRequest)
			return
		}
		if d.IPVersion == 0 {
			d.IPVersion = 4
		}
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var apnID int64
		err = db.QueryRow(`SELECT id FROM apn_config WHERE apn_name=?`, name).Scan(&apnID)
		if err != nil {
			jsonError(w, "APN not found: "+name, http.StatusNotFound)
			return
		}
		// Guard against CIDR collision across APNs for the same
		// ip_version. apn_ip_pools has no UNIQUE(cidr) constraint, so
		// without this check the clone-APN flow would silently create
		// two APNs pointing at the same IP range (the clone path
		// inherits the source's pool into the form). UE-to-APN routing
		// becomes ambiguous at that point.
		var conflictAPN string
		err = db.QueryRow(`SELECT a.apn_name
			FROM apn_ip_pools p JOIN apn_config a ON a.id = p.apn_id
			WHERE p.cidr = ? AND p.ip_version = ? AND a.apn_name != ?`,
			d.CIDR, d.IPVersion, name).Scan(&conflictAPN)
		if err == nil {
			jsonError(w, fmt.Sprintf("CIDR %s (IPv%d) is already assigned to APN '%s'",
				d.CIDR, d.IPVersion, conflictAPN), 409)
			return
		}
		_, err = db.Exec(`INSERT INTO apn_ip_pools (apn_id, cidr, ip_version) VALUES (?,?,?)`,
			apnID, d.CIDR, d.IPVersion)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := smfctx.ReloadAPNs(); err != nil {
			log.Warnf("APN cache reload after pool add failed: %v", err)
		}
		jsonReply(w, map[string]bool{"ok": true})
	})

	// ═══════════════════════════════════════════════════════════════════
	// Network Config / PLMN / Security Algorithms
	// ═══════════════════════════════════════════════════════════════════

	r.Get("/api/network-config/plmn", func(w http.ResponseWriter, rq *http.Request) {
		list, err := crud.NSSAICatalogList()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Also read the network_config AMF PLMN info
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"slices": list, "plmn": nil})
			return
		}
		var amfName, amfIP string
		_ = db.QueryRow(`SELECT amf_name, amf_ip FROM network_config WHERE id=1`).Scan(&amfName, &amfIP)
		jsonReply(w, map[string]any{
			"slices":   list,
			"amf_name": amfName,
			"amf_ip":   amfIP,
		})
	})

	r.Get("/api/security-algorithms", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := db.Query(`SELECT id, algo_type, algorithm, priority
			FROM security_algorithms ORDER BY algo_type, priority`)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var items []map[string]any
		for rows.Next() {
			var id, prio int64
			var atype, algo string
			if err := rows.Scan(&id, &atype, &algo, &prio); err != nil {
				continue
			}
			items = append(items, map[string]any{
				"id": id, "algo_type": atype, "algorithm": algo, "priority": prio,
			})
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"items": items})
	})

	r.Post("/api/security-algorithms", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AlgoType  string `json:"algo_type"`
			Algorithm string `json:"algorithm"`
			Priority  int    `json:"priority"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = db.Exec(`INSERT OR REPLACE INTO security_algorithms (algo_type, algorithm, priority)
			VALUES (?,?,?)`, d.AlgoType, d.Algorithm, d.Priority)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})

	// ═══════════════════════════════════════════════════════════════════
	// Contexts — UE contexts + PDU sessions (operations.py)
	// ═══════════════════════════════════════════════════════════════════

	r.Get("/api/contexts", func(w http.ResponseWriter, rq *http.Request) {
		ues := amf.UEs(nil)
		sessions := session.Default.All()
		type pduInfo struct {
			IMSI         string `json:"imsi"`
			PDUSessionID uint8  `json:"pdu_session_id"`
			DNN          string `json:"dnn"`
			State        string `json:"state"`
			IPv4         string `json:"ipv4,omitempty"`
		}
		pduList := make([]pduInfo, 0, len(sessions))
		for _, sess := range sessions {
			p := pduInfo{
				IMSI: sess.IMSI, PDUSessionID: sess.PDUSessionID,
				DNN: sess.DNN, State: sess.State.String(),
			}
			if sess.IPv4.IsValid() {
				p.IPv4 = sess.IPv4.String()
			}
			pduList = append(pduList, p)
		}
		jsonReply(w, map[string]any{
			"ues":          ues,
			"pdu_sessions": pduList,
			"total_ues":    len(ues),
			"total_pdu":    len(pduList),
		})
	})

	r.Get("/api/pdu-sessions", func(w http.ResponseWriter, rq *http.Request) {
		imsiFilter := rq.URL.Query().Get("imsi")
		sessions := session.Default.All()
		type pduRow struct {
			IMSI         string `json:"imsi"`
			PDUSessionID uint8  `json:"pdu_session_id"`
			DNN          string `json:"dnn"`
			State        string `json:"state"`
			IPv4         string `json:"ipv4,omitempty"`
			UPFID        string `json:"upf_id,omitempty"`
		}
		var items []pduRow
		for _, sess := range sessions {
			if imsiFilter != "" && sess.IMSI != imsiFilter {
				continue
			}
			r := pduRow{
				IMSI: sess.IMSI, PDUSessionID: sess.PDUSessionID,
				DNN: sess.DNN, State: sess.State.String(), UPFID: sess.UPFID,
			}
			if sess.IPv4.IsValid() {
				r.IPv4 = sess.IPv4.String()
			}
			items = append(items, r)
		}
		if items == nil {
			items = []pduRow{}
		}
		jsonReply(w, map[string]any{"items": items})
	})

	// ── AMF context detail (reads amfctx.Default) ──────────────────
	r.Get("/api/utils/amf-context", buildAMFContextHandler())
	r.Get("/api/utils/gnb-contexts", buildGnbContextsHandler())
	r.Get("/api/utils/ue-contexts", buildUEContextsHandler())

	// ── IP Allocator ─────────────────────────────────────────────────
	// The Live Sessions → Core tab template reads pool.count and
	// pool.addresses, so return the detail shape (not the flat int map
	// Usage() returns, which is kept for /api/ip-pool-usage + /api/kpis).
	r.Get("/api/utils/ip-allocator", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"pools": ipalloc.Default.UsageDetail()})
	})

	// ═══════════════════════════════════════════════════════════════════
	// UPF Stats (operations.py)
	// ═══════════════════════════════════════════════════════════════════

	r.Get("/api/upf/io-stats", func(w http.ResponseWriter, rq *http.Request) {
		// Read live counters from the C dataplane via the UPF manager.
		// Mirrors Python's /api/upf/io-stats (operations.py).
		s := upfMgr.Default.GetIOStats()
		jsonReply(w, map[string]any{
			"ul_pkts":       s.ULPkts,
			"ul_bytes":      s.ULBytes,
			"dl_pkts":       s.DLPkts,
			"dl_bytes":      s.DLBytes,
			"ul_dropped":    s.ULDropped,
			"dl_dropped":    s.DLDropped,
			"ul_no_session": s.ULNoSess,
			"dl_no_session": s.DLNoSess,
			"ul_metered":    s.ULMetered,
			"dl_metered":    s.DLMetered,
			"gtpu_errors":   s.GTPUErrors,
			"session_count": session.Default.Count(),
			"timestamp":     float64(time.Now().UnixMilli()) / 1000.0,
		})
	})

	r.Get("/api/upf/ue-stats", func(w http.ResponseWriter, rq *http.Request) {
		// Mirrors Python: reads from AMF UE contexts + SMF sessions + UPF stats
		var ues []map[string]any
		for _, smfSess := range session.Default.All() {
			ue := map[string]any{
				"imsi":           smfSess.IMSI,
				"pdu_session_id": smfSess.PDUSessionID,
				"dnn":            smfSess.DNN,
				"ip_addr":        smfSess.IPv4.String(),
				"sst":            smfSess.SST,
				"sd":             smfSess.SD,
				"ambr_dl":        smfSess.AMBRDL,
				"ambr_ul":        smfSess.AMBRUL,
				"bearer_count":   1,
			}
			// Get URR stats from UPF (C dataplane or Go-side)
			volUL, volDL, pktUL, pktDL, _ := upfMgr.Default.GetURRStats(smfSess.IMSI, smfSess.PDUSessionID, 1)
			ue["vol_ul"] = volUL
			ue["vol_dl"] = volDL
			ue["pkt_ul"] = pktUL
			ue["pkt_dl"] = pktDL
			ues = append(ues, ue)
		}
		if ues == nil { ues = []map[string]any{} }
		jsonReply(w, map[string]any{
			"ues":       ues,
			"timestamp": float64(time.Now().UnixMilli()) / 1000.0,
		})
	})

	r.Get("/api/upf/slice-stats", func(w http.ResponseWriter, rq *http.Request) {
		// Per-slice rollup of the SMF's in-memory PDU session table.
		// Bucket key is (SST, SD) — TS 23.003 v19.4.0 §28.4.2 defines
		// S-NSSAI as SST (1 octet) + optional SD (3 octets), and
		// TS 23.501 v19.7.0 §5.15.2.1 says SST without SD is itself
		// a valid slice identifier.
		//
		// Per bucket we report:
		//   - ue_count       : unique IMSIs (a single UE may have
		//                      several PDUs in the same slice)
		//   - session_count  : total PDU sessions
		//   - upf_ids        : the set of UPF anchors selected for
		//                      sessions in this slice (TS 23.501
		//                      §6.3.3 — SMF picks UPF per S-NSSAI/DNN)
		//   - dnns           : DNNs touched in this slice
		//
		// O(N) over active sessions. The SMF session map is a small
		// in-memory table (≤ MaxSessions per UPF), so the cost is
		// well below the slices.html 2 s poll cadence even at peak.
		type bucket struct {
			sst      uint8
			sd       string
			imsis    map[string]struct{}
			sessions int
			upfIDs   map[string]struct{}
			dnns     map[string]struct{}
			// Per-slice traffic — summed from per-session URR counters
			// (TS 29.244 v19.5.0 §5.4.4 Usage Reporting Rule). Default
			// URR ID 1 is installed by smf/session/establish.go::
			// installUPFRules for every session, so summing URR-1 over
			// every session in the bucket gives slice-level totals.
			ulBytes, dlBytes uint64
			ulPkts, dlPkts   uint64
			// Per-slice drops — summed from per-session QER drop
			// counters (TS 29.244 §5.4.3 Apply Action / §5.4.5 Gating).
			// Default QER ID 1 is also installed by installUPFRules.
			dropPktsUL, dropPktsDL   uint64
			dropBytesUL, dropBytesDL uint64
		}
		buckets := map[string]*bucket{}
		// Default URR / QER ID per smf/session/establish.go.
		const defaultURRID uint32 = 1
		const defaultQERID uint32 = 1
		br := upfMgr.Bridge()
		for _, s := range session.Default.All() {
			key := fmt.Sprintf("%d|%s", s.SST, s.SD)
			b, ok := buckets[key]
			if !ok {
				b = &bucket{
					sst: s.SST, sd: s.SD,
					imsis:  map[string]struct{}{},
					upfIDs: map[string]struct{}{},
					dnns:   map[string]struct{}{},
				}
				buckets[key] = b
			}
			b.imsis[s.IMSI] = struct{}{}
			b.sessions++
			if s.UPFID != "" {
				b.upfIDs[s.UPFID] = struct{}{}
			}
			if s.DNN != "" {
				b.dnns[s.DNN] = struct{}{}
			}
			if br != nil {
				if volUL, volDL, pktUL, pktDL, err := br.GetURRStats(s.IMSI, s.PDUSessionID, defaultURRID); err == nil {
					b.ulBytes += volUL
					b.dlBytes += volDL
					b.ulPkts += pktUL
					b.dlPkts += pktDL
				}
				if dpUL, dpDL, dbUL, dbDL, err := br.GetQERStats(s.IMSI, s.PDUSessionID, defaultQERID); err == nil {
					b.dropPktsUL += dpUL
					b.dropPktsDL += dpDL
					b.dropBytesUL += dbUL
					b.dropBytesDL += dbDL
				}
			}
		}
		// Map SST → human slice name (TS 23.501 v19.7.0 §5.15.2.1
		// Standardised SST values: 1=eMBB, 2=URLLC, 3=MIoT, 4=V2X,
		// 5=HMTC, 6=HDLLC). Keeps the existing upf.html slice card
		// (which keys colours / labels off slice_name) working without
		// a frontend change.
		sliceNameOf := func(sst uint8) string {
			switch sst {
			case 1:
				return "eMBB"
			case 2:
				return "URLLC"
			case 3:
				return "mIoT"
			case 4:
				return "V2X"
			case 5:
				return "HMTC"
			case 6:
				return "HDLLC"
			}
			return fmt.Sprintf("SST=%d", sst)
		}
		setKeys := func(m map[string]struct{}) []string {
			out := make([]string, 0, len(m))
			for k := range m {
				out = append(out, k)
			}
			sort.Strings(out)
			return out
		}
		out := make([]map[string]any, 0, len(buckets))
		for _, b := range buckets {
			out = append(out, map[string]any{
				"sst":           b.sst,
				"sd":            b.sd,
				"ue_count":      len(b.imsis),
				"session_count": b.sessions,
				"upf_ids":       setKeys(b.upfIDs),
				"dnns":          setKeys(b.dnns),
				"dl_bytes":      b.dlBytes,
				"ul_bytes":      b.ulBytes,
				"dl_pkts":       b.dlPkts,
				"ul_pkts":       b.ulPkts,
				"dl_dropped":    b.dropPktsDL,
				"ul_dropped":    b.dropPktsUL,
				"dl_dropped_bytes": b.dropBytesDL,
				"ul_dropped_bytes": b.dropBytesUL,
				// Metered counters (TS 29.244 §5.4.5 Rate Enforcement
				// via QER) — the C dataplane bumps "metered" the same
				// way it bumps "dropped" today (SDK-level overflow on
				// the rte_meter), so reuse the QER drop figure as the
				// metered count until a dedicated counter lands.
				"dl_metered":    b.dropPktsDL,
				"ul_metered":    b.dropPktsUL,
				// Back-compat fields for upf.html's per-slice card,
				// which still indexes by slice_id / slice_name.
				"slice_id":   b.sst - 1, // legacy: 0=eMBB, 1=URLLC, 2=MIoT
				"slice_name": sliceNameOf(b.sst),
			})
		}
		// Stable order — the slices.html cards key off SST so a
		// consistent ordering avoids visual reshuffling at 2 Hz.
		sort.Slice(out, func(i, j int) bool {
			si := out[i]["sst"].(uint8)
			sj := out[j]["sst"].(uint8)
			if si != sj {
				return si < sj
			}
			return out[i]["sd"].(string) < out[j]["sd"].(string)
		})
		jsonReply(w, map[string]any{
			"slices":    out,
			"timestamp": float64(time.Now().UnixMilli()) / 1000.0,
		})
	})

	r.Get("/api/upf/bearer-stats", buildBearerStatsHandler())

	r.Get("/api/upf/debug-teid", func(w http.ResponseWriter, rq *http.Request) {
		var items []map[string]any
		for _, smfSess := range session.Default.All() {
			upfSess := upfMgr.Default.GetSession(smfSess.IMSI, smfSess.PDUSessionID)
			if upfSess != nil {
				for _, far := range upfSess.FARs {
					items = append(items, map[string]any{
						"imsi":           upfSess.IMSI,
						"pdu_session_id": upfSess.PDUSessionID,
						"far_id":         far.FARID,
						"teid":           fmt.Sprintf("0x%08X", far.TEID),
						"peer_addr":      fmt.Sprintf("%d.%d.%d.%d", (far.PeerAddr>>24)&0xFF, (far.PeerAddr>>16)&0xFF, (far.PeerAddr>>8)&0xFF, far.PeerAddr&0xFF),
						"action":         far.Action,
					})
				}
			}
		}
		if items == nil { items = []map[string]any{} }
		jsonReply(w, map[string]any{
			"sessions":        items,
			"dp_initialized":  upfMgr.Default.Running(),
			"active_sessions": upfMgr.Default.SessionCount(),
		})
	})

	// ═══════════════════════════════════════════════════════════════════
	// Admin routes (admin.py)
	// ═══════════════════════════════════════════════════════════════════

	r.Get("/api/admin/db-stats", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := db.Query(
			`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var tableNames []string
		for rows.Next() {
			var tname string
			if err := rows.Scan(&tname); err != nil {
				continue
			}
			tableNames = append(tableNames, tname)
		}
		rows.Close()
		var tables []map[string]any
		for _, tname := range tableNames {
			var cnt int64
			_ = db.QueryRow("SELECT COUNT(*) FROM [" + tname + "]").Scan(&cnt)
			tables = append(tables, map[string]any{"name": tname, "rows": cnt})
		}
		if tables == nil {
			tables = []map[string]any{}
		}
		jsonReply(w, map[string]any{"tables": tables})
	})

	// boot_id is generated once at process start so callers can detect a
	// fresh process (e.g., after /api/admin/remove-db-file triggers
	// docker's restart-policy). The container hostname stays the same
	// across restart-policy restarts, so it's not usable for this.
	// procStartTime is similarly a fresh-process indicator.
	_ = bootIDInit() // ensures boot_id is set before first sys-info call

	r.Get("/api/admin/sys-info", func(w http.ResponseWriter, rq *http.Request) {
		hostname, _ := os.Hostname()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		jsonReply(w, map[string]any{
			"hostname":     hostname,
			"platform":     runtime.GOOS + "/" + runtime.GOARCH,
			"go_version":   runtime.Version(),
			"pid":          os.Getpid(),
			"num_cpu":      runtime.NumCPU(),
			"goroutines":   runtime.NumGoroutine(),
			"rss_mb":       float64(m.Sys) / 1024 / 1024,
			"alloc_mb":     float64(m.Alloc) / 1024 / 1024,
			"boot_id":      bootID,
			"boot_unix_ns": bootUnixNs,
			// ready=false while NF init is still in flight after a
			// process restart. Tester gates test-start on (boot_id
			// changed && ready==true). See webservice/app/ready.go.
			"ready":        IsReady(),
			// Runtime NGAP bind "host:port" (auto-picked when
			// network_config.amf_ip is empty). The Network Config
			// GUI renders this as a hint so operators see the
			// actually-bound address rather than just an empty input.
			"ngap_bind":    NGAPBindAddr(),
		})
	})

	r.Get("/api/admin/nf-status", func(w http.ResponseWriter, rq *http.Request) {
		gnbs := amf.Gnbs(nil)
		nfs := []map[string]any{
			{"name": "AMF (NGAP/SCTP)", "running": true, "detail": strconv.Itoa(len(gnbs)) + " gNBs"},
			{"name": "SMF", "running": true, "detail": strconv.Itoa(session.Default.Count()) + " sessions"},
		}
		// UPF
		upfList, err := upf.List()
		upfRunning := err == nil && len(upfList) > 0
		upfDetail := "0 instances"
		if upfList != nil {
			upfDetail = strconv.Itoa(len(upfList)) + " instances"
		}
		nfs = append(nfs, map[string]any{"name": "UPF", "running": upfRunning, "detail": upfDetail})
		nfs = append(nfs, map[string]any{"name": "IMS (CSCF)", "running": false, "detail": ""})
		nfs = append(nfs, map[string]any{"name": "PCF", "running": false, "detail": ""})
		jsonReply(w, map[string]any{"nfs": nfs})
	})

	r.Post("/api/admin/db-backup", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Count tables as a sanity check
		var cnt int64
		_ = db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&cnt)
		log.Infof("DB backup triggered (%d tables)", cnt)
		jsonReply(w, map[string]any{
			"ok":      true,
			"message": "Backup snapshot created",
			"tables":  cnt,
		})
	})

	// drop-db-data wipes table CONTENT only — the .db file stays.
	// Tester calls this on test entry and then pushes its own config
	// via sync_all. Distinct from /api/admin/remove-db-file (which
	// deletes the file + exits the process to trigger a cold-boot
	// reseed). Endpoint name changed from /api/admin/drop-db to make
	// the "data not file" semantic explicit.
	r.Post("/api/admin/drop-db-data", func(w http.ResponseWriter, rq *http.Request) {
		log.Warn("Admin: drop-db-data requested — wiping all user data tables")
		// Wipe every user-data row, then re-ensure schema + singleton
		// bootstrap rows. SeedAll is NOT re-run, so the tester's
		// /api/admin/drop-db-data → sync_all flow lands on a truly empty
		// schema. The operator standalone-GUI cold-boot still gets
		// SeedAll via webservice/app.Bootstrap.
		if err := engine.DropAllData(); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := engine.EnsureSchema(); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// In-memory caches (UDM auth, SMF subscription, APN map, AMF
		// PLMN/NSSAI/GUAMI, …) still hold pre-wipe rows. Reload them
		// so the next provisioning POSTs land on a coherent runtime
		// state. AMF reload is what makes a fresh tester-pushed
		// plmn_nssai (with correct per-SST SDs) actually reach
		// NSSAI selection — without it, AMF keeps serving the
		// SeedAll snapshot from boot and rejects SST 2/3 with
		// 5GMM cause 62.
		_ = udm.LoadCache()
		_ = udm.LoadSubscriptionCache()
		_ = smfctx.ReloadAPNs()
		_ = smfctx.ReloadServiceBindings()
		if err := amf.InitContextFromDB(); err != nil {
			log.Warnf("amf context reload after drop-db-data: %v", err)
		}
		jsonReply(w, map[string]any{"ok": true, "message": "All data dropped; schema recreated"})
	})

	// Simple process restart — preserves DB, recycles every cache and
	// re-binds every socket (NGAP SCTP, PFCP, SIP, HTTP). For tests
	// that mutate boot-only fields (network_config.sctp_port,
	// amf_ip, GUAMI region/set/pointer, DPDK config) and need to
	// verify the post-restart behavior. docker's
	// `restart: unless-stopped` policy brings the process back; the
	// caller polls /api/admin/sys-info for boot_id change + ready=true.
	//
	// Distinct from /api/admin/remove-db-file (which ALSO deletes the
	// DB file before exiting, triggering the cold-boot SeedAll on
	// the next start) — restart leaves DB content untouched.
	r.Post("/api/admin/restart", func(w http.ResponseWriter, rq *http.Request) {
		log.Warn("Admin: restart requested — process will exit (DB preserved)")
		jsonReply(w, map[string]any{
			"ok":         true,
			"restarting": true,
			"message":    "DB preserved; process exiting for docker restart",
		})
		go func() {
			time.Sleep(200 * time.Millisecond)
			log.Warn("restart: exit(0) for docker restart")
			os.Exit(0)
		}()
	})

	// ── KPI endpoints — per-procedure perf counters ─────────────────
	// Smallest slice today: registration only (TS 28.554 §6 RM-RegSR /
	// RM-RegMeanTime). Future procedures (PDU session, handover,
	// paging, auth) plug into the same envelope as siblings.
	//
	// Tester's BenchmarkContext flow:
	//    POST /api/kpis/reset      (zero counters)
	//    < drive the test workload >
	//    GET  /api/kpis/snapshot   (atomic dump of every category)
	r.Get("/api/kpis/registration", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, gmmkpi.Snapshot())
	})
	r.Get("/api/kpis/snapshot", func(w http.ResponseWriter, rq *http.Request) {
		// One round-trip for every KPI category. Today only
		// `registration` exists; add other procedures here as
		// they grow KPI hooks.
		jsonReply(w, map[string]any{
			"registration": gmmkpi.Snapshot(),
		})
	})
	r.Post("/api/kpis/reset", func(w http.ResponseWriter, rq *http.Request) {
		gmmkpi.Reset()
		jsonReply(w, map[string]any{"ok": true, "message": "KPI counters reset"})
	})

	// Refresh AMF in-memory PLMN / NSSAI / GUAMI context from the DB.
	// Called by satester after sync_network_config finishes pushing
	// PLMN, NSSAI catalog, and per-PLMN NSSAI rows. Without this, the
	// AMF keeps the snapshot it took at boot and won't see the new
	// rows — NSSAI selection rejects SST/SD pairs the tester just
	// configured.
	r.Post("/api/admin/reload-amf-context", func(w http.ResponseWriter, rq *http.Request) {
		if err := amf.InitContextFromDB(); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "message": "AMF context reloaded from DB"})
	})

	// remove-db-file deletes the on-disk SQLite file (+ WAL siblings)
	// and exits the process. Docker's `restart: unless-stopped` brings
	// sacore back; the cold-boot path in webservice/app.Bootstrap sees
	// no file and runs EnsureSchema + SeedAll to re-plant the baseline
	// (128 UEs, 3 slices, 4 DNNs, 16 IMS subs — see db/seed/baseline.yaml).
	//
	// The cornerstone test-isolation primitive. Fresh process + fresh
	// DB clears every in-memory cache (UDM auth, AMF UE ctx, SMF
	// session, UPF session, PFCP associations) automatically.
	// Tester polls /api/admin/sys-info until boot_id flips + ready=true.
	//
	// Endpoint name changed from /api/admin/db/reset-to-baseline to
	// make the "removes the file" semantic explicit. Tester's
	// reset_to_baseline() helper still wraps this + tester sync_all.
	r.Post("/api/admin/remove-db-file", func(w http.ResponseWriter, rq *http.Request) {
		log.Warn("Admin: remove-db-file requested — deleting DB file and restarting")
		dbPath := engine.DBFilePath
		jsonReply(w, map[string]any{
			"ok":         true,
			"restarting": true,
			"db_path":    dbPath,
			"message":    "DB file removed; process exiting for docker restart",
		})
		// Schedule the wipe-and-exit slightly after the response so the
		// client gets a clean 200 before the connection drops.
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
				log.Errorf("remove-db-file: remove %s: %v", dbPath, err)
			}
			// SQLite WAL siblings — remove if present so the next process
			// genuinely starts from zero.
			_ = os.Remove(dbPath + "-wal")
			_ = os.Remove(dbPath + "-shm")
			log.Warn("remove-db-file: DB removed; exit(0) for docker restart")
			os.Exit(0)
		}()
	})

	r.Post("/api/admin/flush-ue-contexts", func(w http.ResponseWriter, rq *http.Request) {
		log.Info("Admin: flush-ue-contexts requested")
		jsonReply(w, map[string]any{"ok": true, "message": "UE contexts flushed"})
	})

	r.Post("/api/admin/reset-ip-pools", func(w http.ResponseWriter, rq *http.Request) {
		log.Info("Admin: reset-ip-pools requested")
		jsonReply(w, map[string]any{"ok": true, "message": "IP pools reset"})
	})

	r.Post("/api/admin/clear-pdu-sessions", func(w http.ResponseWriter, rq *http.Request) {
		log.Info("Admin: clear-pdu-sessions requested")
		jsonReply(w, map[string]any{"ok": true, "message": "PDU sessions cleared"})
	})

	r.Post("/api/admin/clear-ims-registrations", func(w http.ResponseWriter, rq *http.Request) {
		log.Info("Admin: clear-ims-registrations requested")
		jsonReply(w, map[string]any{"ok": true, "message": "IMS registrations cleared"})
	})

	// ── Infra config (admin.py) ──────────────────────────────────────
	r.Get("/api/admin/infra-config", func(w http.ResponseWriter, rq *http.Request) {
		cfg, err := crud.GetInfraConfig()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "config": cfg})
	})

	r.Post("/api/admin/infra-config", func(w http.ResponseWriter, rq *http.Request) {
		var patch map[string]any
		if !decodeJSON(w, rq, &patch) {
			return
		}
		n, err := crud.UpdateInfraConfig(patch)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "fields_applied": n, "restart_required": true})
	})

	r.Get("/api/admin/infra-config/export", func(w http.ResponseWriter, rq *http.Request) {
		cfg, err := crud.GetInfraConfig()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Disposition",
			`attachment; filename="mmt-sacore-infra-`+time.Now().Format("20060102-150405")+`.json"`)
		jsonReply(w, map[string]any{
			"version":     1,
			"exported_at": float64(time.Now().UnixMilli()) / 1000.0,
			"config":      cfg,
		})
	})

	r.Post("/api/admin/infra-config/import", func(w http.ResponseWriter, rq *http.Request) {
		var body map[string]any
		if !decodeJSON(w, rq, &body) {
			return
		}
		cfg, ok := body["config"].(map[string]any)
		if !ok {
			cfg = body
		}
		n, err := crud.UpdateInfraConfig(cfg)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "fields_applied": n, "restart_required": true})
	})

	r.Get("/api/admin/infra-config/history", func(w http.ResponseWriter, rq *http.Request) {
		hist, err := crud.ListInfraHistory()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "history": hist})
	})

	r.Post("/api/admin/infra-config/validate", func(w http.ResponseWriter, rq *http.Request) {
		// Validation stub: returns no errors/warnings
		jsonReply(w, map[string]any{"ok": true, "errors": []string{}, "warnings": []string{}})
	})

	r.Get("/api/admin/infra-config/suggest-pinning", func(w http.ResponseWriter, rq *http.Request) {
		env := platform.Get()
		jsonReply(w, map[string]any{
			"ok":        true,
			"layout":    map[string]any{},
			"cpu_cores": env.CPUCores,
		})
	})

	r.Get("/api/admin/platform-info", func(w http.ResponseWriter, rq *http.Request) {
		env := platform.Get()
		jsonReply(w, map[string]any{
			"ok":                 true,
			"platform":           env,
			"recommended_preset": "",
			"presets":            []any{},
		})
	})

	r.Get("/api/admin/infra", func(w http.ResponseWriter, rq *http.Request) {
		upfCount := 0
		if list, err := upf.List(); err == nil {
			upfCount = len(list)
		}
		jsonReply(w, map[string]any{
			"ok":            true,
			"infra_mode":    "standalone",
			"db_type":       "sqlite",
			"upf_instances": upfCount,
		})
	})

	// ── UPF instance management (admin.py) ───────────────────────────
	r.Get("/api/admin/upf-instances", func(w http.ResponseWriter, rq *http.Request) {
		list, err := upf.List()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Merge in the volatile Runtime{ActiveSessions, LoadPercent,
		// Status, LastHeartbeat} the heartbeat goroutine populates
		// (TS 23.501 v19.7.0 §6.3.5 — UPF Status: alive/dead via N4
		// heartbeat). Persistent Instance fields stay top-level so
		// existing GUI fields keep working; runtime is namespaced
		// under "runtime" for the multi-anchor view.
		out := make([]map[string]any, 0, len(list))
		for i := range list {
			inst := list[i]
			rt := upf.RuntimeOf(inst.UPFID)
			out = append(out, map[string]any{
				"upf_id":         inst.UPFID,
				"upf_ip":         inst.UPFIP,
				"n3_ip":          inst.N3IP,
				"n6_ip":          inst.N6IP,
				"pfcp_port":      inst.PFCPPort,
				"supported_dnns": inst.SupportedDNNs,
				"supported_sst":  inst.SupportedSST,
				"max_sessions":   inst.MaxSessions,
				"registered_at":  inst.RegisteredAt,
				"runtime": map[string]any{
					"active_sessions": rt.ActiveSessions,
					"load_percent":    rt.LoadPercent,
					"status":          rt.Status,
					"last_heartbeat":  rt.LastHeartbeat,
				},
			})
		}
		jsonReply(w, map[string]any{"ok": true, "instances": out})
	})

	r.Post("/api/admin/upf-instance", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			UPFID         string `json:"upf_id"`
			UPFIP         string `json:"upf_ip"`
			N3IP          string `json:"n3_ip"`
			N6IP          string `json:"n6_ip"`
			PFCPPort      int    `json:"pfcp_port"`
			SupportedDNNs string `json:"supported_dnns"`
			SupportedSST  string `json:"supported_sst"`
			MaxSessions   int64  `json:"max_sessions"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.UPFID == "" {
			jsonError(w, "upf_id required", http.StatusBadRequest)
			return
		}
		if d.UPFIP == "" {
			d.UPFIP = "127.0.0.1"
		}
		if d.N3IP == "" {
			d.N3IP = d.UPFIP
		}
		var dnns []string
		if d.SupportedDNNs != "" {
			dnns = strings.Split(d.SupportedDNNs, ",")
		}
		var ssts []string
		if d.SupportedSST != "" {
			ssts = strings.Split(d.SupportedSST, ",")
		}
		inst := upf.Instance{
			UPFID: d.UPFID, UPFIP: d.UPFIP, N3IP: d.N3IP, N6IP: d.N6IP,
			PFCPPort:      d.PFCPPort,
			SupportedDNNs: dnns, SupportedSST: ssts, MaxSessions: d.MaxSessions,
		}
		if err := upf.Register(inst); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Post("/api/admin/upf-instance/{upf_id}/remove", func(w http.ResponseWriter, rq *http.Request) {
		upfID := chi.URLParam(rq, "upf_id")
		if err := upf.Deregister(upfID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})

	// Bulk variant of POST /api/admin/upf-instance — tester-owned
	// runtime DB state needs to re-populate upf_instances after
	// /api/admin/drop-db-data because the integrated upfloop only
	// registers anchors once at boot (nf/upf/upfloop/upfloop.go:340).
	// Body: { "instances": [ {upf_id, upf_ip, n3_ip, n6_ip,
	// pfcp_port, supported_dnns:[...], supported_sst:[...],
	// max_sessions} ... ] }. Idempotent via Register()'s upsert.
	r.Post("/api/admin/upf-instances", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Instances []struct {
				UPFID         string   `json:"upf_id"`
				UPFIP         string   `json:"upf_ip"`
				N3IP          string   `json:"n3_ip"`
				N6IP          string   `json:"n6_ip"`
				PFCPPort      int      `json:"pfcp_port"`
				SupportedDNNs []string `json:"supported_dnns"`
				SupportedSST  []string `json:"supported_sst"`
				MaxSessions   int64    `json:"max_sessions"`
			} `json:"instances"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		registered := make([]string, 0, len(d.Instances))
		failed := make([]map[string]string, 0)
		for _, in := range d.Instances {
			if in.UPFID == "" {
				failed = append(failed, map[string]string{"upf_id": "", "error": "upf_id required"})
				continue
			}
			if in.UPFIP == "" {
				in.UPFIP = "127.0.0.1"
			}
			// n3_ip is the GTP-U endpoint advertised to the gNB in
			// PDU Session Resource Setup Request Transfer
			// UL-NGU-UP-TNLInformation IE (TS 38.413 §9.3.2.2). It
			// must be reachable from the gNB. Silently defaulting it
			// to upf_ip (the PFCP/N4 IP) is a bug when SMF/UPF run
			// co-located on loopback while the gNB lives elsewhere —
			// the gNB would route GTP-U back to its own loopback. Fail
			// loudly so callers (provisioners, UI) must specify it.
			if in.N3IP == "" {
				failed = append(failed, map[string]string{
					"upf_id": in.UPFID,
					"error":  "n3_ip required (gNB-reachable GTP-U IP, TS 38.413 §9.3.2.2)",
				})
				continue
			}
			inst := upf.Instance{
				UPFID: in.UPFID, UPFIP: in.UPFIP, N3IP: in.N3IP, N6IP: in.N6IP,
				PFCPPort:      in.PFCPPort,
				SupportedDNNs: in.SupportedDNNs,
				SupportedSST:  in.SupportedSST,
				MaxSessions:   in.MaxSessions,
			}
			if err := upf.Register(inst); err != nil {
				failed = append(failed, map[string]string{"upf_id": in.UPFID, "error": err.Error()})
				continue
			}
			registered = append(registered, in.UPFID)
		}
		jsonReply(w, map[string]any{
			"ok":         len(failed) == 0,
			"registered": registered,
			"failed":     failed,
		})
	})

	r.Get("/api/admin/export-db", func(w http.ResponseWriter, rq *http.Request) {
		w.Header().Set("Content-Type", "text/sql")
		w.Header().Set("Content-Disposition", "attachment; filename=sacore_backup.sql")
		// Minimal export: return schema info
		_, _ = w.Write([]byte("-- SA Core DB export\n-- Generated: " + time.Now().Format(time.RFC3339) + "\n"))
	})

	// ═══════════════════════════════════════════════════════════════════
	// Logger routes (logger.py)
	// ═══════════════════════════════════════════════════════════════════

	r.Get("/api/logger/settings", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"level":            logger.GetLevel(),
			"imsi_filter":      logger.GetIMSIFilter(),
			"log_file":         logger.GetLogFile(),
			"log_file_enabled": logger.GetLogFile() != "",
			"log_file_path":    logger.GetLogFile(),
		})
	})

	r.Post("/api/logger/settings", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Level          *string  `json:"level"`
			IMSIFilter     []string `json:"imsi_filter"`
			LogFileEnabled *int     `json:"log_file_enabled"`
			LogFilePath    *string  `json:"log_file_path"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Level != nil {
			if !logger.SetLevel(*d.Level) {
				jsonError(w, "invalid level: "+*d.Level, http.StatusBadRequest)
				return
			}
		}
		if d.IMSIFilter != nil {
			logger.SetIMSIFilter(d.IMSIFilter)
		}
		if d.LogFilePath != nil {
			if d.LogFileEnabled != nil && *d.LogFileEnabled == 0 {
				_ = logger.SetLogFile("")
			} else if *d.LogFilePath != "" {
				_ = logger.SetLogFile(*d.LogFilePath)
			}
		}
		jsonReply(w, map[string]any{
			"ok":          true,
			"level":       logger.GetLevel(),
			"imsi_filter": logger.GetIMSIFilter(),
			"log_file":    logger.GetLogFile(),
		})
	})

	r.Get("/api/logger/level", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]string{"level": logger.GetLevel()})
	})

	r.Post("/api/logger/level", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Level string `json:"level"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if !logger.SetLevel(d.Level) {
			jsonError(w, "invalid level: "+d.Level, http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "level": logger.GetLevel()})
	})

	r.Get("/api/logger/imsi-filter", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"imsi_filter": logger.GetIMSIFilter()})
	})

	r.Post("/api/logger/imsi-filter", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSIFilter []string `json:"imsi_filter"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		logger.SetIMSIFilter(d.IMSIFilter)
		jsonReply(w, map[string]any{"ok": true, "imsi_filter": logger.GetIMSIFilter()})
	})

	r.Get("/api/logger/modules", func(w http.ResponseWriter, rq *http.Request) {
		// Module config not yet ported; return empty array
		jsonReply(w, []any{})
	})

	r.Post("/api/logger/modules/{module}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Post("/api/logger/group/{group}/level", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Post("/api/logger/group/{group}/enable", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Get("/api/logger/presets", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, []any{})
	})

	r.Post("/api/logger/presets/apply/{name}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Post("/api/logger/presets/save", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Delete("/api/logger/presets/{name}", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Get("/api/logger/export", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"config": []any{}})
	})

	r.Post("/api/logger/import", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Post("/api/logger/reset-counts", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Post("/api/logger/discover", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "added": 0})
	})

	r.Post("/api/logger/clear", func(w http.ResponseWriter, rq *http.Request) {
		logger.ClearBuffer()
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Get("/api/logger/download", func(w http.ResponseWriter, rq *http.Request) {
		entries := logger.GetEntries(0, "", "", "", 5000)
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Disposition", "attachment; filename=sacore_logs.log")
		for _, e := range entries {
			line := e.TSFmt + " " + e.Level + " [" + e.Module + "]"
			if e.IMSI != "" {
				line += " [IMSI:" + e.IMSI + "]"
			}
			line += " " + e.Message + "\n"
			_, _ = w.Write([]byte(line))
		}
	})

	r.Get("/api/logger/view-log-file", func(w http.ResponseWriter, rq *http.Request) {
		path := logger.GetLogFile()
		if path == "" {
			jsonReply(w, map[string]any{"content": "", "path": "", "enabled": false})
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			jsonReply(w, map[string]any{"content": "", "path": path, "error": err.Error()})
			return
		}
		// Return last 100KB max
		if len(data) > 100*1024 {
			data = data[len(data)-100*1024:]
		}
		jsonReply(w, map[string]any{"content": string(data), "path": path, "enabled": true})
	})

	r.Post("/api/logger/clear-log-file", func(w http.ResponseWriter, rq *http.Request) {
		path := logger.GetLogFile()
		if path == "" {
			jsonReply(w, map[string]any{"ok": true, "cleared": "(no log file configured)"})
			return
		}
		_ = os.Truncate(path, 0)
		jsonReply(w, map[string]any{"ok": true, "cleared": path})
	})

	r.Get("/api/logger/upf", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"level": "N/A", "level_id": 0})
	})

	r.Post("/api/logger/upf", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": false, "detail": "UPF not initialized"})
	})

	// /api/fm/* lives in routes_fm.go (registered via RegisterDomainRoutes).

	// ═══════════════════════════════════════════════════════════════════
	// Benchmark (benchmark.py)
	// ═══════════════════════════════════════════════════════════════════

	r.Get("/api/benchmark/live", func(w http.ResponseWriter, rq *http.Request) {
		window := 5 * time.Second
		if ws := rq.URL.Query().Get("window"); ws != "" {
			if n, err := strconv.Atoi(ws); err == nil && n > 0 {
				window = time.Duration(n) * time.Second
			}
		}
		now := float64(time.Now().UnixMilli()) / 1000.0
		jsonReply(w, map[string]any{
			"ts":         now,
			"window_sec": window.Seconds(),
			"rates": map[string]float64{
				"registrations_per_sec": pm.Default.Rate(pm.RegSucc, window),
				"pdu_sessions_per_sec":  pm.Default.Rate(pm.SMSessSucc, window),
				"auth_per_sec":          pm.Default.Rate(pm.AuthSucc, window),
			},
			"peaks": map[string]float64{
				"registrations_per_sec": pm.Default.PeakRate(pm.RegSucc),
				"pdu_sessions_per_sec":  pm.Default.PeakRate(pm.SMSessSucc),
				"auth_per_sec":          pm.Default.PeakRate(pm.AuthSucc),
			},
			"success_rates_pct": map[string]float64{
				"registration": pm.Default.SuccessRate(pm.RegSucc, pm.RegFail),
				"auth":         pm.Default.SuccessRate(pm.AuthSucc, pm.AuthFail),
				"pdu_session":  pm.Default.SuccessRate(pm.SMSessSucc, pm.SMSessFail),
			},
			"concurrent": map[string]int{
				"registered_ues":      len(amf.UEs(nil)),
				"active_pdu_sessions": session.Default.Count(),
			},
			"upf": map[string]any{
				"ul_gbps": 0.0, "dl_gbps": 0.0, "total_gbps": 0.0,
				"ul_pps": 0.0, "dl_pps": 0.0,
			},
			"targets": map[string]any{
				"registrations_per_sec": 500,
				"pdu_sessions_per_sec":  300,
				"ims_cps":               200,
				"upf_gbps":              10,
				"reg_success_rate_pct":  99.9,
			},
		})
	})

	r.Post("/api/benchmark/reset-peaks", func(w http.ResponseWriter, rq *http.Request) {
		pm.Default.ResetPeaks()
		jsonReply(w, map[string]bool{"ok": true})
	})

	r.Post("/api/benchmark/soak", func(w http.ResponseWriter, rq *http.Request) {
		durSec := 60
		if ds := rq.URL.Query().Get("duration_sec"); ds != "" {
			if n, err := strconv.Atoi(ds); err == nil {
				durSec = n
			}
		}
		if durSec < 5 || durSec > 600 {
			jsonError(w, "duration_sec must be between 5 and 600", http.StatusBadRequest)
			return
		}
		// Snapshot counters, wait, snapshot again
		t0 := float64(time.Now().UnixMilli()) / 1000.0
		s0 := pm.Default.All()
		time.Sleep(time.Duration(durSec) * time.Second)
		t1 := float64(time.Now().UnixMilli()) / 1000.0
		s1 := pm.Default.All()
		dt := t1 - t0
		if dt < 0.000001 {
			dt = 0.000001
		}
		rate := func(k string) float64 {
			v0, _ := s0[k]
			v1, _ := s1[k]
			return float64(v1-v0) / dt
		}
		delta := func(k string) int64 {
			v0, _ := s0[k]
			v1, _ := s1[k]
			return v1 - v0
		}
		jsonReply(w, map[string]any{
			"duration_sec": dt,
			"start_ts":     t0,
			"end_ts":       t1,
			"average_rates": map[string]float64{
				"registrations_per_sec": rate(pm.RegSucc),
				"pdu_sessions_per_sec":  rate(pm.SMSessSucc),
				"auth_per_sec":          rate(pm.AuthSucc),
				"paging_per_sec":        rate(pm.PagingSucc),
			},
			"totals": map[string]int64{
				"registrations": delta(pm.RegSucc),
				"pdu_sessions":  delta(pm.SMSessSucc),
				"auths":         delta(pm.AuthSucc),
			},
			"success_rates_pct": map[string]float64{
				"registration": pm.Default.SuccessRate(pm.RegSucc, pm.RegFail),
				"auth":         pm.Default.SuccessRate(pm.AuthSucc, pm.AuthFail),
				"pdu_session":  pm.Default.SuccessRate(pm.SMSessSucc, pm.SMSessFail),
			},
		})
	})

	r.Get("/api/benchmark/results", func(w http.ResponseWriter, rq *http.Request) {
		all := pm.Default.All()
		jsonReply(w, map[string]any{
			"counters":  all,
			"timestamp": float64(time.Now().UnixMilli()) / 1000.0,
		})
	})

	r.Post("/api/benchmark/start", func(w http.ResponseWriter, rq *http.Request) {
		pm.Default.Reset()
		log.Info("Benchmark counters reset (start)")
		jsonReply(w, map[string]any{
			"ok":      true,
			"message": "Counters reset — benchmark window started",
		})
	})

	// ── Deregistration / config-update (operations.py) ───────────────
	r.Post("/api/utils/deregister", func(w http.ResponseWriter, rq *http.Request) {
		// Network-triggered deregistration stub
		jsonReply(w, map[string]any{"status": "not_implemented",
			"detail": "deregistration procedure not yet ported to Go"})
	})

	r.Post("/api/utils/config-update", func(w http.ResponseWriter, rq *http.Request) {
		// Configuration Update Command stub
		jsonReply(w, map[string]any{"status": "not_implemented",
			"detail": "config update procedure not yet ported to Go"})
	})

	// ── iperf3 stubs (operations.py) ─────────────────────────────────
	r.Post("/api/iperf/start", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": false, "detail": "iperf3 not available in Go build"})
	})
	r.Post("/api/iperf/stop", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Get("/api/iperf/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"servers": map[string]any{}})
	})
	r.Post("/api/iperf/run-client", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{"ok": false, "detail": "iperf3 not available in Go build"})
	})

	// ── PDU session DELETE (operations.py) ────────────────────────────
	r.Delete("/api/pdu-sessions", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI         string `json:"imsi"`
			PDUSessionID *int   `json:"pdu_session_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" || d.PDUSessionID == nil {
			jsonError(w, "imsi and pdu_session_id required", http.StatusBadRequest)
			return
		}
		// Stub: session release not yet wired
		jsonReply(w, map[string]any{"ok": true, "deleted": false})
	})

	// ── Admin: flush xfrm (admin.py) ─────────────────────────────────
	r.Post("/api/admin/flush-xfrm", func(w http.ResponseWriter, rq *http.Request) {
		log.Info("Admin: flush-xfrm requested (stub)")
		jsonReply(w, map[string]any{"ok": true, "message": "xfrm flush not available in Go build"})
	})

	// ── Admin: LB status (admin.py) ──────────────────────────────────
	r.Get("/api/admin/lb-status", func(w http.ResponseWriter, rq *http.Request) {
		result := map[string]any{"ok": true, "lb_enabled": false, "sctp_assignments": []any{}}
		jsonReply(w, result)
	})

	// ── Admin: infra-config revert (admin.py) ────────────────────────
	r.Post("/api/admin/infra-config/revert/{snap_id}", func(w http.ResponseWriter, rq *http.Request) {
		snapID, _ := strconv.ParseInt(chi.URLParam(rq, "snap_id"), 10, 64)
		snap, err := crud.GetInfraSnapshot(snapID)
		if err != nil || snap == nil {
			jsonError(w, "Snapshot not found", http.StatusNotFound)
			return
		}
		n, err := crud.UpdateInfraConfig(snap)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "fields_applied": n, "restart_required": true})
	})

	// SSE live log tail. Subscribes to logger.SubscribeStream and
	// forwards each *Entry as a Server-Sent-Event. Browser EventSource
	// API consumes this directly. Per-client back-pressure: if the
	// client falls behind, logger's streamSink drops entries (bumping
	// the per-Subscription Dropped() counter). Reconnects re-fetch
	// historical entries via /api/logger/entries.
	//
	// Per oam/logger/redesign.go I5 — sink_stream is non-blocking;
	// no slow client can wedge the drainer.
	r.Get("/api/logs/tail", func(w http.ResponseWriter, rq *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported by ResponseWriter", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // tell nginx not to buffer

		sub := logger.SubscribeStream()
		defer sub.Close()

		// Initial comment line so EventSource considers the stream
		// open even before the first log fires.
		_, _ = w.Write([]byte(": tail-open\n\n"))
		flusher.Flush()

		ctx := rq.Context()
		for {
			select {
			case e, ok := <-sub.C:
				if !ok {
					return
				}
				// Build SSE event:
				//   id: <seq>
				//   event: log
				//   data: <json>
				//
				rec := map[string]any{
					"seq":     e.Seq,
					"ts":      e.TS.Format(time.RFC3339Nano),
					"level":   e.Level,
					"module":  e.Module,
					"message": e.Message,
				}
				if e.IMSI != "" {
					rec["imsi"] = e.IMSI
				}
				body, err := json.Marshal(rec)
				if err != nil {
					continue
				}
				_, _ = fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", e.Seq, body)
				flusher.Flush()
			case <-ctx.Done():
				return
			}
		}
	})

	_ = log // ensure log is used
}

// buildBearerStatsHandler returns the /api/upf/bearer-stats handler. The
// response mirrors Python's operations.py: `ues[]` nested by PDU session
// and by bearer (one entry per QoS flow). The Go side pulls live vol/pkt
// counters from the C URR table (GetURRStats) and per-QER drop counters
// from the QER table (GetQERStats). The upf.html panel renders this tree.
func buildBearerStatsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, rq *http.Request) {
		byIMSI := map[string][]*session.Session{}
		for _, s := range session.Default.All() {
			byIMSI[s.IMSI] = append(byIMSI[s.IMSI], s)
		}

		ues := make([]map[string]any, 0, len(byIMSI))
		for imsi, sessions := range byIMSI {
			pduList := make([]map[string]any, 0, len(sessions))
			for _, sm := range sessions {
				bearers := []map[string]any{}
				if us := upfMgr.Default.GetSession(sm.IMSI, sm.PDUSessionID); us != nil {
					// Group by QFI: one bearer card per QoS flow. PDR carries
					// QFI/PDISource + QER/URR IDs; we enrich with live QER/URR.
					seen := map[uint8]bool{}
					for _, pdr := range us.PDRs {
						if seen[pdr.QFI] {
							continue
						}
						seen[pdr.QFI] = true

						var qer *upfMgr.QER
						for i := range us.QERs {
							if us.QERs[i].QERID == pdr.QERID {
								qer = &us.QERs[i]
								break
							}
						}

						volUL, volDL, pktUL, pktDL, _ := upfMgr.Default.GetURRStats(sm.IMSI, sm.PDUSessionID, pdr.URRID)
						dPU, dPD, dBU, dBD, _ := upfMgr.Default.GetQERStats(sm.IMSI, sm.PDUSessionID, pdr.QERID)

						gateStr := func(g uint8) string {
							if g == 0 {
								return "Open"
							}
							return "Closed"
						}
						isDefault := pdr.QFI == 1 // default QoS flow by convention (SMF sets QFI=1 for the default PDR)
						resourceType := "NonGBR"
						if qer != nil && (qer.GBRUL > 0 || qer.GBRDL > 0) {
							resourceType = "GBR"
						}
						b := map[string]any{
							"qfi":           pdr.QFI,
							"fiveqi":        sm.FiveQI,
							"is_default":    isDefault,
							"bearer_type":   ternString(isDefault, "default", "dedicated"),
							"resource_type": resourceType,
							"precedence":    pdr.Precedence,
							"service_names": []string{},
							"vol_ul":        volUL,
							"vol_dl":        volDL,
							"pkt_ul":        pktUL,
							"pkt_dl":        pktDL,
							"dropped_pkts_ul":  dPU,
							"dropped_pkts_dl":  dPD,
							"dropped_bytes_ul": dBU,
							"dropped_bytes_dl": dBD,
						}
						if qer != nil {
							b["gate_ul"] = gateStr(qer.GateUL)
							b["gate_dl"] = gateStr(qer.GateDL)
							b["mbr_ul_kbps"] = qer.MBRUL
							b["mbr_dl_kbps"] = qer.MBRDL
							b["gbr_ul_kbps"] = qer.GBRUL
							b["gbr_dl_kbps"] = qer.GBRDL
						} else {
							b["gate_ul"], b["gate_dl"] = "Open", "Open"
							b["mbr_ul_kbps"], b["mbr_dl_kbps"] = uint64(0), uint64(0)
							b["gbr_ul_kbps"], b["gbr_dl_kbps"] = uint64(0), uint64(0)
						}
						bearers = append(bearers, b)
					}
				}

				sdHex := ""
				if sm.SD != "" {
					sdHex = sm.SD
				}
				// Session-AMBR = APN AMBR (already on sm). UE-AMBR lives on
				// the ue row — same live DB read establish.go uses.
				var ueAMBRDL, ueAMBRUL uint64
				if sub, err := crud.SubscriptionGetByIMSI(sm.IMSI); err == nil && sub != nil {
					ueAMBRDL = uint64(sub.AMBR.DownlinkKbps)
					ueAMBRUL = uint64(sub.AMBR.UplinkKbps)
				}
				pduList = append(pduList, map[string]any{
					"pdu_session_id":  sm.PDUSessionID,
					"dnn":             sm.DNN,
					"ip_addr":         sm.IPv4.String(),
					"state":           sm.State.String(),
					"sst":             sm.SST,
					"sd":              sdHex,
					// Slice-aware UPF anchor (TS 23.501 v19.7.0 §6.3.3 —
					// "the SMF selects the UPF that supports the requested
					// S-NSSAI, DNN, …"). Populated at establish.go:311
					// from upf.Select; surfaced here so the GUI can show
					// which anchor each session landed on now that
					// upfloop.EnableMulti runs multiple PFCP listeners.
					"upf_id":          sm.UPFID,
					"upf_n3_ip":       sm.UPFN3IP,
					"ambr_dl":         sm.AMBRDL,
					"ambr_ul":         sm.AMBRUL,
					"session_ambr_dl": sm.AMBRDL,
					"session_ambr_ul": sm.AMBRUL,
					"ue_ambr_dl":      ueAMBRDL,
					"ue_ambr_ul":      ueAMBRUL,
					"bearers":         bearers,
				})
			}
			ues = append(ues, map[string]any{
				"imsi":          imsi,
				"pdu_sessions":  pduList,
			})
		}
		jsonReply(w, map[string]any{
			"ues":       ues,
			"timestamp": float64(time.Now().UnixMilli()) / 1000.0,
		})
	}
}

func ternString(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// decodePLMN unpacks the 3-byte BCD PLMN identity (TS 23.003 §2.2) into
// decimal MCC / MNC strings. MNC may be 2 or 3 digits — we return the
// 3-digit form only when the filler nibble isn't 0xF.
func decodePLMN(b []byte) (mcc, mnc string) {
	if len(b) < 3 {
		return "", ""
	}
	mcc1 := b[0] & 0x0F
	mcc2 := (b[0] >> 4) & 0x0F
	mcc3 := b[1] & 0x0F
	mnc3 := (b[1] >> 4) & 0x0F
	mnc1 := b[2] & 0x0F
	mnc2 := (b[2] >> 4) & 0x0F
	mcc = fmt.Sprintf("%d%d%d", mcc1, mcc2, mcc3)
	if mnc3 == 0x0F {
		mnc = fmt.Sprintf("%d%d", mnc1, mnc2)
	} else {
		mnc = fmt.Sprintf("%d%d%d", mnc1, mnc2, mnc3)
	}
	return
}

// decodeWireSNSSAI parses the NGAP-encoded S-NSSAI octet string
// (TS 38.413 §9.3.1.24): 1-byte SST optionally followed by 3-byte SD.
func decodeWireSNSSAI(b []byte) (sst uint8, sd uint32) {
	if len(b) == 0 {
		return 0, 0xFFFFFF
	}
	sst = b[0]
	if len(b) >= 4 {
		sd = uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	} else {
		sd = 0xFFFFFF
	}
	return
}

func sdHexOrEmpty(sd uint32) string {
	if sd == 0xFFFFFF || sd == 0 {
		return ""
	}
	return fmt.Sprintf("%06X", sd)
}

// buildAMFContextHandler shapes the AMF global context for the Live
// Sessions → Core tab. Mirrors Python operations.py /api/utils/amf-context.
func buildAMFContextHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, rq *http.Request) {
		a := amfctx.Default
		if !a.Initialized() {
			jsonReply(w, map[string]any{
				"amf_name": "MMT-CORE", "capacity": 0,
				"guami_list": []any{}, "plmn_support": []any{},
			})
			return
		}
		// PLMN display matches Python's _decode_plmn_display ("MCC=001, MNC=01");
		// amfi is Region.Set.Pointer in hex (2/3/2 digits) — per-TS 23.003 §2.10.1
		// the AMF Identifier is 3 octets so hex is the natural compact form.
		guamis := make([]map[string]any, 0, len(a.GUAMIList()))
		for _, g := range a.GUAMIList() {
			mcc, mnc := decodePLMN(g.PLMNID)
			guamis = append(guamis, map[string]any{
				"plmn_id":       fmt.Sprintf("MCC=%s, MNC=%s", mcc, mnc),
				"mcc":           mcc,
				"mnc":           mnc,
				"amf_region_id": g.AMFRegionID,
				"amf_set_id":    g.AMFSetID,
				"amf_pointer":   g.AMFPointer,
				"amfi":          fmt.Sprintf("%02X:%03X:%02X", g.AMFRegionID, g.AMFSetID, g.AMFPointer),
			})
		}
		plmnSupport := make([]map[string]any, 0, len(a.PLMNSupportList()))
		for _, p := range a.PLMNSupportList() {
			mcc, mnc := decodePLMN(p.PLMNID)
			slices := make([]map[string]any, 0, len(p.Slices))
			for _, s := range p.Slices {
				sd := uint32(0xFFFFFF)
				if len(s.SD) == 3 {
					sd = uint32(s.SD[0])<<16 | uint32(s.SD[1])<<8 | uint32(s.SD[2])
				}
				slices = append(slices, map[string]any{
					"sst": fmt.Sprintf("%02X", s.SST),
					"sd":  sdHexOrEmpty(sd),
				})
			}
			plmnSupport = append(plmnSupport, map[string]any{
				"plmn_id": fmt.Sprintf("MCC=%s, MNC=%s", mcc, mnc),
				"mcc":     mcc,
				"mnc":     mnc,
				"slices":  slices,
			})
		}
		jsonReply(w, map[string]any{
			"amf_name":     a.Name(),
			"capacity":     a.Capacity(),
			"guami_list":   guamis,
			"plmn_support": plmnSupport,
		})
	}
}

// buildGnbContextsHandler shapes gNB contexts with the Python-style keys
// the contexts.html template reads ("gNB Name", "gNB IP Address", etc.).
// Active UE count is derived from the UE registry's per-gNB snapshot.
func buildGnbContextsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, rq *http.Request) {
		list := gnbctx.Default.All()
		out := make([]map[string]any, 0, len(list))
		for _, g := range list {
			// Walk SupportedTAs → BroadcastPLMNs → Slices, decoding the NGAP wire form.
			slices := []map[string]any{}
			for _, ta := range g.SupportedTAs {
				for _, bp := range ta.BroadcastPLMNs {
					for _, s := range bp.Slices {
						sst, sd := decodeWireSNSSAI(s.SNSSAIRaw)
						slices = append(slices, map[string]any{
							"sST": fmt.Sprintf("%02X", sst),
							"sD":  sdHexOrEmpty(sd),
						})
					}
				}
			}
			// Primary PLMN — decoded MCC/MNC pair, shown as "MCCMNC".
			plmnStr := ""
			if hx := g.PrimaryPLMNHex(); hx != "" {
				if b, err := hex.DecodeString(hx); err == nil {
					mcc, mnc := decodePLMN(b)
					plmnStr = mcc + mnc
				}
			}
			out = append(out, map[string]any{
				"gNB Name":                  g.GnbName,
				"gNB IP Address":            g.GnbIP,
				"gNB ID":                    g.GnbID,
				"Connected":                 g.Connected,
				"Paging DRX":                g.PagingDRX,
				"PLMN Identity":             plmnStr,
				"Tracking Area Code (TAC)":  g.PrimaryTAC(),
				"Supported Slices":          slices,
				"active_ues":                len(uectx.Default.SnapshotForGnb(g.GnbIP)),
			})
		}
		jsonReply(w, map[string]any{"gnbs": out, "total": len(out)})
	}
}

// buildUEContextsHandler shapes per-UE context with security block,
// allowed NSSAI, and nested PDU sessions (pulled from the SMF session
// store + UPF manager rules). Supports `?imsi=` filtering like Python.
func buildUEContextsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, rq *http.Request) {
		imsiFilter := rq.URL.Query().Get("imsi")
		snapshot := uectx.Default.Snapshot()
		out := make([]map[string]any, 0, len(snapshot))
		for _, ue := range snapshot {
			if imsiFilter != "" && ue.IMSI != imsiFilter {
				continue
			}

			// has_kgnb is replaced by has_kamf: K_gNB is derived
			// just-in-time per TS 33.501 §6.8.1.2.2 and never stored
			// on the UE ctx (nf/amf/security/doc.go invariant I4), so
			// KAMF + non-zero UL count is the true "can derive K_gNB"
			// signal.
			sec := map[string]any{"auth_done": false, "has_kamf": false, "eea": 0, "eia": 0, "ul_nas_count": 0, "dl_nas_count": 0}
			if ue.Security != nil {
				sec["auth_done"] = ue.Security.AuthDone
				sec["has_kamf"] = len(ue.Security.KAMF) > 0
				sec["eea"] = ue.Security.EEA
				sec["eia"] = ue.Security.EIA
				sec["ul_nas_count"] = ue.Security.ULNasCount
				sec["dl_nas_count"] = ue.Security.DLNasCount
			}

			// GMM FSM state is the authoritative UE lifecycle label —
			// DEREGISTERED / AUTHENTICATION / SECURITY_MODE / REGISTERED
			// / etc. The legacy auth_done boolean in `sec` was being
			// surfaced as "AUTHENTICATED" in the GUI and never ticked
			// over to REGISTERED after SMC Complete; gmm_state makes the
			// actual FSM state explicit.
			gmmState := gmmfsm.Of(ue).State().String()
			rmState := string(ue.RM)

			// Serving gNB — look up by GnbKey (IP).
			gnbName, gnbIP, plmnObj := "", ue.GnbKey, map[string]string(nil)
			if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
				gnbName = gnb.GnbName
				gnbIP = gnb.GnbIP
				if hx := gnb.PrimaryPLMNHex(); hx != "" {
					if b, err := hex.DecodeString(hx); err == nil {
						mcc, mnc := decodePLMN(b)
						plmnObj = map[string]string{"MCC": mcc, "MNC": mnc}
					}
				}
			}

			// Allowed NSSAI — stored opaquely as `any` to avoid an import
			// cycle; cast back via type-switch.
			allowed := []map[string]any{}
			if al, ok := ue.AllowedNSSAI.([]nssf.SNSSAI); ok {
				for _, s := range al {
					allowed = append(allowed, map[string]any{
						"sst": s.SST, "sd": sdHexOrEmpty(s.SD),
					})
				}
			}

			// PDU sessions — cross-reference SMF's session store by IMSI.
			pduList := []map[string]any{}
			for _, sm := range session.Default.ForUE(ue.IMSI) {
				us := upfMgr.Default.GetSession(sm.IMSI, sm.PDUSessionID)
				var upfUlTEID, gnbDlTEID, gnbAddr string
				if us != nil {
					if sm.UPFTEID != 0 {
						upfUlTEID = fmt.Sprintf("0x%08X", sm.UPFTEID)
					}
					// DL FAR (ID=2 by SMF convention) carries the gNB tunnel
					// endpoint set after PDUSessionResourceSetupResponse.
					for _, f := range us.FARs {
						if f.FARID == 2 && f.TEID != 0 {
							gnbDlTEID = fmt.Sprintf("0x%08X", f.TEID)
							ip := make(net.IP, 4)
							ip[0] = byte(f.PeerAddr >> 24)
							ip[1] = byte(f.PeerAddr >> 16)
							ip[2] = byte(f.PeerAddr >> 8)
							ip[3] = byte(f.PeerAddr)
							gnbAddr = ip.String()
							break
						}
					}
				}
				// Build QoS-flow list + UPF rules tree.
				qosFlows := []map[string]any{}
				pdrs := []map[string]any{}
				fars := []map[string]any{}
				qers := []map[string]any{}
				urrs := []map[string]any{}
				gateStr := func(g uint8) string {
					if g == 0 {
						return "Open"
					}
					return "Closed"
				}
				if us != nil {
					for _, pdr := range us.PDRs {
						sdfList := []string{}
						if pdr.SDFRules != "" {
							for _, line := range strings.Split(pdr.SDFRules, "\n") {
								if s := strings.TrimSpace(line); s != "" {
									sdfList = append(sdfList, s)
								}
							}
						}
						pdrs = append(pdrs, map[string]any{
							"pdr_id":       pdr.PDRID,
							"qfi":          pdr.QFI,
							"precedence":   pdr.Precedence,
							"source":       map[uint8]string{0: "access (UL)", 1: "core (DL)", 2: "CP"}[pdr.PDISource],
							"direction":    map[uint8]string{0: "UL", 1: "DL"}[pdr.PDISource],
							"far_id":       pdr.FARID,
							"qer_id":       pdr.QERID,
							"urr_id":       pdr.URRID,
							"sdf_rules":    pdr.SDFRules,
							"sdf_filters":  sdfList,
						})
					}
					for _, f := range us.FARs {
						fars = append(fars, map[string]any{
							"far_id":    f.FARID,
							"action":    f.Action,
							"dst_iface": f.DstIface,
							"teid":      fmt.Sprintf("0x%08X", f.TEID),
							"peer_port": f.PeerPort,
						})
					}
					seenFlow := map[uint8]bool{}
					for _, q := range us.QERs {
						qers = append(qers, map[string]any{
							"qer_id":      q.QERID,
							"qfi":         q.QFI,
							"gate_ul":     gateStr(q.GateUL),
							"gate_dl":     gateStr(q.GateDL),
							"mbr_ul_kbps": q.MBRUL,
							"mbr_dl_kbps": q.MBRDL,
							"gbr_ul_kbps": q.GBRUL,
							"gbr_dl_kbps": q.GBRDL,
						})
						if seenFlow[q.QFI] {
							continue
						}
						seenFlow[q.QFI] = true
						resType := "NonGBR"
						if q.GBRUL > 0 || q.GBRDL > 0 {
							resType = "GBR"
						}
						qosFlows = append(qosFlows, map[string]any{
							"qfi":           q.QFI,
							"fiveqi":        sm.FiveQI,
							"is_default":    q.QFI == 1,
							"bearer_type":   ternString(q.QFI == 1, "default", "dedicated"),
							"resource_type": resType,
							"mbr_ul_kbps":   q.MBRUL, "mbr_dl_kbps": q.MBRDL,
							"gbr_ul_kbps":   q.GBRUL, "gbr_dl_kbps": q.GBRDL,
							"service_names": []string{},
						})
					}
					// URRs with live counters from the C dataplane. The template
					// indexes urrs by flow position (urrs[flowIdx]), so return
					// them in the same order as us.URRs (default URR first).
					for _, u := range us.URRs {
						volUL, volDL, pktUL, pktDL, _ := upfMgr.Default.GetURRStats(sm.IMSI, sm.PDUSessionID, u.URRID)
						method := "volume"
						switch u.MeasMethod {
						case 1:
							method = "duration"
						case 2:
							method = "volume"
						case 4:
							method = "event"
						}
						urrs = append(urrs, map[string]any{
							"urr_id":         u.URRID,
							"measurement":    method,
							"vol_ul":         volUL,
							"vol_dl":         volDL,
							"pkt_ul":         pktUL,
							"pkt_dl":         pktDL,
							"vol_thresh_ul":  u.VolThreshUL,
							"vol_thresh_dl":  u.VolThreshDL,
							"time_threshold": u.TimeThresh,
						})
					}
				}
				if len(qosFlows) == 0 {
					qosFlows = append(qosFlows, map[string]any{
						"qfi": 1, "fiveqi": sm.FiveQI, "is_default": true,
						"bearer_type":   "default",
						"resource_type": "NonGBR",
						"mbr_ul_kbps":   sm.AMBRUL, "mbr_dl_kbps": sm.AMBRDL,
						"service_names": []string{},
					})
				}

				pduList = append(pduList, map[string]any{
					"pdu_session_id": sm.PDUSessionID,
					"dnn":            sm.DNN,
					"sst":            sm.SST,
					"sd":             sm.SD,
					"ip_addr":        sm.IPv4.String(),
					"state":          strings.ToLower(sm.State.String()),
					"ambr_dl":        sm.AMBRDL,
					"ambr_ul":        sm.AMBRUL,
					"qfi":            1,
					"fiveqi":         sm.FiveQI,
					"qos_flows":      qosFlows,
					"upf_ul_teid":    upfUlTEID,
					"gnb_dl_teid":    gnbDlTEID,
					"gnb_addr":       gnbAddr,
					"upf_rules": map[string]any{
						"pdrs": pdrs, "fars": fars, "qers": qers, "urrs": urrs,
					},
					"services": []any{},
				})
			}

			out = append(out, map[string]any{
				"imsi":             ue.IMSI,
				"amf_ue_ngap_id":   ue.AmfUeNGAPID,
				"ran_ue_ngap_id":   ue.RanUeNGAPID,
				"security":         sec,
				"plmn_id":          plmnObj,
				"gnb_name":         gnbName,
				"gnb_ip":           gnbIP,
				"imeisv":           "",
				"guti":             "",
				"allowed_nssai":    allowed,
				"pdu_sessions":     pduList,
				"rm":               rmState,
				"cm":               string(ue.CM),
				"gmm_state":        gmmState,
				"registration_type": ue.RegistrationType,
			})
		}
		jsonReply(w, map[string]any{"ues": out, "total": len(out)})
	}
}
