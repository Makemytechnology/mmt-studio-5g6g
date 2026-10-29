// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Provisioning REST routes — UE auth / subscription / services / bindings /
// nssai / charging / network config / infra config. Go port of
// webservice/routes/provisioning.py + operations.py.
//
// Every endpoint mirrors the Python FastAPI path exactly so the existing
// HTML panels (which poll these URIs via fetch()) continue to work without
// any frontend change.
package app

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/db/engine"
	gmmfsm "github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
	sctpfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/sctpfsm"
	pfcpfsm "github.com/mmt/mmt-studio-core/nf/smf/pfcp/fsm"
	smfctx "github.com/mmt/mmt-studio-core/nf/smf/ctx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/nf/smf/session/pti"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// RegisterProvisioningRoutes wires the UE / network / SMF endpoints.
func (s *Server) RegisterProvisioningRoutes() {
	r := s.Router
	log := logger.Get("webservice.api")

	// ── UE Auth ──────────────────────────────────────────────────────
	r.Get("/api/ue/auth/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		auth, err := crud.AuthGetByIMSI(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if auth == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, auth)
	})
	r.Post("/api/ue/auth", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI         string `json:"imsi"`
			OpType       string `json:"op_type"`
			OP           string `json:"op"`
			K            string `json:"k"`
			MSISDN       string `json:"msisdn"`
			AMF          string `json:"amf"`
			SQN          int64  `json:"sqn"`
			SUCIProfile  string `json:"suci_profile"`
			HNPrivateKey string `json:"hn_private_key"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if strings.TrimSpace(d.IMSI) == "" {
			jsonError(w, "IMSI required", http.StatusBadRequest)
			return
		}
		if err := crud.AuthUpsert(crud.AuthUpsertIn{
			IMSI: d.IMSI, MSISDN: d.MSISDN, OpType: d.OpType,
			OP: d.OP, K: d.K, AMF: d.AMF, SQN: d.SQN,
			SUCIProfile: d.SUCIProfile, HNPrivateKey: d.HNPrivateKey,
		}); err != nil {
			log.Warnf("POST /api/ue/auth: %v", err)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Refresh the UDM auth cache so the new K/OP/AMF take effect
		// on the subscriber's next registration without a restart.
		if _, err := udm.ReloadAuth(d.IMSI); err != nil {
			log.Warnf("udm.ReloadAuth(%s): %v", d.IMSI, err)
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": d.IMSI})
	})
	r.Delete("/api/ue/auth/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		ok, err := crud.AuthDelete(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		udm.DropAuth(imsi)
		jsonReply(w, map[string]bool{"ok": true, "deleted": ok})
	})

	// ── UE Subscription (AMBR) ───────────────────────────────────────
	r.Get("/api/ue/subscription/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		sub, err := crud.SubscriptionGetByIMSI(chi.URLParam(rq, "imsi"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sub == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, sub)
	})

	// ── UE List (dashboard) ──────────────────────────────────────────
	r.Get("/api/ue/list", func(w http.ResponseWriter, rq *http.Request) {
		list, err := crud.UEList()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, list)
	})
	r.Get("/api/ue", func(w http.ResponseWriter, rq *http.Request) {
		list, err := crud.UEList()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"items": list})
	})
	r.Delete("/api/ue", func(w http.ResponseWriter, rq *http.Request) {
		var d struct{ IMSI string `json:"imsi"` }
		if !decodeJSON(w, rq, &d) {
			return
		}
		n, err := crud.UEDeleteByIMSI(d.IMSI)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "deleted": n})
	})

	// ── UE Subscription CRUD ─────────────────────────────────────────
	r.Post("/api/ue/subscription", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI string `json:"imsi"`
			AMBR struct {
				DownlinkKbps int64 `json:"downlink_kbps"`
				UplinkKbps   int64 `json:"uplink_kbps"`
			} `json:"ambr"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := crud.SubscriptionUpsert(d.IMSI, d.AMBR.DownlinkKbps, d.AMBR.UplinkKbps); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := udm.ReloadSubscription(d.IMSI); err != nil {
			logger.Get("webservice").Warnf("udm.ReloadSubscription(%s): %v", d.IMSI, err)
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	r.Delete("/api/ue/subscription/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		ok, err := crud.SubscriptionDelete(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// SubscriptionDelete resets AMBR to 0 rather than removing the
		// row; refresh so the cache reflects the 'unlimited' state.
		if err := udm.ReloadSubscription(imsi); err != nil {
			logger.Get("webservice").Warnf("udm.ReloadSubscription(%s) after delete: %v", imsi, err)
		}
		jsonReply(w, map[string]bool{"ok": true, "deleted": ok})
	})

	// ── UE Clone ─────────────────────────────────────────────────────
	r.Post("/api/ue/clone", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SourceIMSI string `json:"source_imsi"`
			NewIMSI    string `json:"new_imsi"`
			NewMSISDN  string `json:"new_msisdn"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// Deep clone: auth + subscription AMBR + subscribed NSSAI +
		// slice-DNN authorisations + service bindings. All copied in
		// a single transaction so partial clones can't leave a UE
		// stranded with auth but no bindings.
		if _, err := crud.UECloneDeep(d.SourceIMSI, d.NewIMSI, d.NewMSISDN); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// New UE means a new (IMSI,DNN) → default-5QI entry is now in
		// the service_bindings table; drop the SMF cache so the clone
		// starts with the full QoS profile on its first PDU session.
		if err := smfctx.ReloadServiceBindings(); err != nil {
			logger.Get("webservice").Warnf("reload bindings after clone: %v", err)
		}
		if _, err := udm.ReloadAuth(d.NewIMSI); err != nil {
			logger.Get("webservice").Warnf("reload auth after clone: %v", err)
		}
		if err := udm.ReloadSubscription(d.NewIMSI); err != nil {
			logger.Get("webservice").Warnf("reload subscription after clone: %v", err)
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": d.NewIMSI})
	})

	// Bulk clone — take one template IMSI and a starting IMSI + count,
	// deep-clone sequentially. MSISDN for each clone is IMSI[5:] (drop
	// MCC+MNC), matching the seed-data convention. Safe to re-run: each
	// deep-clone step is an upsert / INSERT OR IGNORE.
	r.Post("/api/ue/clone/range", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SourceIMSI string `json:"source_imsi"`
			StartIMSI  string `json:"start_imsi"`
			Count      int    `json:"count"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		n, err := crud.UECloneRange(d.SourceIMSI, d.StartIMSI, d.Count)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := smfctx.ReloadServiceBindings(); err != nil {
			logger.Get("webservice").Warnf("reload bindings after range clone: %v", err)
		}
		// Rebuild the whole auth + subscription caches — simpler than
		// reloading N IMSIs individually, and a range clone is a
		// cold-path admin action.
		if err := udm.LoadCache(); err != nil {
			logger.Get("webservice").Warnf("udm.LoadCache after range clone: %v", err)
		}
		if err := udm.LoadSubscriptionCache(); err != nil {
			logger.Get("webservice").Warnf("udm.LoadSubscriptionCache after range clone: %v", err)
		}
		jsonReply(w, map[string]any{"ok": true, "created": n})
	})

	// Bulk delete — symmetric to /api/ue/clone/range. Walks the IMSI
	// range (start_imsi … +count-1) and removes each row via the
	// same UEDeleteByIMSI cascade used by the single-UE delete path,
	// so foreign-key cleanup of auth / nssai / slice_dnn / bindings
	// is identical. Idempotent: missing IMSIs are silently skipped.
	r.Post("/api/ue/delete/range", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			StartIMSI string `json:"start_imsi"`
			Count     int    `json:"count"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		n, err := crud.UEDeleteRange(d.StartIMSI, d.Count)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Same cache rebuilds as the range-clone path: drop the SMF
		// per-IMSI default-5QI cache + rebuild UDM auth / subscription
		// caches so the deleted IMSIs aren't served stale.
		if err := smfctx.ReloadServiceBindings(); err != nil {
			logger.Get("webservice").Warnf("reload bindings after range delete: %v", err)
		}
		if err := udm.LoadCache(); err != nil {
			logger.Get("webservice").Warnf("udm.LoadCache after range delete: %v", err)
		}
		if err := udm.LoadSubscriptionCache(); err != nil {
			logger.Get("webservice").Warnf("udm.LoadSubscriptionCache after range delete: %v", err)
		}
		jsonReply(w, map[string]any{"ok": true, "deleted": n})
	})

	// Bulk UE provisioning — one transaction, one template, N rows.
	// Replaces the 3-round-trip-per-UE flow (auth + tree + AMBR) the
	// tester ran when re-baselining a fresh core: a 128-UE bucket used
	// to be 384 separate API calls; with this endpoint it's one. K /
	// OPc are derived server-side from IMSI + template.kdf_version
	// (sha256(imsi || "MMT-K-"   || kdf_version)[:16] and the OPc
	// analogue) so the wire payload only carries IMSI + MSISDN per UE.
	//
	// Accepts EITHER an explicit `ues` list OR a compact `range`
	// (imsi_start + msisdn_start + count) which the handler expands.
	r.Post("/api/ue/bulk-provision", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Template crud.UEBulkTemplate `json:"template"`
			UEs      []crud.UEBulkEntry  `json:"ues"`
			Range    *crud.UEBulkRange   `json:"range"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// Expand `range` into UEs. IMSI and MSISDN both increment by 1
		// across `count`; MSISDN width is preserved (zero-padded).
		if d.Range != nil && d.Range.Count > 0 {
			imsi0, err1 := strconv.ParseInt(d.Range.IMSIStart, 10, 64)
			msisdn0, err2 := strconv.ParseInt(d.Range.MSISDNStart, 10, 64)
			if err1 != nil || err2 != nil {
				jsonError(w, "range.imsi_start and range.msisdn_start must be numeric", http.StatusBadRequest)
				return
			}
			imsiW := len(d.Range.IMSIStart)
			msW := len(d.Range.MSISDNStart)
			for i := 0; i < d.Range.Count; i++ {
				d.UEs = append(d.UEs, crud.UEBulkEntry{
					IMSI:   fmt.Sprintf("%0*d", imsiW, imsi0+int64(i)),
					MSISDN: fmt.Sprintf("%0*d", msW, msisdn0+int64(i)),
				})
			}
		}
		if len(d.UEs) == 0 {
			jsonError(w, "either ues[] or range must be provided", http.StatusBadRequest)
			return
		}
		res, err := crud.UEBulkProvision(d.Template, d.UEs)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Cache refresh — one rebuild per bulk call instead of N per-UE
		// reloads. Mirrors what /api/ue/clone/range does after its
		// transaction commits.
		if err := udm.LoadCache(); err != nil {
			log.Warnf("udm.LoadCache after bulk-provision: %v", err)
		}
		if err := udm.LoadSubscriptionCache(); err != nil {
			log.Warnf("udm.LoadSubscriptionCache after bulk-provision: %v", err)
		}
		if err := smfctx.ReloadServiceBindings(); err != nil {
			log.Warnf("reload bindings after bulk-provision: %v", err)
		}
		jsonReply(w, map[string]any{
			"ok":            true,
			"ues_total":     len(d.UEs),
			"ues_created":   res.UEsCreated,
			"auth_rows":     res.AuthRows,
			"slices":        res.Slices,
			"dnns":          res.DNNs,
			"bindings":      res.Bindings,
		})
	})

	// ── Service Bindings ─────────────────────────────────────────────
	r.Get("/api/service-bindings", func(w http.ResponseWriter, rq *http.Request) {
		f := crud.BindingFilter{
			IMSI: rq.URL.Query().Get("imsi"),
			DNN:  rq.URL.Query().Get("dnn"),
			SST:  rq.URL.Query().Get("sst"),
			SD:   rq.URL.Query().Get("sd"),
		}
		list, err := crud.BindingsList(f)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"items": list})
	})
	r.Post("/api/service-bindings", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI        string `json:"imsi"`
			DNN         string `json:"dnn"`
			SST         string `json:"sst"`
			SD          string `json:"sd"`
			ServiceName string `json:"service_name"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// Use bulk with single item as workaround for missing BindingsUpsert
		_, err := crud.BindingsBulk([]crud.BindingsBulkItem{{
			IMSI: d.IMSI, DNN: d.DNN, SST: d.SST, SD: d.SD,
			ServiceName: d.ServiceName,
		}})
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Refresh SMF's cached (IMSI,DNN)→default-5QI map so new PDU
		// sessions for this subscriber see the new binding immediately.
		if err := smfctx.ReloadServiceBindings(); err != nil {
			logger.Get("webservice").Warnf("service-binding reload: %v", err)
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Post("/api/service-bindings/bulk", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Items []crud.BindingsBulkItem `json:"items"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		n, err := crud.BindingsBulk(d.Items)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := smfctx.ReloadServiceBindings(); err != nil {
			logger.Get("webservice").Warnf("service-binding reload: %v", err)
		}
		jsonReply(w, map[string]any{"ok": true, "count": n})
	})
	r.Delete("/api/service-bindings", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI        string `json:"imsi"`
			DNN         string `json:"dnn"`
			SST         string `json:"sst"`
			SD          string `json:"sd"`
			ServiceName string `json:"service_name"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		crud.BindingsDelete(d.IMSI, d.DNN, d.SST, d.SD, d.ServiceName)
		if err := smfctx.ReloadServiceBindings(); err != nil {
			logger.Get("webservice").Warnf("service-binding reload: %v", err)
		}
		jsonReply(w, map[string]bool{"ok": true})
	})

	// ── Subscriber subscription tree (NSSAI + DNN + service bindings) ──
	r.Get("/api/subscriber/{imsi}/subscription", func(w http.ResponseWriter, rq *http.Request) {
		tree, err := crud.SubscriptionTreeGet(chi.URLParam(rq, "imsi"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if tree == nil {
			jsonReply(w, map[string]any{"imsi": chi.URLParam(rq, "imsi"), "slices": []any{}})
			return
		}
		jsonReply(w, tree)
	})
	r.Post("/api/subscriber/{imsi}/subscription", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		var tree crud.SubscriptionTree
		if !decodeJSON(w, rq, &tree) {
			return
		}
		slices, dnns, bindings, err := crud.SubscriptionTreeSave(imsi, tree)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "slices": slices, "dnns": dnns, "bindings": bindings})
	})

	// ── Catalog endpoints (for subscription editor dropdowns) ────────
	r.Get("/api/catalog/nssai", func(w http.ResponseWriter, rq *http.Request) {
		list, err := crud.NSSAICatalogList()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"items": list})
	})
	r.Post("/api/catalog/nssai", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SST  any    `json:"sst"`
			SD   string `json:"sd"`
			Name string `json:"name"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		entry, err := crud.NSSAICatalogAdd(d.SST, d.SD, d.Name)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, entry)
	})
	r.Put("/api/catalog/nssai/{nssai_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "nssai_id"), 10, 64)
		var d struct {
			SST  any    `json:"sst"`
			SD   string `json:"sd"`
			Name string `json:"name"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		n, err := crud.NSSAICatalogUpdate(id, d.SST, d.SD, d.Name)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "updated": n})
	})
	r.Delete("/api/catalog/nssai/{nssai_id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "nssai_id"), 10, 64)
		n, err := crud.NSSAICatalogDelete(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "deleted": n})
	})

	// UPF anchor ↔ S-NSSAI binding (TS 23.501 v19.7.0 §6.3.3 — SMF UPF
	// selection). Replaces the legacy upf_instances.supported_sst CSV
	// with a normalised join. The runtime selector
	// (nf/smf/upf/registry.Select) prefers this table when populated;
	// the legacy CSV survives as a fallback for un-migrated rows.
	r.Get("/api/admin/upf-instance/{upf_id}/nssai", func(w http.ResponseWriter, rq *http.Request) {
		upfID := chi.URLParam(rq, "upf_id")
		ids, err := crud.UPFSupportedNSSAIList(upfID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"upf_id": upfID, "nssai_ids": ids})
	})
	r.Put("/api/admin/upf-instance/{upf_id}/nssai", func(w http.ResponseWriter, rq *http.Request) {
		upfID := chi.URLParam(rq, "upf_id")
		var d struct {
			NSSAIIDs []int64 `json:"nssai_ids"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := crud.UPFSupportedNSSAISet(upfID, d.NSSAIIDs); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "upf_id": upfID, "count": len(d.NSSAIIDs)})
	})
	r.Get("/api/catalog/dnn", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := db.Query(`SELECT apn_name, pdu_session_type, ssc_mode FROM apn_config ORDER BY apn_name`)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var items []map[string]any
		for rows.Next() {
			var name, pdu string
			var ssc int
			if rows.Scan(&name, &pdu, &ssc) == nil {
				items = append(items, map[string]any{"apn_name": name, "pdu_session_type": pdu, "ssc_mode": ssc})
			}
		}
		rows.Close()
		if items == nil { items = []map[string]any{} }
		jsonReply(w, map[string]any{"items": items})
	})
	r.Get("/api/catalog/services", func(w http.ResponseWriter, rq *http.Request) {
		list, err := crud.ServicesList()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"items": list})
	})

	// ── Services (QoS profiles) ──────────────────────────────────────
	r.Get("/api/services", func(w http.ResponseWriter, rq *http.Request) {
		list, err := crud.ServicesList()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"items": list})
	})
	r.Get("/api/services/{name}", func(w http.ResponseWriter, rq *http.Request) {
		svc, err := crud.ServicesGet(chi.URLParam(rq, "name"))
		if err != nil || svc == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, svc)
	})
	r.Post("/api/services", func(w http.ResponseWriter, rq *http.Request) {
		var svc crud.Service
		if !decodeJSON(w, rq, &svc) {
			return
		}
		name, err := crud.ServicesUpsert(svc)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// service_bindings cache JOINs the services row for MBR/GBR/5QI,
		// so every services.MBR edit needs a bindings reload to reach
		// the next PDU session's QER.
		if err := smfctx.ReloadServiceBindings(); err != nil {
			logger.Get("webservice").Warnf("service-binding reload after services upsert: %v", err)
		}
		jsonReply(w, map[string]string{"name": name})
	})
	r.Delete("/api/services/{name}", func(w http.ResponseWriter, rq *http.Request) {
		n, err := crud.ServicesDelete(chi.URLParam(rq, "name"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := smfctx.ReloadServiceBindings(); err != nil {
			logger.Get("webservice").Warnf("service-binding reload after services delete: %v", err)
		}
		jsonReply(w, map[string]int64{"deleted": n})
	})

	// ── Charging Profiles ────────────────────────────────────────────
	r.Get("/api/charging-profiles", func(w http.ResponseWriter, rq *http.Request) {
		list, err := crud.ChargingProfilesList()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"items": list})
	})
	r.Get("/api/charging-profiles/names", func(w http.ResponseWriter, rq *http.Request) {
		names, err := crud.ChargingProfilesNames()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"names": names})
	})
	r.Get("/api/charging-profiles/{name}", func(w http.ResponseWriter, rq *http.Request) {
		p, err := crud.ChargingProfilesGet(chi.URLParam(rq, "name"))
		if err != nil || p == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, p)
	})
	r.Post("/api/charging-profiles", func(w http.ResponseWriter, rq *http.Request) {
		var p crud.ChargingProfile
		if !decodeJSON(w, rq, &p) {
			return
		}
		name, err := crud.ChargingProfilesUpsert(p)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "name": name})
	})
	r.Delete("/api/charging-profiles/{name}", func(w http.ResponseWriter, rq *http.Request) {
		n, err := crud.ChargingProfilesDelete(chi.URLParam(rq, "name"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "deleted": n})
	})

	// ── Network Config (singleton — network_config + security_algorithms) ──
	r.Get("/api/network-config", func(w http.ResponseWriter, rq *http.Request) {
		cfg, err := loadNetworkConfig()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"config": cfg})
	})
	r.Post("/api/network-config", func(w http.ResponseWriter, rq *http.Request) {
		var patch map[string]any
		if !decodeJSON(w, rq, &patch) {
			return
		}
		if err := saveNetworkConfig(patch); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cfg, err := loadNetworkConfig()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"config": cfg})
	})

	// ── NSSAI Catalog ────────────────────────────────────────────────
	r.Get("/api/nssai/catalog", func(w http.ResponseWriter, rq *http.Request) {
		list, err := crud.NSSAICatalogList()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, list)
	})

	// ── Bindings ─────────────────────────────────────────────────────
	r.Get("/api/bindings", func(w http.ResponseWriter, rq *http.Request) {
		f := crud.BindingFilter{
			IMSI: rq.URL.Query().Get("imsi"),
			DNN:  rq.URL.Query().Get("dnn"),
			SST:  rq.URL.Query().Get("sst"),
			SD:   rq.URL.Query().Get("sd"),
		}
		list, err := crud.BindingsList(f)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, list)
	})

	// ── Subscription Tree ────────────────────────────────────────────
	r.Get("/api/subscription-tree/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		tree, err := crud.SubscriptionTreeGet(chi.URLParam(rq, "imsi"))
		if err != nil || tree == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, tree)
	})

	// ── SMF Sessions ─────────────────────────────────────────────────
	r.Get("/api/smf/sessions", func(w http.ResponseWriter, rq *http.Request) {
		all := session.Default.All()
		type row struct {
			IMSI         string `json:"imsi"`
			PDUSessionID uint8  `json:"pdu_session_id"`
			DNN          string `json:"dnn"`
			State        string `json:"state"`
			IPv4         string `json:"ipv4,omitempty"`
			IPv6         string `json:"ipv6,omitempty"`
			UPFID        string `json:"upf_id"`
		}
		out := make([]row, 0, len(all))
		for _, s := range all {
			r := row{
				IMSI: s.IMSI, PDUSessionID: s.PDUSessionID,
				DNN: s.DNN, State: s.State.String(), UPFID: s.UPFID,
			}
			if s.IPv4.IsValid() {
				r.IPv4 = s.IPv4.String()
			}
			if s.IPv6.IsValid() {
				r.IPv6 = s.IPv6.String()
			}
			out = append(out, r)
		}
		jsonReply(w, out)
	})

	// ── FSM snapshots (observability for the state machines) ─────────
	//
	// One endpoint per per-subject FSM. Useful to watch transitions
	// in real time during troubleshooting without tail-ing logs.
	r.Get("/api/smf/pti", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, pti.Default.Active())
	})
	// ── Prometheus scrape target (operator observability) ───────────
	//
	// Exposes every pm counter in text-exposition format plus current
	// FSM state counts (how many UEs are in REGISTERED vs AUTHENTICATION,
	// how many PDU sessions in ACTIVE vs RELEASE_PENDING, etc.). Scrape
	// this from Prometheus / Grafana Agent / OTEL collector:
	//
	//   - job_name: sacore
	//     static_configs: [targets: ['amf:5000']]
	//     metrics_path: /metrics
	r.Get("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		pm.Default.WritePrometheus(w)
		// FSM gauges — count by state for each of the four FSMs.
		writeFSMGauges(w)
	})

	r.Get("/api/amf/fsm/gmm", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, gmmfsm.AllSnapshots())
	})
	r.Get("/api/amf/fsm/ngap", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, ngapfsm.AllSnapshots())
	})
	r.Get("/api/amf/ngap/sctp", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, sctpfsm.AllSnapshots())
	})
	r.Get("/api/amf/ngap/sctp/paths", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, sctpfsm.AllPaths())
	})
	r.Get("/api/smf/pfcp", func(w http.ResponseWriter, _ *http.Request) {
		type row struct {
			UPFNode      string `json:"upf_node"`
			IMSI         string `json:"imsi"`
			PDUSessionID uint8  `json:"pdu_session_id"`
			State        string `json:"state"`
			SEID         string `json:"seid,omitempty"`
		}
		entries := pfcpfsm.All()
		out := make([]row, 0, len(entries))
		for _, f := range entries {
			k := f.KeyOf()
			r := row{
				UPFNode: k.UPFNode, IMSI: k.IMSI,
				PDUSessionID: k.PDUSessionID, State: f.State().String(),
			}
			if s := f.SEID(); s != 0 {
				r.SEID = fmt.Sprintf("%#x", s)
			}
			out = append(out, r)
		}
		jsonReply(w, out)
	})

	// ── Logger config ────────────────────────────────────────────────
	r.Get("/api/logger/entries", func(w http.ResponseWriter, rq *http.Request) {
		afterSeq, _ := strconv.ParseInt(rq.URL.Query().Get("after_seq"), 10, 64)
		limit, _ := strconv.Atoi(rq.URL.Query().Get("limit"))
		if limit <= 0 || limit > 500 {
			limit = 500
		}
		entries := logger.GetEntries(afterSeq, rq.URL.Query().Get("level"),
			rq.URL.Query().Get("imsi"), rq.URL.Query().Get("module"), limit)
		jsonReply(w, map[string]any{"entries": entries})
	})
}

