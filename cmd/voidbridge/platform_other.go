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
