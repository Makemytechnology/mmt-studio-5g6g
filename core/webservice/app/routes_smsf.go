// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_smsf.go — REST surface for the SMS Function (SMSF).
//
// Wires `nf/smsf` to /api/smsf/*. The package owns SMS encoding (GSM
// 7-bit / UCS-2), MO/MT delivery, store-and-forward, and the
// `sms_messages` / `sms_routing` persistence.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.502 §4.13.3 — SMS over NAS (MO + MT delivery procedures).
//   - TS 23.040 §9.2.2.1 / §9.2.2.2 — SMS-SUBMIT / SMS-DELIVER TPDU.
//   - TS 23.040 §9.2.3.12 — TP-Validity-Period (expiry).
//   - TS 23.040 §9.2.3.24.1 — UDH for concatenated SMS.
//   - TS 23.038 §6.2.1 — GSM 7-bit Default Alphabet.
//   - TS 24.011 §7.2 / §7.3 — CP / RP framing.
//
// All response shapes are `{ok: true, ...}` envelopes — gives the
// panel a uniform success / error path.
package app

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/nf/smsf"
)

// msisdnRE accepts E.164-shaped numbers (optional leading '+', 3-15
// digits) — TS 23.003 §3.3 / E.164. Stricter than the package
// helpers so bad data never reaches the encoder.
var msisdnRE = regexp.MustCompile(`^\+?[0-9]{3,15}$`)

// validSMSEncoding gates the encoding vocabulary so a bad string
// surfaces as a 400 instead of silently flowing into SegmentText
// and getting forced to UCS-2.
func validSMSEncoding(enc string) bool {
	switch enc {
	case "", "gsm7", "ucs2":
		return true
	}
	return false
}

func (s *Server) registerSMSFRoutes() {
	r := s.Router

	// ── Stats / dashboard ────────────────────────────────────────
	r.Get("/api/smsf/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": smsf.GetStats()})
	})

	// ── Active CP/RP sessions (TS 24.011 §7.2 / §7.3) ────────────
	r.Get("/api/smsf/sessions", func(w http.ResponseWriter, _ *http.Request) {
		ctx := smsf.GetContext()
		jsonReply(w, map[string]any{
			"ok":       true,
			"sessions": ctx.GetAllSessions(),
			"count":    ctx.GetSessionCount(),
		})
	})

	// ── Message list (newest first) ──────────────────────────────
	r.Get("/api/smsf/messages", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		limit := 200
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		msgs, err := smsf.GetMessages(imsi, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if msgs == nil {
			msgs = []map[string]any{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "messages": msgs, "count": len(msgs),
		})
	})

	// ── Single message read (TS 23.040 §9.2 SMS-DELIVER body) ────
	r.Get("/api/smsf/messages/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		m, err := smsf.GetMessage(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if m == nil {
			jsonError(w, "message not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "message": m})
	})

	// ── Single message delete ────────────────────────────────────
	r.Delete("/api/smsf/messages/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		if err := smsf.DeleteMessage(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Send MT-SMS ──────────────────────────────────────────────
	// Validates MSISDNs + encoding vocab + body length (TS 23.040
	// §9.2 limits + GSM-7 vs UCS-2 segmentation budget) before
	// hitting the codec.
	r.Post("/api/smsf/send", func(w http.ResponseWriter, rq *http.Request) {
		// Accept both naming conventions:
		//   - {sender, recipient, body, encoding}              (E.164 in/out)
		//   - {sender_imsi, recipient_msisdn, text, encoding}  (GUI + tester)
		// The northbound contract used by the SMSF panel
		// (webservice/templates/smsf.html) and the test harness
		// (mmt_studio_core_tester) uses the second shape, where
		// sender_imsi is a subscriber selector that the SMSF
		// resolves to an E.164 MSISDN via UDM (TS 29.503 §5.2.2.2
		// Nudm_SubscriberDataManagement). The first shape is the
		// pure E.164 form that internal callers can use when they
		// already know both MSISDNs.
		var d struct {
			Sender          string `json:"sender"`
			SenderIMSI      string `json:"sender_imsi"`
			Recipient       string `json:"recipient"`
			RecipientMSISDN string `json:"recipient_msisdn"`
			Body            string `json:"body"`
			Text            string `json:"text"`
			Encoding        string `json:"encoding"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// Resolve sender. If only an IMSI was given, look up the
		// subscriber MSISDN — UE table is the canonical source.
		sender := d.Sender
		if sender == "" && d.SenderIMSI != "" {
			ue, err := crud.UEGetByIMSI(d.SenderIMSI)
			if err != nil || ue == nil {
				jsonError(w, "sender_imsi: subscriber not found",
					http.StatusBadRequest)
				return
			}
			if !ue.MSISDN.Valid || ue.MSISDN.String == "" {
				jsonError(w, "sender_imsi: subscriber has no MSISDN provisioned",
					http.StatusBadRequest)
				return
			}
			sender = ue.MSISDN.String
		}
		recipient := d.Recipient
		if recipient == "" {
			recipient = d.RecipientMSISDN
		}
		body := d.Body
		if body == "" {
			body = d.Text
		}
		if !msisdnRE.MatchString(sender) {
			jsonError(w, "sender / sender_imsi must resolve to E.164 (3-15 digits, optional +)",
				http.StatusBadRequest)
			return
		}
		if !msisdnRE.MatchString(recipient) {
			jsonError(w, "recipient / recipient_msisdn must be E.164 (3-15 digits, optional +)",
				http.StatusBadRequest)
			return
		}
		if !validSMSEncoding(d.Encoding) {
			jsonError(w, "encoding must be gsm7 or ucs2",
				http.StatusBadRequest)
			return
		}
		if body == "" {
			jsonError(w, "body / text required", http.StatusBadRequest)
			return
		}
		// Cap raw body length so a 10MB POST can't queue a million
		// segments. 10 000 chars is well past any operational use
		// case (≈64 segments at GSM-7).
		if len(body) > 10000 {
			jsonError(w, "body too long (max 10000 chars)",
				http.StatusBadRequest)
			return
		}
		enc := d.Encoding
		if enc == "" {
			enc = "gsm7"
		}
		result := smsf.SendMTSMS(d.SenderIMSI, sender, recipient, body, enc)
		if !result.OK {
			jsonError(w, result.Error, http.StatusInternalServerError)
			return
		}
		// `message_id` (first segment row) lets the caller correlate
		// against /api/smsf/messages; `message_ids` carries the full
		// per-segment list for concatenated SMS (TS 23.040 §9.2.3.24.1).
		var firstID int64
		if len(result.MsgIDs) > 0 {
			firstID = result.MsgIDs[0]
		}
		jsonReply(w, map[string]any{
			"ok":          true,
			"status":      result.Status,
			"segments":    result.Segments,
			"encoding":    result.Encoding,
			"message_id":  firstID,
			"message_ids": result.MsgIDs,
		})
	})

	// ── Segmentation utility (TS 23.040 §9.2.3.24.1 UDH preview) ─
	// Lets the panel show "this body is N segments at encoding E"
	// before the operator commits to send. Pure compute — no DB.
	r.Post("/api/smsf/segment", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Body     string `json:"body"`
			Encoding string `json:"encoding"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if !validSMSEncoding(d.Encoding) {
			jsonError(w, "encoding must be gsm7 or ucs2",
				http.StatusBadRequest)
			return
		}
		enc := d.Encoding
		gsm7Compat := smsf.IsGSM7Encodable(d.Body)
		if enc == "" {
			if gsm7Compat {
				enc = "gsm7"
			} else {
				enc = "ucs2"
			}
		}
		// Force UCS-2 fallback when the user picked gsm7 but the
		// body has out-of-alphabet chars; same rule as SendMTSMS.
		if enc == "gsm7" && !gsm7Compat {
			enc = "ucs2"
		}
		segments := smsf.SegmentText(d.Body, enc)
		jsonReply(w, map[string]any{
			"ok":              true,
			"encoding":        enc,
			"segments":        segments,
			"count":           len(segments),
			"gsm7_compatible": gsm7Compat,
		})
	})

	// ── Routing rules CRUD ───────────────────────────────────────
	r.Get("/api/smsf/routing", func(w http.ResponseWriter, _ *http.Request) {
		routes, err := smsf.GetRoutingRules()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if routes == nil {
			routes = []map[string]any{}
		}
		jsonReply(w, map[string]any{
			"ok": true, "rules": routes, "count": len(routes),
		})
	})

	r.Post("/api/smsf/routing", func(w http.ResponseWriter, rq *http.Request) {
		// Accept both `msisdn_pattern` (canonical, matches sms_routing
		// schema column) and `pattern` (shorter alias used by the
		// tester + GUI). Same dual-naming convention as the /send
		// endpoint above.
		var d struct {
			MSISDNPattern string `json:"msisdn_pattern"`
			Pattern       string `json:"pattern"`
			RouteType     string `json:"route_type"`
			Destination   string `json:"destination"`
			Priority      int    `json:"priority"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		pattern := strings.TrimSpace(d.MSISDNPattern)
		if pattern == "" {
			pattern = strings.TrimSpace(d.Pattern)
		}
		if pattern == "" {
			jsonError(w, "msisdn_pattern / pattern required", http.StatusBadRequest)
			return
		}
		switch d.RouteType {
		case "local", "smsc", "forward", "":
		default:
			jsonError(w,
				"route_type must be one of local|smsc|forward",
				http.StatusBadRequest)
			return
		}
		row, err := smsf.AddRoutingRule(pattern, d.RouteType,
			d.Destination, d.Priority)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Surface rule_id at the top level so callers don't have to
		// crack the inner "rule" map to correlate the create with
		// later DELETE / PATCH.
		var ruleID any
		if row != nil {
			if v, ok := row["id"]; ok {
				ruleID = v
			}
		}
		jsonReply(w, map[string]any{"ok": true, "rule": row, "rule_id": ruleID})
	})

	r.Delete("/api/smsf/routing/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		if err := smsf.DeleteRoutingRule(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── Force-expiry sweep (TS 23.040 §9.2.3.12 TP-VP) ───────────
	// Operator drill — normally the expiry runs on a tick; this
	// lets the panel kick it on demand for a stuck queue.
	r.Post("/api/smsf/expire", func(w http.ResponseWriter, _ *http.Request) {
		n := smsf.ExpireOldMessages()
		jsonReply(w, map[string]any{"ok": true, "expired": n})
	})

	// ── Deliver pending MT-SMS to a UE ───────────────────────────
	// TS 23.502 §4.13.3.5 (CM-CONNECTED MT delivery). Used after a
	// store-and-forward queue accumulated while the UE was idle.
	r.Post("/api/smsf/deliver/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		if imsi == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		n := smsf.DeliverPending(imsi)
		jsonReply(w, map[string]any{
			"ok":        true,
			"imsi":      imsi,
			"delivered": n,
		})
	})
}
