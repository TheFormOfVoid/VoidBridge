//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The "Send with VoidBridge ▸ device" entry in the file manager's right-click
// menu, as FreeDesktop file-manager actions (PCManFM on Raspberry Pi OS reads
// them from ~/.local/share/file-manager/actions).

type menuDevice struct {
	ID, Name, Kind string
}

func actionsDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "file-manager", "actions")
}

// clean keeps a value on one line of a .desktop file.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// quoteExec quotes an argument for a desktop entry's Exec key. Characters
// that need escaping in Exec (and again in the file) aren't allowed at all.
func quoteExec(s string) (string, bool) {
	if strings.ContainsAny(s, "\"`$%\\\n\r") {
		return "", false
	}
	return `"` + s + `"`, true
}

// writeMenu installs the menu, or removes it.
func writeMenu(remove bool, devices []menuDevice) error {
	dir := actionsDir()
	old, _ := filepath.Glob(filepath.Join(dir, "voidbridge*.desktop"))
	for _, f := range old {
		os.Remove(f)
	}
	if remove {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	qexe, ok := quoteExec(exe)
	if !ok {
		return fmt.Errorf("can't add the menu for a program at %q; move it to ~/.local/bin", exe)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	write := func(name, body string) error {
		return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	var items []string
	for i, d := range devices {
		qid, ok := quoteExec(d.ID)
		if !ok {
			continue
		}
		id := fmt.Sprintf("voidbridge-send-%02d", i)
		items = append(items, id)
		label := clean(d.Name)
		if d.Kind == "android" {
			label += " 📱"
		}
		err := write(id+".desktop", fmt.Sprintf(`[Desktop Entry]
Type=Action
Name=%s
Tooltip=Send to %s with VoidBridge
Icon=document-send
Profiles=files;

[X-Action-Profile files]
MimeTypes=all/allfiles;
SelectionCount=>0
Exec=%s send %s %%F
`, label, label, qexe, qid))
		if err != nil {
			return err
		}
	}
	if len(items) == 0 {
		// Nothing to send to yet; say so rather than show an empty menu.
		items = append(items, "voidbridge-none")
		if err := write("voidbridge-none.desktop", fmt.Sprintf(`[Desktop Entry]
Type=Action
Name=No devices yet (run voidbridge status)
Profiles=files;

[X-Action-Profile files]
MimeTypes=all/allfiles;
Exec=%s status
`, qexe)); err != nil {
			return err
		}
	}
	return write("voidbridge.desktop", fmt.Sprintf(`[Desktop Entry]
Type=Menu
Name=Send with VoidBridge
Icon=document-send
ItemsList=%s;
`, strings.Join(items, ";")))
}

// sessionEnv adds what desktop tools need when VoidBridge was started outside
// the desktop session (e.g. by systemd).
func sessionEnv() []string {
	env := os.Environ()
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		run = fmt.Sprintf("/run/user/%d", os.Getuid())
		env = append(env, "XDG_RUNTIME_DIR="+run)
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		if _, err := os.Stat(filepath.Join(run, "bus")); err == nil {
			env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(run, "bus"))
		}
	}
	return env
}

// notify shows a desktop notification, if the desktop has notifications.
func notify(title, body string) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return
	}
	cmd := exec.Command("notify-send", "--app-name=VoidBridge", "--icon=document-send", title, body)
	cmd.Env = sessionEnv()
	cmd.Start()
	go cmd.Wait()
}
