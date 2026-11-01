package main

import (
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

// dualListener serves TLS and plain HTTP on one port: it looks at the first
// byte of each connection (0x16 starts a TLS handshake). The server itself
// only answers plain HTTP from the home network and Tailscale.
type dualListener struct {
	net.Listener
	cfg   *tls.Config
	conns chan net.Conn
	errc  chan error
	once  sync.Once
	done  chan struct{}
}

func newDualListener(ln net.Listener, cfg *tls.Config) *dualListener {
	d := &dualListener{Listener: ln, cfg: cfg, conns: make(chan net.Conn), errc: make(chan error, 1), done: make(chan struct{})}
	go d.acceptLoop()
	return d
}

func (d *dualListener) acceptLoop() {
	for {
		c, err := d.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			d.errc <- err
			return
		}
		go d.sniff(c)
	}
}

// sniff reads the first byte without blocking other connections.
func (d *dualListener) sniff(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	b := make([]byte, 1)
	if _, err := c.Read(b); err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	var out net.Conn = &prefixConn{Conn: c, first: b}
	if b[0] == 0x16 && d.cfg != nil {
		out = tls.Server(out, d.cfg)
	}
	select {
	case d.conns <- out:
	case <-d.done:
		c.Close()
	}
}

func (d *dualListener) Accept() (net.Conn, error) {
	select {
	case c := <-d.conns:
		return c, nil
	case err := <-d.errc:
		return nil, err
	case <-d.done:
		return nil, net.ErrClosed
	}
}

func (d *dualListener) Close() error {
	d.once.Do(func() { close(d.done) })
	return d.Listener.Close()
}

// prefixConn replays the sniffed byte before the rest of the stream.
type prefixConn struct {
	net.Conn
	first []byte
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.first) > 0 {
		n := copy(b, p.first)
		p.first = p.first[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}
