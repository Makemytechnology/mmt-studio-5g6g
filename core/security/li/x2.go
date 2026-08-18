// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// x2.go — POI → MDF IRI delivery (X2 reference point).
//
// Spec anchors:
//
//   - TS 33.127 §6.3 "X2 (IRI delivery)" — the POI ships every
//     captured IRI event to the warrant's MDF over an authenticated
//     transport; the MDF acks each row before it is removed from the
//     POI's pending queue. Stage-3 ASN.1 (TS 33.128) defines the
//     wire envelope; the local product ships JSON until those PDFs
//     are loaded.
//
// Implementation today:
//
//   - One package-level Deliverer goroutine pulls rows from
//     li_iri_events WHERE delivered=0 AND li_warrants.mdf_endpoint!=''
//     (joined). For each (warrant, batch) it POSTs to
//     {mdf_endpoint}/x2/iri with a JSON envelope describing every
//     event in id-order. On HTTP 200 it flips the batch to
//     delivered=1.
//
//   - Failure is non-destructive: a 5xx / network error leaves the
//     rows pending. The queue is the buffer (TS 33.127 expects an
//     MDF-down window to be replayable).
//
//   - Throughput knob: poll interval comes from
//     network_config.li_mdf_poll_interval_ms. Defaults to 1000 ms
//     (one round-trip per second per warrant); operator-tunable.
//
// What is *not* in scope here:
//
//   - mTLS — the operator can put HTTPS in the mdf_endpoint URL but
//     we do not enforce client-cert auth today. TS 33.127 §6.5 mTLS
//     is a roadmap item.
//   - ASN.1 stage-3 envelope — JSON until TS 33.128 PDFs land.

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

// X2Event is the JSON envelope shipped to the MDF /x2/iri endpoint.
// Fields are role-named (target_imsi rather than supi) to match the
// rest of this package and the TS 33.128 §6.2 IRI vocabulary.
type X2Event struct {
	Sequence   int64                  `json:"sequence"`    // monotone within (warrant_id)
	WarrantID  string                 `json:"warrant_id"`
	EventType  string                 `json:"event_type"`
	TargetIMSI string                 `json:"target_imsi"`
	EventData  map[string]interface{} `json:"event_data"`
	Timestamp  string                 `json:"timestamp"`
}

// X2Batch is the body of one POST: every pending row for a single
// warrant, in id-order. Receiver acks the whole batch with HTTP 200
// and the POI flips delivered=1 for every id <= MaxID.
type X2Batch struct {
	WarrantID string    `json:"warrant_id"`
	Events    []X2Event `json:"events"`
	MaxID     int64     `json:"max_id"`
}

// X2DelivererConfig drives the worker's loop. Loaded from network_config.
type X2DelivererConfig struct {
	Enabled        bool
	PollInterval   time.Duration
	HTTPTimeout    time.Duration
	BatchSizePerMDF int
}

func defaultX2Config() X2DelivererConfig {
	return X2DelivererConfig{
		Enabled:         false, // off until operator sets li_x2_enabled=1
		PollInterval:    1 * time.Second,
		HTTPTimeout:     5 * time.Second,
		BatchSizePerMDF: 64,
	}
}

// loadX2Config reads the live config row. Falls back to defaults on
// error so a clean DB still starts the loop in the disabled state.
func loadX2Config() X2DelivererConfig {
	cfg := defaultX2Config()
	db, err := engine.Open()
	if err != nil {
		return cfg
	}
	var enabled int
	var intervalMs int
	row := db.QueryRow("SELECT COALESCE(li_x2_enabled,0), COALESCE(li_mdf_poll_interval_ms,0) FROM network_config WHERE id=1")
	if err := row.Scan(&enabled, &intervalMs); err == nil {
		cfg.Enabled = enabled != 0
		if intervalMs > 0 {
			cfg.PollInterval = time.Duration(intervalMs) * time.Millisecond
		}
	}
	return cfg
}

// Deliverer is the singleton X2 worker. One goroutine per process —
// the per-warrant fan-out happens inside the loop body.
type Deliverer struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	client *http.Client
	log    *logger.Logger
}

var x2Deliverer = &Deliverer{}

// StartX2 launches the X2 IRI-delivery loop. Idempotent; calling
// twice is a no-op. The loop honours the Enabled flag at every tick,
// so toggling network_config.li_x2_enabled at runtime takes effect
// on the next tick without a restart.
func StartX2() {
	x2Deliverer.mu.Lock()
	defer x2Deliverer.mu.Unlock()
	if x2Deliverer.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	x2Deliverer.cancel = cancel
	x2Deliverer.client = &http.Client{Timeout: defaultX2Config().HTTPTimeout}
	x2Deliverer.log = logger.Get("li.x2")
	go x2Deliverer.run(ctx)
}

// StopX2 cancels the loop. Safe to call from a lifecycle.Register
// shutdown hook.
func StopX2() {
	x2Deliverer.mu.Lock()
	defer x2Deliverer.mu.Unlock()
	if x2Deliverer.cancel != nil {
		x2Deliverer.cancel()
		x2Deliverer.cancel = nil
	}
}

func (d *Deliverer) run(ctx context.Context) {
	// First tick uses the cold default; the loop re-reads config at
	// every iteration so operator changes take effect immediately.
	t := time.NewTimer(defaultX2Config().PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cfg := loadX2Config()
			if cfg.Enabled {
				d.tick(cfg)
			}
			t.Reset(cfg.PollInterval)
		}
	}
}

// tick scans for pending IRI batches and ships each to its MDF. One
// HTTP round-trip per (warrant, batch). Errors are logged once per
// warrant per tick; the row stays pending and will retry on the
// next tick.
func (d *Deliverer) tick(cfg X2DelivererConfig) {
	batches, err := d.collectPending(cfg.BatchSizePerMDF)
	if err != nil {
		d.log.Warnf("x2: collect pending failed: %v", err)
		return
	}
	for _, b := range batches {
		if err := d.deliver(b); err != nil {
			d.log.Warnf("x2: deliver to %s warrant=%s: %v",
				b.MDFEndpoint, b.Batch.WarrantID, err)
			continue
		}
		_, _ = engine.Exec(
			"UPDATE li_iri_events SET delivered=1 WHERE warrant_id=? AND id<=? AND delivered=0",
			b.Batch.WarrantID, b.Batch.MaxID)
		audit("x2_delivered", b.Batch.WarrantID, "system",
			fmt.Sprintf("count=%d max_id=%d mdf=%s",
				len(b.Batch.Events), b.Batch.MaxID, b.MDFEndpoint))
	}
}

type pendingBatch struct {
	MDFEndpoint string
	Batch       X2Batch
}

// collectPending groups pending IRI rows by (warrant_id, mdf_endpoint).
// Only warrants with a non-empty mdf_endpoint participate; warrants
// without one stay queued (operator-driven mark-delivered is the
// fallback path for those).
func (d *Deliverer) collectPending(batchSize int) ([]pendingBatch, error) {
	if batchSize <= 0 {
		batchSize = 64
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT e.id, e.warrant_id, e.event_type, e.target_imsi, e.event_data,
		       e.timestamp, w.mdf_endpoint
		  FROM li_iri_events e
		  JOIN li_warrants w ON w.warrant_id = e.warrant_id
		 WHERE e.delivered = 0
		   AND w.mdf_endpoint IS NOT NULL
		   AND TRIM(w.mdf_endpoint) <> ''
		 ORDER BY e.warrant_id, e.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make(map[string]*pendingBatch)
	order := []string{}
	for rows.Next() {
		var id int64
		var wid, evType, imsi, data, ts string
		var mdf sql.NullString
		if err := rows.Scan(&id, &wid, &evType, &imsi, &data, &ts, &mdf); err != nil {
			continue
		}
		ep := strings.TrimSpace(mdf.String)
		if ep == "" {
			continue
		}
		g, ok := groups[wid]
		if !ok {
			g = &pendingBatch{
				MDFEndpoint: ep,
				Batch:       X2Batch{WarrantID: wid},
			}
			groups[wid] = g
			order = append(order, wid)
		}
		if len(g.Batch.Events) >= batchSize {
			continue
		}
		var payload map[string]interface{}
		_ = json.Unmarshal([]byte(data), &payload)
		g.Batch.Events = append(g.Batch.Events, X2Event{
			Sequence:   id,
			WarrantID:  wid,
			EventType:  evType,
			TargetIMSI: imsi,
			EventData:  payload,
			Timestamp:  ts,
		})
		if id > g.Batch.MaxID {
			g.Batch.MaxID = id
		}
	}
	out := make([]pendingBatch, 0, len(order))
	for _, wid := range order {
		out = append(out, *groups[wid])
	}
	return out, nil
}

// deliver POSTs one batch and returns nil on HTTP 2xx.
func (d *Deliverer) deliver(b pendingBatch) error {
	body, err := json.Marshal(b.Batch)
	if err != nil {
		return err
	}
	url := strings.TrimRight(b.MDFEndpoint, "/") + "/x2/iri"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LI-Reference-Point", "X2")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mdf returned %s", resp.Status)
	}
	return nil
}
