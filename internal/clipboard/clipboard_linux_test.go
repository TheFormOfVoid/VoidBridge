//go:build linux

package clipboard

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// Fake wl-clipboard: each type's data is a file in $FAKE_CB; wl-copy marks a
// change that `wl-paste --watch` reports (unless FAKE_NOWATCH is set).
const fakeWlCopy = `#!/bin/sh
[ "$1" = "--type" ] || exit 2
rm -f "$FAKE_CB"/t_*
f="$FAKE_CB/t_$(printf %s "$2" | tr '/;=' '___')"
cat > "$f"
printf %s "$2" > "$f.name"
touch "$FAKE_CB/changed"
`

const fakeWlPaste = `#!/bin/sh
case "$1" in
--list-types)
	for n in "$FAKE_CB"/t_*.name; do [ -f "$n" ] && { cat "$n"; echo; }; done ;;
--no-newline)
	f="$FAKE_CB/t_$(printf %s "$3" | tr '/;=' '___')"
	[ -f "$f" ] && cat "$f" || exit 1 ;;
--watch)
	[ -n "$FAKE_NOWATCH" ] && exit 1
	echo
	while true; do
		if [ -f "$FAKE_CB/changed" ]; then rm -f "$FAKE_CB/changed"; echo; fi
		sleep 0.02
	done ;;
esac
`

func fakeWayland(t *testing.T, watch bool) string {
	t.Helper()
	bin, store, run := t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(bin, "wl-copy"), []byte(fakeWlCopy), 0o755)
	os.WriteFile(filepath.Join(bin, "wl-paste"), []byte(fakeWlPaste), 0o755)
	os.WriteFile(filepath.Join(run, "wayland-1"), nil, 0o600) // stands in for the socket
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", run)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("FAKE_CB", store)
	if !watch {
		t.Setenv("FAKE_NOWATCH", "1")
	}
	return store
}

func waitChange(t *testing.T, cb Clipboard, from uint64) uint64 {
	t.Helper()
	for i := 0; i < 300; i++ {
		if s, err := cb.Seq(); err == nil && s != from {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("clipboard change never noticed")
	return 0
}

func testRoundTrip(t *testing.T, watch bool) {
	store := fakeWayland(t, watch)
	cb := System()
	seq, err := cb.Seq()
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := cb.Read(); c != nil {
		t.Fatalf("empty clipboard read as %+v", c)
	}

	if err := cb.Write(&Content{Type: protocol.ClipText, Text: "héllo\nworld"}); err != nil {
		t.Fatal(err)
	}
	seq = waitChange(t, cb, seq)
	c, err := cb.Read()
	if err != nil || c == nil || c.Type != protocol.ClipText || c.Text != "héllo\nworld" {
		t.Fatalf("read %+v, %v", c, err)
	}

	png := []byte("\x89PNG\r\n\x1a\nfake image")
	if err := cb.Write(&Content{Type: protocol.ClipImage, Data: png, Mime: "image/png"}); err != nil {
		t.Fatal(err)
	}
	seq = waitChange(t, cb, seq)
	c, _ = cb.Read()
	if c == nil || c.Type != protocol.ClipImage || !bytes.Equal(c.Data, png) {
		t.Fatalf("image read as %+v", c)
	}

	// A password manager's copy is marked sensitive.
	cb.Write(&Content{Type: protocol.ClipText, Text: "hunter2"})
	os.WriteFile(filepath.Join(store, "t_x-kde-passwordManagerHint"), []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(store, "t_x-kde-passwordManagerHint.name"), []byte(passwordHint), 0o600)
	waitChange(t, cb, seq)
	c, _ = cb.Read()
	if c == nil || c.Text != "hunter2" || !c.Sensitive {
		t.Fatalf("password read as %+v", c)
	}
}

func TestWaylandWatch(t *testing.T)  { testRoundTrip(t, true) }
func TestWaylandPolled(t *testing.T) { testRoundTrip(t, false) }

func TestNoDesktop(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	if exists("/tmp/.X11-unix/X0") {
		t.Skip("this machine has an X display")
	}
	cb := System()
	if _, err := cb.Seq(); err == nil {
		t.Fatal("expected an error without a desktop session")
	}
}
