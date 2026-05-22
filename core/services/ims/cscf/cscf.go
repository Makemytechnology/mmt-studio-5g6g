// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package cscf — combined P/I/S-CSCF container.
//
// The S-CSCF registration machinery lives in registration.go as a
// per-IMPI libs/fsm state machine (TS 24.229 §5.4.1). This file is
// the container that routes incoming REGISTER requests to the right
// per-IMPI FSM and holds the dialog table used by §5.4.3 "General
// treatment for all dialogs". Dialog state is still a simple map —
// a dialog FSM is a separate future change.
//
// The actual SIP procedures that sit on top of the FSMs (reading a
// REGISTER off the wire, building the 401 challenge response,
// routing INVITEs through the registrar) are not implemented in
// this package yet — see services/ims/cscf/register.go for the
// REGISTER parsing helper that pulls identities out of a libs/sip
// message.
//
// PDF anchor: specs/3gpp/ts_124229v190600p.pdf.
package cscf

import (
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/services/ims/conference"
)

var log = logger.Get("ims.cscf")

// Config holds CSCF configuration.
type Config struct {
	Host       string
	Port       int
	IMSDomain  string
	ServerName string

	// AuthorizeMedia is invoked when an INVITE arrives carrying SDP.
	// The CSCF passes the caller's IMSI (resolved from the From URI
	// via the registered IMPU→IMPI map) and the raw SDP body so the
	// implementation can run the AF→PCF authorization (TS 29.514
	// §4.2.2 Npcf_PolicyAuthorization_Create + the §4.2.3 N7
	// UpdateNotify push to the SMF).
	//
	// nil → AF authorization is skipped (best-effort media; no GBR
	// QoS Flow will be added). services/ims wires this to the real
	// AF/PCF/SM-policy chain.
	AuthorizeMedia func(imsi, sdp string) bool

	// ReleaseMedia is the symmetric BYE-side hook (TS 29.514 §4.2.4
	// Npcf_PolicyAuthorization_Delete) — deactivates the PCC rules
	// installed at INVITE time.
	ReleaseMedia func(imsi string) bool
}

// CSCF is the combined P/I/S-CSCF container.
type CSCF struct {
	Config

	mu            sync.Mutex
	registrations map[string]*Registration // IMPI → per-IMPI FSM
	dialogs       map[string]*DialogInfo   // callID → dialog

	// ConferenceAS is the Conference Application Server backing
	// the conference-factory URI (TS 24.147 §5.3.2.3.1). When set,
	// dispatchInvite routes INVITEs whose Request-URI matches the
	// factory pattern (e.g. sip:conference-factory@<domain>) to the
	// focus role instead of running the normal registrar terminating
	// lookup. nil → factory INVITEs fall through to the 480 path
	// (lab configurations that don't need conferencing).
	ConferenceAS *conference.ConferenceAS
}

// DialogInfo tracks an established SIP dialog (§5.4.3 / RFC 3261 §12.1.1).
//
// Captured at 200 OK time on the INVITE-receiving UAS; the to-tag we
// minted on the 18x / 200 is the local-tag component of the dialog
// ID per §12.1.1, and CallerIMSI lets the BYE / CANCEL / re-INVITE
// path resolve back to the SDP-driven SmPolicy association without
// re-running the From-URI → Registration → IMSI lookup (which can
// fail on terminating-leg BYEs where the dialog roles are inverted).
type DialogInfo struct {
	ToTag string
	// CallerIMSI is the IMSI of the UE that originated the INVITE.
	// All AF/PCF policy for this dialog hangs off this IMSI's PDU
	// session — even a callee-side BYE/CANCEL must release the
	// caller's GBR rules, not the callee's.
	CallerIMSI string
	// ConferenceURI is the conf URI minted by the focus role
	// (TS 24.147 §5.3.2.3.1 step 3) when this dialog was created
	// from a conference-factory INVITE. Empty for normal UE-to-UE
	// dialogs. Used so re-INVITEs in the same dialog reuse the
	// existing URI (RFC 3261 §12.2.1.1 stable dialog ID).
	ConferenceURI string
}

// New creates a CSCF container.
func New(host string, port int, domain string) *CSCF {
	return &CSCF{
		Config: Config{
			Host: host, Port: port, IMSDomain: domain,
			ServerName: fmt.Sprintf("sip:scscf.%s:%d", domain, port),
		},
		registrations: make(map[string]*Registration),
		dialogs:       make(map[string]*DialogInfo),
	}
}

// GetOrCreateRegistration returns the per-IMPI Registration FSM,
// creating (and starting) one on first access.
func (c *CSCF) GetOrCreateRegistration(impi string) *Registration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.registrations[impi]; ok {
		return r
	}
	r := NewRegistration(impi)
	c.registrations[impi] = r
	log.Infof("CSCF: created registration FSM for IMPI=%s", impi)
	return r
}

// GetRegistration returns the FSM if one exists, else nil.
func (c *CSCF) GetRegistration(impi string) *Registration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.registrations[impi]
}

// LookupRegistrationByIMPU walks the registration table looking for a
// Registered FSM whose IMPU matches the supplied URI (e.g. the To-URI
// of an incoming INVITE). Returns nil if no Registered match.
//
// TS 24.229 §5.4.3.1 makes the S-CSCF responsible for routing
// terminating requests to the contact address bound at registration
// time; this is the registrar half of that lookup.
func (c *CSCF) LookupRegistrationByIMPU(impu string) *Registration {
	c.mu.Lock()
	regs := make([]*Registration, 0, len(c.registrations))
	for _, r := range c.registrations {
		regs = append(regs, r)
	}
	c.mu.Unlock()
	for _, r := range regs {
		snap := r.Snapshot()
		if snap == nil {
			continue
		}
		if snap["state"] != "registered" {
			continue
		}
		if got, _ := snap["impu"].(string); got == impu {
			return r
		}
	}
	return nil
}

// ListRegistrations returns snapshots of all tracked IMPIs.
func (c *CSCF) ListRegistrations() []map[string]any {
	c.mu.Lock()
	regs := make([]*Registration, 0, len(c.registrations))
	for _, r := range c.registrations {
		regs = append(regs, r)
	}
	c.mu.Unlock()

	out := make([]map[string]any, 0, len(regs))
	for _, r := range regs {
		out = append(out, r.Snapshot())
	}
	return out
}

// RemoveRegistration stops the FSM and drops the entry.
func (c *CSCF) RemoveRegistration(impi string) {
	c.mu.Lock()
	r, ok := c.registrations[impi]
	if ok {
		delete(c.registrations, impi)
	}
	c.mu.Unlock()
	if r != nil {
		r.Stop()
		log.Infof("CSCF: removed registration FSM for IMPI=%s", impi)
	}
}

// StoreDialog records an established SIP dialog.
func (c *CSCF) StoreDialog(callID string, info *DialogInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dialogs[callID] = info
}

// GetDialog retrieves dialog info.
func (c *CSCF) GetDialog(callID string) *DialogInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dialogs[callID]
}

// RemoveDialog clears a dialog.
func (c *CSCF) RemoveDialog(callID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.dialogs, callID)
}
