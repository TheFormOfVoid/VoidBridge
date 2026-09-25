// Command voidbridge is the Windows desktop app: a window for setup, history
// and phone setup, plus a tray icon, around the VoidBridge sync engine.
package main

import (
	"embed"
	"flag"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/TheFormOfVoid/VoidBridge/internal/config"
)

var version = "dev"

//go:embed all:frontend
var assets embed.FS

func setupLog() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "voidbridge.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 2<<20 {
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
	hidden := flag.Bool("hidden", false, "start in the tray without showing the window")
	flag.Parse()

	logPath := setupLog()
	log.Printf("VoidBridge %s starting (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
	cfg, err := config.Load()
	if err != nil {
		showMessage("VoidBridge", "Could not load settings: "+err.Error())
		os.Exit(1)
	}
	first := cfg.Mode == config.ModeNone
	if first && !autostartEnabled() && os.Getenv("VOIDBRIDGE_NO_AUTOSTART") == "" {
		setAutostart(true) // on by default; one click to turn off in Settings
	}

	svc := NewService(cfg)
	app := NewApp(cfg, svc, logPath)
	go runTray(app)

	err = wails.Run(&options.App{
		Title:             "VoidBridge",
		Width:             1000,
		Height:            700,
		MinWidth:          780,
		MinHeight:         540,
		StartHidden:       *hidden && !first,
		HideWindowOnClose: true,
		BackgroundColour:  &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         app.startup,
		Bind:              []any{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "com.theformofvoid.voidbridge",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { app.Show() },
		},
		Windows: &windows.Options{
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			BackdropType:         windows.Mica,
			Theme:                windows.SystemDefault,
		},
	})
	if err != nil {
		showMessage("VoidBridge", "VoidBridge could not start its window: "+err.Error()+
			"\n\nIt needs the Microsoft Edge WebView2 runtime, which is built into Windows 11 and current Windows 10.")
		log.Fatal(err)
	}
	systray.Quit()
}

// runTray shows the tray icon. Left-click opens the window; right-click shows
// the menu.
func runTray(app *App) {
	runtime.LockOSThread()
	systray.Run(func() {
		ico := runtime.GOOS == "windows"
		iconOn := makeIcon(color.NRGBA{0x7c, 0x5c, 0xff, 0xff}, ico)
		iconOff := makeIcon(color.NRGBA{0x80, 0x80, 0x80, 0xff}, ico)
		iconPaused := makeIcon(color.NRGBA{0xe0, 0xa0, 0x30, 0xff}, ico)
		systray.SetIcon(iconOff)
		systray.SetTooltip("VoidBridge")
		systray.SetOnTapped(app.Show)

		mOpen := systray.AddMenuItem("Open VoidBridge", "")
		mStatus := systray.AddMenuItem("Starting…", "")
		mStatus.Disable()
		systray.AddSeparator()
		mPause := systray.AddMenuItemCheckbox("Pause syncing", "", app.cfg.Paused)
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit VoidBridge", "")

		app.onState = func(st State) {
			text := describe(st)
			mStatus.SetTitle(text)
			systray.SetTooltip("VoidBridge — " + text)
			switch {
			case st.Paused:
				systray.SetIcon(iconPaused)
			case len(st.Devices) > 0:
				systray.SetIcon(iconOn)
			default:
				systray.SetIcon(iconOff)
			}
			if st.Paused {
				mPause.Check()
			} else {
				mPause.Uncheck()
			}
		}
		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					app.Show()
				case <-mPause.ClickedCh:
					app.SetPaused(!app.cfg.Paused)
				case <-mQuit.ClickedCh:
					app.Quit()
					return
				}
			}
		}()
	}, nil)
}

func describe(st State) string {
	switch {
	case st.Mode == config.ModeNone:
		return "Not set up yet"
	case st.Paused:
		return "Paused"
	case len(st.Devices) == 1:
		return "Syncing with " + st.Devices[0].Name
	case len(st.Devices) > 1:
		names := make([]string, len(st.Devices))
		for i, d := range st.Devices {
			names[i] = d.Name
		}
		return fmt.Sprintf("Syncing with %d devices (%s)", len(st.Devices), strings.Join(names, ", "))
	default:
		return "Waiting for your other devices"
	}
}
