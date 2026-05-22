// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
//go:build linux

// SCTP notification parsing — lifts MSG_NOTIFICATION-flagged recvmsg
// payloads out of the NGAP byte stream and turns them into sctpfsm
// events. Struct layouts come from <linux/sctp.h> and RFC 6458 §6.1.
package ngap

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	sctpfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/sctpfsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Notification header — common prefix of every sctp_notification.
//   __u16 sn_type;    offset 0
//   __u16 sn_flags;   offset 2
//   __u32 sn_length;  offset 4

const (
	sctpAssocChange     = 0x0001 // SCTP_ASSOC_CHANGE
	sctpPeerAddrChange  = 0x0002 // SCTP_PEER_ADDR_CHANGE
	sctpRemoteError     = 0x0003 // SCTP_REMOTE_ERROR
	sctpSendFailed      = 0x0004 // SCTP_SEND_FAILED
	sctpShutdownEvent   = 0x0005 // SCTP_SHUTDOWN_EVENT
	sctpPartialDelivery = 0x0007
)

// sctp_assoc_change sac_state values (RFC 6458 §6.1.1).
const (
	sacCommUp          = 0 // SCTP_COMM_UP
	sacCommLost        = 1 // SCTP_COMM_LOST
	sacRestart         = 2 // SCTP_RESTART
	sacShutdownComp    = 3 // SCTP_SHUTDOWN_COMP
	sacCantStrAssoc    = 4 // SCTP_CANT_STR_ASSOC
)

// parseSCTPNotification decodes one sctp_notification blob and fires
// the matching sctpfsm event against the association identified by
// the notification. gnbIP is the peer address of the connection the
// blob arrived on (used to key the per-assoc FSM alongside the
// kernel assoc_id).
//
// Returns true if the bytes were a recognised SCTP notification (and
// therefore should NOT be handed to the NGAP dispatcher).
func parseSCTPNotification(gnbIP string, b []byte) bool {
	if len(b) < 8 {
		return false
	}
	snType := binary.LittleEndian.Uint16(b[0:2])

	switch snType {
	case sctpAssocChange:
		// struct sctp_assoc_change {
		//   __u16 sac_type;      off 0
		//   __u16 sac_flags;     off 2
		//   __u32 sac_length;    off 4
		//   __u16 sac_state;     off 8
		//   __u16 sac_error;     off 10
		//   __u16 sac_outbound_streams; off 12
		//   __u16 sac_inbound_streams;  off 14
		//   sctp_assoc_t sac_assoc_id;  off 16  (s32 on Linux)
		//   __u8  sac_info[];           off 20  (variable)
		// }
		if len(b) < 20 {
			return true
		}
		state := binary.LittleEndian.Uint16(b[8:10])
		errCode := binary.LittleEndian.Uint16(b[10:12])
		outStreams := binary.LittleEndian.Uint16(b[12:14])
		inStreams := binary.LittleEndian.Uint16(b[14:16])
		assocID := int32(binary.LittleEndian.Uint32(b[16:20]))
		ev := sctpfsm.Event(0)
		switch state {
		case sacCommUp:
			ev = sctpfsm.EvCommUp
			// RFC 6458 §6.1.1 — sac_outbound_streams is the authoritative
			// negotiated value for DATA we can send. Update GnbCtx so
			// UEStream() never picks a stream index the peer refused.
			// TS 38.412 §7 reserves stream 0 for non-UE signalling; the
			// modulo in UEStream skips it.
			if g := gnbctx.Default.GetByIP(gnbIP); g != nil {
				g.SetNumSCTPStreams(int(outStreams))
				logger.Get("amf.ngap.sctp").
					Infof("gNB %s: SCTP COMM_UP negotiated streams in=%d out=%d",
						gnbIP, inStreams, outStreams)
			}
		case sacCommLost:
			ev = sctpfsm.EvCommLost
		case sacRestart:
			ev = sctpfsm.EvRestart
		case sacShutdownComp:
			ev = sctpfsm.EvShutdownRx
		case sacCantStrAssoc:
			ev = sctpfsm.EvAbort
		default:
			logger.Get("amf.ngap.sctp").Warnf("unknown SCTP_ASSOC_CHANGE state %d assocID=%d gNB=%s",
				state, assocID, gnbIP)
			return true
		}
		k := sctpfsm.Key{GnbIP: gnbIP, AssocID: assocID}
		_ = sctpfsm.Of(k).Fire(&sctpfsm.Context{
			Key: k, Event: ev, Cause: errCode,
			Reason: sacStateName(state),
		})
		return true

	case sctpShutdownEvent:
		// struct sctp_shutdown_event { sn_type, sn_flags, sn_length,
		//                              sctp_assoc_t sse_assoc_id }
		if len(b) < 12 {
			return true
		}
		assocID := int32(binary.LittleEndian.Uint32(b[8:12]))
		k := sctpfsm.Key{GnbIP: gnbIP, AssocID: assocID}
		_ = sctpfsm.Of(k).Fire(&sctpfsm.Context{
			Key: k, Event: sctpfsm.EvShutdownChunkRx,
			Reason: "peer-sent SHUTDOWN chunk",
		})
		return true

	case sctpSendFailed:
		// struct sctp_send_failed { sn_type, sn_flags, sn_length,
		//                           __u32 ssf_error, struct sctp_sndrcvinfo ssf_info,
		//                           sctp_assoc_t ssf_assoc_id, __u8 ssf_data[] }
		if len(b) < 12 {
			return true
		}
		errCode := binary.LittleEndian.Uint32(b[8:12])
		// Assoc id sits after the sndrcvinfo (32 bytes). Best-effort parse.
		assocID := int32(0)
		if len(b) >= 12+32+4 {
			assocID = int32(binary.LittleEndian.Uint32(b[12+32 : 12+32+4]))
		}
		k := sctpfsm.Key{GnbIP: gnbIP, AssocID: assocID}
		_ = sctpfsm.Of(k).Fire(&sctpfsm.Context{
			Key: k, Event: sctpfsm.EvSendFailed, Cause: uint16(errCode),
			Reason: "outbound DATA undeliverable",
		})
		return true

	case sctpRemoteError:
		// struct sctp_remote_error { sn_type, sn_flags, sn_length,
		//   __u16 sre_error, sctp_assoc_t sre_assoc_id, __u8 sre_data[] }
		if len(b) < 14 {
			return true
		}
		errCode := binary.LittleEndian.Uint16(b[8:10])
		assocID := int32(binary.LittleEndian.Uint32(b[10:14]))
		k := sctpfsm.Key{GnbIP: gnbIP, AssocID: assocID}
		_ = sctpfsm.Of(k).Fire(&sctpfsm.Context{
			Key: k, Event: sctpfsm.EvRemoteError, Cause: errCode,
			Reason: "peer OP-ERROR",
		})
		return true

	case sctpPeerAddrChange:
		// struct sctp_paddr_change {
		//   __u16 spc_type;   off 0
		//   __u16 spc_flags;  off 2
		//   __u32 spc_length; off 4
		//   struct sockaddr_storage spc_aaddr; off 8 (128 bytes)
		//   __u32 spc_state;  off 136
		//   __u32 spc_error;  off 140
		//   sctp_assoc_t spc_assoc_id; off 144
		// }
		// spc_state values (RFC 6458 §6.1.2):
		//   0 SPC_ADDR_AVAILABLE     — new path is reachable
		//   1 SPC_ADDR_UNREACHABLE   — HB retransmits exceeded
		//   2 SPC_ADDR_REMOVED       — address dynamically removed
		//   3 SPC_ADDR_ADDED         — new address added mid-assoc
		//   4 SPC_ADDR_MADE_PRIM     — primary promoted
		//   5 SPC_ADDR_CONFIRMED     — formerly-unreachable returned
		if len(b) < 148 {
			return true
		}
		peerIP := parseSockaddrStorage(b[8 : 8+128])
		spcState := binary.LittleEndian.Uint32(b[136:140])
		spcError := binary.LittleEndian.Uint16(b[140:142])
		assocID := int32(binary.LittleEndian.Uint32(b[144:148]))
		pk := sctpfsm.PathKey{GnbIP: gnbIP, AssocID: assocID, Peer: peerIP}
		switch spcState {
		case 0, 3, 4: // AVAILABLE / ADDED / MADE_PRIM → Active
			sctpfsm.SetPathState(pk, sctpfsm.PathActive, spcError)
		case 1: // UNREACHABLE
			sctpfsm.SetPathState(pk, sctpfsm.PathInactive, spcError)
		case 2: // REMOVED
			sctpfsm.SetPathState(pk, sctpfsm.PathUnreachable, spcError)
		case 5: // CONFIRMED
			sctpfsm.SetPathState(pk, sctpfsm.PathConfirmed, spcError)
		}
		return true

	case sctpPartialDelivery:
		// Partial-delivery events — ignored; our 64K recv buffer is
		// always big enough to receive full SCTP messages.
		return true
	}
	return false
}

// parseSockaddrStorage pulls the IP (v4 or v6) out of a Linux
// sockaddr_storage layout (<sys/socket.h>): first 2 bytes = family.
// Returns "" on unrecognised family rather than erroring — we only
// use this for display.
func parseSockaddrStorage(b []byte) string {
	if len(b) < 4 {
		return ""
	}
	family := binary.LittleEndian.Uint16(b[0:2])
	switch family {
	case 2: // AF_INET
		if len(b) < 8 {
			return ""
		}
		return fmt.Sprintf("%d.%d.%d.%d", b[4], b[5], b[6], b[7])
	case 10: // AF_INET6
		if len(b) < 24 {
			return ""
		}
		return net.IP(b[8:24]).String()
	}
	return ""
}

func sacStateName(state uint16) string {
	switch state {
	case sacCommUp:
		return "COMM_UP"
	case sacCommLost:
		return "COMM_LOST"
	case sacRestart:
		return "RESTART"
	case sacShutdownComp:
		return "SHUTDOWN_COMP"
	case sacCantStrAssoc:
		return "CANT_STR_ASSOC"
	}
	return "UNKNOWN"
}
