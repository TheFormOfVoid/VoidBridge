//go:build linux

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/config"
	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
	"github.com/TheFormOfVoid/VoidBridge/internal/relay"
	"github.com/TheFormOfVoid/VoidBridge/internal/server"
)

func quiet(string, ...any) {}

// A fake wl-clipboard (see internal/clipboard's tests for a fuller one).
const fakeWlCopy = `#!/bin/sh
cat > "$FAKE_CB/data"; printf %s "$2" > "$FAKE_CB/type"; touch "$FAKE_CB/changed"
`
const fakeWlPaste = `#!/bin/sh
case "$1" in
--list-types) [ -f "$FAKE_CB/type" ] && { cat "$FAKE_CB/type"; echo; } ;;
--no-newline) cat "$FAKE_CB/data" ;;
--watch) echo; while true; do [ -f "$FAKE_CB/changed" ] && { rm -f "$FAKE_CB/changed"; echo; }; sleep 0.02; done ;;
esac
`

type memFiles struct {
	mu  sync.Mutex
	got map[string][]byte
}

type memWriter struct {
	bytes.Buffer
	name string
	f    *memFiles
}

func (f *memFiles) Begin(m protocol.FileMeta, _ node.FileSender) (node.FileWriter, error) {
	return &memWriter{name: m.Name, f: f}, nil
}
func (w *memWriter) Write(p []byte) error { _, err := w.Buffer.Write(p); return err }
func (w *memWriter) Abort()               {}
func (w *memWriter) Commit() (string, error) {
	w.f.mu.Lock()
	w.f.got[w.name] = w.Bytes()
	w.f.mu.Unlock()
	return "memory", nil
}
func (f *memFiles) Received(protocol.FileMeta, node.FileSender, string, error) {}
func (f *memFiles) Progress(string, protocol.FileMeta, node.FileSender, int64) {}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never happened", what)
}

func TestLinuxClient(t *testing.T) {
	// A desktop session with a fake clipboard, and private dirs for everything.
	bin, cb, run, home := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(bin, "wl-copy"), []byte(fakeWlCopy), 0o755)
	os.WriteFile(filepath.Join(bin, "wl-paste"), []byte(fakeWlPaste), 0o755)
	os.WriteFile(filepath.Join(run, "wayland-0"), nil, 0o600)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CB", cb)
	t.Setenv("XDG_RUNTIME_DIR", run)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	// A server with an account, and a "phone" signed in to it.
	st, err := server.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	srv := server.New(st, server.SignupInvite)
	srv.Logf = quiet
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	keys := protocol.KeysFromMaster(protocol.MasterFromAccount("pi-user", "password1"))
	phoneID := protocol.Identity{ID: "phone-1", Name: "Pixel", Kind: "android"}
	api := &relay.API{Base: ts.URL}
	if _, err := api.Register("pi-user", keys, "", phoneID); err != nil {
		t.Fatal(err)
	}
	phoneCB := &clipboard.Memory{}
	phoneFiles := &memFiles{got: map[string][]byte{}}
	phone := node.New(phoneID, keys, phoneCB, nil)
	phone.Logf = quiet
	phone.SetFileReceiver(phoneFiles)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go phone.Run(stop)
	go (&relay.Client{Base: ts.URL, Token: api.Token, Node: phone, Logf: quiet}).Run(stop)

	// This computer, signed in to the same account (as `voidbridge login` does).
	cfg, _ := config.Load()
	login, err := (&relay.API{Base: ts.URL}).Login("pi-user", keys, identity(cfg))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Mode, cfg.Server, cfg.Username, cfg.Token = config.ModeAccount, ts.URL, "pi-user", login.Token
	cfg.Direct = false
	cfg.ReceiveDir = filepath.Join(home, "Received")
	cfg.SetMaster(protocol.MasterFromAccount("pi-user", "password1"))
	cfg.Save()

	d, ln, err := startDaemon()
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, d.handler())
	t.Cleanup(func() { ln.Close(); d.shutdown() })

	// Status over the control socket, as the CLI sees it.
	eventually(t, "phone visible", func() bool {
		s, err := getStatus()
		return err == nil && len(s.Devices) == 1 && s.Devices[0].Name == "Pixel" && s.ServerUp
	})
	s, _ := getStatus()
	if s.Clipboard != "ok" {
		t.Fatalf("clipboard: %s", s.Clipboard)
	}
	if id, err := resolveDevice(s, "pixel"); err != nil || id != "phone-1" {
		t.Fatalf("resolve by name: %q %v", id, err)
	}

	// The file manager menu lists the phone.
	actions := filepath.Join(home, ".local", "share", "file-manager", "actions")
	eventually(t, "menu written", func() bool {
		b, _ := os.ReadFile(filepath.Join(actions, "voidbridge-send-00.desktop"))
		return strings.Contains(string(b), "Name=Pixel 📱") && strings.Contains(string(b), `send "phone-1" %F`)
	})
	menu, _ := os.ReadFile(filepath.Join(actions, "voidbridge.desktop"))
	if !strings.Contains(string(menu), "ItemsList=voidbridge-send-00;") {
		t.Fatalf("menu: %s", menu)
	}

	// Clipboard both ways.
	time.Sleep(300 * time.Millisecond) // let the clipboard watcher start
	phone.LocalCopy(&clipboard.Content{Type: protocol.ClipText, Text: "from the phone", Mime: "text/plain"})
	eventually(t, "clip on the Pi", func() bool {
		b, _ := os.ReadFile(filepath.Join(cb, "data"))
		return string(b) == "from the phone"
	})
	wlCopy := func(text string) {
		os.WriteFile(filepath.Join(cb, "data"), []byte(text), 0o600)
		os.WriteFile(filepath.Join(cb, "type"), []byte("text/plain;charset=utf-8"), 0o600)
		os.WriteFile(filepath.Join(cb, "changed"), nil, 0o600)
	}
	wlCopy("copied on the Pi")
	eventually(t, "clip on the phone", func() bool {
		c, _ := phoneCB.Read()
		return c != nil && c.Text == "copied on the Pi"
	})

	// Sending a file, as `voidbridge send Pixel FILE` does.
	doc := filepath.Join(home, "notes.txt")
	os.WriteFile(doc, []byte("hello phone"), 0o644)
	if err := send([]string{"Pixel", doc}); err != nil {
		t.Fatal(err)
	}
	phoneFiles.mu.Lock()
	got := string(phoneFiles.got["notes.txt"])
	phoneFiles.mu.Unlock()
	if got != "hello phone" {
		t.Fatalf("phone got %q", got)
	}

	// Receiving a file from the phone into the chosen folder.
	err = phone.SendFile(context.Background(), cfg.DeviceID, protocol.FileMeta{Name: "photo.jpg", Size: 5}, bytes.NewReader([]byte("jpeg!")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "Received", "photo.jpg")); err != nil || string(b) != "jpeg!" {
		t.Fatalf("received %q, %v", b, err)
	}

	// Turning the menu off removes it.
	c2, _ := config.Load()
	c2.HideContextMenu = true
	c2.Save()
	if err := call("POST", "/reload", nil, nil, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	eventually(t, "menu removed", func() bool {
		left, _ := filepath.Glob(filepath.Join(actions, "voidbridge*"))
		return len(left) == 0
	})
}

func TestQuoteExec(t *testing.T) {
	if q, ok := quoteExec("/home/pi/.local/bin/voidbridge"); !ok || q != `"/home/pi/.local/bin/voidbridge"` {
		t.Fatal(q)
	}
	for _, bad := range []string{`a"b`, "a$b", "a`b", `a\b`, "a%b", "a\nb"} {
		if _, ok := quoteExec(bad); ok {
			t.Errorf("quoteExec accepted %q", bad)
		}
	}
}
