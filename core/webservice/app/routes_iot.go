// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// CIoT / Ambient-IoT management surface. The backing state lives in
// the iot module (iot/nbiot, iot/nidd, iot/ambient — each spec-cited
// per file); these routes replace the earlier emptyArrayRoute stubs
// that made every IoT feature invisible over REST:
//
//	PSM        TS 23.682 §4.5.4  (Active Time T3324 → PSM sleep)
//	eDRX       TS 23.682 §4.5.13 (extended idle-mode DRX)
//	CP data    TS 23.401 §4.3.17 (NAS small data), buffering while
//	           the UE sleeps per TS 23.682 §4.5.7 (high-latency comm)
//	NIDD       TS 23.682 §4.5.14 (non-IP data delivery via SCEF/NEF)
//	Ambient    TS 22.369 (tag / reader / inventory service model)
package app

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/iot/ambient"
	"github.com/mmt/mmt-studio-core/iot/nbiot"
	"github.com/mmt/mmt-studio-core/iot/nidd"
)

// psmTimers drives the TS 23.682 §4.5.4 Active-Time state machine:
// SetPSM leaves the UE 'active'; when T3324 expires without further
// activity the UE enters PSM ('sleeping'). One timer per IMSI,
// re-armed on every (re)configuration.
var (
	psmTimerMu sync.Mutex
	psmTimers  = map[string]*time.Timer{}
)

func armPSMTimer(imsi string, t3324 time.Duration) {
	psmTimerMu.Lock()
	defer psmTimerMu.Unlock()
	if t := psmTimers[imsi]; t != nil {
		t.Stop()
	}
	psmTimers[imsi] = time.AfterFunc(t3324, func() {
		_ = nbiot.EnterSleep(imsi)
	})
}

func (s *Server) registerIoTRoutes() {
	r := s.Router

	// ── NB-IoT PSM (TS 23.682 §4.5.4) ─────────────────────────────
	r.Post("/api/iot/nbiot/psm", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI      string `json:"imsi"`
			T3324s    int    `json:"t3324_s"`
			T3412Exts int    `json:"t3412_ext_s"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := nbiot.SetPSM(d.IMSI, d.T3324s, d.T3412Exts); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// §4.5.4: the UE is reachable for the Active Time, then
		// enters PSM. Arm the T3324 expiry.
		armPSMTimer(d.IMSI, time.Duration(d.T3324s)*time.Second)
		jsonReply(w, map[string]any{"ok": true, "imsi": d.IMSI,
			"t3324_s": d.T3324s, "t3412_ext_s": d.T3412Exts})
	})
	r.Get("/api/iot/nbiot/psm", func(w http.ResponseWriter, rq *http.Request) {
		items, err := nbiot.List()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, it := range items {
			// alias for clients reading the short key
			it["state"] = it["psm_state"]
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"items": items})
	})

	// ── CP CIoT small data (TS 23.401 §4.3.17) ────────────────────
	r.Get("/api/iot/nbiot/cp-data", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		pending, err := nidd.PendingCP(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if pending == nil {
			pending = []nidd.CPData{}
		}
		jsonReply(w, map[string]any{"items": pending, "pending": len(pending)})
	})
	r.Post("/api/iot/nbiot/cp-data/dl", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI    string `json:"imsi"`
			DataHex string `json:"data_hex"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		payload, err := hex.DecodeString(d.DataHex)
		if err != nil {
			jsonError(w, "data_hex: "+err.Error(), http.StatusBadRequest)
			return
		}
		rec, err := nidd.AppendCP(d.IMSI, "DL", payload, nil)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// TS 23.682 §4.5.7 high-latency communication: DL data for a
		// UE in PSM is buffered until the UE next becomes reachable
		// (T3412-ext TAU or MO activity). Report the buffering state.
		state := "active"
		if p, _ := nbiot.GetPSM(d.IMSI); p != nil {
			state = p.State
		}
		if state == "sleeping" || state == "unreachable" {
			w.WriteHeader(http.StatusAccepted) // 202 — buffered
			jsonReply(w, map[string]any{"buffered": true, "id": rec.ID,
				"ue_state": state, "bytes": len(payload)})
			return
		}
		_ = nidd.MarkCPDelivered(rec.ID)
		jsonReply(w, map[string]any{"delivered": true, "id": rec.ID,
			"ue_state": state, "bytes": len(payload)})
	})

	// ── Coverage Enhancement stats (TS 36.331 CE levels; capability
	//    rows per TS 23.401 Annex F, stored by iot/nbiot) ───────────
	r.Get("/api/iot/nbiot/coverage-stats", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]any{
			"nbiot":  nbiot.Status(),
			"levels": []string{"CE0 (normal)", "CE1 (+10dB)", "CE2 (+20dB)"},
		})
	})

	// ── eDRX (TS 23.682 §4.5.13) ──────────────────────────────────
	r.Post("/api/iot/edrx", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI       string  `json:"imsi"`
			CycleS     float64 `json:"cycle_s"`
			PTWs       float64 `json:"ptw_s"`
			DeviceType string  `json:"device_type"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := nbiot.SetEDRX(d.IMSI, d.DeviceType, d.CycleS, d.PTWs); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": d.IMSI,
			"cycle_s": d.CycleS, "ptw_s": d.PTWs})
	})
	r.Get("/api/iot/edrx", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		if imsi != "" {
			cfg, err := nbiot.GetEDRX(imsi)
			if err != nil {
				jsonError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			jsonReply(w, map[string]any{"items": []any{cfg}})
			return
		}
		jsonReply(w, map[string]any{"nbiot": nbiot.Status()})
	})

	// ── NIDD sessions (TS 23.682 §4.5.14) ─────────────────────────
	r.Post("/api/iot/nidd/session", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI         string `json:"imsi"`
			APN          string `json:"apn"`
			AppServerURL string `json:"app_server_url"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		sid := fmt.Sprintf("nidd-%s-%d", d.IMSI, time.Now().UnixNano())
		sess, err := nidd.CreateSession(d.IMSI, sid, d.APN, d.AppServerURL)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true,
			"session_id": sess.SessionID, "id": sess.ID, "status": sess.Status})
	})
	r.Post("/api/iot/nidd/session/{id}/dl", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			DataHex string `json:"data_hex"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		sess, err := nidd.GetSessionBySessionID(chi.URLParam(rq, "id"))
		if err != nil || sess == nil {
			jsonError(w, "unknown NIDD session", http.StatusNotFound)
			return
		}
		payload, err := hex.DecodeString(d.DataHex)
		if err != nil {
			jsonError(w, "data_hex: "+err.Error(), http.StatusBadRequest)
			return
		}
		ueState := "active"
		if p, _ := nbiot.GetPSM(sess.IMSI); p != nil {
			ueState = p.State
		}
		rec, err := nidd.SendMT(sess.ID, payload, ueState)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if rec.Status != "delivered" {
			w.WriteHeader(http.StatusAccepted) // buffered per §4.5.7
		}
		jsonReply(w, map[string]any{"ok": true, "status": rec.Status,
			"delivered": rec.Status == "delivered",
			"ue_state": ueState, "bytes": len(payload)})
	})

	// ── Ambient IoT (TS 22.369) ───────────────────────────────────
	r.Post("/api/iot/tag", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TagID    string  `json:"tag_id"`
			TagClass string  `json:"tag_class"`
			TagType  string  `json:"tag_type"`
			Group    *string `json:"group"`
			Owner    *string `json:"owner"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := ambient.RegisterTag(d.TagID, d.TagClass, d.TagType, d.Group, d.Owner); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "tag_id": d.TagID})
	})
	r.Get("/api/iot/tags", func(w http.ResponseWriter, rq *http.Request) {
		tags, err := ambient.ListTags(rq.URL.Query().Get("type"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if group := rq.URL.Query().Get("group"); group != "" {
			filtered := tags[:0]
			for _, t := range tags {
				if t.GroupID != nil && *t.GroupID == group {
					filtered = append(filtered, t)
				}
			}
			tags = filtered
		}
		if tags == nil {
			tags = []ambient.Tag{}
		}
		jsonReply(w, tags)
	})
	r.Post("/api/iot/reader", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			ReaderID     string   `json:"reader_id"`
			GnbIP        *string  `json:"gnb_ip"`
			Latitude     *float64 `json:"latitude"`
			Longitude    *float64 `json:"longitude"`
			Capabilities []string `json:"capabilities"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		var caps *string
		if len(d.Capabilities) > 0 {
			joined := ""
			for i, c := range d.Capabilities {
				if i > 0 {
					joined += ","
				}
				joined += c
			}
			caps = &joined
		}
		if err := ambient.RegisterReader(d.ReaderID, d.GnbIP, caps, d.Latitude, d.Longitude); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "reader_id": d.ReaderID})
	})
	r.Get("/api/iot/readers", func(w http.ResponseWriter, rq *http.Request) {
		readers, err := ambient.ListReaders()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if readers == nil {
			readers = []ambient.Reader{}
		}
		jsonReply(w, readers)
	})
	r.Post("/api/iot/inventory", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			ReaderID  string `json:"reader_id"`
			EventType string `json:"event_type"`
			TagsFound []struct {
				TagID string `json:"tag_id"`
				RSSI  int    `json:"rssi"`
			} `json:"tags_found"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		for _, t := range d.TagsFound {
			_ = ambient.SeenTag(t.TagID, d.ReaderID, nil, nil, nil)
		}
		id, err := ambient.LogInventory(d.ReaderID, d.EventType, len(d.TagsFound), nil)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "event_id": id,
			"tags_found": len(d.TagsFound)})
	})
	r.Get("/api/iot/inventory/history", func(w http.ResponseWriter, rq *http.Request) {
		limit, _ := strconv.Atoi(rq.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 100
		}
		events, err := ambient.ListInventoryEvents(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if events == nil {
			events = []ambient.InventoryEvent{}
		}
		jsonReply(w, events)
	})
}
