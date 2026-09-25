package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// User is an account. Its clipboard is end-to-end encrypted with a key derived
// from the password, which the server never sees: it stores only a hash of
// the auth key (itself derived from the password with PBKDF2 + HKDF).
type User struct {
	Name     string    `json:"name"`
	AuthHash string    `json:"auth_hash"`
	Admin    bool      `json:"admin"`
	Disabled bool      `json:"disabled"`
	Created  time.Time `json:"created"`
}

// Invite lets someone create an account.
type Invite struct {
	Code      string    `json:"code"`
	CreatedBy string    `json:"created_by"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	UsesLeft  int       `json:"uses_left"`
	Note      string    `json:"note,omitempty"`
}

// Session is one logged-in device.
type Session struct {
	User       string    `json:"user"`
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
	DeviceKind string    `json:"device_kind"`
	Created    time.Time `json:"created"`
	LastSeen   time.Time `json:"last_seen"`
}

type storeData struct {
	Users    map[string]*User    `json:"users"`
	Invites  map[string]*Invite  `json:"invites"`
	Sessions map[string]*Session `json:"sessions"` // by sha256(token)
}

// Store persists accounts as a JSON file. It's small (a handful of users and
// devices), so the whole thing is kept in memory and rewritten on change.
type Store struct {
	mu   sync.Mutex
	path string
	d    storeData
}

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)

var (
	ErrBadUsername = errors.New("usernames are 3-32 characters: letters, digits, dot, dash, underscore")
	ErrTaken       = errors.New("that username is taken")
	ErrBadInvite   = errors.New("invalid or expired invite code")
	ErrBadLogin    = errors.New("wrong username or password")
	ErrDisabled    = errors.New("this account is disabled")
	ErrNotFound    = errors.New("not found")
)

// OpenStore loads (or creates) the store in dir.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "voidbridge-server.json")}
	s.d = storeData{Users: map[string]*User{}, Invites: map[string]*Invite{}, Sessions: map[string]*Session{}}
	b, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.d.Users == nil {
		s.d.Users = map[string]*User{}
	}
	if s.d.Invites == nil {
		s.d.Invites = map[string]*Invite{}
	}
	if s.d.Sessions == nil {
		s.d.Sessions = map[string]*Session{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	b, _ := json.MarshalIndent(&s.d, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// HasUsers reports whether any account exists.
func (s *Store) HasUsers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.d.Users) > 0
}

// Register creates an account. invite may be empty when needInvite is false.
// The first account on a server is its admin.
func (s *Store) Register(name string, authKey []byte, invite string, needInvite bool) (*User, error) {
	name = protocol.NormalizeUsername(name)
	if !usernameRE.MatchString(name) {
		return nil, ErrBadUsername
	}
	if len(authKey) != 32 {
		return nil, errors.New("bad auth key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.d.Users[name]; ok {
		return nil, ErrTaken
	}
	first := len(s.d.Users) == 0
	var inv *Invite
	if needInvite && !first {
		inv = s.d.Invites[normInvite(invite)]
		if inv == nil || inv.UsesLeft <= 0 || (!inv.Expires.IsZero() && time.Now().After(inv.Expires)) {
			return nil, ErrBadInvite
		}
	}
	u := &User{Name: name, AuthHash: hashHex(authKey), Admin: first, Created: time.Now()}
	s.d.Users[name] = u
	if inv != nil {
		inv.UsesLeft--
		if inv.UsesLeft <= 0 {
			delete(s.d.Invites, inv.Code)
		}
	}
	cp := *u
	return &cp, s.saveLocked()
}

// CheckLogin verifies credentials.
func (s *Store) CheckLogin(name string, authKey []byte) (*User, error) {
	name = protocol.NormalizeUsername(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.d.Users[name]
	want := "0000000000000000000000000000000000000000000000000000000000000000"
	if u != nil {
		want = u.AuthHash
	}
	ok := subtle.ConstantTimeCompare([]byte(hashHex(authKey)), []byte(want)) == 1
	if u == nil || !ok {
		return nil, ErrBadLogin
	}
	if u.Disabled {
		return nil, ErrDisabled
	}
	cp := *u
	return &cp, nil
}

// NewSession issues a device token, replacing any older token for the same device.
func (s *Store) NewSession(user, deviceID, deviceName, deviceKind string) (string, error) {
	b := make([]byte, 32)
	rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, se := range s.d.Sessions {
		if se.User == user && se.DeviceID == deviceID {
			delete(s.d.Sessions, k)
		}
	}
	now := time.Now()
	s.d.Sessions[hashHex([]byte(tok))] = &Session{User: user, DeviceID: deviceID, DeviceName: deviceName, DeviceKind: deviceKind, Created: now, LastSeen: now}
	return tok, s.saveLocked()
}

// Authenticate resolves a token to its session and user.
func (s *Store) Authenticate(token string) (*Session, *User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se := s.d.Sessions[hashHex([]byte(token))]
	if se == nil {
		return nil, nil, ErrBadLogin
	}
	u := s.d.Users[se.User]
	if u == nil {
		return nil, nil, ErrBadLogin
	}
	if u.Disabled {
		return nil, nil, ErrDisabled
	}
	if time.Since(se.LastSeen) > time.Hour {
		se.LastSeen = time.Now()
		s.saveLocked()
	}
	scp, ucp := *se, *u
	return &scp, &ucp, nil
}

// Touch records that a device was online.
func (s *Store) Touch(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if se := s.d.Sessions[hashHex([]byte(token))]; se != nil {
		se.LastSeen = time.Now()
		s.saveLocked()
	}
}

// Logout revokes one token.
func (s *Store) Logout(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.d.Sessions, hashHex([]byte(token)))
	return s.saveLocked()
}

// Devices lists a user's logged-in devices.
func (s *Store) Devices(user string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, se := range s.d.Sessions {
		if se.User == user {
			out = append(out, *se)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceName < out[j].DeviceName })
	return out
}

// RevokeDevice signs a device out.
func (s *Store) RevokeDevice(user, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for k, se := range s.d.Sessions {
		if se.User == user && se.DeviceID == deviceID {
			delete(s.d.Sessions, k)
			found = true
		}
	}
	if !found {
		return ErrNotFound
	}
	return s.saveLocked()
}

// Users lists accounts.
func (s *Store) Users() []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []User
	for _, u := range s.d.Users {
		cp := *u
		cp.AuthHash = ""
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetDisabled enables or disables an account (disabling signs out its devices).
func (s *Store) SetDisabled(name string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.d.Users[name]
	if u == nil {
		return ErrNotFound
	}
	u.Disabled = disabled
	return s.saveLocked()
}

// SetAdmin grants or removes admin rights.
func (s *Store) SetAdmin(name string, admin bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.d.Users[name]
	if u == nil {
		return ErrNotFound
	}
	u.Admin = admin
	return s.saveLocked()
}

// DeleteUser removes an account and its devices.
func (s *Store) DeleteUser(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Users[name] == nil {
		return ErrNotFound
	}
	delete(s.d.Users, name)
	for k, se := range s.d.Sessions {
		if se.User == name {
			delete(s.d.Sessions, k)
		}
	}
	return s.saveLocked()
}

// CreateInvite makes an invite code usable `uses` times for `valid` (0 = forever).
func (s *Store) CreateInvite(by string, uses int, valid time.Duration, note string) (*Invite, error) {
	if uses <= 0 {
		uses = 1
	}
	b := make([]byte, 8)
	rand.Read(b)
	code := base32.StdEncoding.EncodeToString(b)[:12]
	inv := &Invite{Code: code, CreatedBy: by, Created: time.Now(), UsesLeft: uses, Note: note}
	if valid > 0 {
		inv.Expires = inv.Created.Add(valid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Invites[code] = inv
	cp := *inv
	cp.Code = FormatInvite(code)
	return &cp, s.saveLocked()
}

// Invites lists unexpired invites.
func (s *Store) Invites() []Invite {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Invite
	for k, inv := range s.d.Invites {
		if !inv.Expires.IsZero() && time.Now().After(inv.Expires) {
			delete(s.d.Invites, k)
			continue
		}
		cp := *inv
		cp.Code = FormatInvite(inv.Code)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// RevokeInvite deletes an invite.
func (s *Store) RevokeInvite(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := normInvite(code)
	if s.d.Invites[c] == nil {
		return ErrNotFound
	}
	delete(s.d.Invites, c)
	return s.saveLocked()
}

func normInvite(code string) string { return protocol.NormalizeCode(code) }

// FormatInvite shows an invite as XXXX-XXXX-XXXX.
func FormatInvite(code string) string {
	c := normInvite(code)
	if len(c) != 12 {
		return code
	}
	return c[0:4] + "-" + c[4:8] + "-" + c[8:12]
}
