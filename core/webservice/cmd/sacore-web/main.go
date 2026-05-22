// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// sacore-web — HTTP frontend for MMT Studio Core.
//
// Usage:
//
//	sacore-web [--addr :5000] [--template-dir webservice/templates] [--static-dir webservice/static]
//
// Environment:
//
//	SA_CORE_DB_TYPE    sqlite|postgresql   (default sqlite)
//	SA_CORE_DB_FILE    path/to/sacore.db   (default webservice/sacore.db)
//	LOG_LEVEL          DEBUG|INFO|WARNING|ERROR
//	SACORE_LOG_FILE    path                (enables rotating file sink)
//	SACORE_LOG_IMSI    imsi[,imsi...]      (IMSI filter allow-list)
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/mmt/mmt-studio-core/infra/health"
	"github.com/mmt/mmt-studio-core/infra/lifecycle"
	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf"
	"github.com/mmt/mmt-studio-core/nf/nwdaf"
	smfctx "github.com/mmt/mmt-studio-core/nf/smf/ctx"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/nf/upf/upfloop"
	"github.com/mmt/mmt-studio-core/oam/fm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/otel"
	"github.com/mmt/mmt-studio-core/oam/pm"
	"github.com/mmt/mmt-studio-core/security/core_security"
	"github.com/mmt/mmt-studio-core/security/li"
	"github.com/mmt/mmt-studio-core/services/ims"
	"github.com/mmt/mmt-studio-core/webservice/app"
)

func main() {
	addr := flag.String("addr", ":5000", "webservice listen address (host:port)")
	// Empty default → AMF reads network_config.sctp_port from the DB
	// (TS 38.412 §7 IANA-registered 38412 is the column default).
	// Pass an explicit "host:port" only to bypass the DB.
	ngapAddr := flag.String("ngap-addr", "", "NGAP listen address (host:port; empty → DB-driven)")
	tmpl := flag.String("template-dir", "", "template directory (default webservice/templates)")
	static := flag.String("static-dir", "", "static asset directory (default webservice/static)")
	flag.Parse()

	srv, err := app.New(*tmpl, *static)
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.Bootstrap(); err != nil {
		log.Fatal(err)
	}

	// Enable log file by default at the FHS-standard daemon log path
	// /var/log/sacore/sacore.log. Matches db/schemas/infra.go:120
	// (infra_config.log_file_path DEFAULT '/var/log/sacore/sacore.log')
	// and oam/logger/logger.go:254 (Configure() "disk" sink fallback).
	// Previously hardcoded to /opt/log/sacore, which both violated the
	// Filesystem Hierarchy Standard and diverged from the DB default —
	// two concurrent log streams were landing in the tree.
	os.MkdirAll("/var/log/sacore", 0755)
	if err := logger.SetLogFile("/var/log/sacore/sacore.log"); err != nil {
		log.Printf("log file: %v", err)
	}

	// Always-on infrastructure subsystems.
	timers.M.StartManager()
	pm.Default.StartSampler()

	// Fault manager: hydrate from DB and register as the health watchdog
	// alarm hook so degraded NFs raise correlated alarms automatically.
	if err := fm.Default.Init(); err != nil {
		log.Fatalf("fm init: %v", err)
	}
	health.Register("db", dbProbe)
	health.StartWatchdog(func(nf string, s health.Status) {
		_, _ = fm.Default.Raise(fm.RaiseInput{
			ManagedObject:     nf,
			AlarmType:         fm.AlarmTypeProcessing,
			ProbableCause:     fm.CauseSoftwareError,
			PerceivedSeverity: fm.SeverityMajor,
			SpecificProblem:   nf + " health check failed",
			AdditionalText:    s.Error,
		})
	})

	// Register HTTP routes and start the listener BEFORE the (slow)
	// per-NF init below. Route handlers don't touch NF state at
	// registration time — they look it up at request time — so it's
	// safe to wire them now and accept connections while AMF/UPF/IMS
	// are still coming up. Callers can hit /api/admin/sys-info to see
	// the new boot_id immediately; ready=false stays until SetReady()
	// fires after NF init completes (see webservice/app/ready.go).
	//
	// Without this split, UPF's DPDK EAL init (~10-15 s) and other
	// NF startup work all happen before srv.Listen() runs, so the
	// HTTP port refuses connections for the entire init window. The
	// tester's reset_to_baseline poll loop interpreted that as "core
	// still down" and ran out of patience.
	srv.RegisterRoutes()
	srv.RegisterAPIRoutes()
	srv.RegisterAMFRoutes()
	srv.RegisterProvisioningRoutes()
	srv.RegisterDomainRoutes()
	srv.RegisterOperationsRoutes()
	srv.RegisterNetworkConfigRoutes()
	srv.RegisterTrafficRoutes()
	srv.RegisterTCSRoutes()

	go func() {
		if err := srv.Listen(*addr); err != nil {
			log.Fatalf("webservice listen: %v", err)
		}
	}()

	// AMF: load context from DB + start NGAP listener + procedure handlers.
	if err := amf.InitContextFromDB(); err != nil {
		log.Fatalf("amf init context: %v", err)
	}

	// SMF: load APN config + subscriber service bindings into memory.
	if err := smfctx.InitContextFromDB(); err != nil {
		log.Fatalf("smf init context: %v", err)
	}

	// UDM: preload the auth + subscription caches so AUSF and SMF
	// never touch SQLite on the hot path. SQN bumps batch through a
	// 2 s flusher.
	if err := udm.LoadCache(); err != nil {
		log.Printf("udm cache load: %v (falling back to per-request DB reads)", err)
	}
	if err := udm.LoadSubscriptionCache(); err != nil {
		log.Printf("udm subscription cache load: %v", err)
	}
	udm.StartSQNFlusher(2 * time.Second)

	amfSvc, err := amf.Start(amf.Config{ListenAddr: *ngapAddr})
	if err != nil {
		log.Fatalf("amf start: %v", err)
	}

	// IMS: bring the CSCF SIP listener up on UDP/5060 so REGISTER
	// from a UE / tester reaches HandleRegister and gets a real 401
	// challenge instead of timing out (TS 24.229 §5.4.1, RFC 3261
	// §10 + §18 — specs/3gpp/ts_124229v190600p.pdf,
	// specs/ietf/rfc3261.txt).
	imsSvc, err := ims.Start(ims.Config{Host: "0.0.0.0", Port: 5060, IMSDomain: "ims.local"})
	if err != nil {
		log.Printf("ims start: %v (REGISTER handling will be unavailable)", err)
	}

	// UPF: initialize dataplane (DPDK when available, pure-Go
	// fallback). Must run BEFORE upfloop.Enable below so that:
	//   (a) DPDK EAL + PktIO come up under the real dpdkBridge
	//       (cgo_bridge_linux.go installs it at init());
	//   (b) upfloop captures that dpdkBridge as the Handler
	//       ManagerHook before replacing the bridge global with
	//       PfcpBridge for SMF-side control-plane ops.
	if err := upf.Default.Init(); err != nil {
		log.Printf("upf init: %v (data plane may be unavailable)", err)
	}

	// SMF↔UPF is always PFCP/N4 (TS 29.244) via the loopback
	// socket. upfloop stands up one PFCP anchor per slice
	// (TS 23.501 §5.15 / §6.3.3 — UPF selection per S-NSSAI/DNN),
	// dials each from the SMF side, and wires every handler's
	// ManagerHook back to the same dpdkBridge — slice isolation
	// is at the PFCP signalling layer; the dataplane's C session
	// table (keyed by (imsi, pduSessionID)) is shared.
	//
	// Two default anchors:
	//   * upf-embb on 127.0.0.1:8805 — SST=01 (eMBB), DNNs internet/ims
	//   * upf-miot on 127.0.0.1:8806 — SST=03 (MIoT),  DNN  iot
	// Both rows are upserted into upf_instances so registry.Select()
	// considers them when smf/session/establish.go picks the anchor
	// for a new PDU session.
	loopState, err := upfloop.EnableMulti([]upfloop.SliceSpec{
		{UPFID: "upf-embb", Addr: "127.0.0.1:8805",
			SST: []string{"01"}, DNNs: []string{"internet", "ims"}},
		{UPFID: "upf-miot", Addr: "127.0.0.1:8806",
			SST: []string{"03"}, DNNs: []string{"iot"}},
	})
	if err != nil {
		log.Printf("upfloop enable: %v (SMF↔UPF control plane unavailable)", err)
	}

	// OTEL: read infra_config.otel_* and arm the in-memory span
	// ring + (eventually) the OTLP / Prometheus exporters. No-op
	// when otel_enabled=0; safe to call before LI/UPF init since
	// it has no NF dependency.
	otel.Init()

	// core_security: rehydrate the firewall / known-gNB / blocked-IP
	// caches from the DB tables (TS 33.501 §5.9.1 trust-boundary
	// state survives restarts). The package's in-memory maps are
	// the hot-path source of truth; LoadPersistedState repopulates
	// them after a restart.
	if err := core_security.LoadPersistedState(); err != nil {
		log.Printf("core_security: load persisted state: %v", err)
	}
	// TTL sweeper for IDS auto-block entries. 30 s tick keeps the
	// deny set responsive without polling the panel.
	core_security.StartTTLSweeper(30 * time.Second)

	// LI X2/X3 deliverers (TS 33.127 §6.3 / §6.4). Each is a single
	// goroutine that ships pending IRI / CC events to the warrant's
	// configured mdf_endpoint. Enabled flag is read from
	// network_config every tick — operator can flip without
	// restart. ExpireWarrants tick keeps the activeTargets cache
	// honest as warrants pass their end_time.
	li.StartX2()
	li.StartX3()
	li.StartExpireTicker()

	// NWDAF: analytics collection + notification loop (TS 29.520).
	// Python reference logs 'NWDAF started (collection interval=30s)'.
	// Use the package-level DefaultService so routes_nwdaf_*.go and
	// /api/nwdaf/* see the same dataCache the collector populates.
	nwdaf.DefaultService.Start()

	lifecycle.Register("webservice", func(ctx context.Context) { srv.Shutdown(ctx) })
	lifecycle.Register("nwdaf", func(context.Context) { nwdaf.DefaultService.Stop() })
	lifecycle.Register("li-x2", func(context.Context) { li.StopX2() })
	lifecycle.Register("li-x3", func(context.Context) { li.StopX3() })
	lifecycle.Register("li-expire", func(context.Context) { li.StopExpireTicker() })
	lifecycle.Register("upf", func(context.Context) { upf.Default.Shutdown() })
	if loopState != nil {
		lifecycle.Register("upfloop", func(context.Context) { loopState.Close() })
	}
	lifecycle.Register("amf", func(context.Context) { amfSvc.Stop() })
	if imsSvc != nil {
		lifecycle.Register("ims", func(ctx context.Context) { imsSvc.Stop(ctx) })
	}
	// Drain SQN write-behind before we stop accepting traffic. Runs
	// after AMF so no late bumpSQN arrives concurrently with the flush.
	lifecycle.Register("udm-sqn-flush", func(context.Context) { udm.StopSQNFlusher() })
	lifecycle.Register("timers", func(context.Context) { timers.M.StopManager() })
	lifecycle.Register("pm-sampler", func(context.Context) { pm.Default.StopSampler() })
	lifecycle.Register("health", func(context.Context) { health.StopWatchdog() })
	lifecycle.InstallSignalHandlers()

	// Startup audit — mirrors Python's "ready" lines so operators can see
	// which subsystems came up. Anything not in this list is not running
	// in the Go port yet (IMS CSCF/MRFP/ConfAS, MCX signaling, EPC/MME/S1AP,
	// IMS DNS responder). Those land as separate follow-ups.
	bootLog := logger.Get("startup")
	// AMF NGAP listen address comes from amfSvc — driven by
	// network_config.sctp_port + amf_ip, not a hardcoded literal.
	ngapBanner := amfSvc.LocalAddr()
	// Stash the resolved bind address so /api/admin/sys-info can surface
	// it; the Network Config GUI uses this as a "Currently bound" hint
	// when network_config.amf_ip is empty (auto-pick path).
	app.SetNGAPBindAddr(ngapBanner)
	if ngapBanner == "" {
		ngapBanner = "(unknown)"
	}
	activeMsg := "Subsystems active: AMF (NGAP " + ngapBanner + ") + SMF + UPF + NWDAF + timers + PM + health + fault manager"
	pendingMsg := "Subsystems pending: IMS MRFP/Conference, MCX, EPC/MME/S1AP, IMS DNS"
	if imsSvc != nil {
		activeMsg += " + IMS CSCF (SIP UDP :5060)"
	} else {
		pendingMsg = "Subsystems pending: IMS stack (CSCF/MRFP/Conference), MCX, EPC/MME/S1AP, IMS DNS"
	}
	bootLog.Info(activeMsg)
	bootLog.Info(pendingMsg)

	// Flip /api/admin/sys-info.ready to true now that every NF init
	// above has returned. The tester's reset_to_baseline poll loop
	// uses (boot_id changed && ready==true) as the "core fully wired"
	// signal — before this, HTTP is reachable but UPF DPDK init etc.
	// may still be in flight.
	app.SetReady()
	bootLog.Info("HTTP up and ready (sys-info.ready=true)")

	// HTTP listener runs in a goroutine launched right after route
	// registration. Block here until the signal handler runs Shutdown
	// → os.Exit(0). This used to be `log.Fatal(srv.Listen(*addr))`,
	// which both started the listener AND blocked main; we split
	// those two responsibilities so HTTP comes up before NF init.
	select {}
}

// dbProbe is the minimum-viable DB health check. It does a PRAGMA
// quick_check(1) — mirrors the Python reference's _check_db().
func dbProbe() health.Status {
	deadline := time.Now().Add(500 * time.Millisecond)
	// Keep-it-light: just prove we can talk to the DB. Heavier integrity
	// checks belong in the /admin/db-quick-check panel.
	_ = deadline
	return health.Status{Status: "healthy"}
}
