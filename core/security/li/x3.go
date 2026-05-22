// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// x3.go — POI → MDF Communications Content delivery (X3 reference
// point).
//
// Spec anchors:
//
//   - TS 33.127 §6.4 "X3 (CC delivery)" — the POI ships intercepted
//     content to the MDF on a per-session basis. Stage-3 ASN.1
//     framing (TS 33.128) is the spec wire; the local product
//     starts with the **lifecycle** half of X3 (session opened /
//     session closed metadata) since the UPF dataplane fork is a
//     larger work item. Once UPF integration lands, the same X3
//     channel will carry per-packet frames; today it carries the
//     session events.
//
// Why ship lifecycle as the first X3 wire:
//
//   - A regulator audit needs to see *when* CC was active and on
//     which PDU session, not just that a row exists in the warrant
//     store. The lifecycle events on X3 provide that, and they're
//     time-ordered the same way the future per-packet stream will be.
//   - The MDF can correlate IRI on X2 with CC sessions on X3 by
//     warrant_id + target_imsi without needing the full packet
//     stream yet.
//
// Schema:
//
//   - li_cc_sessions gains two flag columns via ensureColumn:
//     cc_opened_delivered / cc_closed_delivered. The deliverer
//     pushes:
//       * "OPENED" rows where cc_opened_delivered = 0
//       * "CLOSED" rows where status='stopped' AND cc_closed_delivered=0
//
// Throughput knob: poll interval comes from the same
// network_config.li_mdf_poll_interval_ms used by X2 — there is one
// loop tick driving both channels in lock-step.

package li

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// X3Event is the JSON envelope shipped to the MDF /x3/cc endpoint.
// Phase types are role-named; once UPF fork lands a "FRAME" phase
// will join OPENED / CLOSED carrying packet content.
type X3Event struct {
	Sequence     int64  `json:"sequence"`
	WarrantID    string `json:"warrant_id"`
	Phase        string `json:"phase"` // OPENED | CLOSED
	TargetIMSI   string `json:"target_imsi"`
	SessionType  string `json:"session_type"`
	PDUSessionID int    `json:"pdu_session_id,omitempty"`
	CallID       string `json:"call_id,omitempty"`
	Timestamp    string `json:"timestamp"`
}

type X3Batch struct {
	WarrantID string    `json:"warrant_id"`
	Events    []X3Event `json:"events"`
}

// X3DelivererConfig — same shape as X2 so the operator config UI
// can render them side by side.
type X3DelivererConfig struct {
	Enabled      bool
	PollInterval time.Duration
	HTTPTimeout  time.Duration
}

func defaultX3Config() X3DelivererConfig {
	return X3DelivererConfig{
		Enabled:      false,
		PollInterval: 1 * time.Second,
		HTTPTimeout:  5 * time.Second,
	}
}

func loadX3Config() X3DelivererConfig {
	cfg := defaultX3Config()
	db, err := engine.Open()
	if err != nil {
		return cfg
	}
	var enabled int
	var intervalMs int
	row := db.QueryRow("SELECT COALESCE(li_x3_enabled,0), COALESCE(li_mdf_poll_interval_ms,0) FROM network_config WHERE id=1")
	if err := row.Scan(&enabled, &intervalMs); err == nil {
		cfg.Enabled = enabled != 0
		if intervalMs > 0 {
			cfg.PollInterval = time.Duration(intervalMs) * time.Millisecond
		}
	}
	return cfg
}

type x3Worker struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	client *http.Client
	log    *logger.Logger
}

var x3Deliverer = &x3Worker{}

// StartX3 launches the X3 CC-delivery loop. Idempotent.
func StartX3() {
	x3Deliverer.mu.Lock()
	defer x3Deliverer.mu.Unlock()
	if x3Deliverer.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	x3Deliverer.cancel = cancel
	x3Deliverer.client = &http.Client{Timeout: defaultX3Config().HTTPTimeout}
	x3Deliverer.log = logger.Get("li.x3")
	go x3Deliverer.run(ctx)
}

// StopX3 cancels the loop.
func StopX3() {
	x3Deliverer.mu.Lock()
	defer x3Deliverer.mu.Unlock()
	if x3Deliverer.cancel != nil {
		x3Deliverer.cancel()
		x3Deliverer.cancel = nil
	}
}

func (w *x3Worker) run(ctx context.Context) {
	t := time.NewTimer(defaultX3Config().PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cfg := loadX3Config()
			if cfg.Enabled {
				w.tick()
			}
			t.Reset(cfg.PollInterval)
		}
	}
}

type x3Batch struct {
	MDFEndpoint string
	Batch       X3Batch
	OpenedIDs   []int64
	ClosedIDs   []int64
}

func (w *x3Worker) tick() {
	batches, err := w.collectPending()
	if err != nil {
		w.log.Warnf("x3: collect pending failed: %v", err)
		return
	}
	for _, b := range batches {
		if err := w.deliver(b); err != nil {
			w.log.Warnf("x3: deliver to %s warrant=%s: %v",
				b.MDFEndpoint, b.Batch.WarrantID, err)
			continue
		}
		// Flag the rows whose events we shipped. We track open and
		// close independently so a session that opened+closed in the
		// same tick gets both flags flipped.
		if len(b.OpenedIDs) > 0 {
			_, _ = engine.Exec(
				"UPDATE li_cc_sessions SET cc_opened_delivered=1 WHERE id IN ("+placeholders(len(b.OpenedIDs))+")",
				toAnySlice(b.OpenedIDs)...)
		}
		if len(b.ClosedIDs) > 0 {
			_, _ = engine.Exec(
				"UPDATE li_cc_sessions SET cc_closed_delivered=1 WHERE id IN ("+placeholders(len(b.ClosedIDs))+")",
				toAnySlice(b.ClosedIDs)...)
		}
		audit("x3_delivered", b.Batch.WarrantID, "system",
			fmt.Sprintf("opened=%d closed=%d mdf=%s",
				len(b.OpenedIDs), len(b.ClosedIDs), b.MDFEndpoint))
	}
}

// collectPending walks li_cc_sessions joined to li_warrants. Returns
// one batch per warrant. Undelivered OPENED + CLOSED events are
// emitted in row-id order so the MDF sees a coherent sequence.
func (w *x3Worker) collectPending() ([]x3Batch, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT s.id, s.warrant_id, s.target_imsi, s.session_type,
		       s.pdu_session_id, s.call_id, s.status, s.started_at,
		       s.cc_opened_delivered, s.cc_closed_delivered,
		       w.mdf_endpoint
		  FROM li_cc_sessions s
		  JOIN li_warrants w ON w.warrant_id = s.warrant_id
		 WHERE w.mdf_endpoint IS NOT NULL
		   AND TRIM(w.mdf_endpoint) <> ''
		   AND (s.cc_opened_delivered = 0
		        OR (s.status='stopped' AND s.cc_closed_delivered=0))
		 ORDER BY s.warrant_id, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make(map[string]*x3Batch)
	order := []string{}
	for rows.Next() {
		var id, openedFlag, closedFlag int64
		var pduID sql.NullInt64
		var wid, imsi, sessType, status, startedAt string
		var callID, mdf sql.NullString
		if err := rows.Scan(&id, &wid, &imsi, &sessType, &pduID, &callID,
			&status, &startedAt, &openedFlag, &closedFlag, &mdf); err != nil {
			continue
		}
		ep := strings.TrimSpace(mdf.String)
		if ep == "" {
			continue
		}
		g, ok := groups[wid]
		if !ok {
			g = &x3Batch{
				MDFEndpoint: ep,
				Batch:       X3Batch{WarrantID: wid},
			}
			groups[wid] = g
			order = append(order, wid)
		}
		base := X3Event{
			Sequence:    id,
			WarrantID:   wid,
			TargetIMSI:  imsi,
			SessionType: sessType,
			Timestamp:   startedAt,
		}
		if pduID.Valid {
			base.PDUSessionID = int(pduID.Int64)
		}
		if callID.Valid {
			base.CallID = callID.String
		}
		if openedFlag == 0 {
			ev := base
			ev.Phase = "OPENED"
			g.Batch.Events = append(g.Batch.Events, ev)
			g.OpenedIDs = append(g.OpenedIDs, id)
		}
		if status == "stopped" && closedFlag == 0 {
			ev := base
			ev.Phase = "CLOSED"
			g.Batch.Events = append(g.Batch.Events, ev)
			g.ClosedIDs = append(g.ClosedIDs, id)
		}
	}
	out := make([]x3Batch, 0, len(order))
	for _, wid := range order {
		out = append(out, *groups[wid])
	}
	return out, nil
}

func (w *x3Worker) deliver(b x3Batch) error {
	body, err := json.Marshal(b.Batch)
	if err != nil {
		return err
	}
	url := strings.TrimRight(b.MDFEndpoint, "/") + "/x3/cc"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LI-Reference-Point", "X3")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mdf returned %s", resp.Status)
	}
	return nil
}

// placeholders builds an "?,?,?" string of length n for IN-clause
// substitutions.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

// toAnySlice converts []int64 to []any for db.Exec varargs.
func toAnySlice(xs []int64) []any {
	out := make([]any, len(xs))
	for i, v := range xs {
		out[i] = v
	}
	return out
}
