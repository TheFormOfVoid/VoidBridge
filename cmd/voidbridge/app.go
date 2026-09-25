package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/image/draw"

	"github.com/TheFormOfVoid/VoidBridge/internal/adb"
	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/config"
	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/peer"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
	"github.com/TheFormOfVoid/VoidBridge/internal/relay"
	"github.com/TheFormOfVoid/VoidBridge/internal/server"
)

// App is bound to the frontend: every exported method is callable from JS as
// window.go.main.App.<Method>(...), returning a promise.
type App struct {
	ctx     context.Context
	cfg     *config.Config
	svc     *Service
	logPath string

	thumbMu sync.Mutex
	thumbs  map[string]string

	emitMu    sync.Mutex
	emitTimer *time.Timer
	onState   func(State) // tray
}

func NewApp(cfg *config.Config, svc *Service, logPath string) *App {
	a := &App{cfg: cfg, svc: svc, logPath: logPath, thumbs: map[string]string{}}
	svc.OnUpdate = a.changed
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.svc.Restart()
}

// changed coalesces bursts of updates into one "state" event.
func (a *App) changed() {
	a.emitMu.Lock()
	defer a.emitMu.Unlock()
	if a.emitTimer != nil {
		return
	}
	a.emitTimer = time.AfterFunc(150*time.Millisecond, func() {
		a.emitMu.Lock()
		a.emitTimer = nil
		a.emitMu.Unlock()
		st := a.State()
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "state", st)
		}
		if a.onState != nil {
			a.onState(st)
		}
	})
}

// ---- state ----

// State is everything the UI shows.
type State struct {
	Version    string        `json:"version"`
	DeviceID   string        `json:"deviceId"`
	DeviceName string        `json:"deviceName"`
	Mode       string        `json:"mode"`
	Code       string        `json:"code"`
	Server     string        `json:"server"`
	Username   string        `json:"username"`
	Admin      bool          `json:"admin"`
	ServerUp   bool          `json:"serverUp"`
	ServerErr  string        `json:"serverErr"`
	SignedOut  bool          `json:"signedOut"`
	Direct     bool          `json:"direct"`
	Tailscale  bool          `json:"tailscale"`
	TSFound    bool          `json:"tailscaleInstalled"`
	Manual     []string      `json:"manual"`
	Paused     bool          `json:"paused"`
	SkipSens   bool          `json:"skipSensitive"`
	History    bool          `json:"history"`
	Autostart  bool          `json:"autostart"`
	Devices    []node.Device `json:"devices"`
	LastSync   int64         `json:"lastSync"`
	PeerErr    string        `json:"peerErr"`
	LocalIPs   []string      `json:"localIPs"`
	Port       int           `json:"port"`
	HistoryLen int           `json:"historyLen"`
}

func (a *App) State() State {
	snap := a.svc.Snapshot()
	st := State{
		Version: version, DeviceID: a.cfg.DeviceID, DeviceName: a.svc.identity().Name,
		Mode: a.cfg.Mode, Code: a.cfg.Code, Server: a.cfg.Server, Username: a.cfg.Username, Admin: a.cfg.Admin,
		ServerUp: snap.Relay.Connected, ServerErr: snap.Relay.Error, SignedOut: snap.SignedOut,
		Direct: a.cfg.Direct, Tailscale: a.cfg.Tailscale, TSFound: peer.TailscaleInstalled(), Manual: a.cfg.Manual,
		Paused: a.cfg.Paused, SkipSens: a.cfg.SkipSensitive, History: a.cfg.History, Autostart: autostartEnabled(),
		Devices: snap.Node.Devices, PeerErr: snap.PeerErr, LocalIPs: snap.LocalIPs, Port: a.cfg.Port,
		HistoryLen: len(a.svc.hist.Items()),
	}
	if st.Devices == nil {
		st.Devices = []node.Device{}
	}
	if st.Manual == nil {
		st.Manual = []string{}
	}
	if !snap.Node.LastSync.IsZero() {
		st.LastSync = snap.Node.LastSync.UnixMilli()
	}
	return st
}

func (a *App) save() error {
	if err := a.cfg.Save(); err != nil {
		return err
	}
	a.changed()
	return nil
}

// ---- joining a group ----

// CreateGroup starts a new serverless group and returns its code.
func (a *App) CreateGroup() (string, error) {
	return a.joinCode(protocol.NewGroupCode())
}

// JoinGroup joins an existing serverless group.
func (a *App) JoinGroup(code string) (string, error) {
	if !protocol.ValidCode(code) {
		return "", errors.New("a sync code has 16 letters and digits, like ABCD-EFGH-2345-WXYZ")
	}
	return a.joinCode(code)
}

func (a *App) joinCode(code string) (string, error) {
	a.leaveServer()
	a.cfg.Leave()
	a.cfg.Mode = config.ModeCode
	a.cfg.Code = protocol.FormatCode(code)
	if err := a.cfg.SetMaster(protocol.MasterFromCode(code)); err != nil {
		return "", err
	}
	if err := a.save(); err != nil {
		return "", err
	}
	a.svc.Restart()
	return a.cfg.Code, nil
}

// ServerInfo checks a server address before signing in.
func (a *App) ServerInfo(addr string) (*server.Info, error) {
	base, err := relay.NormalizeURL(addr)
	if err != nil {
		return nil, err
	}
	return (&relay.API{Base: base}).Info()
}

// SignIn logs in to an account on a server.
func (a *App) SignIn(addr, username, password string) error {
	return a.account(addr, username, password, "", false)
}

// Register creates an account (invite may be empty for the first account).
func (a *App) Register(addr, username, password, invite string) error {
	if len(password) < 8 {
		return errors.New("use a password of at least 8 characters: it also encrypts your clipboard")
	}
	return a.account(addr, username, password, invite, true)
}

func (a *App) account(addr, username, password, invite string, create bool) error {
	base, err := relay.NormalizeURL(addr)
	if err != nil {
		return err
	}
	api := &relay.API{Base: base}
	if _, err := api.Info(); err != nil {
		return err
	}
	username = protocol.NormalizeUsername(username)
	master := protocol.MasterFromAccount(username, password)
	keys := protocol.KeysFromMaster(master)
	var res *server.LoginResult
	if create {
		res, err = api.Register(username, keys, invite, a.svc.identity())
	} else {
		res, err = api.Login(username, keys, a.svc.identity())
	}
	if err != nil {
		return err
	}
	a.leaveServer()
	a.cfg.Leave()
	a.cfg.Mode, a.cfg.Server, a.cfg.Username, a.cfg.Token, a.cfg.Admin = config.ModeAccount, base, res.Username, res.Token, res.Admin
	if err := a.cfg.SetMaster(master); err != nil {
		return err
	}
	if err := a.save(); err != nil {
		return err
	}
	a.svc.Restart()
	return nil
}

func (a *App) api() *relay.API { return &relay.API{Base: a.cfg.Server, Token: a.cfg.Token} }

func (a *App) leaveServer() {
	if a.cfg.Mode == config.ModeAccount && a.cfg.Token != "" {
		a.api().Logout() // best effort
	}
}

// Leave signs out / leaves the group on this device.
func (a *App) Leave() error {
	a.leaveServer()
	a.cfg.Leave()
	if err := a.save(); err != nil {
		return err
	}
	a.svc.Restart()
	return nil
}

// ---- settings ----

// Settings is what the settings UI can change.
type Settings struct {
	DeviceName    string `json:"deviceName"`
	Paused        bool   `json:"paused"`
	SkipSensitive bool   `json:"skipSensitive"`
	History       bool   `json:"history"`
	Autostart     bool   `json:"autostart"`
	Direct        bool   `json:"direct"`
	Tailscale     bool   `json:"tailscale"`
}

func (a *App) SetSettings(s Settings) error {
	restart := s.Direct != a.cfg.Direct || s.Tailscale != a.cfg.Tailscale
	name := strings.TrimSpace(s.DeviceName)
	if host, _ := os.Hostname(); name == host {
		name = ""
	}
	if name != a.cfg.DeviceName {
		a.cfg.DeviceName = name
		restart = true
	}
	a.cfg.Paused, a.cfg.SkipSensitive, a.cfg.History = s.Paused, s.SkipSensitive, s.History
	a.cfg.Direct, a.cfg.Tailscale = s.Direct, s.Tailscale
	if s.Autostart != autostartEnabled() {
		if err := setAutostart(s.Autostart); err != nil {
			return err
		}
	}
	if err := a.save(); err != nil {
		return err
	}
	if restart {
		a.svc.Restart()
	} else {
		a.svc.ApplySettings()
	}
	return nil
}

// SetPaused toggles syncing (also used by the tray).
func (a *App) SetPaused(p bool) error {
	a.cfg.Paused = p
	if err := a.save(); err != nil {
		return err
	}
	a.svc.ApplySettings()
	return nil
}

// SetManualPeers sets addresses of devices to connect to directly.
func (a *App) SetManualPeers(addrs []string) error {
	var clean []string
	for _, s := range addrs {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	a.cfg.Manual = clean
	a.svc.SetManual(clean)
	return a.save()
}

// OpenLog opens the log file.
func (a *App) OpenLog() { openFile(a.logPath) }

// ---- history ----

// HistoryItem is a history entry for the UI.
type HistoryItem struct {
	ID     string `json:"id"`
	Time   int64  `json:"time"`
	From   string `json:"from"`
	Local  bool   `json:"local"`
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Mime   string `json:"mime"`
	Size   int    `json:"size"`
	Thumb  string `json:"thumb,omitempty"` // data: URL
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

func (a *App) History() []HistoryItem {
	out := []HistoryItem{}
	for _, it := range a.svc.hist.Items() {
		h := HistoryItem{ID: it.ID, Time: it.Time.UnixMilli(), From: it.OriginName, Local: it.Local, Type: it.Content.Type, Mime: it.Content.Mime, Size: len(it.Content.Bytes())}
		if h.Local {
			h.From = a.svc.identity().Name
		}
		if it.Content.Type == protocol.ClipText {
			h.Text = truncate(it.Content.Text, 4000)
		} else {
			h.Thumb, h.Width, h.Height = a.thumb(it.ID, it.Content.Data)
		}
		out = append(out, h)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func (a *App) thumb(id string, data []byte) (string, int, int) {
	a.thumbMu.Lock()
	defer a.thumbMu.Unlock()
	if t, ok := a.thumbs[id]; ok {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		return t, cfg.Width, cfg.Height
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	tw, th := w, h
	if tw > 480 {
		tw, th = 480, h*480/w
	}
	if th < 1 {
		th = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	p, err := clipboard.EncodePNG(dst)
	if err != nil {
		return "", 0, 0
	}
	t := "data:image/png;base64," + base64.StdEncoding.EncodeToString(p)
	a.thumbs[id] = t
	if len(a.thumbs) > 200 {
		a.thumbs = map[string]string{id: t}
	}
	return t, w, h
}

// CopyFromHistory puts an old clip back on the clipboard (and syncs it).
func (a *App) CopyFromHistory(id string) error { return a.svc.Recopy(id) }

// ClearHistory forgets all history.
func (a *App) ClearHistory() {
	a.svc.hist.Clear()
	a.changed()
}

// ---- phone setup (adb) ----

// AdbStatus says whether adb is available and lists phones.
type AdbStatus struct {
	Installed bool         `json:"installed"`
	Devices   []adb.Device `json:"devices"`
	Error     string       `json:"error,omitempty"`
}

func (a *App) AdbStatus() AdbStatus {
	st := AdbStatus{Installed: adb.Path() != "", Devices: []adb.Device{}}
	if !st.Installed {
		return st
	}
	d, err := adb.Devices()
	if err != nil {
		st.Error = err.Error()
	} else if d != nil {
		st.Devices = d
	}
	return st
}

func (a *App) AdbInstall() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return adb.Install(ctx)
}

func (a *App) AdbPair(hostPort, code string) error { return adb.Pair(hostPort, code) }
func (a *App) AdbConnect(hostPort string) error    { return adb.Connect(hostPort) }
func (a *App) AdbSetup(serial string) ([]adb.Step, error) {
	steps, err := adb.Setup(serial)
	if err == nil {
		log.Printf("phone setup on %s: %+v", serial, steps)
	}
	return steps, err
}

// ---- server (account devices and admin) ----

func (a *App) needAccount() error {
	if a.cfg.Mode != config.ModeAccount {
		return errors.New("not signed in to a server")
	}
	return nil
}

func (a *App) ServerMe() (*server.Me, error) {
	if err := a.needAccount(); err != nil {
		return nil, err
	}
	me, err := a.api().Me()
	if err == nil && me.Admin != a.cfg.Admin {
		a.cfg.Admin = me.Admin
		a.save()
	}
	return me, err
}

func (a *App) RevokeDevice(id string) error { return a.api().RevokeDevice(id) }

func (a *App) AdminUsers() ([]server.UserView, error) {
	u, err := a.api().Users()
	if u == nil {
		u = []server.UserView{}
	}
	return u, err
}
func (a *App) AdminInvites() ([]server.Invite, error) {
	i, err := a.api().Invites()
	if i == nil {
		i = []server.Invite{}
	}
	return i, err
}
func (a *App) AdminCreateInvite(uses, days int, note string) (*server.Invite, error) {
	return a.api().CreateInvite(uses, days, note)
}
func (a *App) AdminRevokeInvite(code string) error         { return a.api().RevokeInvite(code) }
func (a *App) AdminSetDisabled(name string, d bool) error  { return a.api().SetUserDisabled(name, d) }
func (a *App) AdminSetAdmin(name string, admin bool) error { return a.api().SetUserAdmin(name, admin) }
func (a *App) AdminDeleteUser(name string) error           { return a.api().DeleteUser(name) }

// ---- window ----

// Hide sends the window to the tray.
func (a *App) Hide() { runtime.WindowHide(a.ctx) }

// Show brings the window forward.
func (a *App) Show() {
	if a.ctx == nil {
		return
	}
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowSetAlwaysOnTop(a.ctx, false)
}

// Quit exits the app.
func (a *App) Quit() {
	a.svc.Stop()
	runtime.Quit(a.ctx)
}
