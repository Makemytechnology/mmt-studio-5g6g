// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package amf — AMF top-level assembly. Registers every NGAP procedure handler
// and wraps NGAP listener + registries behind a single Start/Stop surface.
//
// Callers (main.go, webservice cmd) use the Service struct; test harnesses
// can poke at the sub-packages directly.
package amf

import (
	"fmt"
	"net"
	"strconv"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/infra/plmn"
	"github.com/mmt/mmt-studio-core/nf/amf/ctx"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/initialctxsetup"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/initialue"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/ngsetup"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pdumodify"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pdurelease"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pdusetup"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pwscancel"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pwsfailure"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pwsrestart"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/rrcinactivetx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/writereplace"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/uectxrelease"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/ulnas"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
)

// Config captures the boot-time wiring parameters. Both fields fall
// back to operator config (network_config row read by InitContextFromDB)
// when left at their zero values; the spec defaults below are only the
// last-resort fallback for callers that bypass the DB entirely.
type Config struct {
	ListenAddr     string // empty → ":<network_config.sctp_port>" (TS 38.412 §7, IANA 38412)
	NumSCTPStreams int    // 0     → 16 (TS 38.412 §7 minimum 2 pairs; we exceed it)
	// AMFName + Capacity are read from ctx.Default, which callers should
	// Initialize before Start (typically from the DB-backed network_config).
}

// Service is the running AMF. Zero value is not usable — use Start.
type Service struct {
	listener ngap.Listener
	server   *ngap.Server
}

// Start registers every NGAP handler, opens the NGAP listener, and kicks
// off the accept loop. Returns the running service.
func Start(cfg Config) (*Service, error) {
	// Resolve the SCTP transport port. TS 38.412 §7 IANA-registers 38412
	// as the NGAP port but the spec doesn't mandate it — operators on
	// shared boxes / multi-tenant deployments rebind to avoid clashes.
	// Priority: explicit ":port" in ListenAddr > network_config.sctp_port
	// > IANA default 38412.
	dbPort := ctx.Default.SCTPPort()
	if cfg.ListenAddr == "" {
		port := dbPort
		if port == 0 {
			port = 38412
		}
		cfg.ListenAddr = fmt.Sprintf(":%d", port)
	}
	if cfg.NumSCTPStreams <= 0 {
		cfg.NumSCTPStreams = 16
	}

	// Bind-IP priority: explicit host in ListenAddr > network_config.amf_ip
	// > transport_linux auto-pick > 0.0.0.0. When the CLI passes a
	// wildcard (":<port>" or "0.0.0.0:<port>"), overlay the DB value so
	// the SCTP association single-homes onto the operator-declared
	// management IP (TS 38.412 §7 peer paths hygiene — see
	// transport_linux.go pickPrimaryIPv4 for the risk this guards).
	//
	// Whichever source the IP comes from, verify it's actually on a
	// local interface before handing it to SCTP bind. EADDRNOTAVAIL
	// would otherwise sink the AMF — we'd rather come up on 0.0.0.0
	// with a loud WARN so the operator can fix the value in the
	// Network Config GUI without restarting the host.
	ilog := logger.Get("amf.init")
	if host, port, ok := splitHostPort(cfg.ListenAddr); ok {
		// Overlay DB port whenever the caller used the wildcard form
		// (host==""/"0.0.0.0") and didn't set a specific port. This
		// keeps explicit `--ngap-addr <ip>:<port>` overrides intact.
		if dbPort > 0 {
			dbPortStr := strconv.Itoa(dbPort)
			if (host == "" || host == "0.0.0.0") && port != dbPortStr {
				port = dbPortStr
				cfg.ListenAddr = host + ":" + port
				ilog.Infof("NGAP bind: using network_config.sctp_port=%d", dbPort)
			}
		}
		switch {
		case host == "" || host == "0.0.0.0":
			if dbIP := ctx.Default.IP(); dbIP != "" {
				if isLocalIPv4(dbIP) {
					cfg.ListenAddr = dbIP + ":" + port
					ilog.Infof("NGAP bind: using network_config.amf_ip=%s", dbIP)
				} else {
					ilog.Warnf(
						"network_config.amf_ip=%s is not on any local interface — "+
							"falling back to auto-pick / 0.0.0.0. "+
							"Fix via Network Config GUI so NGAP single-homes correctly.",
						dbIP)
				}
			}
		default:
			if !isLocalIPv4(host) {
				ilog.Warnf(
					"--ngap-addr host %s is not on any local interface — "+
						"falling back to 0.0.0.0:%s so the AMF still starts. "+
						"Fix the flag or network_config.amf_ip.",
					host, port)
				cfg.ListenAddr = "0.0.0.0:" + port
			}
		}
	}

	// Install every procedure handler.
	ngsetup.Register()
	initialue.Register()
	ulnas.Register()
	dlnas.Register()
	initialctxsetup.Register()
	uectxrelease.Register()
	pdusetup.Register()    // TS 38.413 §8.2.1
	pdurelease.Register()  // TS 38.413 §8.2.2
	pdumodify.Register()   // TS 38.413 §8.2.3
	pwsrestart.Register()    // TS 38.413 §8.9.3
	pwsfailure.Register()    // TS 38.413 §8.9.4
	writereplace.Register()  // TS 38.413 §8.9.1 — AMF-initiated; HandleResponse catches the gNB ACK
	pwscancel.Register()     // TS 38.413 §8.9.2 — AMF-initiated; HandleResponse catches the gNB ACK
	rrcinactivetx.Register() // TS 38.413 §8.3.5 — gNB → AMF, RRC INACTIVE TRANSITION REPORT (gap A)
	// TODO: register paging, handover, amfconfigupdate,
	//   ranconfigupdate, ngreset, errorindication.

	lis, err := ngap.Listen(ngap.ListenConfig{
		Addr:           cfg.ListenAddr,
		NumSCTPStreams: cfg.NumSCTPStreams,
	})
	if err != nil {
		return nil, err
	}
	srv := ngap.NewServer(lis, gnbctx.Default, uectx.Default, ngap.DefaultDispatcher)
	srv.Start()
	return &Service{listener: lis, server: srv}, nil
}

// splitHostPort breaks "host:port" or ":port" into its pieces. Returns
// (host, port, true) on success; the simplified net.SplitHostPort
// wrapper avoids pulling net into the caller just for this one-liner.
// host can be empty (wildcard); port is returned as a string so the
// caller can stitch it back without re-parsing.
func splitHostPort(addr string) (host, port string, ok bool) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:], true
		}
	}
	return "", "", false
}

// isLocalIPv4 reports whether the given dotted-quad string matches any
// IPv4 address currently configured on a host interface. Used to
// validate network_config.amf_ip before we hand it to SCTP bind —
// binding to a non-local address would fail with EADDRNOTAVAIL.
func isLocalIPv4(s string) bool {
	target := net.ParseIP(s)
	if target == nil {
		return false
	}
	target = target.To4()
	if target == nil {
		return false
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}
			if ip4 := ip.To4(); ip4 != nil && ip4.Equal(target) {
				return true
			}
		}
	}
	return false
}

// Stop halts the accept loop and closes every gNB association.
func (s *Service) Stop() {
	if s == nil || s.server == nil {
		return
	}
	s.server.Stop()
}

// LocalAddr returns the resolved NGAP listen address ("host:port") so
// the startup banner / status APIs can report the operator-configured
// port without re-reading network_config.
func (s *Service) LocalAddr() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.LocalAddr()
}

// UESummary is the UE row shape returned to the web UI.
type UESummary struct {
	AmfUeNGAPID int64  `json:"amf_ue_ngap_id"`
	RanUeNGAPID int64  `json:"ran_ue_ngap_id"`
	IMSI        string `json:"imsi,omitempty"`
	GnbKey      string `json:"gnb"`
	RM          string `json:"rm"`
	CM          string `json:"cm"`
	GMMProc     string `json:"gmm_proc"`
	GMMSub      string `json:"gmm_sub"`
	NGAPProc    string `json:"ngap_proc"`
}

// GnbSummary is the gNB row shape returned to the web UI.
type GnbSummary struct {
	IP        string   `json:"ip"`
	Name      string   `json:"name,omitempty"`
	ID        string   `json:"id,omitempty"`
	PagingDRX string   `json:"paging_drx,omitempty"`
	Connected bool     `json:"connected"`
	TACs      []string `json:"tacs"`
	PLMNs     []string `json:"plmns"`
	Streams   int      `json:"sctp_streams"`
}

// UEs returns a snapshot of every UE currently known to the AMF. The
// registry holds the authoritative state; we shape it for JSON here.
func UEs(r *uectx.Registry) []UESummary {
	if r == nil {
		r = uectx.Default
	}
	// The registry has no public iterator; walk via a quick sample query
	// on the allocator — count bumps with each Insert so we can probe.
	// This isn't pretty; when we need it for real we'll add Registry.All().
	out := make([]UESummary, 0, r.Count())
	// Expose an iterator-friendly path: call through to a helper in uectx.
	for _, ue := range r.Snapshot() {
		out = append(out, UESummary{
			AmfUeNGAPID: ue.AmfUeNGAPID,
			RanUeNGAPID: ue.RanUeNGAPID,
			IMSI:        ue.IMSI,
			GnbKey:      ue.GnbKey,
			RM:          string(ue.RM),
			CM:          string(ue.CM),
			GMMProc:     string(ue.GMMProc),
			GMMSub:      string(ue.GMMSub),
			NGAPProc:    string(ue.NGAPProc),
		})
	}
	return out
}

// Gnbs returns a JSON-friendly snapshot of every gNB currently in the registry.
func Gnbs(r *gnbctx.Registry) []GnbSummary {
	if r == nil {
		r = gnbctx.Default
	}
	list := r.All()
	out := make([]GnbSummary, 0, len(list))
	for _, g := range list {
		out = append(out, GnbSummary{
			IP:        g.GnbIP,
			Name:      g.GnbName,
			ID:        g.GnbID,
			PagingDRX: g.PagingDRX,
			Connected: g.Connected,
			TACs:      g.AllTACs(),
			PLMNs:     g.AllPLMNs(),
			Streams:   g.NumSCTPStreams,
		})
	}
	return out
}

// InitContextFromDB loads AMF context from network_config + supported_plmns.
// Returns an error if DB is unavailable or required config is missing.
func InitContextFromDB() error {
	log := logger.Get("amf.init")
	ctx.Default = &ctx.AMF{}

	// Read network_config
	db, err := engine.Open()
	if err != nil {
		return fmt.Errorf("amf: cannot open DB: %w", err)
	}
	var name, amfIP string
	var dbCap, sctpPort int64
	var nfs ctx.NetworkFeatureSupport
	if err := db.QueryRow(`SELECT
			amf_name, amf_ip, sctp_port, relative_amf_capacity,
			ims_vops_3gpp, ims_vops_n3gpp, emc, emf, iwk_n26, mpsi,
			emcn3, mcsi, restrict_ec, n3_data, cp_ciot, up_ciot, iphc_cp_ciot
		FROM network_config WHERE id=1`).Scan(
		&name, &amfIP, &sctpPort, &dbCap,
		&nfs.IMSVoPS3GPP, &nfs.IMSVoPSN3GPP, &nfs.EMC, &nfs.EMF, &nfs.IWKN26, &nfs.MPSI,
		&nfs.EMCN3, &nfs.MCSI, &nfs.RestrictEC, &nfs.N3Data, &nfs.CPCIoT, &nfs.UPCIoT, &nfs.IPHCCPCIoT,
	); err != nil {
		return fmt.Errorf("amf: failed to read network_config: %w", err)
	}
	if name == "" {
		return fmt.Errorf("amf: amf_name is empty in network_config")
	}
	if sctpPort < 1 || sctpPort > 65535 {
		return fmt.Errorf("amf: sctp_port %d out of range [1,65535]", sctpPort)
	}
	ctx.Default.SetIP(amfIP)
	ctx.Default.SetSCTPPort(int(sctpPort))
	if dbCap < 0 || dbCap > 255 {
		return fmt.Errorf("amf: relative_amf_capacity %d out of range [0,255]", dbCap)
	}
	var capacity = uint8(dbCap)

	// Read supported_plmns with GUAMI + NSSAI
	var guamis []ctx.GUAMI
	var plmnSupport []ctx.PLMNSupport
	plmns, err := plmn.List(true)
	if err == nil {
		for _, p := range plmns {
			encoded, err := plmn.EncodePLMN(p.MCC, p.MNC)
			if err != nil {
				log.Warnf("Skip PLMN %s/%s: %v", p.MCC, p.MNC, err)
				continue
			}
			if p.AMFRegionID.Valid || p.AMFSetID.Valid || p.AMFPointer.Valid {
				guamis = append(guamis, ctx.GUAMI{
					PLMNID:      encoded,
					AMFRegionID: uint8(p.AMFRegionID.Int64),
					AMFSetID:    uint16(p.AMFSetID.Int64),
					AMFPointer:  uint8(p.AMFPointer.Int64),
				})
			}

			// Load NSSAI for this PLMN
			var slices []ctx.SNSSAI
			if db != nil {
				rows, err := db.Query(`SELECT sst, sd FROM plmn_nssai WHERE plmn_id=?`, p.PLMNID)
				if err == nil {
					for rows.Next() {
						var sst int
						var sd string
						if rows.Scan(&sst, &sd) == nil {
							s := ctx.SNSSAI{SST: uint8(sst)}
							if len(sd) >= 6 {
								// Parse hex SD to 3 bytes
								for i := 0; i < 3 && i*2+1 < len(sd); i++ {
									var b byte
									for _, c := range sd[i*2 : i*2+2] {
										b <<= 4
										if c >= '0' && c <= '9' {
											b |= byte(c - '0')
										} else if c >= 'a' && c <= 'f' {
											b |= byte(c-'a') + 10
										} else if c >= 'A' && c <= 'F' {
											b |= byte(c-'A') + 10
										}
									}
									s.SD = append(s.SD, b)
								}
							}
							slices = append(slices, s)
						}
					}
					rows.Close()
				}
			}
			plmnSupport = append(plmnSupport, ctx.PLMNSupport{
				PLMNID: encoded,
				Slices: slices,
			})
		}
	}

	if len(guamis) == 0 {
		log.Error("No PLMNs with GUAMI configured in DB — GUAMI list is empty")
	}

	// Load security algorithm priorities from DB
	algoID := map[string]uint8{"NEA0": 0, "NEA1": 1, "NEA2": 2, "NEA3": 3, "NIA0": 0, "NIA1": 1, "NIA2": 2, "NIA3": 3}
	loadAlgos := func(algoType string) []ctx.AlgoPriority {
		rows, err := db.Query(`SELECT algorithm, priority FROM security_algorithms WHERE algo_type=? ORDER BY priority`, algoType)
		if err != nil {
			log.Warnf("Failed to load %s algorithms: %v", algoType, err)
			return nil
		}
		defer rows.Close()
		var out []ctx.AlgoPriority
		for rows.Next() {
			var name string
			var prio int
			if rows.Scan(&name, &prio) == nil {
				if id, ok := algoID[name]; ok {
					out = append(out, ctx.AlgoPriority{Algorithm: name, AlgoID: id, Priority: prio})
				}
			}
		}
		return out
	}
	ciphAlgos := loadAlgos("ciphering")
	integAlgos := loadAlgos("integrity")
	if len(ciphAlgos) == 0 || len(integAlgos) == 0 {
		return fmt.Errorf("amf: security_algorithms table empty — configure ciphering and integrity algorithms")
	}
	log.Infof("Security algorithms: %d ciphering, %d integrity", len(ciphAlgos), len(integAlgos))

	ctx.Default.Initialize(name, capacity, guamis, plmnSupport, ciphAlgos, integAlgos, nfs)
	return nil
}
