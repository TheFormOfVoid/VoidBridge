package server_test

import (
	"fmt"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/peer"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
	"github.com/TheFormOfVoid/VoidBridge/internal/relay"
	"github.com/TheFormOfVoid/VoidBridge/internal/server"
)

func init() { node.PollInterval = 10 * time.Millisecond }

func setup(t *testing.T) (*server.Server, string) {
	st, err := server.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(st, server.SignupInvite)
	s.Logf = t.Logf
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts.URL
}

type device struct {
	cb   *clipboard.Memory
	n    *node.Node
	stop chan struct{}
}

// connect signs a device in and runs it until the test ends.
func connect(t *testing.T, base, user, pass, id string) *device {
	t.Helper()
	keys := protocol.KeysFromMaster(protocol.MasterFromAccount(user, pass))
	me := protocol.Identity{ID: id, Name: id, Kind: "windows"}
	api := &relay.API{Base: base}
	if _, err := api.Login(user, keys, me); err != nil {
		t.Fatal(err)
	}
	d := &device{cb: &clipboard.Memory{}, stop: make(chan struct{})}
	d.n = node.New(me, keys, d.cb, nil)
	d.n.Logf = t.Logf
	go d.n.Run(d.stop)
	go (&relay.Client{Base: base, Token: api.Token, Node: d.n, Logf: t.Logf}).Run(d.stop)
	t.Cleanup(func() { close(d.stop) })
	return d
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func keys(u, p string) protocol.Keys {
	return protocol.KeysFromMaster(protocol.MasterFromAccount(u, p))
}

func TestAccountsAndInvites(t *testing.T) {
	_, base := setup(t)
	api := &relay.API{Base: base}
	info, err := api.Info()
	if err != nil || info.HasUsers {
		t.Fatal(info, err)
	}
	// First account: no invite needed, becomes admin.
	r, err := api.Register("Alice", keys("alice", "pw-a"), "", protocol.Identity{ID: "a1", Name: "PC"})
	if err != nil || !r.Admin || r.Username != "alice" {
		t.Fatal(r, err)
	}
	// Second needs an invite.
	bob := &relay.API{Base: base}
	if _, err := bob.Register("bob", keys("bob", "pw-b"), "", protocol.Identity{ID: "b1"}); err == nil {
		t.Fatal("registered without invite")
	}
	inv, err := api.CreateInvite(1, 7, "for bob")
	if err != nil {
		t.Fatal(err)
	}
	if r, err := bob.Register("bob", keys("bob", "pw-b"), inv.Code, protocol.Identity{ID: "b1"}); err != nil || r.Admin {
		t.Fatal(r, err)
	}
	// Single-use invite is gone.
	if _, err := (&relay.API{Base: base}).Register("carol", keys("carol", "x"), inv.Code, protocol.Identity{ID: "c1"}); err == nil {
		t.Fatal("invite reused")
	}
	// Non-admins can't manage.
	if _, err := bob.Users(); err == nil {
		t.Fatal("non-admin listed users")
	}
	// Wrong password.
	if _, err := (&relay.API{Base: base}).Login("alice", keys("alice", "nope"), protocol.Identity{ID: "x"}); err == nil {
		t.Fatal("wrong password accepted")
	}
	// Disable bob: his token stops working.
	if err := api.SetUserDisabled("bob", true); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Me(); err == nil {
		t.Fatal("disabled user still authorised")
	}
}

func TestRelaySync(t *testing.T) {
	_, base := setup(t)
	(&relay.API{Base: base}).Register("alice", keys("alice", "pw"), "", protocol.Identity{ID: "setup"})
	inv, _ := func() (*server.Invite, error) {
		a := &relay.API{Base: base}
		a.Login("alice", keys("alice", "pw"), protocol.Identity{ID: "setup"})
		return a.CreateInvite(1, 0, "")
	}()
	(&relay.API{Base: base}).Register("bob", keys("bob", "pw2"), inv.Code, protocol.Identity{ID: "bsetup"})

	pc := connect(t, base, "alice", "pw", "pc")
	phone := connect(t, base, "alice", "pw", "phone")
	tablet := connect(t, base, "alice", "pw", "tablet")
	bobs := connect(t, base, "bob", "pw2", "bobpc")
	eventually(t, func() bool { return len(pc.n.Status().Devices) == 2 })

	pc.cb.WriteText("hello everyone")
	eventually(t, func() bool { return phone.cb.Text() == "hello everyone" && tablet.cb.Text() == "hello everyone" })
	tablet.cb.Write(&clipboard.Content{Type: protocol.ClipImage, Data: []byte("fake png"), Mime: "image/png"})
	eventually(t, func() bool { c, _ := pc.cb.Read(); return c != nil && c.Type == protocol.ClipImage })
	time.Sleep(100 * time.Millisecond)
	if bobs.cb.Text() != "" {
		t.Fatal("clip crossed accounts")
	}

	// A device that connects later gets the latest clip.
	late := connect(t, base, "alice", "pw", "laptop")
	eventually(t, func() bool { c, _ := late.cb.Read(); return c != nil && string(c.Data) == "fake png" })
}

func TestRevokeKicks(t *testing.T) {
	_, base := setup(t)
	a := &relay.API{Base: base}
	a.Register("alice", keys("alice", "pw"), "", protocol.Identity{ID: "pc"})
	phone := connect(t, base, "alice", "pw", "phone")
	eventually(t, func() bool { return len(phone.n.Status().Links) == 1 })
	if err := a.RevokeDevice("phone"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return len(phone.n.Status().Links) == 0 })
}

func TestServerPlusDirectFallback(t *testing.T) {
	st, _ := server.OpenStore(t.TempDir())
	s := server.New(st, server.SignupInvite)
	s.Logf = t.Logf
	ts := httptest.NewServer(s)
	base := ts.URL
	(&relay.API{Base: base}).Register("alice", keys("alice", "pw"), "", protocol.Identity{ID: "setup"})

	pc := connect(t, base, "alice", "pw", "pc")
	phone := connect(t, base, "alice", "pw", "phone")
	// Also link them directly, as they would be on the same Wi-Fi.
	k := keys("alice", "pw")
	mPC := peer.NewManager(pc.n, k, 0)
	mPC.Discovery, mPC.Logf = false, t.Logf
	mPhone := peer.NewManager(phone.n, k, 0)
	mPhone.Discovery, mPhone.Logf = false, t.Logf
	go mPC.Run(pc.stop)
	go mPhone.Run(phone.stop)
	eventually(t, func() bool { return mPC.Addr() != nil })
	mPhone.SetManual([]string{fmt.Sprintf("127.0.0.1:%d", mPC.Addr().(*net.TCPAddr).Port)})
	eventually(t, func() bool {
		d := pc.n.Status().Devices
		return len(d) == 1 && len(d[0].Via) == 2 // "Wi-Fi" and "Server"
	})

	pc.cb.WriteText("both paths")
	eventually(t, func() bool { return phone.cb.Text() == "both paths" })

	// Server goes down: direct link keeps working.
	ts.CloseClientConnections()
	ts.Close()
	phone.cb.WriteText("server is down")
	eventually(t, func() bool { return pc.cb.Text() == "server is down" })
}
