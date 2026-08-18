// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Balance manager -- prepaid balance enforcement (TS 32.291 section 6.1).
// Port of nf/chf/balance_manager.py.
//
// Manages subscriber balances for online (prepaid) charging:
//   - Check balance before session
//   - Debit on usage
//   - Recharge / top-up
//   - Low balance detection
package chf

import (
	"database/sql"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Balance holds a row from the balances table.
type Balance struct {
	IMSI                string  `json:"imsi"`
	BalanceType         string  `json:"balance_type"`
	BalanceAmount       float64 `json:"balance_amount"`
	Currency            string  `json:"currency"`
	CreditLimit         float64 `json:"credit_limit"`
	LowBalanceThreshold float64 `json:"low_balance_threshold"`
	Status              string  `json:"status"`
}

// GetBalance returns a subscriber's balance for the given type.
// Returns nil if no row exists.
func GetBalance(imsi, balanceType string) *Balance {
	if balanceType == "" {
		balanceType = "main"
	}
	db, err := engine.Open()
	if err != nil {
		return nil
	}
	var b Balance
	err = db.QueryRow(`SELECT imsi, balance_type, balance_amount, currency,
		credit_limit, low_balance_threshold, status
		FROM balances WHERE imsi=? AND balance_type=?`,
		imsi, balanceType).Scan(
		&b.IMSI, &b.BalanceType, &b.BalanceAmount, &b.Currency,
		&b.CreditLimit, &b.LowBalanceThreshold, &b.Status)
	if err != nil {
		return nil
	}
	return &b
}

// GetAllBalances returns all balance types for a subscriber.
func GetAllBalances(imsi string) []Balance {
	db, err := engine.Open()
	if err != nil {
		return nil
	}
	rows, err := db.Query(`SELECT imsi, balance_type, balance_amount, currency,
		credit_limit, low_balance_threshold, status
		FROM balances WHERE imsi=?`, imsi)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var balances []Balance
	for rows.Next() {
		var b Balance
		if rows.Scan(&b.IMSI, &b.BalanceType, &b.BalanceAmount, &b.Currency,
			&b.CreditLimit, &b.LowBalanceThreshold, &b.Status) == nil {
			balances = append(balances, b)
		}
	}
	return balances
}

// CreateBalance creates a balance entry for a subscriber.
func CreateBalance(imsi, balanceType string, amount float64, currency string,
	creditLimit, lowThreshold float64) {
	log := logger.Get("chf.balance")

	if balanceType == "" {
		balanceType = "main"
	}
	if currency == "" {
		currency = "USD"
	}
	db, err := engine.Open()
	if err != nil {
		return
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO balances
		(imsi, balance_type, balance_amount, currency,
		 credit_limit, low_balance_threshold, status)
		VALUES (?, ?, ?, ?, ?, ?, 'active')`,
		imsi, balanceType, amount, currency, creditLimit, lowThreshold)
	if err != nil {
		log.Warnf("CreateBalance: %v", err)
		return
	}
	log.Infof("Balance created: imsi=%s type=%s amount=%.2f %s", imsi, balanceType, amount, currency)
}

// Recharge adds credit to a subscriber balance.
// Returns the new balance amount.
func Recharge(imsi string, amount float64, balanceType, reference string) float64 {
	log := logger.Get("chf.balance")

	if balanceType == "" {
		balanceType = "main"
	}
	db, err := engine.Open()
	if err != nil {
		return 0
	}

	var balanceBefore float64
	err = db.QueryRow(`SELECT balance_amount FROM balances WHERE imsi=? AND balance_type=?`,
		imsi, balanceType).Scan(&balanceBefore)
	if err != nil {
		// No existing balance -- create one.
		CreateBalance(imsi, balanceType, amount, "USD", 0, 1.0)
		recordTxn(db, imsi, "recharge", amount, 0, amount, reference)
		return amount
	}

	balanceAfter := balanceBefore + amount
	now := float64(time.Now().Unix())
	db.Exec(`UPDATE balances SET balance_amount=?, last_recharge_at=?, status='active'
		WHERE imsi=? AND balance_type=?`,
		balanceAfter, now, imsi, balanceType)
	recordTxn(db, imsi, "recharge", amount, balanceBefore, balanceAfter, reference)

	log.Infof("Recharge: imsi=%s +%.2f (%.2f -> %.2f)", imsi, amount, balanceBefore, balanceAfter)
	return balanceAfter
}

// Debit subtracts from a subscriber balance (usage charge).
// Returns (success, newBalance).
func Debit(imsi string, amount float64, balanceType, reference string) (bool, float64) {
	log := logger.Get("chf.balance")

	if balanceType == "" {
		balanceType = "main"
	}
	db, err := engine.Open()
	if err != nil {
		return false, 0
	}

	var balanceBefore, creditLimit, threshold float64
	err = db.QueryRow(`SELECT balance_amount, credit_limit, low_balance_threshold
		FROM balances WHERE imsi=? AND balance_type=?`,
		imsi, balanceType).Scan(&balanceBefore, &creditLimit, &threshold)
	if err != nil {
		return false, 0
	}

	balanceAfter := balanceBefore - amount

	// Check if within credit limit.
	if balanceAfter < -creditLimit {
		log.Warnf("Debit rejected: imsi=%s amount=%.2f balance=%.2f limit=%.2f",
			imsi, amount, balanceBefore, creditLimit)
		return false, balanceBefore
	}

	status := "active"
	if balanceAfter <= 0 {
		status = "exhausted"
	} else if balanceAfter <= threshold {
		log.Warnf("Low balance alert: imsi=%s balance=%.2f", imsi, balanceAfter)
	}

	db.Exec(`UPDATE balances SET balance_amount=?, status=? WHERE imsi=? AND balance_type=?`,
		balanceAfter, status, imsi, balanceType)
	recordTxn(db, imsi, "debit", amount, balanceBefore, balanceAfter, reference)

	log.Infof("Debit: imsi=%s -%.2f (%.2f -> %.2f) status=%s",
		imsi, amount, balanceBefore, balanceAfter, status)
	return true, balanceAfter
}

// CheckBalance returns (allowed, currentBalance) for a prepaid subscriber.
// If requiredAmount > 0 it checks that balance + credit_limit >= required.
// If no balance row exists the subscriber is treated as postpaid (allowed).
func CheckBalance(imsi string, requiredAmount float64) (bool, float64) {
	bal := GetBalance(imsi, "main")
	if bal == nil {
		return true, 0 // no balance row -> postpaid
	}
	if bal.Status == "exhausted" {
		return false, bal.BalanceAmount
	}
	if bal.BalanceAmount+bal.CreditLimit < requiredAmount {
		return false, bal.BalanceAmount
	}
	return true, bal.BalanceAmount
}

// recordTxn inserts a payment_transactions row.
func recordTxn(db *sql.DB, imsi, txnType string, amount, before, after float64, reference string) {
	now := float64(time.Now().Unix())
	db.Exec(`INSERT INTO payment_transactions
		(imsi, txn_type, amount, currency, balance_before, balance_after,
		 reference, created_at)
		VALUES (?, ?, ?, 'USD', ?, ?, ?, ?)`,
		imsi, txnType, amount, before, after, reference, now)
}
