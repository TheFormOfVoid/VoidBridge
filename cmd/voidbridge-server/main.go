// Command voidbridge-server is the self-hosted VoidBridge relay (e.g. on a
// Raspberry Pi). It stores accounts and passes end-to-end encrypted clips
// between each account's devices.
//
//	voidbridge-server [serve] [-listen :47831] [-data DIR] [-signup invite|open]
//	voidbridge-server invite [-uses N] [-days D] [-note TEXT]
//	voidbridge-server users
//	voidbridge-server admin NAME        (make an account admin)
//	voidbridge-server enable|disable NAME
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
	"github.com/TheFormOfVoid/VoidBridge/internal/server"
)

var version = "dev"

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		serve(args)
	case "invite", "users", "admin", "enable", "disable":
		console(cmd, args)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\nCommands: serve, invite, users, admin NAME, enable NAME, disable NAME, version\n", cmd)
		os.Exit(2)
	}
}

func defaultData() string {
	if d := os.Getenv("STATE_DIRECTORY"); d != "" { // set by systemd
		return d
	}
	return "voidbridge-data"
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", fmt.Sprintf(":%d", protocol.ServerPort), "address to listen on")
	data := fs.String("data", defaultData(), "directory for the account database")
	signup := fs.String("signup", server.SignupInvite, `who can create accounts: "invite" (first account is free and becomes admin, then invite codes only) or "open"`)
	cert := fs.String("tls-cert", "", "TLS certificate file (optional; not needed over Tailscale)")
	key := fs.String("tls-key", "", "TLS key file")
	fs.Parse(args)
	if *signup != server.SignupInvite && *signup != server.SignupOpen {
		log.Fatalf("-signup must be invite or open")
	}

	st, err := server.OpenStore(*data)
	if err != nil {
		log.Fatalf("opening database in %s: %v", *data, err)
	}
	s := server.New(st, *signup)
	s.Version = version
	hs := &http.Server{Addr: *listen, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("VoidBridge server %s listening on %s (data: %s, signup: %s)", version, *listen, *data, *signup)
	if !st.HasUsers() {
		log.Printf("No accounts yet: the first account created from an app becomes the admin.")
	}
	if *cert != "" {
		err = hs.ListenAndServeTLS(*cert, *key)
	} else {
		err = hs.ListenAndServe()
	}
	log.Fatal(err)
}

// console talks to the running server's localhost-only endpoints.
func console(cmd string, args []string) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	port := fs.Int("port", protocol.ServerPort, "port the server listens on")
	uses := fs.Int("uses", 1, "invite: how many accounts it can create")
	days := fs.Int("days", 7, "invite: days until it expires (0 = never)")
	note := fs.String("note", "", "invite: a note for yourself")
	fs.Parse(args)
	base := fmt.Sprintf("http://127.0.0.1:%d/api/local", *port)

	call := func(method, path string, body any) []byte {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Fatalf("can't reach the server on port %d (is it running?): %v", *port, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			log.Fatalf("%s: %s", resp.Status, b)
		}
		return b
	}
	name := fs.Arg(0)
	needName := func() {
		if name == "" {
			log.Fatalf("usage: voidbridge-server %s NAME", cmd)
		}
	}

	switch cmd {
	case "invite":
		var inv server.Invite
		json.Unmarshal(call("POST", "/invites", server.InviteRequest{Uses: *uses, Days: *days, Note: *note}), &inv)
		exp := "never expires"
		if !inv.Expires.IsZero() {
			exp = "expires " + inv.Expires.Format("2006-01-02")
		}
		fmt.Printf("Invite code: %s  (%d use(s), %s)\n", inv.Code, inv.UsesLeft, exp)
	case "users":
		var users []server.UserView
		json.Unmarshal(call("GET", "/users", nil), &users)
		if len(users) == 0 {
			fmt.Println("No accounts yet.")
		}
		for _, u := range users {
			flags := []string{}
			if u.Admin {
				flags = append(flags, "admin")
			}
			if u.Disabled {
				flags = append(flags, "disabled")
			}
			if u.Online {
				flags = append(flags, "online")
			}
			fmt.Printf("%-24s %d device(s)  %s\n", u.Name, u.Devices, strings.Join(flags, ", "))
		}
	case "admin":
		needName()
		call("PATCH", "/users/"+name, map[string]bool{"admin": true})
		fmt.Printf("%s is now an admin.\n", name)
	case "enable", "disable":
		needName()
		call("PATCH", "/users/"+name, map[string]bool{"disabled": cmd == "disable"})
		fmt.Printf("%s %sd.\n", name, cmd)
	}
}
