// Package relay connects a device to a self-hosted VoidBridge server: account
// sign-up/sign-in, admin calls, and the long-lived sync connection.
package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
	"github.com/TheFormOfVoid/VoidBridge/internal/server"
)

// ErrUnauthorized means the token was revoked or the account disabled.
var ErrUnauthorized = errors.New("signed out by the server")

// NormalizeURL turns "pi", "pi:47831" or "https://x" into a base URL.
func NormalizeURL(s string) (string, error) {
	s = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), "/"))
	if s == "" {
		return "", errors.New("enter the server address")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%q is not a valid server address", s)
	}
	if u.Port() == "" && u.Scheme == "http" {
		u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(protocol.ServerPort))
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

// API talks to a server's HTTP API.
type API struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func (a *API) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (a *API) do(method, path string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, a.Base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return fmt.Errorf("can't reach the server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		if resp.StatusCode == http.StatusUnauthorized && path != "/api/login" {
			return ErrUnauthorized
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Info fetches server information (also checks it's a VoidBridge server).
func (a *API) Info() (*server.Info, error) {
	var i server.Info
	if err := a.do("GET", "/api/info", nil, &i); err != nil {
		return nil, err
	}
	if i.Server != "voidbridge" {
		return nil, errors.New("that address isn't a VoidBridge server")
	}
	if i.Protocol != protocol.Version {
		return nil, fmt.Errorf("the server speaks protocol %d, this app speaks %d; update both", i.Protocol, protocol.Version)
	}
	return &i, nil
}

func creds(username string, keys protocol.Keys, invite string, me protocol.Identity) server.Credentials {
	return server.Credentials{
		Username: username, AuthKey: base64.StdEncoding.EncodeToString(keys.Auth), Invite: invite,
		DeviceID: me.ID, DeviceName: me.Name, DeviceKind: me.Kind,
	}
}

// Register creates an account and signs this device in.
func (a *API) Register(username string, keys protocol.Keys, invite string, me protocol.Identity) (*server.LoginResult, error) {
	var r server.LoginResult
	if err := a.do("POST", "/api/register", creds(username, keys, invite, me), &r); err != nil {
		return nil, err
	}
	a.Token = r.Token
	return &r, nil
}

// Login signs this device in.
func (a *API) Login(username string, keys protocol.Keys, me protocol.Identity) (*server.LoginResult, error) {
	var r server.LoginResult
	if err := a.do("POST", "/api/login", creds(username, keys, "", me), &r); err != nil {
		return nil, err
	}
	a.Token = r.Token
	return &r, nil
}

func (a *API) Logout() error           { return a.do("POST", "/api/logout", nil, nil) }
func (a *API) Me() (*server.Me, error) { var m server.Me; return &m, a.do("GET", "/api/me", nil, &m) }
func (a *API) RevokeDevice(id string) error {
	return a.do("DELETE", "/api/me/devices/"+url.PathEscape(id), nil, nil)
}
func (a *API) Users() ([]server.UserView, error) {
	var u []server.UserView
	return u, a.do("GET", "/api/admin/users", nil, &u)
}
func (a *API) SetUserDisabled(name string, d bool) error {
	return a.do("PATCH", "/api/admin/users/"+url.PathEscape(name), map[string]bool{"disabled": d}, nil)
}
func (a *API) SetUserAdmin(name string, admin bool) error {
	return a.do("PATCH", "/api/admin/users/"+url.PathEscape(name), map[string]bool{"admin": admin}, nil)
}
func (a *API) DeleteUser(name string) error {
	return a.do("DELETE", "/api/admin/users/"+url.PathEscape(name), nil, nil)
}
func (a *API) Invites() ([]server.Invite, error) {
	var i []server.Invite
	return i, a.do("GET", "/api/admin/invites", nil, &i)
}
func (a *API) CreateInvite(uses, days int, note string) (*server.Invite, error) {
	var i server.Invite
	return &i, a.do("POST", "/api/admin/invites", server.InviteRequest{Uses: uses, Days: days, Note: note}, &i)
}
func (a *API) RevokeInvite(code string) error {
	return a.do("DELETE", "/api/admin/invites/"+url.PathEscape(code), nil, nil)
}

// ---- sync connection ----

// State of the server connection, for the UI.
type State struct {
	Connected bool
	Error     string
}

// Client keeps a device connected to its server.
type Client struct {
	Base    string
	Token   string
	Node    *node.Node
	Logf    func(string, ...any)
	OnState func(State)
	// OnUnauthorized is called once if the server rejects the token.
	OnUnauthorized func()
}

type link struct {
	ws   *websocket.Conn
	name string
	once sync.Once
	stop context.CancelFunc
}

func (l *link) Send(m *protocol.Message) error {
	b := protocol.Encode(m)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second+time.Duration(len(b)/100_000)*time.Second)
	defer cancel()
	return l.ws.Write(ctx, websocket.MessageBinary, b)
}
func (l *link) Close() { l.once.Do(l.stop) }
func (l *link) Info() node.LinkInfo {
	return node.LinkInfo{PeerID: "server", Name: l.name, Kind: "server", Via: "Server"}
}

// Run maintains the connection until stop closes.
func (c *Client) Run(stop <-chan struct{}) {
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-stop; cancel() }()
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		st := State{}
		if err != nil {
			st.Error = err.Error()
		}
		if errors.Is(err, ErrUnauthorized) {
			c.state(st)
			if c.OnUnauthorized != nil {
				c.OnUnauthorized()
			}
			return
		}
		c.state(st)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
		if err == nil {
			backoff = time.Second
		}
	}
}

func (c *Client) state(s State) {
	if c.OnState != nil {
		c.OnState(s)
	}
}

func (c *Client) session(ctx context.Context) error {
	wsURL := "ws" + strings.TrimPrefix(c.Base, "http") + "/api/sync"
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	ws, resp, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}},
	})
	dcancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		return fmt.Errorf("can't reach the server")
	}
	ws.SetReadLimit(protocol.MaxFrame + 64<<10)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	host := c.Base
	if u, err := url.Parse(c.Base); err == nil {
		host = u.Hostname()
	}
	l := &link{ws: ws, name: host, stop: scancel}
	c.Node.AddLink(l)
	defer c.Node.RemoveLink(l)
	c.state(State{Connected: true})
	c.Logf("connected to server %s", c.Base)

	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(sctx, 15*time.Second)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					scancel()
					return
				}
			}
		}
	}()

	defer ws.Close(websocket.StatusNormalClosure, "")
	for {
		typ, b, err := ws.Read(sctx)
		if err != nil {
			if sctx.Err() != nil && ctx.Err() == nil {
				return errors.New("connection to server lost")
			}
			return errors.New("connection to server lost")
		}
		if typ != websocket.MessageBinary {
			continue
		}
		m, err := protocol.Decode(b)
		if err != nil {
			continue
		}
		switch m.Type {
		case protocol.TypeDevices:
			c.Node.SetRemoteDevices(l, m.Peers)
		case protocol.TypeClip:
			c.Node.Handle(l, m)
		}
	}
}
