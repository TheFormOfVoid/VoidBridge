//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// The "Send with VoidBridge ▸ device" entry in Explorer's right-click menu
// for files. It lives in the classic menu (on Windows 11 under "Show more
// options"), stored per user under HKCU so no admin rights are needed.
const menuKey = `Software\Classes\*\shell\VoidBridge`

// menuDevice is one entry in the submenu.
type menuDevice struct {
	ID, Name, Kind string
}

func deleteTree(root registry.Key, path string) error {
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err == registry.ErrNotExist {
		return nil
	}
	if err != nil {
		return err
	}
	subs, _ := k.ReadSubKeyNames(-1)
	k.Close()
	for _, s := range subs {
		if err := deleteTree(root, path+`\`+s); err != nil {
			return err
		}
	}
	return registry.DeleteKey(root, path)
}

// setContextMenu installs (or with devices == nil and remove, deletes) the menu.
func setContextMenu(remove bool, devices []menuDevice) error {
	if err := deleteTree(registry.CURRENT_USER, menuKey); err != nil {
		return err
	}
	if remove {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	root, _, err := registry.CreateKey(registry.CURRENT_USER, menuKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	root.SetStringValue("MUIVerb", "Send with VoidBridge")
	root.SetStringValue("Icon", exe+",0")
	root.SetStringValue("SubCommands", "")
	root.Close()

	add := func(n int, label, command string) error {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, fmt.Sprintf(`%s\shell\%02d`, menuKey, n), registry.SET_VALUE)
		if err != nil {
			return err
		}
		k.SetStringValue("MUIVerb", label)
		k.Close()
		c, _, err := registry.CreateKey(registry.CURRENT_USER, fmt.Sprintf(`%s\shell\%02d\command`, menuKey, n), registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer c.Close()
		return c.SetStringValue("", command)
	}
	if len(devices) == 0 {
		return add(0, "No devices yet (open VoidBridge)", `"`+exe+`"`)
	}
	for i, d := range devices {
		label := d.Name
		if d.Kind == "android" {
			label += "  📱"
		}
		label = strings.ReplaceAll(label, "&", "&&") // & marks a shortcut key in menus
		if err := add(i, label, fmt.Sprintf(`"%s" --send "%s" "%%1"`, exe, d.ID)); err != nil {
			return err
		}
	}
	return nil
}

// revealInExplorer opens Explorer with the file selected.
func revealInExplorer(path string) {
	// Explorer wants /select,"C:\a b\c.pdf" exactly, which Go's usual
	// argument quoting would break for paths with spaces.
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	cmd.Start()
}

// openWithDefaultApp opens a file or folder with its default program.
func openWithDefaultApp(path string) {
	startProcess("explorer.exe", path)
}
