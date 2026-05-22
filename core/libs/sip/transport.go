// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SIP transport — UDP + TCP send/receive (RFC 3261 §18).
// Go port of sip_transport.py.
package sip

import (
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

var tlog = logger.Get("ims.sip.transport")

// MessageHandler is called for each parsed SIP message.
type MessageHandler func(msg interface{}, addr *net.UDPAddr)

// SipTransport supports UDP send/receive for SIP.
type SipTransport struct {
	Host    string
	Port    int
	Handler MessageHandler

	mu       sync.Mutex
	conn     *net.UDPConn
	running  bool
	stopCh   chan struct{}
}

// NewSipTransport creates a SIP transport.
func NewSipTransport(host string, port int, handler MessageHandler) *SipTransport {
	return &SipTransport{Host: host, Port: port, Handler: handler}
}

// Start binds UDP socket and starts receive loop.
func (t *SipTransport) Start() error {
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(t.Host, strconv.Itoa(t.Port)))
	if err != nil {
		return err
	}
	t.conn, err = net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	t.running = true
	t.stopCh = make(chan struct{})
	go t.recvLoop()
	tlog.Infof("SIP transport listening on %s:%d/UDP", t.Host, t.Port)
	return nil
}

// Stop closes the socket and stops loops.
func (t *SipTransport) Stop() {
	t.running = false
	if t.stopCh != nil {
		close(t.stopCh)
	}
	if t.conn != nil {
		t.conn.Close()
	}
}

// LocalAddr returns the kernel-assigned UDP address the transport is
// listening on. Useful when the caller asks for port 0 (e.g. tests
// letting the kernel pick) and needs to discover the actual port.
// Returns nil before Start() or after Stop().
func (t *SipTransport) LocalAddr() *net.UDPAddr {
	if t.conn == nil {
		return nil
	}
	if a, ok := t.conn.LocalAddr().(*net.UDPAddr); ok {
		return a
	}
	return nil
}

// Send transmits a SIP message to the given address via UDP.
func (t *SipTransport) Send(data []byte, addr *net.UDPAddr) error {
	if t.conn == nil {
		return nil
	}
	_, err := t.conn.WriteToUDP(data, addr)
	return err
}

// SendMessage serializes and sends a SipRequest or SipResponse.
func (t *SipTransport) SendMessage(msg interface{}, addr *net.UDPAddr) error {
	var data []byte
	switch m := msg.(type) {
	case *SipRequest:
		data = m.Serialize()
	case *SipResponse:
		data = m.Serialize()
	default:
		return nil
	}
	return t.Send(data, addr)
}

func (t *SipTransport) recvLoop() {
	buf := make([]byte, 65535)
	for t.running {
		t.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if !t.running {
				return
			}
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		go t.dispatch(data, addr)
	}
}

func (t *SipTransport) dispatch(data []byte, addr *net.UDPAddr) {
	msg, err := Parse(data)
	if err != nil {
		tlog.Warnf("SIP parse error from %s: %v", addr, err)
		return
	}
	if t.Handler != nil {
		t.Handler(msg, addr)
	}
}
