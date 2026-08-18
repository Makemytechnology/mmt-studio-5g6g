// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pin -- Personal IoT Network (TS 23.542).
//
// Go port of services/pin/*.py.  PIN network and element CRUD.
// Tables: pin_networks, pin_elements, pin_data_log.
package pin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ---- Types ----

type Network struct {
	ID          int64    `json:"id"`
	OwnerIMSI   string   `json:"owner_imsi"`
	Name        string   `json:"name"`
	Description *string  `json:"description,omitempty"`
	GatewayIMSI *string  `json:"gateway_imsi,omitempty"`
	Status      string   `json:"status"`
	ConfigJSON  *string  `json:"config_json,omitempty"`
	CreatedAt   string   `json:"created_at"`
	Elements    []Element `json:"elements,omitempty"`
}

type Element struct {
	ID          int64   `json:"id"`
	PinID       int64   `json:"pin_id"`
	ElementID   string  `json:"element_id"`
	ElementType string  `json:"element_type"`
	Protocol    string  `json:"protocol"`
	Name        *string `json:"name,omitempty"`
	Status      string  `json:"status"`
	LastSeenAt  *string `json:"last_seen_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
}

// ---- GUI panel API ----

func List() ([]Network, error) { return ListNetworks("") }

func Status() map[string]any {
	nets, _ := ListNetworks("")
	return map[string]any{"count": len(nets), "items": nets}
}

// ---- Network CRUD (TS 23.542 Section 6.2) ----

func ListNetworks(ownerIMSI string) ([]Network, error) {
	q := `SELECT id, owner_imsi, name, description, gateway_imsi, status, config_json, created_at
		FROM pin_networks`
	var args []interface{}
	if ownerIMSI != "" {
		q += " WHERE owner_imsi=?"
		args = append(args, ownerIMSI)
	}
	q += " ORDER BY id"
	rows, err := engine.Query(q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Network
	for rows.Next() {
		var n Network
		if err := rows.Scan(&n.ID, &n.OwnerIMSI, &n.Name, &n.Description,
			&n.GatewayIMSI, &n.Status, &n.ConfigJSON, &n.CreatedAt); err != nil { return nil, err }
		out = append(out, n)
	}
	return out, rows.Err()
}

func GetNetwork(id int64) (*Network, error) {
	row := engine.QueryRow(`SELECT id, owner_imsi, name, description, gateway_imsi,
		status, config_json, created_at FROM pin_networks WHERE id=?`, id)
	var n Network
	err := row.Scan(&n.ID, &n.OwnerIMSI, &n.Name, &n.Description,
		&n.GatewayIMSI, &n.Status, &n.ConfigJSON, &n.CreatedAt)
	if err == sql.ErrNoRows { return nil, nil }
	if err != nil { return nil, err }
	n.Elements, _ = ListElements(id)
	return &n, nil
}

func CreateNetwork(ownerIMSI, name, description, gatewayIMSI string, config map[string]interface{}) (int64, error) {
	if ownerIMSI == "" { return 0, fmt.Errorf("owner_imsi is required") }
	if name == "" { return 0, fmt.Errorf("name is required") }
	var cfgJSON *string
	if config != nil {
		b, _ := json.Marshal(config)
		s := string(b)
		cfgJSON = &s
	}
	res, err := engine.Exec(`INSERT INTO pin_networks
		(owner_imsi, name, description, gateway_imsi, status, config_json)
		VALUES (?,?,?,?,'active',?)`,
		ownerIMSI, name, nilStr(description), nilStr(gatewayIMSI), cfgJSON)
	if err != nil { return 0, err }
	return res.LastInsertId()
}

func SetGateway(pinID int64, gatewayIMSI string) error {
	_, err := engine.Exec(`UPDATE pin_networks SET gateway_imsi=? WHERE id=?`, gatewayIMSI, pinID)
	return err
}

func DeleteNetwork(id int64) error {
	_, err := engine.Exec(`DELETE FROM pin_networks WHERE id=?`, id)
	return err
}

// ---- Element CRUD (TS 23.542 Section 6.3) ----

func ListElements(pinID int64) ([]Element, error) {
	rows, err := engine.Query(`SELECT id, pin_id, element_id, element_type,
		protocol, name, status, last_seen_at, created_at
		FROM pin_elements WHERE pin_id=? ORDER BY id`, pinID)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Element
	for rows.Next() {
		var e Element
		if err := rows.Scan(&e.ID, &e.PinID, &e.ElementID, &e.ElementType,
			&e.Protocol, &e.Name, &e.Status, &e.LastSeenAt, &e.CreatedAt); err != nil { return nil, err }
		out = append(out, e)
	}
	return out, rows.Err()
}

func AddElement(pinID int64, elementID, elementType, protocol, name string) (int64, error) {
	validTypes := map[string]bool{"sensor": true, "actuator": true, "gateway": true, "wearable": true}
	validProtos := map[string]bool{"BLE": true, "Zigbee": true, "WiFi": true, "Thread": true, "NFC": true}
	if !validTypes[elementType] { return 0, fmt.Errorf("invalid element_type") }
	if !validProtos[protocol] { return 0, fmt.Errorf("invalid protocol") }
	res, err := engine.Exec(`INSERT INTO pin_elements
		(pin_id, element_id, element_type, protocol, name, status)
		VALUES (?,?,?,?,?,'disconnected')`, pinID, elementID, elementType, protocol, nilStr(name))
	if err != nil { return 0, err }
	return res.LastInsertId()
}

func RemoveElement(id int64) error {
	_, err := engine.Exec(`DELETE FROM pin_elements WHERE id=?`, id)
	return err
}

// ---- PIN Gateway (TS 23.542 Section 5.2 / Section 6.4-6.5) ----

type Gateway struct {
	IMSI         string                 `json:"imsi"`
	Capabilities map[string]interface{} `json:"capabilities"`
	Status       string                 `json:"status"`
	RegisteredAt string                 `json:"registered_at"`
	LastSeen     string                 `json:"last_seen"`
}

var (
	gateways   = map[string]*Gateway{}
	gatewaysMu sync.Mutex
)

func RegisterGateway(imsi string, capabilities map[string]interface{}) (*Gateway, error) {
	imsi = strings.TrimSpace(imsi)
	if imsi == "" { return nil, fmt.Errorf("imsi is required") }
	if capabilities == nil { capabilities = map[string]interface{}{} }
	now := time.Now().UTC().Format(time.RFC3339)
	gw := &Gateway{IMSI: imsi, Capabilities: capabilities, Status: "reachable", RegisteredAt: now, LastSeen: now}
	gatewaysMu.Lock(); gateways[imsi] = gw; gatewaysMu.Unlock()
	return gw, nil
}

func GetGateway(imsi string) *Gateway {
	gatewaysMu.Lock(); defer gatewaysMu.Unlock()
	return gateways[imsi]
}

func ListGateways() []*Gateway {
	gatewaysMu.Lock(); defer gatewaysMu.Unlock()
	out := make([]*Gateway, 0, len(gateways))
	for _, gw := range gateways { out = append(out, gw) }
	return out
}

func UpdateGatewayReachability(imsi string, reachable bool) (*Gateway, error) {
	gatewaysMu.Lock(); defer gatewaysMu.Unlock()
	gw := gateways[imsi]
	if gw == nil { return nil, fmt.Errorf("gateway %s not registered", imsi) }
	if reachable { gw.Status = "reachable" } else { gw.Status = "unreachable" }
	gw.LastSeen = time.Now().UTC().Format(time.RFC3339)
	return gw, nil
}

// ---- Data Relay (TS 23.542 Section 6.4) ----

func RelayData(pinID int64, elementID, dataHex, direction string) (map[string]interface{}, error) {
	net, err := GetNetwork(pinID)
	if err != nil { return nil, err }
	if net == nil { return nil, fmt.Errorf("PIN network %d not found", pinID) }

	// Check element exists
	row := engine.QueryRow(`SELECT id FROM pin_elements WHERE pin_id=? AND element_id=?`, pinID, elementID)
	var eid int64
	if row.Scan(&eid) != nil { return nil, fmt.Errorf("element %s not found in PIN %d", elementID, pinID) }

	// Check gateway reachability
	if net.GatewayIMSI != nil && *net.GatewayIMSI != "" {
		gatewaysMu.Lock()
		gw := gateways[*net.GatewayIMSI]
		gatewaysMu.Unlock()
		if gw != nil && gw.Status != "reachable" {
			return nil, fmt.Errorf("PINGW %s is unreachable -- cannot relay", *net.GatewayIMSI)
		}
	}

	if direction == "" { direction = "UL" }
	dataSize := len(dataHex) / 2
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := engine.Exec(`INSERT INTO pin_data_log (pin_id, element_id, direction, data_hex, data_size, created_at)
		VALUES (?,?,?,?,?,?)`, pinID, elementID, direction, dataHex, dataSize, now)
	if err != nil { return nil, err }
	logID, _ := res.LastInsertId()

	gwImsi := ""
	if net.GatewayIMSI != nil { gwImsi = *net.GatewayIMSI }
	return map[string]interface{}{
		"log_id": logID, "pin_id": pinID, "element_id": elementID,
		"direction": direction, "data_size": dataSize, "gateway_imsi": gwImsi,
	}, nil
}

// ListDataLog returns recent data relay log entries for a PIN.
func ListDataLog(pinID int64, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 { limit = 200 }
	rows, err := engine.Query(`SELECT id, pin_id, element_id, direction, data_hex, data_size, created_at
		FROM pin_data_log WHERE pin_id=? ORDER BY id DESC LIMIT ?`, pinID, limit)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []map[string]interface{}
	for rows.Next() {
		var id, pid int64; var eid, dir, dh string; var ds int; var ca string
		rows.Scan(&id, &pid, &eid, &dir, &dh, &ds, &ca)
		out = append(out, map[string]interface{}{
			"id": id, "pin_id": pid, "element_id": eid, "direction": dir,
			"data_hex": dh, "data_size": ds, "created_at": ca,
		})
	}
	return out, nil
}

// GetPINStats returns aggregate statistics.
func GetPINStats() map[string]interface{} {
	var nets, elems, logs int
	row := engine.QueryRow(`SELECT COUNT(*) FROM pin_networks`); row.Scan(&nets)
	row = engine.QueryRow(`SELECT COUNT(*) FROM pin_elements`); row.Scan(&elems)
	row = engine.QueryRow(`SELECT COUNT(*) FROM pin_data_log`); row.Scan(&logs)
	return map[string]interface{}{"networks": nets, "elements": elems, "data_logs": logs}
}

func nilStr(s string) interface{} {
	if s == "" { return nil }
	return s
}
