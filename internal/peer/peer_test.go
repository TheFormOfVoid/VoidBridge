package peer

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

func init() {
	node.PollInterval = 10 * time.Millisecond
	DialInterval = 20 * time.Millisecond
	PingInterval = 50 * time.Millisecond
	IdleTimeout = 500 * time.Millisecond
}

var groupKeys = protocol.KeysFromMaster(protocol.MasterFromCode("AAAA-BBBB-CCCC-DDDD"))

type dev struct {
	t    *testing.T
	name string
	cb   *clipboard.Memory
	n    *node.Node
	m    *Manager
	stop chan struct{}
	addr string
}

func newDev(t *testing.T, name string, keys protocol.Keys) *dev {
	t.Helper()
	d := &dev{t: t, name: name, cb: &clipboard.Memory{}, stop: make(chan struct{})}
	d.n = node.New(protocol.Identity{ID: "id-" + name, Name: name, Kind: "windows"}, keys, d.cb, node.NewHistory())
	d.n.Logf = func(string, ...any) {}
	d.m = NewManager(d.n, keys, 0)
	d.m.Discovery = false
	d.m.Logf = func(string, ...any) {}
	go d.n.Run(d.stop)
	go d.m.Run(d.stop)
	eventually(t, func() bool { return d.m.Addr() != nil })
	d.addr = fmt.Sprintf("127.0.0.1:%d", d.m.Addr().(*net.TCPAddr).Port)
	t.Cleanup(d.close)
	return d
}

func (d *dev) close() {
	select {
	case <-d.stop:
	default:
		close(d.stop)
	}
}

func (d *dev) peers() int { return len(d.n.Status().Devices) }

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestThreeDevicesChain(t *testing.T) {
	a, b, c := newDev(t, "a", groupKeys), newDev(t, "b", groupKeys), newDev(t, "c", groupKeys)
	// a only knows b, b only knows c. Clips must still reach everyone.
	a.m.SetManual([]string{b.addr})
	b.m.SetManual([]string{c.addr})
	eventually(t, func() bool { return a.peers() >= 1 && c.peers() >= 1 })

	a.cb.WriteText("from a")
	eventually(t, func() bool { return b.cb.Text() == "from a" && c.cb.Text() == "from a" })
	c.cb.WriteText("from c")
	eventually(t, func() bool { return a.cb.Text() == "from c" && b.cb.Text() == "from c" })
}

func TestImages(t *testing.T) {
	a, b := newDev(t, "a", groupKeys), newDev(t, "b", groupKeys)
	a.m.SetManual([]string{b.addr})
	eventually(t, func() bool { return a.peers() == 1 && b.peers() == 1 })
	img := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 2<<20) // 8 MB
	a.cb.Write(&clipboard.Content{Type: protocol.ClipImage, Data: img, Mime: "image/png"})
	eventually(t, func() bool {
		c, _ := b.cb.Read()
		return c != nil && c.Type == protocol.ClipImage && bytes.Equal(c.Data, img)
	})
}

func TestCatchUpAfterOffline(t *testing.T) {
	a := newDev(t, "a", groupKeys)
	b := newDev(t, "b", groupKeys)
	a.m.SetManual([]string{b.addr})
	eventually(t, func() bool { return a.peers() == 1 })
	b.close()
	eventually(t, func() bool { return a.peers() == 0 })

	a.cb.WriteText("copied while b was off")
	time.Sleep(50 * time.Millisecond)
	b2 := newDev(t, "b", groupKeys)
	a.m.SetManual([]string{b2.addr})
	eventually(t, func() bool { return b2.cb.Text() == "copied while b was off" })
}

func TestNewestWins(t *testing.T) {
	a, b := newDev(t, "a", groupKeys), newDev(t, "b", groupKeys)
	a.cb.WriteText("older")
	time.Sleep(50 * time.Millisecond)
	b.cb.WriteText("newer")
	time.Sleep(50 * time.Millisecond)
	a.m.SetManual([]string{b.addr})
	eventually(t, func() bool { return a.cb.Text() == "newer" })
	time.Sleep(100 * time.Millisecond)
	if b.cb.Text() != "newer" {
		t.Fatalf("older clip overwrote newer: %q", b.cb.Text())
	}
}

func TestSensitiveNotSent(t *testing.T) {
	a, b := newDev(t, "a", groupKeys), newDev(t, "b", groupKeys)
	a.n.SetSettings(node.Settings{SkipSensitive: true})
	a.m.SetManual([]string{b.addr})
	eventually(t, func() bool { return a.peers() == 1 })
	a.cb.Write(&clipboard.Content{Type: protocol.ClipText, Text: "hunter2", Mime: "text/plain", Sensitive: true})
	time.Sleep(150 * time.Millisecond)
	if b.cb.Text() == "hunter2" {
		t.Fatal("sensitive clip was synced")
	}
}

func TestOtherGroupIgnored(t *testing.T) {
	a := newDev(t, "a", groupKeys)
	x := newDev(t, "x", protocol.KeysFromMaster(protocol.MasterFromCode("ZZZZ-ZZZZ-ZZZZ-ZZZZ")))
	a.m.SetManual([]string{x.addr})
	time.Sleep(200 * time.Millisecond)
	if a.peers() != 0 || x.peers() != 0 {
		t.Fatal("devices from different groups linked")
	}
	a.cb.WriteText("private")
	time.Sleep(100 * time.Millisecond)
	if x.cb.Text() != "" {
		t.Fatal("clip leaked to another group")
	}
}

func TestPeerExchange(t *testing.T) {
	allowLoopback.Store(true)
	t.Cleanup(func() { time.Sleep(50 * time.Millisecond); allowLoopback.Store(false) })
	a, b, c := newDev(t, "a", groupKeys), newDev(t, "b", groupKeys), newDev(t, "c", groupKeys)
	// c only knows a; a knows b. a tells c where b is, and c links to b directly.
	a.m.SetManual([]string{b.addr})
	c.m.SetManual([]string{a.addr})
	eventually(t, func() bool {
		for _, d := range c.n.Status().Devices {
			if d.ID == "id-b" {
				return true
			}
		}
		return false
	})
}
