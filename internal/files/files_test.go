package files

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/peer"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"report.pdf":            "report.pdf",
		"../../etc/passwd":      "passwd",
		`..\..\Windows\win.ini`: "win.ini",
		"CON.txt":               "_CON.txt",
		"a<b>c:d|e?f*.txt":      "a_b_c_d_e_f_.txt",
		"   ":                   "file",
		".hidden":               "hidden",
		"trailing. ":            "trailing",
		"":                      "file",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

type dev struct {
	n    *node.Node
	m    *peer.Manager
	dir  string
	got  chan Event
	addr string
}

var keys = protocol.KeysFromMaster(protocol.MasterFromCode("AAAA-BBBB-CCCC-DDDD"))

func newDev(t *testing.T, name string) *dev {
	t.Helper()
	d := &dev{dir: t.TempDir(), got: make(chan Event, 8)}
	d.n = node.New(protocol.Identity{ID: "id-" + name, Name: name, Kind: "windows"}, keys, &clipboard.Memory{}, nil)
	d.n.Logf = func(string, ...any) {}
	d.n.SetFileReceiver(&DirReceiver{Dir: func() string { return d.dir }, OnReceived: func(e Event) { d.got <- e }})
	d.m = peer.NewManager(d.n, keys, 0)
	d.m.Discovery = false
	d.m.Logf = func(string, ...any) {}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go d.n.Run(stop)
	go d.m.Run(stop)
	for d.m.Addr() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	d.addr = fmt.Sprintf("127.0.0.1:%d", d.m.Addr().(*net.TCPAddr).Port)
	return d
}

func linked(t *testing.T) (*dev, *dev) {
	a, b := newDev(t, "a"), newDev(t, "b")
	a.m.SetManual([]string{b.addr})
	for i := 0; len(a.n.Status().Devices) == 0 || len(b.n.Status().Devices) == 0; i++ {
		if i > 500 {
			t.Fatal("devices never linked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return a, b
}

func writeFile(t *testing.T, name string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, data
}

func TestSendFileDirect(t *testing.T) {
	a, b := linked(t)
	path, data := writeFile(t, "big report.pdf", 5<<20+123)
	var lastProgress int64
	if err := Send(a.n, nil, "id-b", path, func(sent, total int64) { lastProgress = sent }); err != nil {
		t.Fatal(err)
	}
	if lastProgress != int64(len(data)) {
		t.Fatalf("progress ended at %d", lastProgress)
	}
	e := <-b.got
	if e.Err != nil || e.From.Name != "a" {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(e.Path)
	if !bytes.Equal(got, data) || filepath.Base(e.Path) != "big report.pdf" {
		t.Fatalf("wrong file at %s", e.Path)
	}

	// Same name again: saved alongside, not overwritten. Empty files work too.
	empty, _ := writeFile(t, "big report.pdf", 0)
	if err := Send(a.n, nil, "id-b", empty, nil); err != nil {
		t.Fatal(err)
	}
	e = <-b.got
	if filepath.Base(e.Path) != "big report (2).pdf" {
		t.Fatalf("second copy saved as %s", e.Path)
	}
	// No temp files left behind.
	left, _ := filepath.Glob(filepath.Join(b.dir, ".voidbridge-*"))
	if len(left) != 0 {
		t.Fatalf("leftover temp files: %v", left)
	}
}

func TestSendFileUnreachable(t *testing.T) {
	a := newDev(t, "a")
	p, _ := writeFile(t, "x.txt", 10)
	if err := Send(a.n, nil, "id-nobody", p, nil); !errors.Is(err, node.ErrDeviceUnreachable) {
		t.Fatalf("got %v", err)
	}
}

func TestReceiverWithoutFolder(t *testing.T) {
	a, b := linked(t)
	b.n.SetFileReceiver(&DirReceiver{Dir: func() string { return "" }})
	p, _ := writeFile(t, "x.txt", 10)
	err := Send(a.n, nil, "id-b", p, nil)
	if err == nil {
		t.Fatal("send to a device that can't store files succeeded")
	}
}
