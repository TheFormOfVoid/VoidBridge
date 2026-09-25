// Package protocol implements the VoidBridge wire format and cryptography.
// docs/PROTOCOL.md is the reference; android/core must stay byte-compatible.
package protocol

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	Version = 2

	// MaxFrame bounds one frame (header + body, before link encryption).
	MaxFrame = 40 << 20
	MaxText  = 1 << 20
	MaxImage = 25 << 20

	PeerPort      = 47829
	DiscoveryPort = 47830
	ServerPort    = 47831

	pbkdfIterations = 200_000
	nonceSize       = 16
)

// Message types.
const (
	TypeHello   = "hello"
	TypeReady   = "ready"
	TypePing    = "ping"
	TypePong    = "pong"
	TypeClip    = "clip"
	TypePeers   = "peers"
	TypeDevices = "devices"
)

// Clip content types.
const (
	ClipText  = "text"
	ClipImage = "image"
)

// PeerInfo is a device and the addresses ("host:port") it may be reached at.
type PeerInfo struct {
	ID    string   `json:"id"`
	Name  string   `json:"name,omitempty"`
	Kind  string   `json:"kind,omitempty"`
	Addrs []string `json:"addrs,omitempty"`
}

// Message is a frame header. Body carries binary payload (encrypted clip
// content) and is not part of the JSON.
type Message struct {
	Type string `json:"t"`

	// hello: device identity. clip: ID is the clip id.
	Version int      `json:"v,omitempty"`
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name,omitempty"`
	Kind    string   `json:"kind,omitempty"` // device kind: "windows", "android"
	Nonce   []byte   `json:"nonce,omitempty"`
	Addrs   []string `json:"addrs,omitempty"`

	// clip
	Origin     string `json:"origin,omitempty"`      // device id that copied it
	OriginName string `json:"origin_name,omitempty"` // for display
	Time       int64  `json:"time,omitempty"`        // unix ms when copied
	ClipType   string `json:"ctype,omitempty"`
	Mime       string `json:"mime,omitempty"`

	// peers / devices
	Peers []PeerInfo `json:"peers,omitempty"`

	Body []byte `json:"-"`
}

// Newer reports whether clip m should replace clip cur (nil cur loses).
// Ordering is by copy time, then by id so every device agrees on ties.
func (m *Message) Newer(cur *Message) bool {
	if cur == nil {
		return true
	}
	if m.Time != cur.Time {
		return m.Time > cur.Time
	}
	return m.ID > cur.ID
}

var (
	ErrFrameTooLarge = errors.New("protocol: frame too large")
	ErrAuth          = errors.New("protocol: authentication failed (devices are not in the same group)")
	ErrDecrypt       = errors.New("protocol: cannot decrypt clip (different group key)")
)

// ---- Keys ----

// Keys are everything derived from a group secret.
type Keys struct {
	Master  []byte
	Link    []byte // authenticates and encrypts direct device-to-device links
	Content []byte // end-to-end encrypts clip contents
	Auth    []byte // proves account ownership to a server
}

// KeysFromMaster derives the subkeys.
func KeysFromMaster(master []byte) Keys {
	sub := func(label string) []byte {
		k, err := hkdf.Key(sha256.New, master, nil, label, 32)
		if err != nil {
			panic(err)
		}
		return k
	}
	return Keys{
		Master:  master,
		Link:    sub("voidbridge-link-v2"),
		Content: sub("voidbridge-content-v2"),
		Auth:    sub("voidbridge-auth-v2"),
	}
}

func pbkdf(secret, salt string) []byte {
	k, err := pbkdf2.Key(sha256.New, secret, []byte(salt), pbkdfIterations, 32)
	if err != nil {
		panic(err)
	}
	return k
}

// MasterFromCode derives the master key of a code-based (serverless) group.
func MasterFromCode(code string) []byte {
	return pbkdf(NormalizeCode(code), "voidbridge-pairing-v1")
}

// MasterFromAccount derives the master key of a server account. The password
// never leaves the device; the server only sees Keys.Auth.
func MasterFromAccount(username, password string) []byte {
	return pbkdf(password, "voidbridge-account-v1:"+NormalizeUsername(username))
}

// NormalizeUsername lowercases and trims a username.
func NormalizeUsername(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

// Fingerprint identifies a group in discovery beacons without revealing keys.
func (k Keys) Fingerprint() string {
	m := hmac.New(sha256.New, k.Link)
	m.Write([]byte("voidbridge-beacon-v2"))
	return hex.EncodeToString(m.Sum(nil)[:8])
}

// NewGroupCode returns a fresh 80-bit code formatted XXXX-XXXX-XXXX-XXXX.
func NewGroupCode() string {
	b := make([]byte, 10)
	rand.Read(b)
	s := base32.StdEncoding.EncodeToString(b)
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// NormalizeCode uppercases, strips separators and fixes 0/1/8 typos.
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

// ValidCode reports whether code is a complete group code.
func ValidCode(code string) bool { return len(NormalizeCode(code)) == 16 }

// FormatCode pretty-prints a code as XXXX-XXXX-XXXX-XXXX.
func FormatCode(code string) string {
	n := NormalizeCode(code)
	if len(n) != 16 {
		return code
	}
	return n[0:4] + "-" + n[4:8] + "-" + n[8:12] + "-" + n[12:16]
}

// NewID returns a random 16-hex-digit id.
func NewID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// NewNonce returns a random handshake nonce.
func NewNonce() []byte {
	b := make([]byte, nonceSize)
	rand.Read(b)
	return b
}

// ---- Clip content encryption (end to end) ----

func clipAAD(m *Message) []byte {
	return []byte("voidbridge-clip-v2|" + m.ID + "|" + m.Origin + "|" + strconv.FormatInt(m.Time, 10) + "|" + m.ClipType + "|" + m.Mime)
}

// SealClip encrypts plaintext into m.Body with the content key. The clip's
// header fields are bound as associated data so they can't be altered.
func SealClip(contentKey []byte, m *Message, plaintext []byte) {
	aead := gcm(contentKey)
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	m.Body = aead.Seal(nonce, nonce, plaintext, clipAAD(m))
}

// OpenClip decrypts m.Body.
func OpenClip(contentKey []byte, m *Message) ([]byte, error) {
	aead := gcm(contentKey)
	if len(m.Body) < aead.NonceSize() {
		return nil, ErrDecrypt
	}
	p, err := aead.Open(nil, m.Body[:aead.NonceSize()], m.Body[aead.NonceSize():], clipAAD(m))
	if err != nil {
		return nil, ErrDecrypt
	}
	return p, nil
}

func gcm(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

// ---- Frame encoding ----

// Encode serialises a message: uint32 header length, JSON header, body.
func Encode(m *Message) []byte {
	h, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	out := make([]byte, 4+len(h)+len(m.Body))
	binary.BigEndian.PutUint32(out, uint32(len(h)))
	copy(out[4:], h)
	copy(out[4+len(h):], m.Body)
	return out
}

// Decode parses a frame produced by Encode.
func Decode(b []byte) (*Message, error) {
	if len(b) < 4 {
		return nil, errors.New("protocol: short frame")
	}
	n := binary.BigEndian.Uint32(b)
	if uint64(n) > uint64(len(b)-4) {
		return nil, errors.New("protocol: bad header length")
	}
	var m Message
	dec := json.NewDecoder(bytes.NewReader(b[4 : 4+n]))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("protocol: bad header: %w", err)
	}
	if m.Type == "" {
		return nil, errors.New("protocol: message without type")
	}
	if rest := b[4+n:]; len(rest) > 0 {
		m.Body = rest
	}
	return &m, nil
}

// WriteFrame writes a length-prefixed blob.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxFrame+64 {
		return ErrFrameTooLarge
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrameHeader reads a frame's length prefix.
func ReadFrameHeader(r io.Reader) (int, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrame+64 {
		return 0, ErrFrameTooLarge
	}
	return int(n), nil
}

// ---- Link cipher (direct links only) ----

// Direction tags for link nonces.
const (
	DirClient uint32 = 0x76620001
	DirServer uint32 = 0x76620002
)

// SessionKey derives a per-connection key from the link key and both nonces.
func SessionKey(linkKey, clientNonce, serverNonce []byte) []byte {
	m := hmac.New(sha256.New, linkKey)
	m.Write([]byte("voidbridge-session-v2"))
	m.Write(clientNonce)
	m.Write(serverNonce)
	return m.Sum(nil)
}

// Cipher seals frames in one direction and opens them in the other, with
// counter nonces the receiver requires in exact order (no replay/reorder).
type Cipher struct {
	aead               cipher.AEAD
	sendDir, recvDir   uint32
	sendCount, recvCnt uint64
}

// NewCipher builds a Cipher; the dialer is the "client".
func NewCipher(sessionKey []byte, isServer bool) *Cipher {
	c := &Cipher{aead: gcm(sessionKey), sendDir: DirClient, recvDir: DirServer}
	if isServer {
		c.sendDir, c.recvDir = DirServer, DirClient
	}
	return c
}

func linkNonce(dir uint32, n uint64) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b, dir)
	binary.BigEndian.PutUint64(b[4:], n)
	return b
}

// Seal encrypts one frame. Not safe for concurrent use.
func (c *Cipher) Seal(plain []byte) []byte {
	out := c.aead.Seal(nil, linkNonce(c.sendDir, c.sendCount), plain, nil)
	c.sendCount++
	return out
}

// Open decrypts the next frame. Not safe for concurrent use.
func (c *Cipher) Open(sealed []byte) ([]byte, error) {
	p, err := c.aead.Open(nil, linkNonce(c.recvDir, c.recvCnt), sealed, nil)
	if err != nil {
		return nil, ErrAuth
	}
	c.recvCnt++
	return p, nil
}

// Hash returns a hex SHA-256, used to recognise identical clipboard content.
func Hash(kind string, data []byte) string {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
