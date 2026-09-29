//go:build linux

// Command voidbridge is the VoidBridge client for Linux desktops such as
// Raspberry Pi OS: it syncs the clipboard with your other devices and sends
// and receives files. It runs in the background as a systemd user service and
// is controlled from the command line.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/TheFormOfVoid/VoidBridge/internal/config"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
	"github.com/TheFormOfVoid/VoidBridge/internal/relay"
	"github.com/TheFormOfVoid/VoidBridge/internal/server"
)

var version = "dev"

const usage = `VoidBridge %s: one clipboard for all your devices.

Getting started:
  voidbridge login SERVER USERNAME          sign in to your VoidBridge server
  voidbridge register SERVER USERNAME [INVITE]
                                            create an account
  voidbridge join CODE                      join devices that use a sync code
  voidbridge new-code                       start a new sync code group
  voidbridge setup                          start VoidBridge with this computer

Everyday use:
  voidbridge status                         connection, clipboard and devices
  voidbridge send DEVICE FILE...            send files (DEVICE: name or id)
  voidbridge devices                        devices you can send files to

Settings:
  voidbridge set name NAME                  how this computer appears elsewhere
  voidbridge set folder PATH                where received files go
  voidbridge set menu on|off                "Send with VoidBridge" in the file manager
  voidbridge set auto-updates on|off        install new versions by themselves
  voidbridge set sensitive on|off           skip clips a password manager marks secret
  voidbridge forget DEVICE                  remove a device from the menu
  voidbridge update                         check for a new version now
  voidbridge logout                         sign out / leave the group
  voidbridge run                            run in the foreground (what setup runs)
  voidbridge version
`

func main() {
	log.SetFlags(log.LstdFlags)
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Printf(usage, version)
		return
	}
	var err error
	switch cmd, rest := args[0], args[1:]; cmd {
	case "run":
		err = runDaemon()
	case "setup":
		err = setup()
	case "login":
		err = account(rest, false)
	case "register":
		err = account(rest, true)
	case "join":
		err = join(rest)
	case "new-code":
		err = newCode()
	case "logout", "leave":
		err = logout()
	case "status":
		err = status()
	case "devices":
		err = devices()
	case "send":
		err = send(rest)
	case "set":
		err = set(rest)
	case "forget":
		err = forget(rest)
	case "update":
		err = updateCmd()
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Printf(usage, version)
	default:
		err = fmt.Errorf("unknown command %q (run voidbridge help)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "voidbridge:", err)
		os.Exit(1)
	}
}

// ---- talking to the background service ----

var errNotRunning = errors.New("VoidBridge isn't running; start it with `voidbridge setup`")

func client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath())
		},
	}}
}

func call(method, path string, body, out any, timeout time.Duration) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://voidbridge"+path, r)
	resp, err := client(timeout).Do(req)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) && ne.Op == "dial" {
			return errNotRunning
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		return errors.New(e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// reload tells the running service the config changed.
func reload() {
	if err := call("POST", "/reload", nil, nil, 10*time.Second); errors.Is(err, errNotRunning) {
		fmt.Println("Now start it with: voidbridge setup")
	} else if err != nil {
		fmt.Println("Couldn't reach the running VoidBridge:", err)
	}
}

// ---- setup ----

const unit = `[Unit]
Description=VoidBridge clipboard sync
After=network-online.target

[Service]
ExecStart=%s run
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`

func setup() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(cfgDir, "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "voidbridge.service"), []byte(fmt.Sprintf(unit, exe)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "voidbridge"}, {"restart", "voidbridge"}} {
		cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), err)
		}
	}
	fmt.Println("VoidBridge now runs in the background and starts when you log in.")
	fmt.Println("Logs: journalctl --user -u voidbridge -f")
	return nil
}

// ---- joining ----

func loadConfig() (*config.Config, error) { return config.Load() }

func prompt(label string) string {
	fmt.Print(label)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// readPassword reads a line from the terminal without echoing it.
func readPassword(label string) (string, error) {
	fmt.Print(label)
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil { // not a terminal (piped in)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}
	noEcho := *old
	noEcho.Lflag &^= unix.ECHO
	noEcho.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &noEcho); err != nil {
		return "", err
	}
	defer func() {
		unix.IoctlSetTermios(fd, unix.TCSETS, old)
		fmt.Println()
	}()
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func leaveServer(cfg *config.Config) {
	if cfg.Mode == config.ModeAccount && cfg.Token != "" {
		(&relay.API{Base: cfg.Server, Token: cfg.Token}).Logout() // best effort
	}
}

func account(args []string, create bool) error {
	if len(args) < 2 {
		if create {
			return errors.New("usage: voidbridge register SERVER USERNAME [INVITE]")
		}
		return errors.New("usage: voidbridge login SERVER USERNAME")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	base, err := relay.NormalizeURL(args[0])
	if err != nil {
		return err
	}
	api := &relay.API{Base: base}
	info, err := api.Info()
	if err != nil {
		return err
	}
	fmt.Printf("Server: %s (VoidBridge server %s)\n", info.Name, info.Version)
	invite := ""
	if len(args) > 2 {
		invite = args[2]
	}
	password, err := readPassword("Password: ")
	if err != nil {
		return err
	}
	if create {
		if len(password) < 8 {
			return errors.New("use a password of at least 8 characters: it also encrypts your clipboard")
		}
		if again, _ := readPassword("Password again: "); again != password {
			return errors.New("the passwords don't match")
		}
	}
	fmt.Println("Signing in…")
	username := protocol.NormalizeUsername(args[1])
	master := protocol.MasterFromAccount(username, password)
	keys := protocol.KeysFromMaster(master)
	var res *server.LoginResult
	if create {
		res, err = api.Register(username, keys, invite, identity(cfg))
	} else {
		res, err = api.Login(username, keys, identity(cfg))
	}
	if err != nil {
		return err
	}
	leaveServer(cfg)
	cfg.Leave()
	cfg.Mode, cfg.Server, cfg.Username, cfg.Token, cfg.Admin = config.ModeAccount, base, res.Username, res.Token, res.Admin
	if err := cfg.SetMaster(master); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("Signed in as %s.\n", res.Username)
	reload()
	return nil
}

func joinCode(code string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	leaveServer(cfg)
	cfg.Leave()
	cfg.Mode = config.ModeCode
	cfg.Code = protocol.FormatCode(code)
	if err := cfg.SetMaster(protocol.MasterFromCode(code)); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("Sync code: %s\nEnter it on your other devices to add them.\n", cfg.Code)
	reload()
	return nil
}

func join(args []string) error {
	if len(args) != 1 || !protocol.ValidCode(args[0]) {
		return errors.New("usage: voidbridge join CODE (16 letters and digits, like ABCD-EFGH-2345-WXYZ)")
	}
	return joinCode(args[0])
}

func newCode() error { return joinCode(protocol.NewGroupCode()) }

func logout() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	leaveServer(cfg)
	cfg.Leave()
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Println("Signed out. This computer no longer syncs.")
	reload()
	return nil
}

// ---- everyday use ----

func getStatus() (*Status, error) {
	var st Status
	return &st, call("GET", "/status", nil, &st, 10*time.Second)
}

func ago(unix int64) string {
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < 2*time.Minute:
		return "just now"
	case d < 2*time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

func status() error {
	st, err := getStatus()
	if err != nil {
		return err
	}
	fmt.Printf("VoidBridge %s on %s\n", st.Version, st.Name)
	switch st.Mode {
	case config.ModeAccount:
		state := "connected"
		switch {
		case st.SignedOut:
			state = "signed out by the server; run voidbridge login again"
		case !st.ServerUp:
			state = "not connected"
			if st.ServerErr != "" {
				state += " (" + st.ServerErr + ")"
			}
		}
		fmt.Printf("Account:   %s on %s, %s\n", st.Username, st.Server, state)
	case config.ModeCode:
		fmt.Printf("Sync code: %s\n", st.Code)
	default:
		fmt.Println("Not set up yet: run voidbridge login or voidbridge join.")
	}
	if st.PeerErr != "" {
		fmt.Println("Direct links: " + st.PeerErr)
	}
	if st.Clipboard == "ok" {
		fmt.Println("Clipboard: syncing")
	} else {
		fmt.Println("Clipboard: " + st.Clipboard)
	}
	if len(st.Devices) == 0 {
		fmt.Println("Devices:   none connected right now")
	} else {
		fmt.Println("Devices:")
		for _, d := range st.Devices {
			fmt.Printf("  %-24s via %s\n", d.Name, strings.Join(d.Via, ", "))
		}
	}
	if st.Update != "" {
		fmt.Printf("Update:    %s is available (voidbridge update)\n", st.Update)
	}
	return nil
}

func devices() error {
	st, err := getStatus()
	if err != nil {
		return err
	}
	online := map[string]bool{}
	for _, d := range st.Devices {
		online[d.ID] = true
	}
	if len(st.Known) == 0 {
		fmt.Println("No devices yet. They appear once they've connected.")
	}
	for _, k := range st.Known {
		state := "last seen " + ago(k.LastSeen)
		if online[k.ID] {
			state = "connected"
		}
		fmt.Printf("%-24s %-20s %s\n", k.Name, k.ID, state)
	}
	return nil
}

// resolveDevice turns a name or id into a device id.
func resolveDevice(st *Status, want string) (string, error) {
	var matches []knownDevice
	for _, k := range st.Known {
		if k.ID == want {
			return k.ID, nil
		}
		if strings.EqualFold(k.Name, want) {
			matches = append(matches, k)
		}
	}
	for _, d := range st.Devices {
		if d.ID == want {
			return d.ID, nil
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].ID, nil
	case 0:
		return "", fmt.Errorf("no device called %q (see voidbridge devices)", want)
	}
	return "", fmt.Errorf("more than one device is called %q; use its id (see voidbridge devices)", want)
}

func send(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: voidbridge send DEVICE FILE...")
	}
	st, err := getStatus()
	if err != nil {
		return err
	}
	id, err := resolveDevice(st, args[0])
	if err != nil {
		return err
	}
	var paths []string
	for _, p := range args[1:] {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		paths = append(paths, abs)
	}
	name := id
	for _, k := range st.Known {
		if k.ID == id {
			name = k.Name
		}
	}
	what := filepath.Base(paths[0])
	if len(paths) > 1 {
		what = fmt.Sprintf("%d files", len(paths))
	}
	fmt.Printf("Sending %s to %s…\n", what, name)
	fromMenu := os.Getenv("TERM") == "" // started from the file manager: report by notification
	if fromMenu {
		notify("Sending "+what+" to "+name, "")
	}
	err = call("POST", "/send", sendRequest{Device: id, Paths: paths}, nil, 0)
	if fromMenu {
		if err != nil {
			notify("Couldn't send "+what, err.Error())
		} else {
			notify("Sent "+what+" to "+name, "")
		}
	}
	if err == nil {
		fmt.Println("Done.")
	}
	return err
}

func forget(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: voidbridge forget DEVICE")
	}
	st, err := getStatus()
	if err != nil && !errors.Is(err, errNotRunning) {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	id := args[0]
	if st != nil {
		if r, err := resolveDevice(st, args[0]); err == nil {
			id = r
		}
	}
	cfg.Forget(id)
	if err := cfg.Save(); err != nil {
		return err
	}
	reload()
	return nil
}

func onOff(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "on", "yes", "true", "1":
		return true, nil
	case "off", "no", "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("expected on or off, not %q", v)
}

func set(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: voidbridge set name|folder|menu|auto-updates|sensitive VALUE")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	value := strings.Join(args[1:], " ")
	switch args[0] {
	case "name":
		cfg.DeviceName = value
	case "folder":
		abs, err := filepath.Abs(value)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return err
		}
		cfg.ReceiveDir = abs
	case "menu":
		on, err := onOff(value)
		if err != nil {
			return err
		}
		cfg.HideContextMenu = !on
	case "auto-updates", "autoupdate", "auto-update":
		on, err := onOff(value)
		if err != nil {
			return err
		}
		cfg.NoAutoUpdate = !on
	case "sensitive":
		on, err := onOff(value)
		if err != nil {
			return err
		}
		cfg.SkipSensitive = on
	default:
		return fmt.Errorf("unknown setting %q", args[0])
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Println("Saved.")
	reload()
	return nil
}

func updateCmd() error {
	var r struct{ Message string }
	if err := call("POST", "/update", nil, &r, 2*time.Minute); err != nil {
		return err
	}
	fmt.Println(r.Message)
	return nil
}
