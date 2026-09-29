//go:build linux

package clipboard

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// On Linux the clipboard belongs to the desktop session, so VoidBridge talks
// to it through wl-clipboard (Wayland, the default on Raspberry Pi OS) or
// xclip (X11). It may start before anyone has logged in to the desktop, so it
// keeps looking for a session until one appears.

// System returns the desktop clipboard, found lazily.
func System() Clipboard { return &linuxClipboard{} }

// WriteFiles is a no-op on Linux; received files are announced by
// notification instead.
func WriteFiles(paths []string) error { return nil }

// passwordHint is the type KeePassXC and KDE apps add to secrets.
const passwordHint = "x-kde-passwordManagerHint"

type backend interface {
	Clipboard
	name() string
	alive() bool
}

type linuxClipboard struct {
	mu       sync.Mutex
	b        backend
	lastLook time.Time
	offset   uint64 // keeps Seq increasing across backends
}

func (l *linuxClipboard) get() backend {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b != nil && !l.b.alive() {
		log.Printf("clipboard: lost the %s session", l.b.name())
		if s, err := l.b.Seq(); err == nil {
			l.offset += s + 1
		}
		l.b = nil
	}
	if l.b == nil && time.Since(l.lastLook) > 5*time.Second {
		l.lastLook = time.Now()
		if b := detect(); b != nil {
			log.Printf("clipboard: using %s", b.name())
			l.b = b
		}
	}
	return l.b
}

var errNoDesktop = errors.New("no desktop session to use the clipboard of")

func (l *linuxClipboard) Read() (*Content, error) {
	if b := l.get(); b != nil {
		return b.Read()
	}
	return nil, errNoDesktop
}

func (l *linuxClipboard) Write(c *Content) error {
	if b := l.get(); b != nil {
		return b.Write(c)
	}
	return errNoDesktop
}

func (l *linuxClipboard) Seq() (uint64, error) {
	b := l.get()
	if b == nil {
		return 0, errNoDesktop
	}
	s, err := b.Seq()
	l.mu.Lock()
	defer l.mu.Unlock()
	return s + l.offset, err
}

// ---- finding the session ----

func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return fmt.Sprintf("/run/user/%d", os.Getuid())
}

// waylandEnv returns the environment for wl-clipboard, or nil without a
// Wayland session.
func waylandEnv() []string {
	dir := runtimeDir()
	display := os.Getenv("WAYLAND_DISPLAY")
	if display == "" || !exists(filepath.Join(dir, display)) {
		display = ""
		socks, _ := filepath.Glob(filepath.Join(dir, "wayland-*"))
		for _, s := range socks {
			if !strings.HasSuffix(s, ".lock") {
				display = filepath.Base(s)
				break
			}
		}
	}
	if display == "" {
		return nil
	}
	return append(os.Environ(), "WAYLAND_DISPLAY="+display, "XDG_RUNTIME_DIR="+dir)
}

// x11Env returns the environment for xclip, or nil without an X session.
func x11Env() []string {
	display := os.Getenv("DISPLAY")
	if display == "" {
		if !exists("/tmp/.X11-unix/X0") {
			return nil
		}
		display = ":0"
	}
	env := append(os.Environ(), "DISPLAY="+display)
	if os.Getenv("XAUTHORITY") == "" {
		if home, err := os.UserHomeDir(); err == nil && exists(filepath.Join(home, ".Xauthority")) {
			env = append(env, "XAUTHORITY="+filepath.Join(home, ".Xauthority"))
		}
	}
	return env
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func have(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

var warned sync.Map

func warnOnce(msg string) {
	if _, dup := warned.LoadOrStore(msg, true); !dup {
		log.Print(msg)
	}
}

func detect() backend {
	if env := waylandEnv(); env != nil {
		if have("wl-paste") && have("wl-copy") {
			return newWayland(env)
		}
		warnOnce("clipboard: this is a Wayland desktop; install wl-clipboard (sudo apt install wl-clipboard)")
	}
	if env := x11Env(); env != nil {
		if have("xclip") {
			return newPolled(&xclip{env: env})
		}
		warnOnce("clipboard: this is an X11 desktop; install xclip (sudo apt install xclip)")
	}
	return nil
}

// ---- reading and writing through a command-line tool ----

type tool interface {
	name() string
	types() ([]string, error)
	read(typ string) ([]byte, error)
	write(typ string, data []byte) error
	alive() bool
}

func run(env []string, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("%s: %v %s", name, err, strings.TrimSpace(errb.String()))
		}
		return out.Bytes(), nil
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		return nil, fmt.Errorf("%s timed out", name)
	}
}

var textTypes = []string{"text/plain;charset=utf-8", "UTF8_STRING", "text/plain", "STRING", "TEXT"}

// readWith reads the clipboard through t: a PNG image if there is one,
// otherwise text.
func readWith(t tool) (*Content, error) {
	types, err := t.types()
	if err != nil || len(types) == 0 {
		return nil, nil // empty clipboard
	}
	sensitive := false
	if slices.Contains(types, passwordHint) {
		if b, err := t.read(passwordHint); err == nil && strings.TrimSpace(string(b)) == "secret" {
			sensitive = true
		}
	}
	if slices.Contains(types, "image/png") {
		b, err := t.read("image/png")
		if err != nil || len(b) == 0 {
			return nil, err
		}
		return &Content{Type: protocol.ClipImage, Data: b, Mime: "image/png", Sensitive: sensitive}, nil
	}
	for _, typ := range textTypes {
		if slices.Contains(types, typ) {
			b, err := t.read(typ)
			if err != nil || len(b) == 0 {
				return nil, err
			}
			return &Content{Type: protocol.ClipText, Text: string(b), Mime: "text/plain", Sensitive: sensitive}, nil
		}
	}
	return nil, nil
}

func writeWith(t tool, c *Content) error {
	if c.Type == protocol.ClipImage {
		mime := c.Mime
		if mime == "" {
			mime = "image/png"
		}
		return t.write(mime, c.Data)
	}
	return t.write("text/plain;charset=utf-8", []byte(c.Text))
}

// ---- Wayland ----

type wlTool struct{ env []string }

func (w *wlTool) name() string { return "Wayland (wl-clipboard)" }
func (w *wlTool) alive() bool  { return waylandEnv() != nil }

func (w *wlTool) types() ([]string, error) {
	out, err := run(w.env, nil, "wl-paste", "--list-types")
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

func (w *wlTool) read(typ string) ([]byte, error) {
	return run(w.env, nil, "wl-paste", "--no-newline", "--type", typ)
}

func (w *wlTool) write(typ string, data []byte) error {
	_, err := run(w.env, data, "wl-copy", "--type", typ)
	return err
}

// wayland counts changes reported by `wl-paste --watch`, falling back to
// polling on compositors without the data-control protocol.
type wayland struct {
	*wlTool
	seq    atomic.Uint64
	polled atomic.Pointer[polled] // set if watching turned out not to work
	dead   atomic.Bool
}

func newWayland(env []string) backend {
	w := &wayland{wlTool: &wlTool{env: env}}
	cmd := exec.Command("wl-paste", "--watch", "echo")
	cmd.Env = env
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		log.Printf("clipboard: wl-paste --watch failed (%v); checking every second instead", err)
		return newPolled(w.wlTool)
	}
	started := time.Now()
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			w.seq.Add(1)
		}
		cmd.Wait()
		if time.Since(started) < 2*time.Second {
			log.Printf("clipboard: this desktop doesn't support watching the clipboard; checking every second instead")
			w.polled.Store(newPolled(w.wlTool))
			return
		}
		w.dead.Store(true) // the session ended; look for a new one
	}()
	return w
}

func (w *wayland) alive() bool { return !w.dead.Load() && w.wlTool.alive() }

func (w *wayland) Read() (*Content, error) { return readWith(w.wlTool) }
func (w *wayland) Write(c *Content) error  { return writeWith(w.wlTool, c) }

func (w *wayland) Seq() (uint64, error) {
	if p := w.polled.Load(); p != nil {
		return p.Seq()
	}
	return w.seq.Load(), nil
}

// ---- X11 ----

type xclip struct{ env []string }

func (x *xclip) name() string { return "X11 (xclip)" }
func (x *xclip) alive() bool  { return x11Env() != nil }

func (x *xclip) types() ([]string, error) {
	out, err := run(x.env, nil, "xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

func (x *xclip) read(typ string) ([]byte, error) {
	return run(x.env, nil, "xclip", "-selection", "clipboard", "-o", "-t", typ)
}

func (x *xclip) write(typ string, data []byte) error {
	_, err := run(x.env, data, "xclip", "-selection", "clipboard", "-t", typ, "-i")
	return err
}

// ---- polling, where changes can't be watched ----

type polled struct {
	t tool

	mu    sync.Mutex
	seq   uint64
	last  string
	check time.Time
}

func newPolled(t tool) *polled { return &polled{t: t} }

func (p *polled) name() string            { return p.t.name() }
func (p *polled) alive() bool             { return p.t.alive() }
func (p *polled) Read() (*Content, error) { return readWith(p.t) }
func (p *polled) Write(c *Content) error  { return writeWith(p.t, c) }

// Seq reads the clipboard at most once a second and counts changes.
func (p *polled) Seq() (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.check) < time.Second {
		return p.seq, nil
	}
	p.check = time.Now()
	c, err := readWith(p.t)
	if err != nil {
		return p.seq, err
	}
	h := ""
	if c != nil {
		h = c.Hash()
	}
	if h != p.last {
		p.last = h
		p.seq++
	}
	return p.seq, nil
}
