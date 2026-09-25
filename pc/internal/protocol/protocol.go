// Package protocol implements the VoidBridge wire protocol.
//
// A connection is a TCP stream of frames. Each frame is a 4-byte big-endian
// length followed by that many bytes of payload. The first frame in each
// direction is a plaintext Hello. Every later frame is JSON sealed with
// AES-256-GCM under a per-connection session key derived from the pairing key
// and both peers' hello nonces. See docs/PROTOCOL.md.
package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	Version = 1

	// MaxFrame bounds a single frame so a bad peer can't make us allocate
	// unbounded memory.
	MaxFrame = 4 << 20
	// MaxText is the largest clipboard text we will send.
	MaxText = 1 << 20

	pairingIterations = 200_000
	pairingSalt       = "voidbridge-pairing-v1"
	sessionLabel      = "voidbridge-session-v1"
	nonceSize         = 16
)

// Message types.
const (
	TypeHello = "hello"
	TypeReady = "ready"
	TypePing  = "ping"
	TypePong  = "pong"
	TypeClip  = "clip"
	TypeAck   = "ack"
)

// Message is the single JSON shape used for every frame; unused fields are
// omitted.
type Message struct {
	Type string `json:"t"`

	// hello
	Version int    `json:"v,omitempty"`
	ID      string `json:"id,omitempty"` // device id (hello) or clip id (clip/ack)
	Name    string `json:"name,omitempty"`
	Nonce   []byte `json:"nonce,omitempty"`

	// hello, clip: sender's wall-clock time in unix milliseconds
	Time int64 `json:"time,omitempty"`

	// clip
	Text string `json:"text,omitempty"`
}

var (
	ErrFrameTooLarge = errors.New("protocol: frame too large")
	ErrAuth          = errors.New("protocol: authentication failed (wrong pairing code?)")
)

// NewPairingCode returns a fresh human-typeable pairing code of 16 base32
// characters (80 bits), grouped as XXXX-XXXX-XXXX-XXXX.
func NewPairingCode() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	s := base32.StdEncoding.EncodeToString(b) // 16 chars, no padding
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// NormalizeCode uppercases a code and strips separators, and maps the
// characters people commonly mistype (0/O, 1/I, 8/B) onto the base32 alphabet.
func NormalizeCode(code string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch {
		case r == '0':
			sb.WriteRune('O')
		case r == '1':
			sb.WriteRune('I')
		case r == '8':
			sb.WriteRune('B')
		case r >= 'A' && r <= 'Z', r >= '2' && r <= '7':
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// ValidCode reports whether code normalizes to a complete pairing code.
func ValidCode(code string) bool {
	return len(NormalizeCode(code)) == 16
}

// DeriveKey stretches a pairing code into the 32-byte long-term pairing key.
func DeriveKey(code string) []byte {
	k, err := pbkdf2.Key(sha256.New, NormalizeCode(code), []byte(pairingSalt), pairingIterations, 32)
	if err != nil {
		panic(err)
	}
	return k
}

// NewNonce returns a random hello nonce.
func NewNonce() []byte {
	b := make([]byte, nonceSize)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// SessionKey derives the per-connection key. Both sides compute the same value.
func SessionKey(pairingKey, clientNonce, serverNonce []byte) []byte {
	m := hmac.New(sha256.New, pairingKey)
	m.Write([]byte(sessionLabel))
	m.Write(clientNonce)
	m.Write(serverNonce)
	return m.Sum(nil)
}

// WriteFrame writes one length-prefixed frame.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxFrame {
		return ErrFrameTooLarge
	}
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	copy(buf[4:], payload)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one length-prefixed frame.
func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrame {
		return nil, ErrFrameTooLarge
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// Cipher seals and opens frames for one direction pair of a connection.
// GCM nonces are a 4-byte direction tag followed by an 8-byte counter; the
// receiver requires counters to arrive in exact order, which rejects replayed,
// dropped or reordered frames.
type Cipher struct {
	aead      cipher.AEAD
	sendDir   uint32
	recvDir   uint32
	sendCount uint64
	recvCount uint64
}

// Direction tags. The client (phone) sends with DirClient, the server (PC)
// with DirServer.
const (
	DirClient uint32 = 0x76620001
	DirServer uint32 = 0x76620002
)

// NewCipher builds a Cipher. isServer selects which direction tag we send with.
func NewCipher(sessionKey []byte, isServer bool) (*Cipher, error) {
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	c := &Cipher{aead: aead, sendDir: DirClient, recvDir: DirServer}
	if isServer {
		c.sendDir, c.recvDir = DirServer, DirClient
	}
	return c, nil
}

func nonce(dir uint32, n uint64) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b, dir)
	binary.BigEndian.PutUint64(b[4:], n)
	return b
}

// Seal encrypts a plaintext. Not safe for concurrent use.
func (c *Cipher) Seal(plain []byte) []byte {
	out := c.aead.Seal(nil, nonce(c.sendDir, c.sendCount), plain, nil)
	c.sendCount++
	return out
}

// Open decrypts the next expected frame. Not safe for concurrent use.
func (c *Cipher) Open(sealed []byte) ([]byte, error) {
	plain, err := c.aead.Open(nil, nonce(c.recvDir, c.recvCount), sealed, nil)
	if err != nil {
		return nil, ErrAuth
	}
	c.recvCount++
	return plain, nil
}

// Encode marshals a message.
func Encode(m *Message) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// Decode unmarshals a message and checks it has a type.
func Decode(b []byte) (*Message, error) {
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("protocol: bad message: %w", err)
	}
	if m.Type == "" {
		return nil, errors.New("protocol: message without type")
	}
	return &m, nil
}
