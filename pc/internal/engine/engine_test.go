package engine

import (
	"net"
	"testing"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/pc/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/pc/internal/protocol"
)

var key = protocol.DeriveKey("AAAA-BBBB-CCCC-DDDD")

func init() {
	PollInterval = 10 * time.Millisecond
	PingInterval = 50 * time.Millisecond
	ReadTimeout = 300 * time.Millisecond
}

func start(t *testing.T) (*Engine, *clipboard.Memory, string) {
	t.Helper()
	cb := &clipboard.Memory{}
	e := New(cb, key, protocol.Identity{ID: "pc", Name: "PC"})
	e.Logf = t.Logf
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go e.Run(ln, stop)
	eventually(t, func() bool { return e.Status().Listening })
	return e, cb, ln.Addr().String()
}

type phone struct {
	t    *testing.T
	conn *protocol.Conn
	msgs chan *protocol.Message
}

func dial(t *testing.T, addr, id string) *phone {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c, err := protocol.Handshake(raw, key, protocol.Identity{ID: id, Name: id}, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p := &phone{t: t, conn: c, msgs: make(chan *protocol.Message, 16)}
	go func() {
		for {
			m, err := c.Recv()
			if err != nil {
				close(p.msgs)
				return
			}
			if m.Type == protocol.TypePing {
				c.Send(&protocol.Message{Type: protocol.TypePong})
				continue
			}
			p.msgs <- m
		}
	}()
	t.Cleanup(func() { c.Close() })
	return p
}

func (p *phone) expect(typ string) *protocol.Message {
	p.t.Helper()
	select {
	case m, ok := <-p.msgs:
		if !ok {
			p.t.Fatalf("connection closed waiting for %s", typ)
		}
		if m.Type != typ {
			p.t.Fatalf("got %s, want %s", m.Type, typ)
		}
		return m
	case <-time.After(2 * time.Second):
		p.t.Fatalf("timed out waiting for %s", typ)
	}
	return nil
}

func (p *phone) expectNothing() {
	p.t.Helper()
	select {
	case m := <-p.msgs:
		p.t.Fatalf("unexpected %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestPCToPhone(t *testing.T) {
	_, cb, addr := start(t)
	p := dial(t, addr, "phone")
	cb.Write("from pc")
	m := p.expect(protocol.TypeClip)
	if m.Text != "from pc" {
		t.Fatal(m.Text)
	}
	p.conn.Send(&protocol.Message{Type: protocol.TypeAck, ID: m.ID})
	p.expectNothing()
}

func TestPhoneToPC(t *testing.T) {
	_, cb, addr := start(t)
	p := dial(t, addr, "phone")
	p.conn.Send(&protocol.Message{Type: protocol.TypeClip, ID: "c1", Time: time.Now().UnixMilli(), Text: "from phone"})
	if a := p.expect(protocol.TypeAck); a.ID != "c1" {
		t.Fatal(a.ID)
	}
	eventually(t, func() bool { s, _ := cb.Read(); return s == "from phone" })
	// Writing the clipboard must not echo the clip back to the phone.
	p.expectNothing()
}

func TestResendAfterReconnect(t *testing.T) {
	_, cb, addr := start(t)
	p := dial(t, addr, "phone")
	cb.Write("first")
	p.expect(protocol.TypeClip) // received, but connection dies before ack
	p.conn.Close()

	p2 := dial(t, addr, "phone")
	if m := p2.expect(protocol.TypeClip); m.Text != "first" {
		t.Fatal(m.Text)
	} else {
		p2.conn.Send(&protocol.Message{Type: protocol.TypeAck, ID: m.ID})
	}
	p2.conn.Close()

	// Acked now, so a third connection gets nothing.
	p3 := dial(t, addr, "phone")
	p3.expectNothing()
}

func TestCopiedWhileDisconnected(t *testing.T) {
	_, cb, addr := start(t)
	cb.Write("offline copy")
	time.Sleep(50 * time.Millisecond)
	p := dial(t, addr, "phone")
	if m := p.expect(protocol.TypeClip); m.Text != "offline copy" {
		t.Fatal(m.Text)
	}
}

func TestStaleClipIgnored(t *testing.T) {
	_, cb, addr := start(t)
	p := dial(t, addr, "phone")
	cb.Write("newer on pc")
	m := p.expect(protocol.TypeClip)
	p.conn.Send(&protocol.Message{Type: protocol.TypeAck, ID: m.ID})
	old := time.Now().Add(-time.Minute).UnixMilli()
	p.conn.Send(&protocol.Message{Type: protocol.TypeClip, ID: "old", Time: old, Text: "older on phone"})
	p.expect(protocol.TypeAck)
	time.Sleep(50 * time.Millisecond)
	if s, _ := cb.Read(); s != "newer on pc" {
		t.Fatalf("stale clip overwrote clipboard: %q", s)
	}
}

func TestRelayBetweenPhones(t *testing.T) {
	e, _, addr := start(t)
	a := dial(t, addr, "a")
	b := dial(t, addr, "b")
	eventually(t, func() bool { return len(e.Status().Peers) == 2 })
	a.conn.Send(&protocol.Message{Type: protocol.TypeClip, ID: "c", Time: time.Now().UnixMilli(), Text: "hi b"})
	a.expect(protocol.TypeAck)
	if m := b.expect(protocol.TypeClip); m.Text != "hi b" {
		t.Fatal(m.Text)
	}
	a.expectNothing()
}

func TestDeadPeerDropped(t *testing.T) {
	e, _, addr := start(t)
	raw, _ := net.Dial("tcp", addr)
	c, err := protocol.Handshake(raw, key, protocol.Identity{ID: "silent"}, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	eventually(t, func() bool { return len(e.Status().Peers) == 1 })
	// Never answer or send anything: the engine must time the session out.
	eventually(t, func() bool { return len(e.Status().Peers) == 0 })
}
