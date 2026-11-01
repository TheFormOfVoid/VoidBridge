// Command voidbridge-server is the self-hosted VoidBridge relay (e.g. on a
// Raspberry Pi). It stores accounts and passes end-to-end encrypted clips
// between each account's devices.
//
//	voidbridge-server [serve] [-listen :47831] [-data DIR] [-signup invite|open]
//	                  [-domain NAME | -tls-cert FILE -tls-key FILE]
//	voidbridge-server invite [-uses N] [-days D] [-note TEXT]
//	voidbridge-server users
//	voidbridge-server admin NAME        (make an account admin)
//	voidbridge-server enable|disable NAME
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

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
	domain := fs.String("domain", "", "get a free HTTPS certificate for this domain from Let's Encrypt (port 443 on the internet must reach this server)")
	email := fs.String("email", "", "optional contact email for Let's Encrypt")
	cert := fs.String("tls-cert", "", "HTTPS certificate file (e.g. from Home Assistant's /ssl); reloaded when it changes")
	key := fs.String("tls-key", "", "HTTPS key file")
	options := fs.String("options", "", "read settings from a Home Assistant add-on options file")
	noConsole := fs.Bool("no-console", false, "turn off the localhost console commands (invite, users, ...)")
	fs.Parse(args)
	if *options != "" {
		if err := applyOptions(*options, listen, signup, domain, email, cert, key); err != nil {
			log.Fatalf("reading %s: %v", *options, err)
		}
	}
	if *signup != server.SignupInvite && *signup != server.SignupOpen {
		log.Fatalf("-signup must be invite or open")
	}
	if (*cert == "") != (*key == "") {
		log.Fatalf("-tls-cert and -tls-key go together")
	}
	if *domain != "" && *cert != "" {
		log.Fatalf("use either -domain (Let's Encrypt) or -tls-cert/-tls-key, not both")
	}

	st, err := server.OpenStore(*data)
	if err != nil {
		log.Fatalf("opening database in %s: %v", *data, err)
	}
	s := server.New(st, *signup)
	s.Version = version
	s.NoConsole = *noConsole

	var tlsCfg *tls.Config
	switch {
	case *domain != "":
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*domain),
			Cache:      autocert.DirCache(filepath.Join(*data, "certs")),
			Email:      *email,
		}
		tlsCfg = m.TLSConfig() // answers Let's Encrypt's TLS-ALPN-01 check on this port
		log.Printf("HTTPS for %s with a Let's Encrypt certificate (the first connection fetches it)", *domain)
	case *cert != "":
		kp := &certFile{cert: *cert, key: *key}
		if _, err := kp.get(nil); err != nil {
			log.Fatalf("loading the certificate: %v", err)
		}
		tlsCfg = &tls.Config{GetCertificate: kp.get}
		log.Printf("HTTPS with the certificate in %s", *cert)
	default:
		log.Printf("No HTTPS certificate: only devices on the home network and Tailscale can connect.")
	}
	if tlsCfg != nil {
		tlsCfg.MinVersion = tls.VersionTLS12
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	hs := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, TLSConfig: tlsCfg}
	log.Printf("VoidBridge server %s listening on %s (data: %s, signup: %s)", version, *listen, *data, *signup)
	if !st.HasUsers() {
		log.Printf("No accounts yet: the first account, created from an app on your home network, becomes the admin.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Printf("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	err = hs.Serve(newDualListener(ln, tlsCfg))
	st.Close()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// certFile serves a certificate from files and picks up renewals (Home
// Assistant's Let's Encrypt and DuckDNS add-ons replace them in place).
type certFile struct {
	cert, key string
	mu        sync.Mutex
	kp        *tls.Certificate
	mod       time.Time
	checked   time.Time
}

func (c *certFile) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kp != nil && time.Since(c.checked) < time.Minute {
		return c.kp, nil
	}
	c.checked = time.Now()
	fi, err := os.Stat(c.cert)
	if err == nil && c.kp != nil && !fi.ModTime().After(c.mod) {
		return c.kp, nil
	}
	kp, lerr := tls.LoadX509KeyPair(c.cert, c.key)
	if lerr != nil {
		if c.kp != nil {
			log.Printf("reloading the certificate: %v (keeping the old one)", lerr)
			return c.kp, nil
		}
		return nil, lerr
	}
	c.kp = &kp
	if err == nil {
		c.mod = fi.ModTime()
	}
	return c.kp, nil
}

// applyOptions reads a Home Assistant add-on's /data/options.json.
func applyOptions(path string, listen, signup, domain, email, cert, key *string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var o struct {
		Port     int    `json:"port"`
		Signup   string `json:"signup"`
		Domain   string `json:"domain"`
		Email    string `json:"email"`
		Certfile string `json:"certfile"`
		Keyfile  string `json:"keyfile"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	if o.Port != 0 {
		*listen = fmt.Sprintf(":%d", o.Port)
	}
	if o.Signup != "" {
		*signup = o.Signup
	}
	*domain, *email = strings.TrimSpace(o.Domain), strings.TrimSpace(o.Email)
	inSSL := func(f string) string {
		f = strings.TrimSpace(f)
		if f == "" || filepath.IsAbs(f) {
			return f
		}
		return filepath.Join("/ssl", f)
	}
	*cert, *key = inSSL(o.Certfile), inSSL(o.Keyfile)
	return nil
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
