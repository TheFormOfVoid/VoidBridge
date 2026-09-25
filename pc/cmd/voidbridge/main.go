// Command voidbridge is the PC side of VoidBridge: a tray app that keeps the
// Windows clipboard in sync with paired Android phones.
package main

import (
	"flag"
	"fmt"
	"image/color"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/TheFormOfVoid/VoidBridge/pc/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/pc/internal/config"
	"github.com/TheFormOfVoid/VoidBridge/pc/internal/discovery"
	"github.com/TheFormOfVoid/VoidBridge/pc/internal/engine"
	"github.com/TheFormOfVoid/VoidBridge/pc/internal/protocol"
)

var version = "dev"

// app owns the running engine and lets the tray restart it with a new code.
type app struct {
	mu      sync.Mutex
	cfg     *config.Config
	eng     *engine.Engine
	stop    chan struct{}
	logPath string
	onState func(engine.Status)
}

func (a *app) start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", a.cfg.Port))
	if err != nil {
		return err
	}
	key := protocol.DeriveKey(a.cfg.PairingCode)
	host, _ := os.Hostname()
	e := engine.New(clipboard.System(), key, protocol.Identity{ID: a.cfg.DeviceID, Name: host})
	e.OnStatus = a.onState
	a.eng = e
	a.stop = make(chan struct{})
	go e.Run(ln, a.stop)
	go discovery.Announce(discovery.Beacon{
		ID: a.cfg.DeviceID, Name: host, Port: a.cfg.Port, Fingerprint: discovery.Fingerprint(key),
	}, a.stop, log.Printf)
	log.Printf("listening on :%d (addresses: %s)", a.cfg.Port, strings.Join(discovery.LocalIPs(), ", "))
	return nil
}

func (a *app) shutdown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stop != nil {
		close(a.stop)
		a.stop = nil
	}
}

func (a *app) newCode() error {
	a.shutdown()
	a.mu.Lock()
	a.cfg.PairingCode = protocol.NewPairingCode()
	err := a.cfg.Save()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	// Give the old listener a moment to release the port.
	time.Sleep(200 * time.Millisecond)
	return a.start()
}

func (a *app) pairingInfo() string {
	host, _ := os.Hostname()
	ips := discovery.LocalIPs()
	if len(ips) == 0 {
		ips = []string{"(no network)"}
	}
	return fmt.Sprintf("Enter this pairing code in the VoidBridge app on your phone:\n\n"+
		"        %s\n\n"+
		"The phone normally finds this PC (%s) automatically. If it doesn't, "+
		"enter one of these addresses in the app:\n\n        %s  (port %d)\n\n"+
		"If Windows Firewall asked about VoidBridge, allow it on private networks.",
		a.cfg.PairingCode, host, strings.Join(ips, "\n        "), a.cfg.Port)
}

func setupLog() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "voidbridge.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 1<<20 {
		os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ""
	}
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	return p
}

func main() {
	headless := flag.Bool("headless", false, "run without a tray icon (logs to the console)")
	showCode := flag.Bool("code", false, "print the pairing code and exit")
	flag.Parse()

	logPath := setupLog()
	log.Printf("VoidBridge %s starting (%s/%s)", version, runtime.GOOS, runtime.GOARCH)

	cfg, err := config.Load()
	if err != nil {
		showMessage("VoidBridge", "Could not load settings: "+err.Error())
		os.Exit(1)
	}
	a := &app{cfg: cfg, logPath: logPath}
	if *showCode {
		fmt.Println(a.pairingInfo())
		return
	}

	if *headless {
		a.onState = func(s engine.Status) { log.Printf("status: %s", describe(s)) }
		if err := a.start(); err != nil {
			log.Fatalf("cannot listen on port %d: %v (is VoidBridge already running?)", cfg.Port, err)
		}
		fmt.Println(a.pairingInfo())
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		a.shutdown()
		return
	}

	systray.Run(func() { a.onTrayReady() }, a.shutdown)
}

func describe(s engine.Status) string {
	switch {
	case len(s.Peers) > 0:
		names := make([]string, len(s.Peers))
		for i, p := range s.Peers {
			names[i] = p.Name
		}
		return "Connected to " + strings.Join(names, ", ")
	case s.LastError != "":
		return s.LastError
	case s.Listening:
		return "Waiting for phone…"
	default:
		return "Not running"
	}
}

func (a *app) onTrayReady() {
	ico := runtime.GOOS == "windows"
	iconOn := makeIcon(color.NRGBA{0x7c, 0x5c, 0xff, 0xff}, ico)
	iconOff := makeIcon(color.NRGBA{0x80, 0x80, 0x80, 0xff}, ico)

	systray.SetIcon(iconOff)
	systray.SetTitle("")
	systray.SetTooltip("VoidBridge")

	mStatus := systray.AddMenuItem("Starting…", "")
	mStatus.Disable()
	systray.AddSeparator()
	mPair := systray.AddMenuItem("Pair a phone…", "Show the pairing code")
	mNewCode := systray.AddMenuItem("Reset pairing code", "Unpair all phones and make a new code")
	mAuto := systray.AddMenuItemCheckbox("Start with Windows", "", autostartEnabled())
	mLog := systray.AddMenuItem("Open log", "")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "")

	a.onState = func(s engine.Status) {
		text := describe(s)
		mStatus.SetTitle(text)
		systray.SetTooltip("VoidBridge — " + text)
		if len(s.Peers) > 0 {
			systray.SetIcon(iconOn)
		} else {
			systray.SetIcon(iconOff)
		}
	}

	if err := a.start(); err != nil {
		mStatus.SetTitle("Error: port in use")
		showMessage("VoidBridge", fmt.Sprintf("VoidBridge can't listen on port %d: %v\n\nIt is probably already running (check the tray).", a.cfg.Port, err))
		systray.Quit()
		return
	}
	if !autostartEnabled() && os.Getenv("VOIDBRIDGE_NO_AUTOSTART") == "" {
		// First run: start with Windows by default; the menu can turn it off.
		if setAutostart(true) == nil {
			mAuto.Check()
		}
		go showMessage("VoidBridge", a.pairingInfo())
	}

	go func() {
		for {
			select {
			case <-mPair.ClickedCh:
				go showMessage("VoidBridge — pair a phone", a.pairingInfo())
			case <-mNewCode.ClickedCh:
				if err := a.newCode(); err != nil {
					go showMessage("VoidBridge", "Could not reset the code: "+err.Error())
				} else {
					go showMessage("VoidBridge — new pairing code", a.pairingInfo())
				}
			case <-mAuto.ClickedCh:
				want := !mAuto.Checked()
				if err := setAutostart(want); err != nil {
					go showMessage("VoidBridge", err.Error())
				} else if want {
					mAuto.Check()
				} else {
					mAuto.Uncheck()
				}
			case <-mLog.ClickedCh:
				openFile(a.logPath)
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
