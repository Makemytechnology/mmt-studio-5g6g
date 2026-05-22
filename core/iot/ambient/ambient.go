// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ambient — Ambient IoT (AmIoT) tag and reader registry.
//
// Spec anchors:
//   - TS 22.369 §4.2 Characteristics of Ambient IoT — defines
//     ambient-powered tags as devices with no battery (energy-
//     harvested) or with limited storage capacitor; communication
//     via backscatter or active radio.
//   - TS 22.369 §4.3 Typical Ambient IoT use cases — inventory,
//     sensor data collection, tracking, actuator control. The four
//     iot_inventory_events.event_type buckets the operator
//     dashboard sorts events into come from this enumeration.
//   - TS 22.369 §4.4 Communication modes — Topology 1 (BS↔AmIoT),
//     Topology 2 (BS↔Intermediate UE↔AmIoT), Topology 3 (BS↔
//     Intermediate node↔AmIoT). Reader.GnbIP captures the BS or
//     Intermediate-node attach point.
//   - TS 22.369 §5.2 Functional service requirements — tag class
//     A/B/C distinction (energy availability, storage, support for
//     half-duplex / full-duplex). iot_tags.tag_class persists this.
//   - TS 22.369 §6.2 Performance requirements for Inventory —
//     inventory event KPIs (latency, completeness) drive the fields
//     iot_inventory_events.tags_found / result_json record.
//
// TS 22.369 is a Stage-1 service-requirements spec. Stage-2 AmIoT
// architecture and Stage-3 wire protocols are not yet at the
// Rel-19 floor in specs/3gpp/ — the operations here are local
// persistence of operator-provisioned tags / readers and the
// inventory event log the reader-side dataplane writes back into.
//
// TODO TS 23.369 — when the Stage-2 AmIoT architecture spec lands,
// anchor the inventory and trigger procedures.
package ambient

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// Tag — ambient IoT tag (TS 22.369 §5.2 tag class A/B/C).
type Tag struct {
	TagID        string   `json:"tag_id"`
	TagClass     string   `json:"tag_class"`     // A | B | C
	TagType      string   `json:"tag_type"`      // asset | sensor | actuator
	GroupID      *string  `json:"group_id,omitempty"`
	Owner        *string  `json:"owner,omitempty"`
	DataPayload  *string  `json:"data_payload,omitempty"`
	LastSeenAt   *string  `json:"last_seen_at,omitempty"`
	LastReaderID *string  `json:"last_reader_id,omitempty"`
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	RegisteredAt string   `json:"registered_at"`
}

// Reader — ambient IoT reader / interrogator (TS 22.369 §4.4).
// The gnb_ip column maps to the gNB or intermediate node anchor
// the reader is associated with.
type Reader struct {
	ReaderID      string   `json:"reader_id"`
	GnbIP         *string  `json:"gnb_ip,omitempty"`
	Latitude      *float64 `json:"latitude,omitempty"`
	Longitude     *float64 `json:"longitude,omitempty"`
	Capabilities  *string  `json:"capabilities,omitempty"`
	Status        string   `json:"status"`         // active | offline | maintenance
	LastHeartbeat *string  `json:"last_heartbeat,omitempty"`
}

// InventoryEvent — log of an inventory / sensor / actuator action
// performed by a reader (TS 22.369 §6.2 / §6.3 / §6.5 KPIs).
type InventoryEvent struct {
	ID         int64   `json:"id"`
	ReaderID   string  `json:"reader_id"`
	EventType  string  `json:"event_type"`     // inventory | sensor_read | actuator | track
	TagsFound  int     `json:"tags_found"`
	ResultJSON *string `json:"result_json,omitempty"`
	Timestamp  string  `json:"timestamp"`
}

// ── Tag CRUD ─────────────────────────────────────────────────────

// RegisterTag persists a new ambient IoT tag with operator-assigned
// class (A/B/C — TS 22.369 §5.2).
func RegisterTag(tagID, tagClass, tagType string, groupID, owner *string) error {
	if strings.TrimSpace(tagID) == "" {
		return fmt.Errorf("tag_id is required")
	}
	if tagClass == "" {
		tagClass = "A"
	}
	if tagClass != "A" && tagClass != "B" && tagClass != "C" {
		return fmt.Errorf("tag_class must be A, B, or C (got %q)", tagClass)
	}
	if tagType == "" {
		tagType = "asset"
	}
	_, err := engine.Exec(`INSERT INTO iot_tags
		(tag_id, tag_class, tag_type, group_id, owner)
		VALUES (?, ?, ?, ?, ?)`,
		tagID, tagClass, tagType, groupID, owner)
	return err
}

// GetTag returns a tag by its tag_id.
func GetTag(tagID string) (*Tag, error) {
	row := engine.QueryRow(`SELECT tag_id, tag_class, tag_type, group_id, owner,
		data_payload, last_seen_at, last_reader_id, latitude, longitude,
		registered_at FROM iot_tags WHERE tag_id=?`, tagID)
	var t Tag
	err := row.Scan(&t.TagID, &t.TagClass, &t.TagType, &t.GroupID, &t.Owner,
		&t.DataPayload, &t.LastSeenAt, &t.LastReaderID, &t.Latitude, &t.Longitude,
		&t.RegisteredAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &t, err
}

// ListTags returns all tags, optionally filtered by tag_type.
func ListTags(tagType string) ([]Tag, error) {
	q := `SELECT tag_id, tag_class, tag_type, group_id, owner, data_payload,
		last_seen_at, last_reader_id, latitude, longitude, registered_at
		FROM iot_tags`
	var args []interface{}
	if tagType != "" {
		q += " WHERE tag_type=?"
		args = append(args, tagType)
	}
	q += " ORDER BY tag_id"
	rows, err := engine.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.TagID, &t.TagClass, &t.TagType, &t.GroupID, &t.Owner,
			&t.DataPayload, &t.LastSeenAt, &t.LastReaderID, &t.Latitude, &t.Longitude,
			&t.RegisteredAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SeenTag updates a tag's last-seen timestamp + reader + payload —
// the dataplane calls this when a reader observes a tag (TS 22.369
// §6.2 inventory; §6.3 sensor data collection).
func SeenTag(tagID, readerID string, payload *string, lat, lon *float64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := engine.Exec(`UPDATE iot_tags SET
		last_seen_at=?, last_reader_id=?, data_payload=COALESCE(?, data_payload),
		latitude=COALESCE(?, latitude), longitude=COALESCE(?, longitude)
		WHERE tag_id=?`,
		now, readerID, payload, lat, lon, tagID)
	return err
}

// DeleteTag removes a tag.
func DeleteTag(tagID string) error {
	_, err := engine.Exec(`DELETE FROM iot_tags WHERE tag_id=?`, tagID)
	return err
}

// ── Reader CRUD ──────────────────────────────────────────────────

// RegisterReader persists a new ambient IoT reader / interrogator.
func RegisterReader(readerID string, gnbIP, capabilities *string,
	lat, lon *float64) error {
	if strings.TrimSpace(readerID) == "" {
		return fmt.Errorf("reader_id is required")
	}
	_, err := engine.Exec(`INSERT INTO iot_readers
		(reader_id, gnb_ip, latitude, longitude, capabilities, status)
		VALUES (?, ?, ?, ?, ?, 'active')`,
		readerID, gnbIP, lat, lon, capabilities)
	return err
}

// HeartbeatReader stamps a reader's last_heartbeat — the operator
// dashboard uses this as the liveness signal.
func HeartbeatReader(readerID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := engine.Exec(`UPDATE iot_readers SET last_heartbeat=?,
		status='active' WHERE reader_id=?`, now, readerID)
	return err
}

// GetReader returns a reader by its reader_id.
func GetReader(readerID string) (*Reader, error) {
	row := engine.QueryRow(`SELECT reader_id, gnb_ip, latitude, longitude,
		capabilities, status, last_heartbeat FROM iot_readers WHERE reader_id=?`,
		readerID)
	var r Reader
	err := row.Scan(&r.ReaderID, &r.GnbIP, &r.Latitude, &r.Longitude,
		&r.Capabilities, &r.Status, &r.LastHeartbeat)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

// ListReaders returns every reader.
func ListReaders() ([]Reader, error) {
	rows, err := engine.Query(`SELECT reader_id, gnb_ip, latitude, longitude,
		capabilities, status, last_heartbeat FROM iot_readers ORDER BY reader_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reader
	for rows.Next() {
		var r Reader
		if err := rows.Scan(&r.ReaderID, &r.GnbIP, &r.Latitude, &r.Longitude,
			&r.Capabilities, &r.Status, &r.LastHeartbeat); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── Inventory event log ──────────────────────────────────────────

// LogInventory records an inventory / sensor-read / actuator event
// done by a reader (TS 22.369 §6.2 / §6.3 / §6.5 KPI capture).
// Returns the new row ID.
func LogInventory(readerID, eventType string, tagsFound int, resultJSON *string) (int64, error) {
	if strings.TrimSpace(readerID) == "" {
		return 0, fmt.Errorf("reader_id is required")
	}
	if eventType == "" {
		eventType = "inventory"
	}
	res, err := engine.Exec(`INSERT INTO iot_inventory_events
		(reader_id, event_type, tags_found, result_json)
		VALUES (?, ?, ?, ?)`, readerID, eventType, tagsFound, resultJSON)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListInventoryEvents returns recent events newest-first.
func ListInventoryEvents(limit int) ([]InventoryEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := engine.Query(`SELECT id, reader_id, event_type, tags_found,
		result_json, timestamp FROM iot_inventory_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InventoryEvent
	for rows.Next() {
		var e InventoryEvent
		if err := rows.Scan(&e.ID, &e.ReaderID, &e.EventType, &e.TagsFound,
			&e.ResultJSON, &e.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── GUI panel surface ────────────────────────────────────────────

// Status returns aggregate counts for the GUI panel.
func Status() map[string]any {
	row := engine.QueryRow(`SELECT
		(SELECT COUNT(*) FROM iot_tags),
		(SELECT COUNT(*) FROM iot_readers WHERE status='active'),
		(SELECT COUNT(*) FROM iot_inventory_events)`)
	var tags, readers, events int
	_ = row.Scan(&tags, &readers, &events)
	return map[string]any{
		"tags": tags, "active_readers": readers, "events": events,
	}
}
