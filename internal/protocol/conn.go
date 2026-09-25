package protocol

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Identity describes the local device.
type Identity struct {
	ID   string
	Name string
	Kind string
}

// Conn is an authenticated, encrypted direct link between two devices.
type Conn struct {
	raw       net.Conn
	cipher    *Cipher
	sendMu    sync.Mutex
	Peer      Identity
	PeerAddrs []string // addresses the peer says it listens on
	Dialer    bool     // we initiated the connection
}

// Handshake runs hello/ready over raw. dialer is true on the side that
// connected. myAddrs are advertised so the peer can share them with others.
func Handshake(raw net.Conn, linkKey []byte, me Identity, myAddrs []string, dialer bool, timeout time.Duration) (*Conn, error) {
	raw.SetDeadline(time.Now().Add(timeout))
	defer raw.SetDeadline(time.Time{})

	myNonce := NewNonce()
	hello := &Message{Type: TypeHello, Version: Version, ID: me.ID, Name: me.Name, Kind: me.Kind, Nonce: myNonce, Addrs: myAddrs}
	if err := WriteFrame(raw, Encode(hello)); err != nil {
		return nil, err
	}
	n, err := ReadFrameHeader(raw)
	if err != nil {
		return nil, err
	}
	if n > 64<<10 {
		return nil, errors.New("protocol: oversized hello")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(raw, buf); err != nil {
		return nil, err
	}
	peer, err := Decode(buf)
	if err != nil {
		return nil, err
	}
	if peer.Type != TypeHello {
		return nil, fmt.Errorf("protocol: expected hello, got %q", peer.Type)
	}
	if peer.Version != Version {
		return nil, fmt.Errorf("protocol: peer speaks version %d, we speak %d (update VoidBridge on both devices)", peer.Version, Version)
	}
	if len(peer.Nonce) != nonceSize || peer.ID == "" {
		return nil, errors.New("protocol: bad hello")
	}
	if peer.ID == me.ID {
		return nil, errors.New("protocol: connected to myself")
	}
	cn, sn := myNonce, peer.Nonce
	if !dialer {
		cn, sn = peer.Nonce, myNonce
	}
	c := &Conn{
		raw:       raw,
		cipher:    NewCipher(SessionKey(linkKey, cn, sn), !dialer),
		Peer:      Identity{ID: peer.ID, Name: peer.Name, Kind: peer.Kind},
		PeerAddrs: peer.Addrs,
		Dialer:    dialer,
	}
	if err := c.Send(&Message{Type: TypeReady}); err != nil {
		return nil, err
	}
	m, err := c.Recv(timeout)
	if err != nil {
		return nil, err
	}
	if m.Type != TypeReady {
		return nil, fmt.Errorf("protocol: expected ready, got %q", m.Type)
	}
	return c, nil
}

// Send encrypts and writes a message. Safe for concurrent use.
func (c *Conn) Send(m *Message) error {
	plain := Encode(m)
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	sealed := c.cipher.Seal(plain)
	// Allow time proportional to size (≥100 KB/s) for large images.
	c.raw.SetWriteDeadline(time.Now().Add(10*time.Second + time.Duration(len(sealed)/100_000)*time.Second))
	return WriteFrame(c.raw, sealed)
}

// Recv reads the next message. idle is how long to wait for a frame to begin;
// once one has begun, time is extended in proportion to its size.
// Only one goroutine may call Recv.
func (c *Conn) Recv(idle time.Duration) (*Message, error) {
	c.raw.SetReadDeadline(time.Now().Add(idle))
	n, err := ReadFrameHeader(c.raw)
	if err != nil {
		return nil, err
	}
	c.raw.SetReadDeadline(time.Now().Add(10*time.Second + time.Duration(n/100_000)*time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.raw, buf); err != nil {
		return nil, err
	}
	plain, err := c.cipher.Open(buf)
	if err != nil {
		return nil, err
	}
	return Decode(plain)
}

// RemoteAddr returns the peer's address.
func (c *Conn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }

// Close closes the link.
func (c *Conn) Close() error { return c.raw.Close() }
