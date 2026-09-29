//go:build !windows

package main

import (
	"errors"
	"log"
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
