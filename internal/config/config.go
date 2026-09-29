// Package config loads and saves the desktop app's settings.
package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// Group modes.
const (
	ModeNone    = ""
	ModeCode    = "code"    // serverless group joined with a code
	ModeAccount = "account" // account on a VoidBridge server
)

// Config is persisted as JSON in %APPDATA%\VoidBridge.
type Config struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name,omitempty"` // empty = computer name

	Mode string `json:"mode"`
	Code string `json:"code,omitempty"` // ModeCode: shown so more devices can join
	// The group master key, protected with Windows DPAPI where available.
	MasterKey string `json:"master_key,omitempty"`

	Server   string `json:"server,omitempty"` // ModeAccount
	Username string `json:"username,omitempty"`
	Token    string `json:"token,omitempty"`
	Admin    bool   `json:"admin,omitempty"`

	Direct    bool     `json:"direct"`    // link to devices on Wi-Fi / Tailscale directly
	Tailscale bool     `json:"tailscale"` // look for devices among Tailscale peers
	Manual    []string `json:"manual,omitempty"`

	Paused        bool `json:"paused"`
	SkipSensitive bool `json:"skip_sensitive"`
	History       bool `json:"history"`
	Port          int  `json:"port"`

	// Files
	ReceiveDir      string                 `json:"receive_dir,omitempty"` // empty: Downloads\VoidBridge
	HideContextMenu bool                   `json:"hide_context_menu,omitempty"`
	NoClipReceived  bool                   `json:"no_clip_received,omitempty"` // don't put received files on the clipboard
	Known           map[string]KnownDevice `json:"known,omitempty"`            // devices seen, for the right-click menu

	mu sync.Mutex
}

// KnownDevice is a device this PC has seen, offered in "Send with VoidBridge".
type KnownDevice struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	LastSeen int64  `json:"last_seen"` // unix seconds
}

// ReceiveFolder returns where received files go.
func (c *Config) ReceiveFolder() string {
	if c.ReceiveDir != "" {
		return c.ReceiveDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Downloads", "VoidBridge")
}

// Dir returns (and creates) the settings directory.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, "VoidBridge")
	return d, os.MkdirAll(d, 0o700)
}

func path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "settings.json"), nil
}

// Load reads the config, filling in defaults.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	c := &Config{Direct: true, Tailscale: true, SkipSensitive: true, History: true}
	b, err := os.ReadFile(p)
	fresh := os.IsNotExist(err)
	if err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, err
		}
	}
	if c.DeviceID == "" {
		c.DeviceID = "pc-" + protocol.NewID()
	}
	if c.Port == 0 {
		c.Port = protocol.PeerPort
	}
	if fresh {
		return c, c.Save()
	}
	return c, nil
}

// Save writes the config atomically.
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := path()
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Keys returns the group keys, or false if not in a group.
func (c *Config) Keys() (protocol.Keys, bool) {
	if c.Mode == ModeNone || c.MasterKey == "" {
		return protocol.Keys{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(c.MasterKey)
	if err != nil {
		return protocol.Keys{}, false
	}
	master, err := unprotect(raw)
	if err != nil || len(master) != 32 {
		return protocol.Keys{}, false
	}
	return protocol.KeysFromMaster(master), true
}

// SetMaster stores the master key.
func (c *Config) SetMaster(master []byte) error {
	p, err := protect(master)
	if err != nil {
		return err
	}
	c.MasterKey = base64.StdEncoding.EncodeToString(p)
	return nil
}

// Leave forgets the group.
func (c *Config) Leave() {
	c.Mode, c.Code, c.MasterKey = ModeNone, "", ""
	c.Server, c.Username, c.Token, c.Admin = "", "", "", false
}

// Remember records a device as seen now. It reports whether the menu needs
// updating (new device or new name). Call Save afterwards.
func (c *Config) Remember(id, name, kind string, now int64) (menuChanged, dirty bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Known == nil {
		c.Known = map[string]KnownDevice{}
	}
	old, ok := c.Known[id]
	if ok && old.Name == name && old.Kind == kind && now-old.LastSeen < 3600 {
		return false, false
	}
	c.Known[id] = KnownDevice{Name: name, Kind: kind, LastSeen: now}
	return !ok || old.Name != name, true
}

// ForgetOlderThan drops devices not seen since cutoff; reports if any were.
func (c *Config) ForgetOlderThan(cutoff int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	gone := false
	for id, d := range c.Known {
		if d.LastSeen < cutoff {
			delete(c.Known, id)
			gone = true
		}
	}
	return gone
}

// Forget removes one device.
func (c *Config) Forget(id string) {
	c.mu.Lock()
	delete(c.Known, id)
	c.mu.Unlock()
}

// KnownDevices returns a copy of the remembered devices.
func (c *Config) KnownDevices() map[string]KnownDevice {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]KnownDevice, len(c.Known))
	for id, d := range c.Known {
		out[id] = d
	}
	return out
}
