//go:build !windows

package main

import (
	"context"
	"errors"
	"log"

	"github.com/TheFormOfVoid/VoidBridge/internal/update"
)

func showMessage(title, text string) { log.Printf("%s: %s", title, text) }

func autostartEnabled() bool { return false }

func setAutostart(bool) error { return errors.New("start at login is only supported on Windows") }

func openFile(path string) { log.Printf("log file: %s", path) }

type menuDevice struct {
	ID, Name, Kind string
}

func setContextMenu(remove bool, devices []menuDevice) error { return nil }

func revealInExplorer(path string) { log.Printf("reveal %s", path) }

func openWithDefaultApp(path string) { log.Printf("open %s", path) }

func selfUpdateSupported() bool { return false }

func downloadUpdate(context.Context, *update.Release) (string, error) {
	return "", errors.New("updates are only supported on Windows")
}

func installUpdate(string, bool) error { return errors.New("updates are only supported on Windows") }

func cleanupUpdate() {}

func waitForExit(int) {}

func windowVisible() bool { return true }
