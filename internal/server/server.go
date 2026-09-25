// Package server is the self-hosted VoidBridge relay: accounts, invite codes
// and a WebSocket hub that passes end-to-end encrypted clips between the
// devices of each account. It cannot read clipboard contents.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// Signup policies.
const (
	SignupInvite = "invite" // first account free (becomes admin), then invite only
	SignupOpen   = "open"   // anyone who can reach the server
)

// Tunables.
var (
	PingInterval = 20 * time.Second
	PingTimeout  = 15 * time.Second
)

// Server is the HTTP handler.
type Server struct {
	Store   *Store
	Signup  string
	Name    string
	Version string
	Logf    func(string, ...any)

	hub     hub
	limiter limiter
	mux     *http.ServeMux
}

// New builds a server around a store.
func New(store *Store, signup string) *Server {
	name, _ := os.Hostname()
	s := &Server{Store: store, Signup: signup, Name: name, Version: "dev", Logf: log.Printf}
	s.hub.accounts = map[string]*account{}
	s.limiter.fails = map[string]*failure{}
	m := http.NewServeMux()
	m.HandleFunc("GET /api/info", s.info)
	m.HandleFunc("POST /api/register", s.register)
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", s.auth(s.logout))
	m.HandleFunc("GET /api/me", s.auth(s.me))
	m.HandleFunc("DELETE /api/me/devices/{id}", s.auth(s.revokeDevice))
	m.HandleFunc("GET /api/sync", s.sync)
	m.HandleFunc("GET /api/admin/users", s.admin(s.listUsers))
	m.HandleFunc("PATCH /api/admin/users/{name}", s.admin(s.patchUser))
	m.HandleFunc("DELETE /api/admin/users/{name}", s.admin(s.deleteUser))
	m.HandleFunc("GET /api/admin/invites", s.admin(s.listInvites))
	m.HandleFunc("POST /api/admin/invites", s.admin(s.createInvite))
	m.HandleFunc("DELETE /api/admin/invites/{code}", s.admin(s.revokeInvite))
	m.HandleFunc("POST /api/local/invites", s.localOnly(s.createInvite))
	m.HandleFunc("GET /api/local/users", s.localOnly(s.listUsers))
	m.HandleFunc("PATCH /api/local/users/{name}", s.localOnly(s.patchUser))
	m.HandleFunc("GET /", s.home)
	s.mux = m
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(v)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return t
	}
	return ""
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type authed func(w http.ResponseWriter, r *http.Request, se *Session, u *User)

func (s *Server) auth(h authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		se, u, err := s.Store.Authenticate(bearer(r))
		if err != nil {
			writeErr(w, http.StatusUnauthorized, err)
			return
		}
		h(w, r, se, u)
	}
}

func (s *Server) admin(h authed) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request, se *Session, u *User) {
		if !u.Admin {
			writeErr(w, http.StatusForbidden, errors.New("admins only"))
			return
		}
		h(w, r, se, u)
	})
}

// localOnly allows requests from the server machine itself, for the CLI.
func (s *Server) localOnly(h authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ip := net.ParseIP(clientIP(r)); ip == nil || !ip.IsLoopback() {
			writeErr(w, http.StatusForbidden, errors.New("only from the server itself"))
			return
		}
		h(w, r, nil, &User{Name: "(server console)", Admin: true})
	}
}

// ---- rate limiting of failed logins ----

type failure struct {
	count int
	first time.Time
}

type limiter struct {
	mu    sync.Mutex
	fails map[string]*failure
}

const (
	maxFails   = 10
	failWindow = 15 * time.Minute
)

func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.fails[key]
	if f == nil {
		return false
	}
	if time.Since(f.first) > failWindow {
		delete(l.fails, key)
		return false
	}
	return f.count >= maxFails
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.fails[key]
	if f == nil || time.Since(f.first) > failWindow {
		f = &failure{first: time.Now()}
		l.fails[key] = f
	}
	f.count++
}

// ---- public API ----

// Info is returned by GET /api/info.
type Info struct {
	Server   string `json:"server"` // "voidbridge"
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	Name     string `json:"name"`
	Signup   string `json:"signup"`    // "invite" or "open"
	HasUsers bool   `json:"has_users"` // false: the next account becomes admin, no invite needed
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, Info{Server: "voidbridge", Protocol: protocol.Version, Version: s.Version, Name: s.Name, Signup: s.Signup, HasUsers: s.Store.HasUsers()})
}

// Credentials is the body of register and login.
type Credentials struct {
	Username   string `json:"username"`
	AuthKey    string `json:"auth_key"` // base64 of Keys.Auth
	Invite     string `json:"invite,omitempty"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	DeviceKind string `json:"device_kind"`
}

// LoginResult is returned by register and login.
type LoginResult struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Admin    bool   `json:"admin"`
}

func (c *Credentials) key() ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(c.AuthKey)
	if err != nil || len(k) != 32 {
		return nil, errors.New("bad auth_key")
	}
	if c.DeviceID == "" || len(c.DeviceID) > 64 || len(c.DeviceName) > 100 {
		return nil, errors.New("bad device")
	}
	return k, nil
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var c Credentials
	if err := readJSON(r, &c); err != nil {
		writeErr(w, 400, err)
		return
	}
	k, err := c.key()
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		writeErr(w, http.StatusTooManyRequests, errors.New("too many attempts; try again later"))
		return
	}
	u, err := s.Store.Register(c.Username, k, c.Invite, s.Signup != SignupOpen)
	if err != nil {
		if errors.Is(err, ErrBadInvite) {
			s.limiter.fail(ip)
		}
		writeErr(w, 400, err)
		return
	}
	s.Logf("new account %q (admin=%v) from %s", u.Name, u.Admin, ip)
	s.issue(w, u, &c)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var c Credentials
	if err := readJSON(r, &c); err != nil {
		writeErr(w, 400, err)
		return
	}
	k, err := c.key()
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ip := clientIP(r)
	user := protocol.NormalizeUsername(c.Username)
	if s.limiter.blocked(ip) || s.limiter.blocked("user:"+user) {
		writeErr(w, http.StatusTooManyRequests, errors.New("too many failed logins; try again in 15 minutes"))
		return
	}
	u, err := s.Store.CheckLogin(user, k)
	if err != nil {
		s.limiter.fail(ip)
		s.limiter.fail("user:" + user)
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	s.issue(w, u, &c)
}

func (s *Server) issue(w http.ResponseWriter, u *User, c *Credentials) {
	tok, err := s.Store.NewSession(u.Name, c.DeviceID, c.DeviceName, c.DeviceKind)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, LoginResult{Token: tok, Username: u.Name, Admin: u.Admin})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, se *Session, u *User) {
	s.Store.Logout(bearer(r))
	s.hub.kick(u.Name, se.DeviceID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// DeviceView is a device in GET /api/me.
type DeviceView struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen"`
	This     bool      `json:"this"`
}

// Me is returned by GET /api/me.
type Me struct {
	Username string       `json:"username"`
	Admin    bool         `json:"admin"`
	Devices  []DeviceView `json:"devices"`
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, se *Session, u *User) {
	online := s.hub.online(u.Name)
	out := Me{Username: u.Name, Admin: u.Admin}
	for _, d := range s.Store.Devices(u.Name) {
		out.Devices = append(out.Devices, DeviceView{ID: d.DeviceID, Name: d.DeviceName, Kind: d.DeviceKind, Online: online[d.DeviceID], LastSeen: d.LastSeen, This: d.DeviceID == se.DeviceID})
	}
	writeJSON(w, 200, out)
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request, se *Session, u *User) {
	id := r.PathValue("id")
	if err := s.Store.RevokeDevice(u.Name, id); err != nil {
		writeErr(w, 404, err)
		return
	}
	s.hub.kick(u.Name, id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- admin API ----

// UserView is a user in the admin list.
type UserView struct {
	User
	Devices int  `json:"devices"`
	Online  bool `json:"online"`
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, _ *Session, _ *User) {
	var out []UserView
	for _, u := range s.Store.Users() {
		out = append(out, UserView{User: u, Devices: len(s.Store.Devices(u.Name)), Online: len(s.hub.online(u.Name)) > 0})
	}
	writeJSON(w, 200, out)
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request, _ *Session, me *User) {
	name := r.PathValue("name")
	var body struct {
		Disabled *bool `json:"disabled"`
		Admin    *bool `json:"admin"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if name == me.Name && ((body.Disabled != nil && *body.Disabled) || (body.Admin != nil && !*body.Admin)) {
		writeErr(w, 400, errors.New("you can't disable yourself or remove your own admin rights"))
		return
	}
	if body.Disabled != nil {
		if err := s.Store.SetDisabled(name, *body.Disabled); err != nil {
			writeErr(w, 404, err)
			return
		}
		if *body.Disabled {
			s.hub.kickAll(name)
		}
	}
	if body.Admin != nil {
		if err := s.Store.SetAdmin(name, *body.Admin); err != nil {
			writeErr(w, 404, err)
			return
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, _ *Session, me *User) {
	name := r.PathValue("name")
	if name == me.Name {
		writeErr(w, 400, errors.New("you can't delete your own account here"))
		return
	}
	if err := s.Store.DeleteUser(name); err != nil {
		writeErr(w, 404, err)
		return
	}
	s.hub.kickAll(name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listInvites(w http.ResponseWriter, r *http.Request, _ *Session, _ *User) {
	writeJSON(w, 200, s.Store.Invites())
}

// InviteRequest is the body of POST /api/admin/invites.
type InviteRequest struct {
	Uses int    `json:"uses"`
	Days int    `json:"days"` // 0 = never expires
	Note string `json:"note"`
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request, _ *Session, u *User) {
	var req InviteRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	inv, err := s.Store.CreateInvite(u.Name, req.Uses, time.Duration(req.Days)*24*time.Hour, req.Note)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, inv)
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request, _ *Session, _ *User) {
	if err := s.Store.RevokeInvite(r.PathValue("code")); err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("VoidBridge server is running.\n\nAdd this address in the VoidBridge app on your devices to sign in or create an account.\nAdmins manage users and invites from the desktop app (Server tab).\n"))
}

// ---- WebSocket hub ----

type conn struct {
	ws     *websocket.Conn
	user   string
	dev    Session
	token  string
	cancel context.CancelFunc
}

func (c *conn) send(m *protocol.Message) error {
	b := protocol.Encode(m)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second+time.Duration(len(b)/100_000)*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageBinary, b)
}

type account struct {
	conns   map[*conn]struct{}
	current *protocol.Message
}

type hub struct {
	mu       sync.Mutex
	accounts map[string]*account
}

func (h *hub) acct(user string) *account {
	a := h.accounts[user]
	if a == nil {
		a = &account{conns: map[*conn]struct{}{}}
		h.accounts[user] = a
	}
	return a
}

func (h *hub) online(user string) map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]bool{}
	if a := h.accounts[user]; a != nil {
		for c := range a.conns {
			out[c.dev.DeviceID] = true
		}
	}
	return out
}

func (h *hub) kick(user, deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a := h.accounts[user]; a != nil {
		for c := range a.conns {
			if c.dev.DeviceID == deviceID {
				c.cancel()
			}
		}
	}
}

func (h *hub) kickAll(user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a := h.accounts[user]; a != nil {
		for c := range a.conns {
			c.cancel()
		}
	}
}

// devicesLocked is the "devices" message for an account.
func devicesLocked(a *account) *protocol.Message {
	m := &protocol.Message{Type: protocol.TypeDevices}
	for c := range a.conns {
		m.Peers = append(m.Peers, protocol.PeerInfo{ID: c.dev.DeviceID, Name: c.dev.DeviceName, Kind: c.dev.DeviceKind})
	}
	return m
}

func (h *hub) broadcastDevices(user string) {
	h.mu.Lock()
	a := h.accounts[user]
	if a == nil {
		h.mu.Unlock()
		return
	}
	m := devicesLocked(a)
	var all []*conn
	for c := range a.conns {
		all = append(all, c)
	}
	h.mu.Unlock()
	for _, c := range all {
		go c.send(m)
	}
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	se, u, err := s.Store.Authenticate(tok)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(protocol.MaxFrame + 64<<10)
	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{ws: ws, user: u.Name, dev: *se, token: tok, cancel: cancel}
	defer cancel()

	s.hub.mu.Lock()
	a := s.hub.acct(u.Name)
	for old := range a.conns {
		if old.dev.DeviceID == se.DeviceID {
			old.cancel() // the device reconnected; drop its stale connection
		}
	}
	a.conns[c] = struct{}{}
	cur := a.current
	s.hub.mu.Unlock()
	s.Logf("%s/%s connected from %s", u.Name, se.DeviceName, clientIP(r))
	s.hub.broadcastDevices(u.Name)
	if cur != nil {
		go c.send(cur)
	}

	go func() {
		t := time.NewTicker(PingInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, PingTimeout)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	err = s.readLoop(ctx, c)
	ws.Close(websocket.StatusNormalClosure, "")
	s.hub.mu.Lock()
	delete(a.conns, c)
	s.hub.mu.Unlock()
	s.Store.Touch(tok)
	s.hub.broadcastDevices(u.Name)
	s.Logf("%s/%s disconnected: %v", u.Name, se.DeviceName, err)
}

func (s *Server) readLoop(ctx context.Context, c *conn) error {
	for {
		typ, b, err := c.ws.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		m, err := protocol.Decode(b)
		if err != nil {
			return err
		}
		if m.Type != protocol.TypeClip {
			continue
		}
		s.hub.mu.Lock()
		a := s.hub.acct(c.user)
		if !m.Newer(a.current) || (a.current != nil && a.current.ID == m.ID) {
			s.hub.mu.Unlock()
			continue
		}
		a.current = m
		var others []*conn
		for o := range a.conns {
			if o != c {
				others = append(others, o)
			}
		}
		s.hub.mu.Unlock()
		for _, o := range others {
			go func(o *conn) {
				if o.send(m) != nil {
					o.cancel()
				}
			}(o)
		}
	}
}
