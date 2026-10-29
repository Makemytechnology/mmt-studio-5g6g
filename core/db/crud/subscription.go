// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/subscription.go — UE AMBR CRUD (now stored on ue table directly)
package crud

import "github.com/mmt/mmt-studio-core/db/engine"

// Subscription is the {imsi, ambr{uplink,downlink}} shape returned by the Python helper.
type Subscription struct {
	IMSI string              `json:"imsi"`
	AMBR SubscriptionAMBR    `json:"ambr"`
}

type SubscriptionAMBR struct {
	DownlinkKbps int64 `json:"downlink_kbps"`
	UplinkKbps   int64 `json:"uplink_kbps"`
}

// SubscriptionGetByIMSI returns the subscription (AMBR) for a UE, or nil if not found.
func SubscriptionGetByIMSI(imsi string) (*Subscription, error) {
	u, err := UEGetByIMSI(imsi)
	if err != nil || u == nil {
		return nil, err
	}
	return &Subscription{
		IMSI: imsi,
		AMBR: SubscriptionAMBR{
			DownlinkKbps: u.AMBRDLKbps,
			UplinkKbps:   u.AMBRULKbps,
		},
	}, nil
}

// SubscriptionUpsert persists AMBR on the ue row (auto-creates the row
// if missing). Stores exactly what the caller provides — no hidden seed
// fallback. 0 means "unlimited" (no UE-AMBR enforcement), which the
// UPF meter treats as pass-through. Callers that want a guard should
// validate before calling.
func SubscriptionUpsert(imsi string, ambrDL, ambrUL int64) error {
	ueID, err := UEGetOrCreateByIMSI(imsi, nil)
	if err != nil {
		return err
	}
	if ambrDL < 0 {
		ambrDL = 0
	}
	if ambrUL < 0 {
		ambrUL = 0
	}
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`UPDATE ue SET ambr_dl_kbps=?, ambr_ul_kbps=? WHERE id=?`,
		ambrDL, ambrUL, ueID,
	)
	return err
}

// SubscriptionDelete clears the UE's AMBR to "unlimited" (0). Reports
// whether the UE existed. Previously wrote back a seed default of
// 1 000 000 kbps — no more: DB must reflect exactly what the operator
// asks for.
func SubscriptionDelete(imsi string) (bool, error) {
	u, err := UEGetByIMSI(imsi)
	if err != nil || u == nil {
		return false, err
	}
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	_, err = db.Exec(
		`UPDATE ue SET ambr_dl_kbps=0, ambr_ul_kbps=0 WHERE id=?`, u.ID,
	)
	return err == nil, err
}
