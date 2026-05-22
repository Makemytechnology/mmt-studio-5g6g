// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GMM Configuration Update (TS 24.501 §5.4.5).
package gmm

import (
	"fmt"

	nas "github.com/mmt/nasgen/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

func init() {
	Register(MsgConfigUpdateComplete, handleConfigUpdateComplete)
}

// ConfigUpdateOpts carries the per-send knobs for the Configuration
// Update Command. All fields are optional; zero-values mean "don't
// include this IE". The ACK flag asks the UE to send ConfigurationUpdate
// Complete; RED (registration required) forces a re-registration after
// the UE applies the update.
type ConfigUpdateOpts struct {
	ACK        bool
	RED        bool
	AllocateNewGUTI bool
}

// SendConfigurationUpdateCommand ships a CONFIGURATION UPDATE COMMAND
// (TS 24.501 §5.4.5.2) to the UE, caches the encoded PDU for T3555
// retransmit, and fires EvConfigUpdateCommandSent so the FSM arms
// T3555 (retransmit up to NASMaxRetransmit per TS 24.501 §10.2
// N3555=4 before final expiry).
//
// Port of Python generate_configuration_update_command(). Subset that
// covers the common operator workflows (GUTI reallocation, TAI list
// refresh, Allowed NSSAI push). Network-name / time-zone IEs are
// intentionally skipped here — they're rarely exercised and needed
// codec work in the Python port too.
func SendConfigurationUpdateCommand(ue *uectx.AmfUeCtx, opts ConfigUpdateOpts) error {
	log := logger.Get("amf.gmm.configupdate")

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		return fmt.Errorf("SendConfigurationUpdateCommand amfUeID=%d: gNB %q gone",
			ue.AmfUeNGAPID, ue.GnbKey)
	}

	cmd := &nas.ConfigurationUpdateCommand{}
	if opts.ACK || opts.RED {
		ind := &nas.ConfigurationUpdateIndication{}
		if opts.ACK {
			ind.ACK = 1
		}
		if opts.RED {
			ind.RED = 1
		}
		cmd.ConfigurationUpdateIndication = ind
	}
	if opts.AllocateNewGUTI {
		// Allocate a fresh 5G-TMSI to break any stalker correlation
		// (same rationale as Registration Accept). buildAssigned5GGUTI
		// cycles ue.TMSI5G; we then let the UE switch over on Complete.
		ue.TMSI5G = 0 // force re-allocation
		if g := buildAssigned5GGUTI(ue); g != nil {
			cmd.GUTI5G = g
		}
	}
	if tai := buildTAIList(gnb); tai != nil {
		cmd.TAIList = tai
	}

	encoded, err := cmd.Encode()
	if err != nil {
		return fmt.Errorf("ConfigurationUpdateCommand encode amfUeID=%d: %w",
			ue.AmfUeNGAPID, err)
	}
	// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
	if err := dlnas.Send(gnb, ue, encoded); err != nil {
		return fmt.Errorf("DL ConfigurationUpdateCommand amfUeID=%d: %w",
			ue.AmfUeNGAPID, err)
	}
	// Cache for T3555 retransmit (TS 24.501 §10.2 N3555=4).
	ue.RetxNASPDU = encoded
	log.WithIMSI(ue.IMSI).Infof("Configuration Update Command sent amfUeID=%d ack=%v red=%v newGUTI=%v",
		ue.AmfUeNGAPID, opts.ACK, opts.RED, opts.AllocateNewGUTI)

	// Fire the FSM event that arms T3555 per the transition table.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvConfigUpdateCommandSent})
	return nil
}

func handleConfigUpdateComplete(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.configupdate")
	// T3555 is cancelled by the FSM on EvConfigUpdateComplete.

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		log.Errorf("ConfigurationUpdateComplete decode: %v", err)
		return
	}
	if _, ok := msg.(*nas.ConfigurationUpdateComplete); !ok {
		log.Errorf("ConfigurationUpdateComplete: unexpected type %T", msg)
		return
	}
	ue.GMMProc = uectx.GMMProcNone
	log.WithIMSI(ue.IMSI).Infof("ConfigurationUpdateComplete amfUeID=%d", ue.AmfUeNGAPID)
	// Self-loop on REGISTERED; transition cancels T3555.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvConfigUpdateComplete, Inner: inner})
}
