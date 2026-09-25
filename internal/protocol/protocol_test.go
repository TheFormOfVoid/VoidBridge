package protocol

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"testing"
	"time"
)

func TestCodes(t *testing.T) {
	c := NewGroupCode()
	if len(c) != 19 || !ValidCode(c) {
		t.Fatalf("bad code %q", c)
	}
	if NormalizeCode("abcd-ef01-2345-6789") != "ABCDEFOI234567B" {
		t.Fatal(NormalizeCode("abcd-ef01-2345-6789"))
	}
	if FormatCode("abcdefghijklmnop") != "ABCD-EFGH-IJKL-MNOP" {
		t.Fatal(FormatCode("abcdefghijklmnop"))
	}
}

// Shared with android/core ProtocolTest.knownAnswersMatchGo.
func TestKnownAnswers(t *testing.T) {
	check := func(name string, got []byte, want string) {
		t.Helper()
		if hex.EncodeToString(got) != want {
			t.Errorf("%s = %x, want %s", name, got, want)
		}
	}
	code := KeysFromMaster(MasterFromCode("ABCD-EFGH-IJKL-MNOP"))
	check("code master", code.Master, "9e3f6a5f684ceef721f4e5f99e8e61f435dc7f28aaa0c4658803808be73c7d59")
	acct := KeysFromMaster(MasterFromAccount(" Alice ", "correct horse"))
	check("account master", acct.Master, KA["account master"])
	check("link", code.Link, KA["link"])
	check("content", code.Content, KA["content"])
	check("auth", acct.Auth, KA["auth"])
	if fp := code.Fingerprint(); fp != KA["fingerprint"] {
		t.Errorf("fingerprint = %s", fp)
	}
	sk := SessionKey(code.Link, bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 16))
	check("session", sk, KA["session"])
	check("first client frame", NewCipher(sk, false).Seal([]byte(`{"t":"ready"}`)), KA["frame"])
	m := &Message{Type: TypeClip, ID: "c1", Origin: "d1", Time: 1700000000000, ClipType: ClipText, Mime: "text/plain"}
	if got := string(clipAAD(m)); got != "voidbridge-clip-v2|c1|d1|1700000000000|text|text/plain" {
		t.Errorf("aad = %s", got)
	}
	check("encoded frame", Encode(&Message{Type: TypePing, Body: []byte{9}}), "0000000c7b2274223a2270696e67227d09")
}

// KA holds the known answers (hex) for values not easy to write inline.
var KA = map[string]string{
	"account master": "02de1a5fe7b5eaaac9fea0798fd73520a99acb964edfecc937f4433ff5a6715a",
	"link":           "7115bab6f55f9e7149e3eedae05a2f4cbb3977d0b1e2ca52306f821e73b96c2b",
	"content":        "3baec9577d1694f354d68114a6fa44fe4cfabfdddede1548844c58018e6ce2e3",
	"auth":           "5d880d9c8233fb24a0819091fdc4fb1cde7878be0f4a8cebb774d55f8961615e",
	"fingerprint":    "96001ca5c8506c14",
	"session":        "7b364ea83e6e69c4ceb6c712569e0614f6628a08ae1c4d8f8bd88871599ea039",
	"frame":          "57a9fc60d3c90b7001a2379a1334f0ccd51dbce163248160116646cfcf",
}

func TestClipSealing(t *testing.T) {
	k := KeysFromMaster(MasterFromCode("AAAA-BBBB-CCCC-DDDD"))
	m := &Message{Type: TypeClip, ID: "x", Origin: "o", Time: 5, ClipType: ClipText, Mime: "text/plain"}
	SealClip(k.Content, m, []byte("secret"))
	if p, err := OpenClip(k.Content, m); err != nil || string(p) != "secret" {
		t.Fatal(p, err)
	}
	m.Time = 6 // tampered header
	if _, err := OpenClip(k.Content, m); !errors.Is(err, ErrDecrypt) {
		t.Fatal("tampered header accepted")
	}
	m.Time = 5
	other := KeysFromMaster(MasterFromCode("AAAA-BBBB-CCCC-DDDE"))
	if _, err := OpenClip(other.Content, m); !errors.Is(err, ErrDecrypt) {
		t.Fatal("wrong key accepted")
	}
	// Round trip through a frame.
	d, err := Decode(Encode(m))
	if err != nil || !bytes.Equal(d.Body, m.Body) || d.Origin != "o" {
		t.Fatal(d, err)
	}
}

func TestCipherReplay(t *testing.T) {
	sk := SessionKey(make([]byte, 32), NewNonce(), NewNonce())
	cli, srv := NewCipher(sk, false), NewCipher(sk, true)
	f := cli.Seal([]byte("one"))
	if p, err := srv.Open(f); err != nil || string(p) != "one" {
		t.Fatal(p, err)
	}
	if _, err := srv.Open(f); err == nil {
		t.Fatal("replay accepted")
	}
	if _, err := srv.Open(srv.Seal([]byte("x"))); err == nil {
		t.Fatal("reflection accepted")
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

func TestHandshakeAndBigFrame(t *testing.T) {
	k := KeysFromMaster(MasterFromCode("AAAA-BBBB-CCCC-DDDD"))
	a, b := pipe(t)
	ch := make(chan *Conn)
	go func() {
		c, err := Handshake(b, k.Link, Identity{ID: "pc", Name: "PC", Kind: "windows"}, []string{"10.0.0.2:47829"}, false, 5*time.Second)
		if err != nil {
			t.Error(err)
		}
		ch <- c
	}()
	cli, err := Handshake(a, k.Link, Identity{ID: "phone", Name: "Phone", Kind: "android"}, nil, true, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	srv := <-ch
	if cli.Peer.ID != "pc" || srv.Peer.Kind != "android" || cli.PeerAddrs[0] != "10.0.0.2:47829" {
		t.Fatal("identities not exchanged")
	}
	big := bytes.Repeat([]byte("x"), 10<<20)
	go cli.Send(&Message{Type: TypeClip, ID: "i", Body: big})
	m, err := srv.Recv(5 * time.Second)
	if err != nil || !bytes.Equal(m.Body, big) {
		t.Fatal(err)
	}
}

func TestHandshakeWrongGroup(t *testing.T) {
	a, b := pipe(t)
	ch := make(chan error)
	go func() {
		_, err := Handshake(b, KeysFromMaster(MasterFromCode("AAAA-BBBB-CCCC-DDDD")).Link, Identity{ID: "pc"}, nil, false, 5*time.Second)
		ch <- err
	}()
	_, err := Handshake(a, KeysFromMaster(MasterFromCode("AAAA-BBBB-CCCC-DDDE")).Link, Identity{ID: "phone"}, nil, true, 5*time.Second)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("dialer: %v", err)
	}
	if err := <-ch; !errors.Is(err, ErrAuth) {
		t.Fatalf("listener: %v", err)
	}
}
