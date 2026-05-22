// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package ngap

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
)

// tcpListen is the shared TCP-based stub used by every platform until
// Linux SCTP wiring is complete. It speaks a trivial framing:
//
//	+--------+-----------+----------+
//	| u32 BE | u16 BE    | payload  |
//	| length | stream-id | NGAP PDU |
//	+--------+-----------+----------+
//
// Round-trips cleanly in unit tests (see server_test.go).
func tcpListen(cfg ListenConfig) (Listener, error) {
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	return &tcpListener{ln: ln}, nil
}

type tcpListener struct {
	ln net.Listener
}

func (l *tcpListener) Accept(ctx context.Context) (IncomingConn, error) {
	done := make(chan struct{})
	var c net.Conn
	var err error
	go func() {
		c, err = l.ln.Accept()
		close(done)
	}()
	select {
	case <-ctx.Done():
		_ = l.ln.Close()
		<-done
		return nil, ctx.Err()
	case <-done:
	}
	if err != nil {
		return nil, err
	}
	return &tcpConn{c: c}, nil
}

func (l *tcpListener) Close() error      { return l.ln.Close() }
func (l *tcpListener) LocalAddr() string { return l.ln.Addr().String() }

type tcpConn struct {
	c  net.Conn
	mu sync.Mutex
}

func (c *tcpConn) Send(data []byte, stream int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var hdr [6]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(data)))
	binary.BigEndian.PutUint16(hdr[4:], uint16(stream))
	if _, err := c.c.Write(hdr[:]); err != nil {
		return err
	}
	_, err := c.c.Write(data)
	return err
}

func (c *tcpConn) Recv() ([]byte, int, error) {
	var hdr [6]byte
	if _, err := io.ReadFull(c.c, hdr[:]); err != nil {
		return nil, 0, err
	}
	size := binary.BigEndian.Uint32(hdr[:4])
	stream := int(binary.BigEndian.Uint16(hdr[4:]))
	buf := make([]byte, size)
	if _, err := io.ReadFull(c.c, buf); err != nil {
		return nil, 0, err
	}
	return buf, stream, nil
}

func (c *tcpConn) RemoteAddr() string { return c.c.RemoteAddr().String() }
func (c *tcpConn) Close() error       { return c.c.Close() }
