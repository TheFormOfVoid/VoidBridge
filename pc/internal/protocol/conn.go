package protocol

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Conn is an authenticated, encrypted VoidBridge connection.
type Conn struct {
	raw    net.Conn
	cipher *Cipher
	sendMu sync.Mutex

	PeerID   string
	PeerName string
	// ClockOffset is peer clock minus our clock, in milliseconds, as estimated
	// during the handshake.
	ClockOffset int64
}

// Identity describes the local device in a hello.
type Identity struct {
	ID   string
	Name string
}

// Handshake runs the hello/ready exchange over raw and returns a Conn.
// isServer must be true on the PC and false on the phone.
func Handshake(raw net.Conn, pairingKey []byte, me Identity, isServer bool, timeout time.Duration) (*Conn, error) {
	raw.SetDeadline(time.Now().Add(timeout))
	defer raw.SetDeadline(time.Time{})

	myNonce := NewNonce()
	hello := &Message{Type: TypeHello, Version: Version, ID: me.ID, Name: me.Name, Nonce: myNonce, Time: nowMillis()}
	if err := WriteFrame(raw, Encode(hello)); err != nil {
		return nil, err
	}
	b, err := ReadFrame(raw)
	if err != nil {
		return nil, err
	}
	peer, err := Decode(b)
	if err != nil {
		return nil, err
	}
	if peer.Type != TypeHello {
		return nil, fmt.Errorf("protocol: expected hello, got %q", peer.Type)
	}
	if peer.Version != Version {
		return nil, fmt.Errorf("protocol: peer speaks version %d, we speak %d", peer.Version, Version)
	}
	if len(peer.Nonce) != nonceSize {
		return nil, errors.New("protocol: bad hello nonce")
	}

	clientNonce, serverNonce := myNonce, peer.Nonce
	if isServer {
		clientNonce, serverNonce = peer.Nonce, myNonce
	}
	c, err := NewCipher(SessionKey(pairingKey, clientNonce, serverNonce), isServer)
	if err != nil {
		return nil, err
	}
	conn := &Conn{raw: raw, cipher: c, PeerID: peer.ID, PeerName: peer.Name}
	if peer.Time != 0 {
		conn.ClockOffset = peer.Time - nowMillis()
	}

	// Each side proves it holds the pairing key by sending an encrypted ready.
	if err := conn.Send(&Message{Type: TypeReady}); err != nil {
		return nil, err
	}
	m, err := conn.Recv()
	if err != nil {
		return nil, err
	}
	if m.Type != TypeReady {
		return nil, fmt.Errorf("protocol: expected ready, got %q", m.Type)
	}
	return conn, nil
}

// Send encrypts and writes a message. Safe for concurrent use.
func (c *Conn) Send(m *Message) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.raw.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return WriteFrame(c.raw, c.cipher.Seal(Encode(m)))
}

// Recv reads and decrypts the next message. Only one goroutine may call it.
func (c *Conn) Recv() (*Message, error) {
	b, err := ReadFrame(c.raw)
	if err != nil {
		return nil, err
	}
	plain, err := c.cipher.Open(b)
	if err != nil {
		return nil, err
	}
	return Decode(plain)
}

// SetReadDeadline forwards to the underlying connection.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.raw.SetReadDeadline(t) }

// RemoteAddr returns the peer's address.
func (c *Conn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }

// Close closes the connection.
func (c *Conn) Close() error { return c.raw.Close() }

func nowMillis() int64 { return time.Now().UnixMilli() }
