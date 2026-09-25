// Command interop runs Go VoidBridge devices for the Android core's interop
// tests (android/core ProtocolTest). It starts:
//
//   - a device in sync-code group -code, listening for direct links on -peer;
//   - a VoidBridge server on -server with account alice / "interop-password";
//   - a device signed in to that account.
//
// Both devices answer a copied "ping:X" by copying "pong:X", and answer an
// image by copying "got-image:<sha256 of the image>", so the Kotlin side can
// check both directions.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/peer"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
	"github.com/TheFormOfVoid/VoidBridge/internal/relay"
	"github.com/TheFormOfVoid/VoidBridge/internal/server"
)

func echo(name string, cb *clipboard.Memory) {
	var last uint64
	for range time.Tick(20 * time.Millisecond) {
		seq, _ := cb.Seq()
		if seq == last {
			continue
		}
		last = seq
		c, _ := cb.Read()
		if c == nil {
			continue
		}
		var reply string
		switch {
		case c.Type == protocol.ClipText && strings.HasPrefix(c.Text, "ping:"):
			reply = "pong:" + strings.TrimPrefix(c.Text, "ping:")
		case c.Type == protocol.ClipImage:
			h := sha256.Sum256(c.Data)
			reply = "got-image:" + hex.EncodeToString(h[:])
		default:
			continue
		}
		log.Printf("%s: got %q, replying %q", name, truncate(c.Text), reply)
		time.Sleep(50 * time.Millisecond)
		cb.WriteText(reply)
		last, _ = cb.Seq()
	}
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}

func main() {
	code := flag.String("code", "ABCD-EFGH-IJKL-MNOP", "sync code of the direct-link device")
	peerPort := flag.Int("peer", 47829, "direct-link port")
	serverPort := flag.Int("server", 47831, "server port")
	flag.Parse()
	node.PollInterval = 20 * time.Millisecond
	stop := make(chan struct{})

	// Direct-link device.
	keys := protocol.KeysFromMaster(protocol.MasterFromCode(*code))
	cb1 := &clipboard.Memory{}
	n1 := node.New(protocol.Identity{ID: "go-peer", Name: "Go peer", Kind: "windows"}, keys, cb1, nil)
	m := peer.NewManager(n1, keys, *peerPort)
	m.Discovery = false
	go n1.Run(stop)
	go func() { log.Fatal(m.Run(stop)) }()
	go echo("peer", cb1)

	// Server and an account device.
	dir, _ := os.MkdirTemp("", "vb-interop")
	st, err := server.OpenStore(dir)
	if err != nil {
		log.Fatal(err)
	}
	srv := server.New(st, server.SignupInvite)
	go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *serverPort), srv)) }()
	time.Sleep(200 * time.Millisecond)
	base := fmt.Sprintf("http://127.0.0.1:%d", *serverPort)
	akeys := protocol.KeysFromMaster(protocol.MasterFromAccount("alice", "interop-password"))
	me := protocol.Identity{ID: "go-account", Name: "Go account device", Kind: "windows"}
	api := &relay.API{Base: base}
	if _, err := api.Register("alice", akeys, "", me); err != nil {
		log.Fatal(err)
	}
	cb2 := &clipboard.Memory{}
	n2 := node.New(me, akeys, cb2, nil)
	go n2.Run(stop)
	go (&relay.Client{Base: base, Token: api.Token, Node: n2}).Run(stop)
	go echo("account", cb2)

	fmt.Println("READY")
	select {}
}
