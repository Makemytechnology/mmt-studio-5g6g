// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package n3iwf

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/mmt/mmt-studio-core/nf/n3iwf/handler"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/userplane"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Transport listens for IKEv2 datagrams on UDP/500 (and optionally
// UDP/4500 for NAT-traversal per RFC 7296 §3.1) and dispatches each
// datagram to the handler. When a userplane.Registry is wired in,
// the UDP/4500 path also demuxes ESP-in-UDP traffic to per-UE
// Bridges so user-plane packets flow toward N3 (UDP/2152, GTP-U).
//
// RFC 7296 §3.1 verbatim: "When sent on UDP port 500, IKE messages
// begin immediately following the UDP header. When sent on UDP port
// 4500, IKE messages have prepended four octets of zeros. These
// four octets of zeros are not part of the IKE message and are not
// included in any of the length fields or checksums defined by
// IKE."
//
// On UDP/4500 we demux IKE vs ESP-in-UDP by the first 4 octets
// (zero ⇒ IKE; non-zero ⇒ ESP SPI per RFC 4303 §2). N3 uses a
// separate UDP/2152 socket per TS 29.281 §4.4.2.
type Transport struct {
	conn500  *net.UDPConn
	conn4500 *net.UDPConn
	conn2152 *net.UDPConn // N3 GTP-U; nil if no UPF address configured
	handler  *handler.Handler
	registry *userplane.Registry // user-plane bridge index; nil ⇒ user plane disabled
}

// ListenConfig groups the bind options for Listen — splits IKE
// (UDP/500), NAT-T (UDP/4500) and N3 (UDP/2152) into one struct so
// callers can pick which ports to bind without overloading Listen's
// positional args every time we add a port.
//
// NATPort and N3Port are bound only when their EnableNAT / EnableN3
// flags are set; this lets tests pass port=0 for ephemeral binding
// without disabling the listener.
type ListenConfig struct {
	IP        string // bind IP; "" / "0.0.0.0" = all interfaces
	IKEPort   int    // typically 500; 0 = ephemeral
	NATPort   int    // typically 4500; 0 = ephemeral when EnableNAT
	N3Port    int    // typically 2152; 0 = ephemeral when EnableN3
	EnableNAT bool   // bind UDP/NATPort
	EnableN3  bool   // bind UDP/N3Port
	Handler   *handler.Handler
	Registry  *userplane.Registry // optional — required for ESP/N3 demux
}

// Listen binds the configured UDP sockets and returns a Transport
// ready to Serve.
func Listen(cfg ListenConfig) (*Transport, error) {
	if cfg.Handler == nil {
		return nil, errors.New("n3iwf transport: nil handler")
	}
	bindIP := net.ParseIP(cfg.IP)
	if cfg.IP != "" && bindIP == nil {
		return nil, fmt.Errorf("n3iwf transport: invalid bind IP %q", cfg.IP)
	}
	c500, err := net.ListenUDP("udp", &net.UDPAddr{IP: bindIP, Port: cfg.IKEPort})
	if err != nil {
		return nil, fmt.Errorf("n3iwf transport: listen UDP/%d: %w", cfg.IKEPort, err)
	}
	t := &Transport{conn500: c500, handler: cfg.Handler, registry: cfg.Registry}
	if cfg.EnableNAT {
		c4500, err := net.ListenUDP("udp", &net.UDPAddr{IP: bindIP, Port: cfg.NATPort})
		if err != nil {
			c500.Close()
			return nil, fmt.Errorf("n3iwf transport: listen UDP/%d: %w", cfg.NATPort, err)
		}
		t.conn4500 = c4500
	}
	if cfg.EnableN3 {
		c2152, err := net.ListenUDP("udp", &net.UDPAddr{IP: bindIP, Port: cfg.N3Port})
		if err != nil {
			c500.Close()
			if t.conn4500 != nil {
				t.conn4500.Close()
			}
			return nil, fmt.Errorf("n3iwf transport: listen UDP/%d: %w", cfg.N3Port, err)
		}
		t.conn2152 = c2152
	}
	return t, nil
}

// Serve runs the receive loops until ctx is cancelled. Each IKE
// datagram is processed synchronously — IKEv2 is half-duplex per
// RFC 7296 §2.1 ("an implementation MUST NOT generate more than one
// outstanding request") so per-UE serialisation is natural.
//
// ESP-in-UDP and N3 G-PDU paths run on their own goroutines because
// user-plane traffic has no IKEv2-style turn discipline.
func (t *Transport) Serve(ctx context.Context) error {
	log := logger.Get("n3iwf.transport")
	go func() {
		<-ctx.Done()
		t.Close()
	}()
	if t.conn4500 != nil {
		go t.runIKELoop(t.conn4500, true /* nat encap */)
		log.Infof("listening UDP %s + %s (NAT-T)",
			t.conn500.LocalAddr(), t.conn4500.LocalAddr())
	} else {
		log.Infof("listening UDP %s", t.conn500.LocalAddr())
	}
	if t.conn2152 != nil {
		go t.runN3Loop(t.conn2152)
		log.Infof("N3 listening UDP %s (TS 29.281 §4.4.2)", t.conn2152.LocalAddr())
	}
	return t.runIKELoop(t.conn500, false)
}

// runIKELoop handles UDP/500 (raw IKE) and UDP/4500 (NAT-T:
// IKE+ESP demuxed by the 4-byte non-ESP marker).
func (t *Transport) runIKELoop(conn *net.UDPConn, natEncap bool) error {
	log := logger.Get("n3iwf.transport")
	buf := make([]byte, 65535)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Err.Error() == "use of closed network connection" {
				return nil
			}
			log.Warnf("ReadFromUDP %s: %v", conn.LocalAddr(), err)
			return err
		}
		msg := buf[:n]
		if natEncap {
			// RFC 7296 §3.1: on UDP/4500, IKE messages are preceded
			// by 4 octets of zeros. Anything else is an ESP-in-UDP
			// frame whose first 4 octets are the SPI (RFC 4303 §2).
			if !userplane.IsIKE(msg) {
				t.dispatchESP(msg, src)
				continue
			}
			msg = msg[4:]
		}
		resp, err := t.handler.Handle(msg, src)
		if err != nil {
			log.Warnf("handler error from %s: %v", src, err)
			continue
		}
		if resp == nil {
			continue
		}
		if natEncap {
			out := make([]byte, 4+len(resp))
			copy(out[4:], resp) // 4 leading zeros
			_, err = conn.WriteToUDP(out, src)
		} else {
			_, err = conn.WriteToUDP(resp, src)
		}
		if err != nil {
			log.Warnf("WriteToUDP to %s: %v", src, err)
		}
	}
}

// dispatchESP handles an ESP-in-UDP frame from UDP/4500 by looking
// up the destination Bridge by SPI and forwarding the resulting
// G-PDU on the N3 socket. RFC 4303 §3.4.2: "if no valid SAD entry
// exists, the receiver MUST discard the packet."
func (t *Transport) dispatchESP(esp []byte, src *net.UDPAddr) {
	log := logger.Get("n3iwf.transport")
	if t.registry == nil || t.conn2152 == nil {
		log.Debugf("ESP from %s dropped: user-plane not configured (registry=%v, n3=%v)",
			src, t.registry != nil, t.conn2152 != nil)
		return
	}
	spi, err := userplane.PeekESPSPI(esp)
	if err != nil {
		log.Warnf("ESP from %s: %v", src, err)
		return
	}
	bridge := t.registry.LookupBySPI(spi)
	if bridge == nil {
		log.Debugf("ESP from %s: no SA for SPI=%08x (RFC 4303 §3.4.2)", src, spi)
		return
	}
	gpdu, err := bridge.HandleNWu(esp)
	if err != nil {
		log.Warnf("ESP from %s SPI=%08x: HandleNWu: %v", src, spi, err)
		return
	}
	if bridge.UPFAddr == nil {
		log.Warnf("bridge SPI=%08x has no UPFAddr — dropping G-PDU", spi)
		return
	}
	if _, err := t.conn2152.WriteToUDP(gpdu, bridge.UPFAddr); err != nil {
		log.Warnf("WriteToUDP %s: %v", bridge.UPFAddr, err)
	}
}

// runN3Loop reads inbound G-PDUs from the UPF on UDP/2152, looks up
// the destination Bridge by TEID, and writes the resulting ESP-in-UDP
// frame back to the UE on UDP/4500.
func (t *Transport) runN3Loop(conn *net.UDPConn) error {
	log := logger.Get("n3iwf.transport")
	buf := make([]byte, 65535)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Err.Error() == "use of closed network connection" {
				return nil
			}
			log.Warnf("ReadFromUDP %s: %v", conn.LocalAddr(), err)
			return err
		}
		t.dispatchN3(buf[:n], src)
	}
}

// dispatchN3 demuxes one inbound G-PDU.
func (t *Transport) dispatchN3(gpdu []byte, src *net.UDPAddr) {
	log := logger.Get("n3iwf.transport")
	if t.registry == nil || t.conn4500 == nil {
		return
	}
	// Peek the TEID without fully decoding — fast path.
	if len(gpdu) < 8 {
		log.Debugf("G-PDU from %s: too short (%d)", src, len(gpdu))
		return
	}
	teid := uint32(gpdu[4])<<24 | uint32(gpdu[5])<<16 | uint32(gpdu[6])<<8 | uint32(gpdu[7])
	bridge := t.registry.LookupByTEID(teid)
	if bridge == nil {
		log.Debugf("G-PDU from %s: no SA for TEID=%08x", src, teid)
		return
	}
	esp, err := bridge.HandleN3(gpdu)
	if err != nil {
		log.Warnf("G-PDU from %s TEID=%08x: HandleN3: %v", src, teid, err)
		return
	}
	if bridge.UEAddr == nil {
		log.Warnf("bridge TEID=%08x has no UEAddr — dropping ESP", teid)
		return
	}
	// ESP-in-UDP wire format: ESP packet bytes go directly on UDP/4500
	// (no 4-zero prefix — that prefix is reserved for IKE).
	if _, err := t.conn4500.WriteToUDP(esp, bridge.UEAddr); err != nil {
		log.Warnf("WriteToUDP %s: %v", bridge.UEAddr, err)
	}
}

// Close shuts down all UDP sockets.
func (t *Transport) Close() {
	if t.conn500 != nil {
		t.conn500.Close()
	}
	if t.conn4500 != nil {
		t.conn4500.Close()
	}
	if t.conn2152 != nil {
		t.conn2152.Close()
	}
}
