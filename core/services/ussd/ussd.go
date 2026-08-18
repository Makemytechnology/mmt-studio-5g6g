// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ussd -- USSD over IMS (TS 24.390).
//
// Go port of services/ussd/*.py.  Menu tree and session management.
// Tables: ussd_menus, ussd_sessions.
package ussd

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ---- Types ----

type Menu struct {
	ID           int64   `json:"id"`
	Code         *string `json:"code,omitempty"`
	ParentID     *int64  `json:"parent_id,omitempty"`
	Title        string  `json:"title"`
	ActionType   *string `json:"action_type,omitempty"`
	ActionData   *string `json:"action_data,omitempty"`
	DisplayOrder int     `json:"display_order"`
}

type Session struct {
	ID            int64   `json:"id"`
	IMSI          string  `json:"imsi"`
	Code          string  `json:"code"`
	State         string  `json:"state"`
	CurrentMenuID *int64  `json:"current_menu_id,omitempty"`
	SessionData   *string `json:"session_data,omitempty"`
	CreatedAt     string  `json:"created_at"`
	EndedAt       *string `json:"ended_at,omitempty"`
}

// ---- GUI panel API ----

func List() ([]Menu, error) { return ListMenus() }

func Status() map[string]any {
	menus, _ := ListMenus()
	return map[string]any{"count": len(menus), "items": menus}
}

// ---- Menu CRUD ----

func ListMenus() ([]Menu, error) {
	rows, err := engine.Query(`SELECT id, code, parent_id, title,
		action_type, action_data, display_order
		FROM ussd_menus ORDER BY display_order, id`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Menu
	for rows.Next() {
		var m Menu
		if err := rows.Scan(&m.ID, &m.Code, &m.ParentID, &m.Title,
			&m.ActionType, &m.ActionData, &m.DisplayOrder); err != nil { return nil, err }
		out = append(out, m)
	}
	return out, rows.Err()
}

func GetMenuByCode(code string) (*Menu, error) {
	row := engine.QueryRow(`SELECT id, code, parent_id, title,
		action_type, action_data, display_order
		FROM ussd_menus WHERE code=?`, code)
	var m Menu
	err := row.Scan(&m.ID, &m.Code, &m.ParentID, &m.Title,
		&m.ActionType, &m.ActionData, &m.DisplayOrder)
	if err == sql.ErrNoRows { return nil, nil }
	return &m, err
}

func GetMenuByID(id int64) (*Menu, error) {
	row := engine.QueryRow(`SELECT id, code, parent_id, title,
		action_type, action_data, display_order
		FROM ussd_menus WHERE id=?`, id)
	var m Menu
	err := row.Scan(&m.ID, &m.Code, &m.ParentID, &m.Title,
		&m.ActionType, &m.ActionData, &m.DisplayOrder)
	if err == sql.ErrNoRows { return nil, nil }
	return &m, err
}

func GetChildren(parentID int64) ([]Menu, error) {
	rows, err := engine.Query(`SELECT id, code, parent_id, title,
		action_type, action_data, display_order
		FROM ussd_menus WHERE parent_id=? ORDER BY display_order, id`, parentID)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Menu
	for rows.Next() {
		var m Menu
		if err := rows.Scan(&m.ID, &m.Code, &m.ParentID, &m.Title,
			&m.ActionType, &m.ActionData, &m.DisplayOrder); err != nil { return nil, err }
		out = append(out, m)
	}
	return out, rows.Err()
}

func CreateMenu(code *string, title string, parentID *int64,
	actionType, actionData string, displayOrder int) (int64, error) {
	res, err := engine.Exec(`INSERT INTO ussd_menus
		(code, parent_id, title, action_type, action_data, display_order)
		VALUES (?,?,?,?,?,?)`,
		code, parentID, title, nilStr(actionType), nilStr(actionData), displayOrder)
	if err != nil { return 0, err }
	return res.LastInsertId()
}

func DeleteMenu(id int64) error {
	_, err := engine.Exec(`DELETE FROM ussd_menus WHERE id=?`, id)
	return err
}

// MenuCount returns the number of menu nodes.
func MenuCount() int {
	row := engine.QueryRow(`SELECT COUNT(*) FROM ussd_menus`)
	var n int
	_ = row.Scan(&n)
	return n
}

// ---- Session Management ----

// InitiateSession starts a USSD session (TS 24.390 Section 4.2.1).
func InitiateSession(imsi, ussdCode string) map[string]interface{} {
	if imsi == "" || ussdCode == "" {
		return map[string]interface{}{"error": "imsi and ussd_string required"}
	}
	code := strings.TrimSpace(ussdCode)
	menu, err := GetMenuByCode(code)
	if err != nil || menu == nil {
		return map[string]interface{}{"error": fmt.Sprintf("unknown USSD code: %s", code)}
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, _ := engine.Exec(`INSERT INTO ussd_sessions
		(imsi, code, state, current_menu_id, created_at)
		VALUES (?,?,'active',?,?)`, imsi, code, menu.ID, now)
	sessionID, _ := res.LastInsertId()

	// If leaf action, execute immediately
	if menu.ActionType != nil && *menu.ActionType != "menu" {
		text := executeAction(imsi, menu)
		endSessionDB(sessionID, "completed")
		return map[string]interface{}{
			"session_id": sessionID, "type": "response", "text": text, "ended": true,
		}
	}

	// Build menu text
	children, _ := GetChildren(menu.ID)
	text := buildMenuText(menu, children)

	// Store in-memory state for ContinueSession
	sessMu.Lock()
	activeSessions[sessionID] = &activeSession{
		imsi: imsi, code: code, currentMenuID: menu.ID,
		children: children, startedAt: time.Now(),
	}
	sessMu.Unlock()

	return map[string]interface{}{
		"session_id": sessionID, "type": "menu", "text": text, "ended": false,
	}
}

// EndSession explicitly ends a session.
func EndSession(sessionID int64) {
	endSessionDB(sessionID, "completed")
}

// ListSessions returns all sessions (optionally filtered).
func ListSessions(imsi, state string) ([]Session, error) {
	q := `SELECT id, imsi, code, state, current_menu_id, session_data, created_at, ended_at
		FROM ussd_sessions`
	var args []interface{}
	var conds []string
	if imsi != "" { conds = append(conds, "imsi=?"); args = append(args, imsi) }
	if state != "" { conds = append(conds, "state=?"); args = append(args, state) }
	if len(conds) > 0 { q += " WHERE " + strings.Join(conds, " AND ") }
	q += " ORDER BY id DESC"
	rows, err := engine.Query(q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.IMSI, &s.Code, &s.State, &s.CurrentMenuID,
			&s.SessionData, &s.CreatedAt, &s.EndedAt); err != nil { return nil, err }
		out = append(out, s)
	}
	return out, rows.Err()
}

// SeedDefaultMenus creates the default menu tree if empty.
func SeedDefaultMenus() {
	if MenuCount() > 0 { return }
	mainID := ptrInt64(0)
	code100 := ptrStr("*100#")
	res, _ := engine.Exec(`INSERT INTO ussd_menus (code, title, action_type, display_order)
		VALUES (?,'Main Menu','menu',0)`, code100)
	id, _ := res.LastInsertId()
	mainID = &id

	for i, item := range []struct{ title, action string }{
		{"Balance Check", "balance_check"}, {"Data Usage", "data_usage"},
		{"Top-Up", "topup"}, {"My Number", "show_msisdn"},
	} {
		_, _ = engine.Exec(`INSERT INTO ussd_menus (parent_id, title, action_type, display_order)
			VALUES (?,?,?,?)`, mainID, item.title, item.action, i+1)
	}
	_, _ = engine.Exec(`INSERT INTO ussd_menus (parent_id, title, action_type, action_data, display_order)
		VALUES (?,'Customer Care','custom_text','Call 100 for customer support',5)`, mainID)

	code123 := ptrStr("*123#")
	code124 := ptrStr("*124#")
	_, _ = engine.Exec(`INSERT INTO ussd_menus (code, title, action_type, display_order)
		VALUES (?,'Quick Balance','balance_check',10)`, code123)
	_, _ = engine.Exec(`INSERT INTO ussd_menus (code, title, action_type, display_order)
		VALUES (?,'Data Bundle','data_usage',11)`, code124)
}

// ---- internal helpers ----

func endSessionDB(sessionID int64, state string) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, _ = engine.Exec(`UPDATE ussd_sessions SET state=?, ended_at=? WHERE id=?`, state, now, sessionID)
}

func buildMenuText(menu *Menu, children []Menu) string {
	var b strings.Builder
	b.WriteString(menu.Title + "\n")
	b.WriteString(strings.Repeat("-", len(menu.Title)) + "\n")
	for i, c := range children {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c.Title)
	}
	b.WriteString("\n0. Exit")
	return b.String()
}

func executeAction(imsi string, menu *Menu) string {
	action := ""
	if menu.ActionType != nil { action = *menu.ActionType }
	switch action {
	case "balance_check":
		return "Balance check: please query via API."
	case "data_usage":
		return "Data usage: please query via API."
	case "show_msisdn":
		row := engine.QueryRow(`SELECT msisdn FROM ue WHERE imsi=?`, imsi)
		var msisdn sql.NullString
		if row.Scan(&msisdn) == nil && msisdn.Valid {
			return "Your number: " + msisdn.String
		}
		return "MSISDN not found."
	case "custom_text":
		if menu.ActionData != nil { return *menu.ActionData }
		return "No information available."
	case "topup":
		return "Enter top-up amount:"
	default:
		return "Unknown action: " + action
	}
}

// ContinueSession continues an active USSD session with user input.
// TS 24.390 Section 4.2.2: User responds to network-initiated menu.
func ContinueSession(sessionID int64, userInput string) map[string]interface{} {
	sessMu.Lock()
	sess, ok := activeSessions[sessionID]
	sessMu.Unlock()

	if !ok {
		return map[string]interface{}{"error": "Session not found", "session_id": sessionID}
	}

	// Check timeout (180s per TS 22.090 Section 3.3)
	if time.Since(sess.startedAt) > sessionTimeout {
		endSessionInternal(sessionID, "timeout")
		return map[string]interface{}{
			"session_id": sessionID, "type": "response",
			"text": "Session timed out. Please try again.", "ended": true,
		}
	}

	input := strings.TrimSpace(userInput)

	// Handle '0' or 'exit' as cancel
	if input == "0" || strings.ToLower(input) == "exit" {
		endSessionInternal(sessionID, "completed")
		return map[string]interface{}{
			"session_id": sessionID, "type": "response",
			"text": "Session ended. Thank you.", "ended": true,
		}
	}

	// Parse numeric choice
	choice, err := strconv.Atoi(input)
	if err != nil {
		// Check if current menu expects text input (e.g. topup amount)
		menu, _ := GetMenuByID(sess.currentMenuID)
		if menu != nil && menu.ActionType != nil && *menu.ActionType == "topup" {
			text := executeTopup(sess.imsi, input)
			endSessionInternal(sessionID, "completed")
			return map[string]interface{}{
				"session_id": sessionID, "type": "response",
				"text": text, "ended": true,
			}
		}
		return map[string]interface{}{
			"session_id": sessionID, "type": "menu",
			"text": fmt.Sprintf("Invalid input. Please enter a number (1-%d) or 0 to exit.", len(sess.children)),
			"ended": false,
		}
	}

	// Validate choice range
	if choice < 1 || choice > len(sess.children) {
		return map[string]interface{}{
			"session_id": sessionID, "type": "menu",
			"text": fmt.Sprintf("Invalid choice. Please enter 1-%d or 0 to exit.", len(sess.children)),
			"ended": false,
		}
	}

	selected := sess.children[choice-1]

	// If selected item is a sub-menu, navigate into it
	if selected.ActionType != nil && *selected.ActionType == "menu" {
		subChildren, _ := GetChildren(selected.ID)
		text := buildMenuText(&selected, subChildren)
		sessMu.Lock()
		if s, ok := activeSessions[sessionID]; ok {
			s.currentMenuID = selected.ID
			s.children = subChildren
		}
		sessMu.Unlock()
		_, _ = engine.Exec(`UPDATE ussd_sessions SET current_menu_id=? WHERE id=?`, selected.ID, sessionID)
		return map[string]interface{}{
			"session_id": sessionID, "type": "menu",
			"text": text, "ended": false,
		}
	}

	// If selected item is topup, prompt for amount
	if selected.ActionType != nil && *selected.ActionType == "topup" {
		sessMu.Lock()
		if s, ok := activeSessions[sessionID]; ok {
			s.currentMenuID = selected.ID
			s.children = nil
		}
		sessMu.Unlock()
		_, _ = engine.Exec(`UPDATE ussd_sessions SET current_menu_id=? WHERE id=?`, selected.ID, sessionID)
		return map[string]interface{}{
			"session_id": sessionID, "type": "menu",
			"text": "Enter top-up amount (e.g. 10, 20, 50):", "ended": false,
		}
	}

	// Leaf action -- execute and end
	text := executeAction(sess.imsi, &selected)
	endSessionInternal(sessionID, "completed")
	return map[string]interface{}{
		"session_id": sessionID, "type": "response",
		"text": text, "ended": true,
	}
}

// UpdateMenu updates an existing menu node.
func UpdateMenu(id int64, fields map[string]interface{}) error {
	setClauses := []string{}
	args := []interface{}{}
	for _, k := range []string{"title", "code", "action_type", "action_data", "display_order", "parent_id"} {
		if v, ok := fields[k]; ok {
			setClauses = append(setClauses, k+"=?")
			args = append(args, v)
		}
	}
	if len(setClauses) == 0 { return nil }
	args = append(args, id)
	_, err := engine.Exec(fmt.Sprintf("UPDATE ussd_menus SET %s WHERE id=?", strings.Join(setClauses, ", ")), args...)
	return err
}

// -- in-memory session store for interactive sessions --

type activeSession struct {
	imsi          string
	code          string
	currentMenuID int64
	children      []Menu
	startedAt     time.Time
}

var (
	activeSessions = map[int64]*activeSession{}
	sessMu         sync.Mutex
	sessionTimeout = 180 * time.Second
)

func endSessionInternal(sessionID int64, state string) {
	sessMu.Lock()
	delete(activeSessions, sessionID)
	sessMu.Unlock()
	endSessionDB(sessionID, state)
}

func executeTopup(imsi, amountStr string) string {
	amount, err := strconv.ParseFloat(amountStr, 64)
	if err != nil || amount <= 0 || amount > 10000 {
		return "Invalid amount. Please enter a value between 1 and 10000."
	}
	now := float64(time.Now().Unix())
	row := engine.QueryRow(`SELECT id, balance_amount, currency FROM balances WHERE imsi=? AND balance_type='main'`, imsi)
	var id int64
	var bal float64
	var currency string
	if row.Scan(&id, &bal, &currency) == nil {
		newBal := bal + amount
		engine.Exec(`UPDATE balances SET balance_amount=?, last_recharge_at=?, status='active' WHERE id=?`, newBal, now, id)
		engine.Exec(`INSERT INTO payment_transactions (imsi, txn_type, amount, currency, balance_before, balance_after, reference, created_at)
			VALUES (?, 'recharge', ?, ?, ?, ?, 'USSD top-up', ?)`, imsi, amount, currency, bal, newBal, now)
		return fmt.Sprintf("Top-up successful!\n  Amount:      %s %.2f\n  New Balance: %s %.2f", currency, amount, currency, newBal)
	}
	// Create new balance
	engine.Exec(`INSERT INTO balances (imsi, balance_type, balance_amount, currency, last_recharge_at, status) VALUES (?, 'main', ?, 'USD', ?, 'active')`, imsi, amount, now)
	engine.Exec(`INSERT INTO payment_transactions (imsi, txn_type, amount, currency, balance_before, balance_after, reference, created_at)
		VALUES (?, 'recharge', ?, 'USD', 0, ?, 'USSD top-up', ?)`, imsi, amount, amount, now)
	return fmt.Sprintf("Top-up successful!\n  Amount:  USD %.2f\n  Balance: USD %.2f", amount, amount)
}

func nilStr(s string) interface{} {
	if s == "" { return nil }
	return s
}
func ptrStr(s string) *string { return &s }
func ptrInt64(n int64) *int64 { return &n }
