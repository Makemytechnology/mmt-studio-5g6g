// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// /metrics helper — emits one Prometheus gauge per (FSM, state) pair.
// Dashboards can aggregate by FSM to show UE distribution across the
// registration ladder or PDU-session lifecycle.
package app

import (
	"fmt"
	"io"

	gmmfsm "github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
	sctpfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/sctpfsm"
	pfcpfsm "github.com/mmt/mmt-studio-core/nf/smf/pfcp/fsm"
	"github.com/mmt/mmt-studio-core/nf/smf/session/pti"
)

// writeFSMGauges emits Prometheus-format gauges for every live FSM:
//
//	sacore_fsm_state{fsm="gmm",state="REGISTERED"}      42
//	sacore_fsm_state{fsm="ngap",state="ESTABLISHED"}    42
//	sacore_fsm_state{fsm="pfcp",state="ESTABLISHED"}    86
//	sacore_pti_active{kind="Establishment"}              7
//
// Scrape these alongside /metrics to alert on "UEs stuck in AUTH >N for
// >30s" or "PFCP sessions in DELETE_IN_PROGRESS > threshold".
func writeFSMGauges(w io.Writer) {
	fmt.Fprintln(w, "# HELP sacore_fsm_state Count of subjects per FSM state")
	fmt.Fprintln(w, "# TYPE sacore_fsm_state gauge")

	// GMM per-UE
	gmmBy := map[string]int{}
	for _, s := range gmmfsm.AllSnapshots() {
		gmmBy[s.State]++
	}
	for state, n := range gmmBy {
		fmt.Fprintf(w, "sacore_fsm_state{fsm=%q,state=%q} %d\n", "gmm", state, n)
	}

	// NGAP per-UE
	ngapBy := map[string]int{}
	for _, s := range ngapfsm.AllSnapshots() {
		ngapBy[s.State]++
	}
	for state, n := range ngapBy {
		fmt.Fprintf(w, "sacore_fsm_state{fsm=%q,state=%q} %d\n", "ngap", state, n)
	}

	// SCTP per-association (the transport underneath NGAP)
	sctpBy := map[string]int{}
	for _, s := range sctpfsm.AllSnapshots() {
		sctpBy[s.State]++
	}
	for state, n := range sctpBy {
		fmt.Fprintf(w, "sacore_fsm_state{fsm=%q,state=%q} %d\n", "sctp", state, n)
	}

	// PFCP per-session
	pfcpBy := map[string]int{}
	for _, f := range pfcpfsm.All() {
		pfcpBy[f.State().String()]++
	}
	for state, n := range pfcpBy {
		fmt.Fprintf(w, "sacore_fsm_state{fsm=%q,state=%q} %d\n", "pfcp", state, n)
	}

	// PTI active transactions
	fmt.Fprintln(w, "# HELP sacore_pti_active In-flight PTI transactions per procedure kind")
	fmt.Fprintln(w, "# TYPE sacore_pti_active gauge")
	ptiBy := map[string]int{}
	for _, t := range pti.Default.Active() {
		ptiBy[t.Kind.String()]++
	}
	for kind, n := range ptiBy {
		fmt.Fprintf(w, "sacore_pti_active{kind=%q} %d\n", kind, n)
	}
}
