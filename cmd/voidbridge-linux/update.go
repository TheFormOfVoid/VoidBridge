//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/update"
)

func assetName() string {
	arch := runtime.GOARCH
	if arch == "arm" {
		arch = "armv7"
	}
	return "voidbridge-linux-" + arch
}

// updateLoop checks for a new stable release a minute after start and every
// 6 hours, and installs it (unless automatic updates are off) when no files
// are being sent or received.
func (d *daemon) updateLoop() {
	if !update.Supported(version) {
		return // dev builds don't update
	}
	time.Sleep(time.Minute)
	for {
		if rel := d.checkUpdate(); rel != nil && !d.config().NoAutoUpdate {
			for d.busy() {
				time.Sleep(time.Minute)
			}
			if err := d.install(rel); err != nil {
				log.Printf("update: %v", err)
			}
		}
		time.Sleep(6 * time.Hour)
	}
}

func (d *daemon) checkUpdate() *update.Release {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rel, err := update.Latest(ctx)
	if err != nil {
		log.Printf("update check: %v", err)
		return nil
	}
	if !update.Newer(rel.Version, version) {
		return nil
	}
	d.mu.Lock()
	d.update = rel.Version
	d.mu.Unlock()
	log.Printf("update available: %s", rel.Version)
	return rel
}

// updateNow is `voidbridge update`.
func (d *daemon) updateNow() (string, error) {
	if !update.Supported(version) {
		return "This is a development build, which doesn't update itself.", nil
	}
	rel := d.checkUpdate()
	if rel == nil {
		return "You have the latest version (" + version + ").", nil
	}
	// Reply first; installing restarts this process.
	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := d.install(rel); err != nil {
			log.Printf("update: %v", err)
			notify("Couldn't update VoidBridge", err.Error())
		}
	}()
	return "Installing " + rel.Version + "; VoidBridge restarts in a moment.", nil
}

// install downloads the new binary, checks it, puts it in place of this one
// and restarts into it.
func (d *daemon) install(rel *update.Release) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	tmp := exe + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("can't write to %s; update by running the install command again", filepath.Dir(exe))
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	err = update.Fetch(ctx, rel, assetName(), f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil { // replacing a running binary is fine on Linux
		os.Remove(tmp)
		return err
	}
	log.Printf("installed %s, restarting", rel.Version)
	d.shutdown()
	os.Remove(socketPath())
	time.Sleep(100 * time.Millisecond)
	// Same process id, so systemd keeps tracking it. Go opens files
	// close-on-exec, so the socket and connections don't leak into it.
	return syscall.Exec(exe, os.Args, os.Environ())
}
