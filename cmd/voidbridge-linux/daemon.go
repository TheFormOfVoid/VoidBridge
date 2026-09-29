//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/config"
	"github.com/TheFormOfVoid/VoidBridge/internal/files"
	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/peer"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
	"github.com/TheFormOfVoid/VoidBridge/internal/relay"
)

// daemon runs sync in the background (normally as a systemd user service)
// and takes commands from the CLI over a Unix socket.
type daemon struct {
	cb       clipboard.Clipboard
	sending  atomic.Int32 // sends in progress
	lastRecv atomic.Int64 // unix time of the last incoming file data

	restartMu sync.Mutex // one restart at a time

	mu        sync.Mutex
	cfg       *config.Config
	stop      chan struct{}
	node      *node.Node
	relay     relay.State
	signedOut bool
	peerErr   string
	update    string // newer version found, if any
}

func socketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	if _, err := os.Stat(dir); err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, fmt.Sprintf("voidbridge-%d.sock", os.Getuid()))
}

func identity(cfg *config.Config) protocol.Identity {
	name := cfg.DeviceName
	if name == "" {
		name, _ = os.Hostname()
	}
	return protocol.Identity{ID: cfg.DeviceID, Name: name, Kind: "linux"}
}

func runDaemon() error {
	d, ln, err := startDaemon()
	if err != nil {
		return err
	}
	defer ln.Close()
	go d.updateLoop()
	return http.Serve(ln, d.handler())
}

// startDaemon starts sync and opens the control socket.
func startDaemon() (*daemon, net.Listener, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	d := &daemon{cfg: cfg, cb: clipboard.System()}
	sock := socketPath()
	if c, err := net.Dial("unix", sock); err == nil {
		c.Close()
		return nil, nil, errors.New("VoidBridge is already running")
	}
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, nil, err
	}
	os.Chmod(sock, 0o600)
	log.Printf("VoidBridge %s starting as %s", version, identity(cfg).Name)
	d.restart()
	return d, ln, nil
}

// shutdown stops sync.
func (d *daemon) shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop != nil {
		close(d.stop)
		d.stop, d.node = nil, nil
	}
}

// restart (re)starts sync from the config file.
// The node calls back into the daemon (which takes d.mu), so d.mu is only
// held while swapping fields, never while setting things up.
func (d *daemon) restart() {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()
	d.shutdown()
	time.Sleep(50 * time.Millisecond) // let listeners release their ports
	cfg, err := config.Load()
	d.mu.Lock()
	if err == nil {
		d.cfg = cfg
	}
	cfg = d.cfg
	d.relay, d.signedOut, d.peerErr = relay.State{}, false, ""
	d.mu.Unlock()
	go d.syncMenu()
	keys, ok := cfg.Keys()
	if !ok {
		log.Printf("not set up yet: run `voidbridge login` or `voidbridge join`")
		return
	}
	stop := make(chan struct{})
	n := node.New(identity(cfg), keys, d.cb, nil)
	n.OnChange = func() { d.remember(n.Status().Devices) }
	n.SetFileReceiver(d.receiver())
	n.SetSettings(node.Settings{Paused: cfg.Paused, SkipSensitive: cfg.SkipSensitive})
	d.mu.Lock()
	d.stop, d.node = stop, n
	d.mu.Unlock()
	go n.Run(stop)

	if cfg.Direct {
		m := peer.NewManager(n, keys, cfg.Port)
		m.SetManual(cfg.Manual)
		if cfg.Tailscale {
			m.Tailscale = peer.TailscalePeers
		}
		go func() {
			if err := m.Run(stop); err != nil {
				log.Printf("direct links: %v", err)
				d.mu.Lock()
				d.peerErr = fmt.Sprintf("can't listen on port %d: %v", cfg.Port, err)
				d.mu.Unlock()
			}
		}()
	}
	if cfg.Mode == config.ModeAccount && cfg.Token != "" {
		c := &relay.Client{Base: cfg.Server, Token: cfg.Token, Node: n}
		c.OnState = func(st relay.State) {
			d.mu.Lock()
			d.relay = st
			d.mu.Unlock()
		}
		c.OnUnauthorized = func() {
			d.mu.Lock()
			d.signedOut = true
			d.mu.Unlock()
			log.Printf("signed out by the server: run `voidbridge login` again")
		}
		go c.Run(stop)
	}
}

func (d *daemon) config() *config.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

// remember records connected devices for the right-click menu.
func (d *daemon) remember(devs []node.Device) {
	cfg := d.config()
	now := time.Now().Unix()
	menu, dirty := false, false
	for _, dev := range devs {
		m, dd := cfg.Remember(dev.ID, dev.Name, dev.Kind, now)
		menu, dirty = menu || m, dirty || dd
	}
	if cfg.ForgetOlderThan(now - 60*24*3600) {
		menu, dirty = true, true
	}
	if dirty {
		cfg.Save()
	}
	if menu {
		d.syncMenu()
	}
}

func (d *daemon) syncMenu() {
	cfg := d.config()
	var devs []menuDevice
	for id, k := range cfg.KnownDevices() {
		devs = append(devs, menuDevice{ID: id, Name: k.Name, Kind: k.Kind})
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].Name < devs[j].Name })
	_, set := cfg.Keys()
	if err := writeMenu(!set || cfg.HideContextMenu, devs); err != nil {
		log.Printf("file manager menu: %v", err)
	}
}

func (d *daemon) receiver() node.FileReceiver {
	return &files.DirReceiver{
		Dir: func() string { return d.config().ReceiveFolder() },
		OnProgress: func(string, protocol.FileMeta, node.FileSender, int64) {
			d.lastRecv.Store(time.Now().Unix())
		},
		OnReceived: func(e files.Event) {
			if e.Err != nil {
				log.Printf("receiving %s from %s: %v", e.Meta.Name, e.From.Name, e.Err)
				notify("Couldn't receive "+e.Meta.Name, e.Err.Error())
				return
			}
			log.Printf("received %s from %s", e.Path, e.From.Name)
			notify(fmt.Sprintf("%s from %s", filepath.Base(e.Path), e.From.Name), "Saved to "+filepath.Dir(e.Path))
		},
	}
}

// sendFiles sends local files to a device, waiting a little for it to connect.
func (d *daemon) sendFiles(deviceID string, paths []string) error {
	name := deviceID
	if k, ok := d.config().KnownDevices()[deviceID]; ok {
		name = k.Name
	}
	var n *node.Node
	ready := false
	for i := 0; i < 40 && !ready; i++ { // up to 20 s
		d.mu.Lock()
		n = d.node
		d.mu.Unlock()
		if n != nil {
			for _, dev := range n.Status().Devices {
				if dev.ID == deviceID {
					ready = true
				}
			}
		}
		if !ready {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if n == nil {
		return errors.New("VoidBridge isn't set up yet")
	}
	if !ready {
		return fmt.Errorf("%s isn't connected right now", name)
	}
	d.sending.Add(1)
	defer d.sending.Add(-1)
	var firstErr error
	for _, p := range paths {
		err := files.Send(n, nil, deviceID, p, nil)
		if err != nil {
			log.Printf("sending %s to %s: %v", p, name, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("couldn't send %s to %s: %w", filepath.Base(p), name, err)
			}
			continue
		}
		log.Printf("sent %s to %s", p, name)
	}
	return firstErr
}

// busy reports whether files are being sent or received.
func (d *daemon) busy() bool {
	return d.sending.Load() > 0 || time.Now().Unix()-d.lastRecv.Load() < 120
}

// ---- control socket ----

// Status is what `voidbridge status` shows.
type Status struct {
	Version   string        `json:"version"`
	Name      string        `json:"name"`
	Mode      string        `json:"mode"`
	Server    string        `json:"server,omitempty"`
	Username  string        `json:"username,omitempty"`
	Code      string        `json:"code,omitempty"`
	ServerUp  bool          `json:"server_up"`
	ServerErr string        `json:"server_err,omitempty"`
	SignedOut bool          `json:"signed_out,omitempty"`
	PeerErr   string        `json:"peer_err,omitempty"`
	Clipboard string        `json:"clipboard"`
	Devices   []node.Device `json:"devices"`
	Known     []knownDevice `json:"known"`
	Update    string        `json:"update,omitempty"`
}

type knownDevice struct {
	ID, Name, Kind string
	LastSeen       int64
}

func (d *daemon) status() Status {
	d.mu.Lock()
	cfg, n := d.cfg, d.node
	st := Status{
		Version: version, Name: identity(cfg).Name, Mode: cfg.Mode, Server: cfg.Server, Username: cfg.Username, Code: cfg.Code,
		ServerUp: d.relay.Connected, ServerErr: d.relay.Error, SignedOut: d.signedOut, PeerErr: d.peerErr, Update: d.update,
		Devices: []node.Device{},
	}
	d.mu.Unlock()
	if n != nil {
		st.Devices = n.Status().Devices
	}
	st.Clipboard = "ok"
	if _, err := d.cb.Seq(); err != nil {
		st.Clipboard = err.Error()
	}
	for id, k := range cfg.KnownDevices() {
		st.Known = append(st.Known, knownDevice{id, k.Name, k.Kind, k.LastSeen})
	}
	sort.Slice(st.Known, func(i, j int) bool { return st.Known[i].LastSeen > st.Known[j].LastSeen })
	return st
}

type sendRequest struct {
	Device string   `json:"device"`
	Paths  []string `json:"paths"`
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(v)
}

func (d *daemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, d.status(), nil)
	})
	mux.HandleFunc("POST /reload", func(w http.ResponseWriter, r *http.Request) {
		d.restart()
		reply(w, map[string]string{}, nil)
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		var req sendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, map[string]string{}, d.sendFiles(req.Device, req.Paths))
	})
	mux.HandleFunc("POST /update", func(w http.ResponseWriter, r *http.Request) {
		msg, err := d.updateNow()
		reply(w, map[string]string{"message": msg}, err)
	})
	return mux
}
