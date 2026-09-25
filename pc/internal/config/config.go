// Package config loads and saves the PC app's settings.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/TheFormOfVoid/VoidBridge/pc/internal/protocol"
)

const DefaultPort = 47829

// Config is persisted as JSON in the user's config directory.
type Config struct {
	DeviceID    string `json:"device_id"`
	PairingCode string `json:"pairing_code"`
	Port        int    `json:"port"`
}

// Dir returns (and creates) the VoidBridge settings directory,
// %APPDATA%\VoidBridge on Windows.
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
	return filepath.Join(d, "config.json"), nil
}

// Load reads the config, filling in and saving defaults for anything missing.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, c)
	}
	dirty := false
	if c.DeviceID == "" {
		b := make([]byte, 8)
		rand.Read(b)
		c.DeviceID = "pc-" + hex.EncodeToString(b)
		dirty = true
	}
	if !protocol.ValidCode(c.PairingCode) {
		c.PairingCode = protocol.NewPairingCode()
		dirty = true
	}
	if c.Port == 0 {
		c.Port = DefaultPort
		dirty = true
	}
	if dirty {
		return c, c.Save()
	}
	return c, nil
}

// Save writes the config atomically.
func (c *Config) Save() error {
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
