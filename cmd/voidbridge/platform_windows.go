//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func showMessage(title, text string) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	const mbOK, mbIconInfo, mbSetForeground = 0x0, 0x40, 0x10000
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(
		0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|mbIconInfo|mbSetForeground)
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("VoidBridge")
	exe, _ := os.Executable()
	return err == nil && v == `"`+exe+`"`
}

func setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		return k.DeleteValue("VoidBridge")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue("VoidBridge", `"`+exe+`"`)
}

func openFile(path string) {
	exec.Command("notepad.exe", path).Start()
}
