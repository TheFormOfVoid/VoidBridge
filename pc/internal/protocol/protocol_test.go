package protocol

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"testing"
	"time"
)

func TestPairingCode(t *testing.T) {
	c := NewPairingCode()
	if len(c) != 19 || !ValidCode(c) {
		t.Fatalf("bad code %q", c)
	}
	if NormalizeCode("abcd-ef01-2345-6789") != "ABCDEFOI234567B" {
		t.Fatalf("normalize: %q", NormalizeCode("abcd-ef01-2345-6789"))
	}
	if !bytes.Equal(DeriveKey("abcd efgh ijkl mnop"), DeriveKey("ABCD-EFGH-IJKL-MNOP")) {
		t.Fatal("formatting should not change the key")
	}
}

// Known-answer values shared with the Android implementation's tests, so both
// sides are guaranteed to derive identical keys.
func TestKnownAnswers(t *testing.T) {
	key := DeriveKey("ABCD-EFGH-IJKL-MNOP")
	check := func(name string, got []byte, want string) {
		t.Helper()
		if hex.EncodeToString(got) != want {
			t.Errorf("%s = %x, want %s", name, got, want)
		}
	}
	check("pairing key", key, "9e3f6a5f684ceef721f4e5f99e8e61f435dc7f28aaa0c4658803808be73c7d59")
	sk := SessionKey(key, bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 16))
	check("session key", sk, "01e00776fc64f1cfc049e5a73b30e7a48ea86a0ee2e32cb7d3a87fe69c50a7c0")
	c, _ := NewCipher(sk, false)
	check("first client frame", c.Seal([]byte(`{"t":"ready"}`)), "093dfe1177152db81e25146905bbcdf7b6e3d8926c36d21e13b6d67d3e")
}

func TestCipherRoundTripAndReplay(t *testing.T) {
	sk := SessionKey(DeriveKey("AAAA-BBBB-CCCC-DDDD"), NewNonce(), NewNonce())
	cli, _ := NewCipher(sk, false)
	srv, _ := NewCipher(sk, true)

	f1 := cli.Seal([]byte("one"))
	f2 := cli.Seal([]byte("two"))
	if p, err := srv.Open(f1); err != nil || string(p) != "one" {
		t.Fatal(p, err)
	}
	if _, err := srv.Open(f1); !errors.Is(err, ErrAuth) {
		t.Fatal("replay accepted")
	}
	if p, err := srv.Open(f2); err != nil || string(p) != "two" {
		t.Fatal(p, err)
	}
	// A frame reflected back at its sender must not decrypt.
	f3 := srv.Seal([]byte("three"))
	if _, err := srv.Open(f3); err == nil {
		t.Fatal("reflected frame accepted")
	}
}

func pipe(t *testing.T) (net.Conn, net.Conn) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ch := make(chan net.Conn)
	go func() { c, _ := ln.Accept(); ch <- c }()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return a, <-ch
}

func TestHandshake(t *testing.T) {
	key := DeriveKey("AAAA-BBBB-CCCC-DDDD")
	a, b := pipe(t)
	type res struct {
		c   *Conn
		err error
	}
	ch := make(chan res)
	go func() {
		c, err := Handshake(b, key, Identity{ID: "pc", Name: "PC"}, true, 5*time.Second)
		ch <- res{c, err}
	}()
	cli, err := Handshake(a, key, Identity{ID: "phone", Name: "Phone"}, false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	if cli.PeerID != "pc" || r.c.PeerName != "Phone" {
		t.Fatal("identities not exchanged")
	}
	go cli.Send(&Message{Type: TypeClip, ID: "x", Text: "héllo 👋"})
	m, err := r.c.Recv()
	if err != nil || m.Text != "héllo 👋" {
		t.Fatal(m, err)
	}
}

func TestHandshakeWrongCode(t *testing.T) {
	a, b := pipe(t)
	ch := make(chan error)
	go func() {
		_, err := Handshake(b, DeriveKey("AAAA-BBBB-CCCC-DDDD"), Identity{ID: "pc"}, true, 5*time.Second)
		ch <- err
	}()
	_, err := Handshake(a, DeriveKey("AAAA-BBBB-CCCC-DDDE"), Identity{ID: "phone"}, false, 5*time.Second)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("client: %v", err)
	}
	if err := <-ch; !errors.Is(err, ErrAuth) {
		t.Fatalf("server: %v", err)
	}
}
