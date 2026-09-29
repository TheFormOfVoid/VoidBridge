//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/TheFormOfVoid/VoidBridge/internal/update"
)

// A running exe can be renamed but not overwritten, so an update renames it
// to VoidBridge.exe.old, puts the new one in its place, starts it and quits.
// The new process waits for the old one to exit before it starts up.

func exePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return exe, nil
}

func selfUpdateSupported() bool { return true }

// downloadUpdate saves the new exe next to the running one and verifies it.
func downloadUpdate(ctx context.Context, rel *update.Release) (string, error) {
	exe, err := exePath()
	if err != nil {
		return "", err
	}
	tmp := exe + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return "", fmt.Errorf("can't write to %s. Move VoidBridge to a folder you own, or update it by hand", filepath.Dir(exe))
		}
		return "", err
	}
	err = update.Fetch(ctx, rel, "VoidBridge-windows-"+runtime.GOARCH+".exe", f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// installUpdate swaps in the downloaded exe and starts it. The caller quits.
func installUpdate(newExe string, hidden bool) error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(newExe, exe); err != nil {
		os.Rename(old, exe)
		return err
	}
	args := []string{"--wait-pid", strconv.Itoa(os.Getpid())}
	if hidden {
		args = append(args, "--hidden")
	}
	if err := exec.Command(exe, args...).Start(); err != nil {
		os.Rename(exe, newExe)
		os.Rename(old, exe)
		return err
	}
	return nil
}

// cleanupUpdate removes what an earlier update left behind.
func cleanupUpdate() {
	if exe, err := exePath(); err == nil {
		os.Remove(exe + ".old")
		os.Remove(exe + ".new")
	}
}

// waitForExit waits (up to 30 s) for the process that started this update.
func waitForExit(pid int) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	windows.WaitForSingleObject(h, 30_000)
}

var procIsIconic = windows.NewLazySystemDLL("user32.dll").NewProc("IsIconic")

// windowVisible reports whether one of our windows is open on screen (not in
// the tray and not minimised), meaning someone may be using it.
func windowVisible() bool {
	me := uint32(os.Getpid())
	visible := false
	cb := syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var pid uint32
		windows.GetWindowThreadProcessId(hwnd, &pid)
		if pid == me && windows.IsWindowVisible(hwnd) {
			if r, _, _ := procIsIconic.Call(uintptr(hwnd)); r == 0 {
				visible = true
				return 0
			}
		}
		return 1
	})
	windows.EnumWindows(cb, unsafe.Pointer(nil))
	return visible
}
