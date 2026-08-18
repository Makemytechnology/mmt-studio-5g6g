// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// REST surface for the Rel-19 TSC control plane:
//
//	/api/tsnaf/*   — TSN AF (TS 23.501 §5.28): 5GS bridge registry,
//	                 CNC-facing capability reports and stream config
//	                 (IEEE 802.1Qcc fully centralized model);
//	/api/tsctsf/*  — TSCTSF (TS 29.565): time sync exposure, TSC app
//	                 sessions, ASTI.
//
// These wrap the in-process service surfaces of nf/tsnaf and nf/tsctsf
// exactly like routes_pcf.go wraps nf/pcf.
package app

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/edge/tsn/gptp"
	"github.com/mmt/mmt-studio-core/nf/tsctsf"
	"github.com/mmt/mmt-studio-core/nf/tsnaf"
	upfpfcp "github.com/mmt/mmt-studio-core/nf/upf/pfcp"
)

func (s *Server) registerTSCRoutes() {
	r := s.Router

	// ── NW-TT (g)PTP / BMCA (TS 23.501 §5.27.1.6, IEEE 802.1AS) ──
	//
	// In a deployed 5GS, gPTP Announce PDUs from the external TSN
	// network arrive at NW-TT ports as raw Ethernet on N6. This
	// surface is that ingress for benches without an L2 path: it
	// builds the Announce and runs it through the same
	// NWTT.ProcessAnnounce → BMCA pipeline the dataplane would use.
	r.Post("/api/upf/nwtt/gptp/announce", func(w http.ResponseWriter, rq *http.Request) {
		var req struct {
			BridgeID     uint64 `json:"bridge_id"`
			Port         uint16 `json:"port"`
			Domain       byte   `json:"domain"`
			GMPriority1  byte   `json:"gm_priority1"`
			GMPriority2  byte   `json:"gm_priority2"`
			GMClockClass byte   `json:"gm_clock_class"`
			GMIdentity   string `json:"gm_identity"` // 8-octet hex
			StepsRemoved uint16 `json:"steps_removed"`
			PduHex       string `json:"pdu_hex,omitempty"` // pre-built Announce overrides fields
		}
		if err := json.NewDecoder(rq.Body).Decode(&req); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		nwtt := upfpfcp.NWTTByNodeID(req.BridgeID)
		if nwtt == nil {
			jsonError(w, "unknown bridge / NW-TT", http.StatusNotFound)
			return
		}
		var pdu []byte
		if req.PduHex != "" {
			b, err := hex.DecodeString(req.PduHex)
			if err != nil {
				jsonError(w, "pdu_hex: "+err.Error(), http.StatusBadRequest)
				return
			}
			pdu = b
		} else {
			ann := &gptp.AnnounceBody{
				GrandmasterPriority1: req.GMPriority1,
				GrandmasterPriority2: req.GMPriority2,
				GrandmasterQuality:   gptp.ClockQuality{ClockClass: req.GMClockClass, ClockAccuracy: 0xFE, OffsetScaledLogVariance: 0x4E5D},
				StepsRemoved:         req.StepsRemoved,
				TimeSource:           0xA0, // internal oscillator
			}
			if req.GMIdentity != "" {
				b, err := hex.DecodeString(req.GMIdentity)
				if err != nil || len(b) != 8 {
					jsonError(w, "gm_identity must be 8 hex octets", http.StatusBadRequest)
					return
				}
				copy(ann.GrandmasterIdentity[:], b)
			}
			m := &gptp.Message{
				MessageType:  gptp.MsgAnnounce,
				DomainNumber: req.Domain,
				Announce:     ann,
			}
			pdu = m.Encode()
		}
		if err := nwtt.ProcessAnnounce(req.Port, pdu, uint64(time.Now().UnixNano())); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		states, localGM := nwtt.PortStates(req.Domain, uint64(time.Now().UnixNano()))
		jsonReply(w, map[string]any{"ok": true, "port_states": portStatesJSON(states), "local_gm": localGM})
	})

	// BMCA outcome per domain: recommended port states + whether the
	// 5GS bridge itself is grandmaster (TS 23.501 §5.27.1.7).
	r.Get("/api/upf/nwtt/gptp/port-states", func(w http.ResponseWriter, rq *http.Request) {
		bid, _ := strconv.ParseUint(rq.URL.Query().Get("bridge_id"), 10, 64)
		dom, _ := strconv.ParseUint(rq.URL.Query().Get("domain"), 10, 8)
		nwtt := upfpfcp.NWTTByNodeID(bid)
		if nwtt == nil {
			jsonError(w, "unknown bridge / NW-TT", http.StatusNotFound)
			return
		}
		states, localGM := nwtt.PortStates(byte(dom), uint64(time.Now().UnixNano()))
		jsonReply(w, map[string]any{"port_states": portStatesJSON(states), "local_gm": localGM})
	})

	// ── TSN AF (TS 23.501 §5.28) ──

	r.Get("/api/tsnaf/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, tsnaf.Default.Status())
	})

	// CNC discovery: list 5GS bridges (§5.28.1).
	r.Get("/api/tsnaf/bridges", func(w http.ResponseWriter, rq *http.Request) {
		out := tsnaf.Default.Bridges()
		if out == nil {
			out = []map[string]any{}
		}
		jsonReply(w, out)
	})

	// CNC read: bridge capabilities incl. §5.27.5 delays per port pair
	// per traffic class.
	r.Get("/api/tsnaf/bridges/{id}/capabilities", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseUint(chi.URLParam(rq, "id"), 0, 64)
		if err != nil {
			jsonError(w, "bridge id must be numeric (bridge_id_num)", http.StatusBadRequest)
			return
		}
		caps, err := tsnaf.Default.Capabilities(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, caps)
	})

	r.Get("/api/tsnaf/streams", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, tsnaf.Default.Streams())
	})

	// CNC read: one stream's status incl. EnTSCAC RAN feedback
	// (BAT offset / adjusted periodicity, TS 23.501 §5.27.2.5).
	r.Get("/api/tsnaf/streams/{id}", func(w http.ResponseWriter, rq *http.Request) {
		st, ok := tsnaf.Default.StreamStatus(chi.URLParam(rq, "id"))
		if !ok {
			jsonError(w, "unknown stream", http.StatusNotFound)
			return
		}
		jsonReply(w, st)
	})

	// CNC write: configure a TSN stream (§5.28.2 — PSFP + scheduled
	// traffic distilled to the 5GS-relevant parameters).
	r.Post("/api/tsn/cnc/streams", func(w http.ResponseWriter, rq *http.Request) {
		var cfg tsnaf.StreamConfig
		if err := json.NewDecoder(rq.Body).Decode(&cfg); err != nil {
			jsonError(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		st, err := tsnaf.Default.ConfigureStream(cfg)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok": true, "stream_id": st.StreamID,
			"direction": st.Direction, "five_qi": st.FiveQI,
			"imsi": st.IMSI, "pdu_session_id": st.PDUSessionID,
		})
	})

	r.Delete("/api/tsn/cnc/streams/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if err := tsnaf.Default.RemoveStream(chi.URLParam(rq, "id")); err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── TSCTSF (TS 29.565) ──

	r.Get("/api/tsctsf/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, tsctsf.Default.Status())
	})

	// Ntsctsf_TimeSynchronization_CapsSubscribe read side (§6.1).
	r.Get("/api/tsctsf/capabilities", func(w http.ResponseWriter, rq *http.Request) {
		caps := tsctsf.Default.Capabilities()
		if caps == nil {
			caps = []tsctsf.TimeSyncCapability{}
		}
		jsonReply(w, caps)
	})

	// Ntsctsf_TimeSynchronization_ConfigCreate/Delete (§6.1.3.4/6).
	r.Get("/api/tsctsf/time-sync/configs", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, tsctsf.Default.TimeSyncConfigs())
	})
	r.Post("/api/tsctsf/time-sync/configs", func(w http.ResponseWriter, rq *http.Request) {
		var cfg tsctsf.TimeSyncConfig
		if err := json.NewDecoder(rq.Body).Decode(&cfg); err != nil {
			jsonError(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := tsctsf.Default.ConfigCreate(&cfg); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "config_id": cfg.ConfigID})
	})
	r.Delete("/api/tsctsf/time-sync/configs/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if err := tsctsf.Default.ConfigDelete(chi.URLParam(rq, "id")); err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// Ntsctsf_QoSandTSCAssistance_Create/Delete (§6.2).
	r.Get("/api/tsctsf/app-sessions", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, tsctsf.Default.AppSessions())
	})
	r.Post("/api/tsctsf/app-sessions", func(w http.ResponseWriter, rq *http.Request) {
		var a tsctsf.TscAppSession
		if err := json.NewDecoder(rq.Body).Decode(&a); err != nil {
			jsonError(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := tsctsf.Default.AppSessionCreate(&a); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "session_id": a.SessionID})
	})
	r.Delete("/api/tsctsf/app-sessions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if err := tsctsf.Default.AppSessionDelete(chi.URLParam(rq, "id")); err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// DetNet controller intake (TS 23.503 §6.1.3.23b, RFC 9633).
	r.Get("/api/tsctsf/detnet", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, tsctsf.Default.DetNetFlows())
	})
	r.Post("/api/tsctsf/detnet", func(w http.ResponseWriter, rq *http.Request) {
		var f tsctsf.DetNetFlow
		if err := json.NewDecoder(rq.Body).Decode(&f); err != nil {
			jsonError(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := tsctsf.Default.DetNetFlowCreate(&f); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "name": f.Name,
			"flow_description": f.FlowDescription()})
	})
	r.Delete("/api/tsctsf/detnet/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if err := tsctsf.Default.DetNetFlowDelete(chi.URLParam(rq, "id")); err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// Ntsctsf_ASTI_Create/Update/Delete/Get (§6.3).
	r.Get("/api/tsctsf/asti", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, tsctsf.Default.AstiConfigs())
	})
	r.Post("/api/tsctsf/asti", func(w http.ResponseWriter, rq *http.Request) {
		var cfg tsctsf.AstiConfig
		if err := json.NewDecoder(rq.Body).Decode(&cfg); err != nil {
			jsonError(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := tsctsf.Default.AstiCreate(&cfg); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "config_id": cfg.ConfigID})
	})
	r.Delete("/api/tsctsf/asti/{id}", func(w http.ResponseWriter, rq *http.Request) {
		if err := tsctsf.Default.AstiDelete(chi.URLParam(rq, "id")); err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
}

// portStatesJSON renders BMCA port states with IEEE 802.1AS state
// names for the REST surface.
func portStatesJSON(states map[uint16]gptp.PortState) map[string]string {
	out := map[string]string{}
	for p, st := range states {
		out[strconv.Itoa(int(p))] = st.String()
	}
	return out
}
