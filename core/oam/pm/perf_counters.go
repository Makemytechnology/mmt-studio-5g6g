// Package pm — Performance measurement counters.
//
// Spec anchors:
//
//   - TODO(spec: TS 28.552, "5G performance measurements") — defines
//     the measurement-family prefixes and per-counter semantics. Not
//     loaded locally, so cited prose-only. Counter constants below
//     follow the TS 28.552 family naming so the catalogue can be
//     re-grounded once the PDF is added to specs/3gpp/:
//
//       AMF clause 5.1: RM.* / MM.* / AUTH.* / NGAP.*
//       SMF clause 5.3: SM.*
//       UPF clause 5.4: N3.*
//       NSSF:           NSSF.*
//
//   - TODO(spec: TS 28.554, "5G end-to-end Key Performance Indicators")
//     — KPI formulas (e.g. Registered Subscriber Number, Mean Number of
//     PDU Sessions). SuccessRate() below is the §6.x Mean-of-Ratios shape
//     but we do not yet emit the full KPI catalogue.
//
// Verified §-anchored counter cites are inline at each constant block
// against the spec that owns the procedure being measured (TS 38.413 for
// PWS, TS 23.502 for N1N2/DL-Notify, TS 29.514/29.517/29.522 for AF,
// TS 29.503 for UDM, TS 29.512 for PCF). Those are speccheck-grounded
// against the local PDFs.
//
// Rates are derived at read time from the ring; peaks track max 1 s delta.
package pm

import (
	"sync"
	"time"
)

const (
	historySeconds = 120
	sampleInterval = time.Second
)

// Counters is the multi-counter aggregate. Safe for concurrent use.
type Counters struct {
	mu        sync.Mutex
	values    map[string]int64
	history   []sample // ring of (ts, snapshot)
	peaks     map[string]float64
	samplerCh chan struct{}
	samplerWG sync.WaitGroup
}

type sample struct {
	ts   time.Time
	snap map[string]int64
}

// Default is the process-wide singleton used by the Inc helper.
var Default = New()

// New returns an independent Counters — mainly for tests.
func New() *Counters {
	return &Counters{
		values: map[string]int64{},
		peaks:  map[string]float64{},
	}
}

// Inc bumps a counter by delta (default 1 when delta == 0).
func (c *Counters) Inc(name string, delta int64) {
	if delta == 0 {
		delta = 1
	}
	c.mu.Lock()
	c.values[name] += delta
	c.mu.Unlock()
}

// Get returns the current counter value (0 if unset).
func (c *Counters) Get(name string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[name]
}

// All returns a snapshot of every counter.
func (c *Counters) All() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

// Prefix returns counters whose names start with prefix.
func (c *Counters) Prefix(prefix string) map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64)
	for k, v := range c.values {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out[k] = v
		}
	}
	return out
}

// Reset zeroes every counter. History is preserved so rates don't spike.
func (c *Counters) Reset() {
	c.mu.Lock()
	c.values = map[string]int64{}
	c.mu.Unlock()
}

// ── Sampler / Rates ─────────────────────────────────────────────────────

// StartSampler spawns a goroutine that snapshots counters once per second.
// Idempotent.
func (c *Counters) StartSampler() {
	c.mu.Lock()
	if c.samplerCh != nil {
		c.mu.Unlock()
		return
	}
	c.samplerCh = make(chan struct{})
	stop := c.samplerCh
	c.mu.Unlock()

	c.samplerWG.Add(1)
	go c.sampleLoop(stop)
}

// StopSampler halts the sampler goroutine (safe if not running).
func (c *Counters) StopSampler() {
	c.mu.Lock()
	ch := c.samplerCh
	c.samplerCh = nil
	c.mu.Unlock()
	if ch != nil {
		close(ch)
		c.samplerWG.Wait()
	}
}

func (c *Counters) sampleLoop(stop <-chan struct{}) {
	defer c.samplerWG.Done()
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c.tickSample()
		}
	}
}

func (c *Counters) tickSample() {
	snap := c.All()
	now := time.Now()
	c.mu.Lock()
	c.history = append(c.history, sample{ts: now, snap: snap})
	if len(c.history) > historySeconds {
		c.history = c.history[len(c.history)-historySeconds:]
	}
	// Update peak 1-second rates against the previous sample.
	if n := len(c.history); n >= 2 {
		prev := c.history[n-2]
		dt := now.Sub(prev.ts).Seconds()
		if dt > 0 {
			for k, v := range snap {
				r := float64(v-prev.snap[k]) / dt
				if r > c.peaks[k] {
					c.peaks[k] = r
				}
			}
		}
	}
	c.mu.Unlock()
}

// Rate returns the events/second rate of counter name over the last
// `window` seconds. Returns 0 if history is too short.
func (c *Counters) Rate(name string, window time.Duration) float64 {
	if window <= 0 {
		window = 5 * time.Second
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.history) < 2 {
		return 0
	}
	nowSample := c.history[len(c.history)-1]
	target := nowSample.ts.Add(-window)
	old := c.history[0]
	for _, s := range c.history {
		if !s.ts.Before(target) {
			old = s
			break
		}
	}
	dt := nowSample.ts.Sub(old.ts).Seconds()
	if dt <= 0 {
		return 0
	}
	return float64(nowSample.snap[name]-old.snap[name]) / dt
}

// PeakRate returns the highest 1-second rate seen since process start.
func (c *Counters) PeakRate(name string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peaks[name]
}

// ResetPeaks clears the per-counter peak table (UI button).
func (c *Counters) ResetPeaks() {
	c.mu.Lock()
	c.peaks = map[string]float64{}
	c.mu.Unlock()
}

// SuccessRate computes Successes / (Successes + Failures) * 100 — the
// Mean-of-Ratios shape used by TS 28.552 success-rate measurements.
// Returns -1 if there have been zero attempts.
func (c *Counters) SuccessRate(success, failure string) float64 {
	c.mu.Lock()
	s := c.values[success]
	f := c.values[failure]
	c.mu.Unlock()
	total := s + f
	if total == 0 {
		return -1
	}
	return float64(s) / float64(total) * 100
}

// Inc forwards to Default.Inc — the primary API used by NFs.
func Inc(name string, delta int64) { Default.Inc(name, delta) }

// ── TS 28.552-style counter name constants (deferred §-cites) ───────────

// AMF — registration management (TS 28.552-deferred clause 5.1.1).
const (
	RegAtt    = "RM.RegAtt"
	RegSucc   = "RM.RegSucc"
	RegFail   = "RM.RegFail"
	DeregAtt  = "RM.DeregAtt"
	DeregSucc = "RM.DeregSucc"
)

// AMF — §5.1.2 Authentication
const (
	AuthAtt     = "AUTH.Att"
	AuthSucc    = "AUTH.Succ"
	AuthFail    = "AUTH.Fail"
	AuthFailMAC = "AUTH.FailMAC"
	AuthFailSQN = "AUTH.FailSQN"
)

// AMF — §5.1.3 Security Mode
const (
	SecAtt  = "SEC.Att"
	SecSucc = "SEC.Succ"
	SecFail = "SEC.Fail"
)

// AMF — §5.1.5 Service Request
const (
	SvcReqAtt  = "MM.SvcReqAtt"
	SvcReqSucc = "MM.SvcReqSucc"
)

// AMF — §5.1.6 Paging
const (
	PagingAtt  = "MM.PagingAtt"
	PagingSucc = "MM.PagingSucc"
)

// AMF — NGAP
const (
	NGAPSetupAtt   = "NGAP.SetupAtt"
	NGAPSetupSucc  = "NGAP.SetupSucc"
	NGAPSetupFail  = "NGAP.SetupFail"
	NGAPPWSWriteReplaceReq  = "NGAP.PWSWriteReplaceReq"  // TS 38.413 §8.9.1 (AMF→gNB Request)
	NGAPPWSWriteReplaceResp = "NGAP.PWSWriteReplaceResp" // TS 38.413 §8.9.1 (gNB→AMF Response)
	NGAPPWSCancelReq        = "NGAP.PWSCancelReq"        // TS 38.413 §8.9.2 (AMF→gNB Request)
	NGAPPWSCancelResp       = "NGAP.PWSCancelResp"       // TS 38.413 §8.9.2 (gNB→AMF Response)
	NGAPPWSRestart          = "NGAP.PWSRestart"          // TS 38.413 §8.9.3
	NGAPPWSFailureInd       = "NGAP.PWSFailureInd"       // TS 38.413 §8.9.4
)

// SMF — §5.3.1 PDU Session Management
const (
	SMSessAtt  = "SM.SessAtt"
	SMSessSucc = "SM.SessSucc"
	SMSessFail = "SM.SessFail"
	SMSessRel  = "SM.SessRel"
	SMModAtt   = "SM.ModAtt"
	SMModSucc  = "SM.ModSucc"
	SMModFail  = "SM.ModFail"
	SMDLNotify = "SM.DLNotify" // TS 23.502 §4.2.3.3 step 2a DL Data Notification
	SMN1N2     = "SM.N1N2"     // TS 23.502 §4.2.3.3 step 3a Namf_Communication_N1N2MessageTransfer
)

// SMF — §5.3.2 QoS Flow
const (
	SMFlowAtt  = "SM.FlowAtt"
	SMFlowSucc = "SM.FlowSucc"
	SMFlowFail = "SM.FlowFail"
)

// NSSF
const (
	NSSFSelAtt  = "NSSF.SelAtt"
	NSSFSelSucc = "NSSF.SelSucc"
	NSSFSelFail = "NSSF.SelFail"
)

// UPF — §7.5.8 PFCP Session Report Request types (nf/upf/report.go).
// One counter per §7.5.8.x subsection so operators can see which
// dataplane event is firing.
const (
	UPFReportDLDR    = "UPF.ReportDLDR"    // §7.5.8.2 Downlink Data Report
	UPFReportUsage   = "UPF.ReportUsage"   // §7.5.8.3 Usage Report (URR)
	UPFReportErrInd  = "UPF.ReportErrInd"  // §7.5.8.4 Error Indication Report
	UPFReportTSC     = "UPF.ReportTSC"     // §7.5.8.5 TSC Management Information
	UPFReportSessRep = "UPF.ReportSessRep" // §7.5.8.6 generic Session Report
	UPFReportOther   = "UPF.ReportOther"   // unknown / unhandled type (shouldn't fire)
)

// AF — Application Function (TS 29.522 NEF northbound / TS 29.514
// Npcf_PolicyAuthorization / TS 29.517 Naf_EventExposure).
const (
	AFSessionCreate          = "AF.SessionCreate"          // 29.514 §4.2.2 Create
	AFSessionUpdate          = "AF.SessionUpdate"          // 29.514 §4.2.3 Update
	AFSessionDelete          = "AF.SessionDelete"          // 29.514 §4.2.4 Delete
	AFSessionNotify          = "AF.SessionNotify"          // 29.514 §4.2.5 Notify
	AFEventSubscribe         = "AF.EventSubscribe"         // 29.517 §4.2 Subscribe
	AFEventUnsubscribe       = "AF.EventUnsubscribe"       // 29.517 §4.2 Unsubscribe
	AFEventNotify            = "AF.EventNotify"            // 29.517 §4.2 Notify
	AFTrafficInfluenceCreate = "AF.TrafficInfluenceCreate" // 29.522 §4.4.7 Create
	AFTrafficInfluenceDelete = "AF.TrafficInfluenceDelete" // 29.522 §4.4.7 Delete
	AFIMSAuthorize           = "AF.IMSAuthorize"           // IMS media → PCF (29.514 §4.2.2)
)

// UDM — Nudm_* services (TS 29.503).
const (
	UDMUeAuthGet       = "UDM.UeAuthGet"       // §5.4.2.2 Nudm_UEAuthentication_Get
	UDMSdmGetAM        = "UDM.SdmGetAM"        // §5.2.2.2.3 Access & Mobility Sub Data Retrieval
	UDMSdmGetSM        = "UDM.SdmGetSM"        // §5.2.2.2.5 Session Mgmt Sub Data Retrieval
	UDMUecmRegister    = "UDM.UecmRegister"    // §5.3.2.2 Registration
	UDMUecmDeregister  = "UDM.UecmDeregister"  // §5.3.2.4 Deregistration
	UDMUecmDeregNotify = "UDM.UecmDeregNotify" // §5.3.2.3 DeregistrationNotification (UDM→AMF)
)

// PCF — Npcf_SMPolicyControl service (TS 29.512 §4.2).
const (
	PCFSmPolicyCreate       = "PCF.SmPolicyCreate"       // §4.2.2 Create
	PCFSmPolicyUpdate       = "PCF.SmPolicyUpdate"       // §4.2.4 Update
	PCFSmPolicyUpdateNotify = "PCF.SmPolicyUpdateNotify" // §4.2.3 UpdateNotify (PCF → SMF)
	PCFSmPolicyDelete       = "PCF.SmPolicyDelete"       // §4.2.5 Delete
	PCFSmPolicyCreateReject = "PCF.SmPolicyCreateReject" // Create-path failure
	PCFSmPolicyRevalidate   = "PCF.SmPolicyRevalidate"   // §4.2.2.4 RE_TIMEOUT fired
)
