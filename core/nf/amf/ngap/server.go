// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package ngap

import (
	"context"
	"sync"

	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/oam/fm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// Server owns the listener + gNB registry and dispatches NGAP PDUs.
// Construct with NewServer; Start launches the accept loop in the
// background.
type Server struct {
	lis       Listener
	gnbs      *gnbctx.Registry
	ues       *uectx.Registry
	log       *logger.Logger
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	dispatch  PDUDispatcher
}

// PDUDispatcher is invoked for each inbound NGAP PDU. Implementations are
// typically `DefaultDispatcher` which demultiplexes on procedureCode.
type PDUDispatcher func(gnb *gnbctx.GnbCtx, data []byte, stream int)

// NewServer wires the pieces. Use defaults when registries are nil.
func NewServer(lis Listener, gnbs *gnbctx.Registry, ues *uectx.Registry, d PDUDispatcher) *Server {
	if gnbs == nil {
		gnbs = gnbctx.Default
	}
	if ues == nil {
		ues = uectx.Default
	}
	if d == nil {
		d = DefaultDispatcher
	}
	return &Server{
		lis:      lis,
		gnbs:     gnbs,
		ues:      ues,
		log:      logger.Get("amf.ngap.server"),
		dispatch: d,
	}
}

// Start runs the accept loop. Call Stop to halt.
func (s *Server) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.log.Infof("NGAP listener ready on %s", s.lis.LocalAddr())
	s.wg.Add(1)
	go s.acceptLoop(ctx)
}

// Stop shuts down the listener + waits for per-gNB goroutines.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	_ = s.lis.Close()
	// Close all active gNB connections to unblock Recv() goroutines.
	for _, gnb := range s.gnbs.All() {
		gnb.CloseConn()
	}
	s.wg.Wait()
	s.log.Info("NGAP listener stopped")
}

func (s *Server) acceptLoop(ctx context.Context) {
	defer s.wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := s.lis.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Warnf("accept: %v", err)
			continue
		}
		ip := ParseGnbIP(conn.RemoteAddr())

		// TS 38.413 §8.7.1.1: a successful NG Setup "re-initialises the
		// NGAP UE-related contexts (if any) and erases all related
		// signalling connections in the two nodes". When a fresh SCTP
		// association arrives for an IP we already track, the prior
		// gNB abandoned its TNL without proper SHUTDOWN — its accept
		// goroutine is still blocked on Recv and would otherwise pile
		// up until process exit, then unwind in a storm (one cascade +
		// one alarm per stale goroutine). Pre-empt the prior context
		// here: cascade-release its UE state, close its conn (which
		// unblocks its Recv → its defer short-circuits via
		// gnbctx.IsSuperseded), and clear the registry entries before
		// installing the new one.
		if old := s.gnbs.GetByIP(ip); old != nil {
			s.log.Warnf("gNB %s: new SCTP association arriving while prior is tracked — superseding (TS 38.413 §8.7.1.1)", ip)
			cascadeNGResetForGnb(ip, "ng-setup-supersede")
			s.ues.RemoveAllForGnb(ip)
			s.gnbs.Remove(ip)
			old.Supersede()
		}

		gnb := gnbctx.New(conn, ip)
		// Replace the default stream count with the real SCTP-negotiated
		// value when the transport can tell us (Linux SCTP). Otherwise we
		// keep the conservative default. Avoids UEStream picking a stream
		// index the peer won't accept (sctp_sendmsg → EINVAL).
		if q, ok := conn.(StreamQuerier); ok {
			if n, ok := q.NegotiatedOutStreams(); ok && n > 0 {
				gnb.SetNumSCTPStreams(n)
				s.log.Infof("gNB %s: SCTP negotiated %d outbound streams", ip, n)
			} else {
				// SCTP_STATUS didn't hand us a value yet. The kernel sometimes
				// populates it only after the COMM_UP notification is delivered
				// to userspace — which happens on the first Recv(). GnbCtx
				// stays at NumSCTPStreams=2 (safe lower bound) and is updated
				// from sac_outbound_streams in parseSCTPNotification below.
				// Logged at INFO (not WARNING) because this is the normal
				// path on Linux — every accept hits it, the fallback
				// works, and COMM_UP always lands on first Recv.
				s.log.Infof("gNB %s: SCTP_STATUS unavailable at accept, using %d streams until COMM_UP notification", ip, gnb.NumSCTPStreams)
			}
		}
		s.gnbs.Add(gnb)
		pm.Inc(pm.NGAPSetupAtt, 0) // wake up counter (no-op if already tracked)
		s.wg.Add(1)
		go s.handleAssociation(ctx, conn, gnb)
	}
}

// pduJob carries a single PDU from the recv loop to its stream worker.
type pduJob struct {
	data   []byte
	stream int
}

func (s *Server) handleAssociation(ctx context.Context, conn IncomingConn, gnb *gnbctx.GnbCtx) {
	defer s.wg.Done()
	s.log.Infof("gNB connected: %s", gnb.GnbIP)

	// Per-stream worker goroutines.
	//
	// Motivation (100k-UE scale): "one goroutine per PDU" breaks
	// ordering for consecutive messages on the same SCTP stream, and
	// lets the total goroutine count follow inbound PDU rate. Per-UE
	// ordering matters for fast Auth→SMC→InitialCtx transitions that
	// happen on a single stream.
	//
	// Design:
	//   * Stream 0 (non-UE signalling, TS 38.412 §7) has its own worker.
	//   * UE-associated streams (1..N-1) each have their own worker.
	//   * All messages for the same UE hash to the same stream via
	//     UEStream(amfUeID) = amfUeID%(N-1)+1, so per-stream FIFO is
	//     equivalent to per-UE FIFO at the NGAP layer.
	//   * Workers consume from a bounded channel; the recv loop drops
	//     into the channel in microseconds and keeps draining the socket.
	const jobBufPerStream = 256
	streamQueues := make(map[int]chan pduJob)
	workerWG := &sync.WaitGroup{}
	dispatchPayload := func(j pduJob) { s.dispatch(gnb, j.data, j.stream) }
	ensureWorker := func(stream int) chan pduJob {
		if q, ok := streamQueues[stream]; ok {
			return q
		}
		q := make(chan pduJob, jobBufPerStream)
		streamQueues[stream] = q
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for j := range q {
				dispatchPayload(j)
			}
		}()
		return q
	}

	defer func() {
		// Superseded path: a newer SCTP association from the same gNB
		// IP has already taken authority for this gNB. The accept-loop
		// drove cascadeNGResetForGnb + registry cleanup at supersede
		// time (TS 38.413 §8.7.1.1) and called Supersede() which closed
		// our conn, unblocking Recv. We just drain workers and exit —
		// re-running cascade / Remove / fm.Raise here would race the
		// new association's UE state and double-fire the SCTP-loss
		// alarm.
		if gnb.IsSuperseded() {
			for _, q := range streamQueues {
				close(q)
			}
			workerWG.Wait()
			s.log.Infof("gNB %s: prior accept goroutine exited (superseded by new TNL)", gnb.GnbIP)
			return
		}

		s.log.Infof("gNB disconnected: %s — cleaning up", gnb.GnbIP)
		// Drain worker queues BEFORE flipping conn to nil. PDUs
		// buffered on the stream workers before the tester's SHUTDOWN
		// landed represent real UE requests (PSR Responses, ICS
		// Responses, UL NAS) that MUST complete their dispatch —
		// otherwise the 5GSM/NGAP FSMs never see the event and
		// stranded sessions linger until the cascade tears them down
		// via release paths, which looks to operators like "AMF
		// dropped my response".
		//
		// Under clean SHUTDOWN the peer already moved to
		// SHUTDOWN-RECEIVED so our outbound writes during drain are
		// discarded by the peer but succeed locally — no EPIPE. On
		// ECONNRESET the outbound sendmsg returns error, which the
		// ErrNoTransport-aware WARN path in ulnas / pdusetup handles
		// cleanly. Either way, RX-only handlers (PSR Response, ICS
		// Response, UL NAS) don't need the transport at all.
		for _, q := range streamQueues {
			close(q)
		}
		workerWG.Wait()
		// Now it's safe to kill the transport — no more workers will
		// call gnb.Send. Any subsequent DL attempt (e.g. a timer-driven
		// retransmit) gets ErrNoTransport.
		gnb.MarkDisconnected()
		// TS 38.413 §8.7 — SCTP down ⇒ every UE riding on it is released
		// right away. Fire EvNGReset into each per-UE NGAP FSM; the FSM's
		// own StopTimers clause cancels Twait-ICS / Twait-ue-ctx-release
		// so stale timer expiries can't drive a detached UE to RELEASED
		// 180 ms after the fact. This is also the path that runs when
		// the SCTP MSG_NOTIFICATION route fires EvCommLost, so ECONNRESET
		// and graceful-SHUTDOWN converge on the same cleanup.
		cascadeNGResetForGnb(gnb.GnbIP, "peer-conn-lost")
		// 5GSM session release is a separate layer — cascade only covers
		// NGAP. Walk the UEs once more for PDU session teardown before
		// the registry is wiped.
		for _, ue := range s.ues.SnapshotForGnb(gnb.GnbIP) {
			for pduID := range ue.PDUSessions {
				session.Release(ue.IMSI, uint8(pduID))
				s.log.Debugf("Released PDU session %d for %s", pduID, ue.IMSI)
			}
		}
		s.gnbs.Remove(gnb.GnbIP)
		s.ues.RemoveAllForGnb(gnb.GnbIP)
		_, _ = fm.Raise(fm.RaiseInput{
			ManagedObject:     "gNB/" + nonEmpty(gnb.GnbName, gnb.GnbIP),
			AlarmType:         fm.AlarmTypeCommunications,
			ProbableCause:     fm.CauseLossOfSignal,
			PerceivedSeverity: fm.SeverityMajor,
			SpecificProblem:   "SCTP association lost",
			AdditionalText:    "gNB " + gnb.GnbIP + " disconnected",
		})
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		data, stream, err := conn.Recv()
		if err != nil {
			// Upgrade from DEBUG to WARN so operators can tell whether
			// 'gNB disconnected' was caused by a clean EOF (peer SHUTDOWN)
			// or a real error on our Recv — kernel SCTP closes the
			// association on hit-asocmaxrxt or ABORT_RECEIVED and we get
			// an error here, then the defer() below closes our fd which
			// in turn sends ABORT back to the peer. The peer then sees
			// EPIPE on every subsequent sctp_sendmsg until it gives up,
			// which looks like 'AMF aborted first' from the peer's side.
			if err.Error() != "EOF" {
				s.log.Warnf("gNB %s recv: %v", gnb.GnbIP, err)
			} else {
				s.log.Infof("gNB %s: peer SHUTDOWN (clean)", gnb.GnbIP)
			}
			return
		}
		if len(data) == 0 {
			return
		}
		s.log.Debugf("RX NGAP %d bytes stream=%d from %s", len(data), stream, gnb.GnbIP)
		// SCTP can bundle multiple DATA chunks into a single delivery
		// (RFC 4960 §6.10). Split the buffer into individual NGAP PDUs
		// so every bundled PDU gets dispatched — under load a tester
		// that sends NGReset+UEContextReleaseRequest in one IP packet
		// would otherwise see only the first PDU handled.
		q := ensureWorker(stream)
		remaining := data
		for len(remaining) > 0 {
			_, consumed, perr := wire.DecodeNext(remaining)
			if perr != nil || consumed <= 0 || consumed > len(remaining) {
				s.log.Warnf("NGAP PDU split failed at offset %d from %s: %v (% x)",
					len(data)-len(remaining), gnb.GnbIP, perr, remaining)
				break
			}
			// Copy the slice for this PDU — Recv may reuse its buffer
			// on the next loop; the worker reads asynchronously.
			payload := make([]byte, consumed)
			copy(payload, remaining[:consumed])
			select {
			case q <- pduJob{data: payload, stream: stream}:
			default:
				s.log.Warnf("NGAP stream %d queue full (%d jobs) from %s — dropping PDU",
					stream, jobBufPerStream, gnb.GnbIP)
			}
			remaining = remaining[consumed:]
		}
	}
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
