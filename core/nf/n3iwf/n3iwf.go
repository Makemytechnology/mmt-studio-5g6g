// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package n3iwf — Non-3GPP Inter-Working Function (TS 23.501 §6.3.1).
//
// Wires together the four sub-packages:
//
//	nf/n3iwf/ikev2/    RFC 7296 IKEv2 wire format + DH + PRF + SK
//	nf/n3iwf/eap5g/    TS 24.502 §9.3.2 EAP-5G TLVs
//	nf/n3iwf/ctx/      per-UE context manager
//	nf/n3iwf/handler/  IKEv2 protocol state machine
//
// plus a UDP transport (transport.go) listening on UDP/500 (+ /4500
// for NAT-T per RFC 7296 §3.1).
//
// Authoritative specs:
//
//	TS 23.501 v19.7.0 §6.3.1 (architecture)
//	TS 24.502 v19.3.0 §7.3   (IKE SA establishment for untrusted
//	                          non-3GPP access)
//	RFC 7296 (IKEv2)
//	RFC 3526 (MODP DH groups)
//	RFC 4303 (ESP — for child SAs, phase 4)
//	RFC 9048 (EAP-AKA' — for primary auth, phase 4)
package n3iwf

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/ctx"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/handler"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/ipool"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/n2"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/userplane"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Config is the n3iwf_config row (singleton, like infra_config). Mirrors
// the columns already provisioned in db/schemas/n3iwf.go.
type Config struct {
	Enabled        bool
	N3IWFIP        string
	IKEPort        int
	IKENATPort     int
	InnerIPPool    string
	IPSecEncAlgo   string
	IPSecIntAlgo   string
	DHGroup        int
	SupportedDNNs  string
	SupportedNSSAI string

	// AMF connectivity (TS 23.501 §4.2.3 N2). Empty AMFAddr keeps
	// the N3IWF running IKE with no AMF bridge wired.
	AMFAddr string // "host" or "host:port" (default 38412)
	PLMNID  string // 6-hex-octet BCD-encoded MCC+MNC, e.g. "09F107"
	N3IWFID int    // 16-bit operator-assigned ID (TS 38.413 §9.3.1.5)
	TAC     string // 6-hex-octet TAC, e.g. "000001" (TS 38.413 §9.3.3.10)
}

// LoadConfig reads the singleton config row. Returns nil, nil if the
// row is absent (operator hasn't configured the N3IWF yet).
func LoadConfig() (*Config, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var c Config
	var enabled int
	err = db.QueryRow(`SELECT enabled, n3iwf_ip, ike_port, ike_nat_port,
        inner_ip_pool, ipsec_enc_algo, ipsec_int_algo, dh_group,
        supported_dnns, supported_nssai,
        amf_addr, plmn_id, n3iwf_id, tac
        FROM n3iwf_config WHERE id=1`,
	).Scan(&enabled, &c.N3IWFIP, &c.IKEPort, &c.IKENATPort,
		&c.InnerIPPool, &c.IPSecEncAlgo, &c.IPSecIntAlgo, &c.DHGroup,
		&c.SupportedDNNs, &c.SupportedNSSAI,
		&c.AMFAddr, &c.PLMNID, &c.N3IWFID, &c.TAC)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = enabled != 0
	return &c, nil
}

// Start brings up the N3IWF if and only if the operator has set
// enabled=1 in the singleton config. Spawns one background
// goroutine that owns the UDP listener; cancel ctx to shut down.
//
// Returns nil on a graceful "feature disabled" path so that callers
// in cmd/main.go can call Start() unconditionally.
func Start(parent context.Context) error {
	log := logger.Get("n3iwf")
	cfg, err := LoadConfig()
	if err != nil {
		log.Warnf("LoadConfig: %v — N3IWF disabled", err)
		return nil
	}
	if cfg == nil || !cfg.Enabled {
		log.Infof("N3IWF feature disabled (n3iwf_config.enabled=0)")
		return nil
	}

	// The IDr we send in IKE_AUTH responses (TS 24.502 §7.3.2.1
	// NOTE: "The N3IWF identifier is the IP address or the FQDN of
	// the N3IWF.") — we use the configured public IP for now.
	identity := cfg.N3IWFIP
	if identity == "" {
		identity = "n3iwf.local"
	}
	h := handler.New(ctx.Default, identity)

	// User-plane registry: indexes ESP↔GTP-U Bridges per UE PDU
	// session. Wired even when no UPF is configured today, so that
	// CREATE_CHILD_SA dispatch + future PDUSessionResourceSetup
	// callers have a target to register against. Transport demux
	// will silently drop if registry is empty (no SAs registered
	// yet) per RFC 4303 §3.4.2.
	registry := userplane.NewRegistry()
	h.SetRegistry(registry)

	// Inner-IP allocator (TS 24.502 §7.3.2.2 — the address the
	// N3IWF hands the UE in CP(CFG_REPLY) on IKE_AUTH success).
	// Operator-supplied via inner_ip_pool. Empty / unparseable
	// pool leaves CP omitted from IKE_AUTH responses but doesn't
	// fail the bring-up — the IKE state machine still works.
	if cfg.InnerIPPool != "" {
		pool, err := ipool.New(cfg.InnerIPPool)
		if err != nil {
			log.Warnf("inner_ip_pool=%q invalid (%v) — UE inner-IP assignment disabled",
				cfg.InnerIPPool, err)
		} else {
			h.SetInnerIPPool(pool)
			log.Infof("inner-IP pool %s ready (capacity=%d, gateway=%s)",
				cfg.InnerIPPool, pool.Capacity(), pool.Gateway())
		}
	}

	// AMF bridge: when amf_addr is configured, dial N2 SCTP, run
	// NG Setup, and wire the resulting *Manager into the handler so
	// EAP-Response/5G-NAS forwarding works end-to-end. amf_addr=""
	// keeps the N3IWF running IKE-only — useful for unit-style
	// deployments that don't pair with an AMF (the handler still
	// emits IDr + EAP-Request/5G-Start on IKE_AUTH#1 and rejects
	// anything beyond with AUTHENTICATION_FAILED).
	if cfg.AMFAddr != "" {
		ng, err := buildNGSetup(cfg)
		if err != nil {
			log.Warnf("N2 NG Setup config invalid (%v) — bringing up IKE without AMF bridge", err)
		} else {
			mgr, err := n2.NewManager(parent, n2.ManagerConfig{
				Dial:    n2.DialConfig{AMFAddr: cfg.AMFAddr, LocalIP: cfg.N3IWFIP},
				NGSetup: ng,
			})
			if err != nil {
				log.Warnf("N2 dial / NG Setup to %s failed (%v) — IKE comes up without AMF bridge",
					cfg.AMFAddr, err)
			} else {
				h.SetBridge(n2.NewBridgeAdapter(mgr))
				log.Infof("N2 Manager up to AMF=%s — handler bridge wired", cfg.AMFAddr)
			}
		}
	}

	t, err := Listen(ListenConfig{
		IP:        cfg.N3IWFIP,
		IKEPort:   cfg.IKEPort,
		NATPort:   cfg.IKENATPort,
		EnableNAT: cfg.IKENATPort > 0,
		EnableN3:  cfg.IKENATPort > 0, // bind UDP/2152 alongside NAT-T
		N3Port:    2152,               // TS 29.281 §4.4.2 (GTP-U)
		Handler:   h,
		Registry:  registry,
	})
	if err != nil {
		log.Errorf("listen: %v", err)
		return err
	}
	go func() {
		if err := t.Serve(parent); err != nil {
			log.Errorf("transport: %v", err)
		}
	}()
	log.Infof("N3IWF up on %s (IKE udp/%d, NAT-T udp/%d) — id=%s",
		cfg.N3IWFIP, cfg.IKEPort, cfg.IKENATPort, identity)
	return nil
}

// buildNGSetup turns the operator-supplied DB strings into an
// n2.NGSetupConfig. Validates PLMN/TAC are even-length hex and the
// SupportedNSSAI string is parseable as 1+ slice IDs.
func buildNGSetup(cfg *Config) (*n2.NGSetupConfig, error) {
	plmn, err := hexBytes(cfg.PLMNID)
	if err != nil || len(plmn) != 3 {
		return nil, errors.New("plmn_id must be 6 hex octets BCD-encoded MCC+MNC (TS 23.003 §2.3)")
	}
	tac, err := hexBytes(cfg.TAC)
	if err != nil || len(tac) != 3 {
		return nil, errors.New("tac must be 6 hex octets (TS 38.413 §9.3.3.10)")
	}
	if cfg.N3IWFID < 0 || cfg.N3IWFID > 0xFFFF {
		return nil, errors.New("n3iwf_id must be 16-bit (0..65535, TS 38.413 §9.3.1.5)")
	}
	// SupportedNSSAI is a comma-separated list of slice values:
	//   "01" = SST-only S-NSSAI (SST=0x01)
	//   "01:000001" = SST=0x01, SD=0x000001
	var nssai []n2.SNSSAI
	for _, s := range strings.Split(cfg.SupportedNSSAI, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		parts := strings.SplitN(s, ":", 2)
		sst, err := hexBytes(parts[0])
		if err != nil || len(sst) != 1 {
			return nil, errors.New("supported_nssai SST must be 2 hex digits per entry")
		}
		entry := n2.SNSSAI{SST: sst[0]}
		if len(parts) == 2 {
			sd, err := hexBytes(parts[1])
			if err != nil || len(sd) != 3 {
				return nil, errors.New("supported_nssai SD must be 6 hex digits when present")
			}
			entry.SD = sd
		}
		nssai = append(nssai, entry)
	}
	if len(nssai) == 0 {
		return nil, errors.New("supported_nssai must list at least one slice (TS 38.413 §9.2.6.1)")
	}
	return &n2.NGSetupConfig{
		PLMNID:           plmn,
		N3IWFID:          uint16(cfg.N3IWFID),
		Name:             "n3iwf-" + cfg.N3IWFIP,
		TACs:             [][]byte{tac},
		SupportedSNSSAIs: nssai,
	}, nil
}

func hexBytes(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty")
	}
	return hex.DecodeString(s)
}
