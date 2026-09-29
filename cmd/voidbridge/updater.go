package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/update"
)

// UpdateInfo describes a newer version, for the UI.
type UpdateInfo struct {
	Version string `json:"version"`
	Page    string `json:"page"`
	State   string `json:"state"` // "available", "downloading", "ready", "installing", "failed"
	Error   string `json:"error,omitempty"`
}

type updater struct {
	mu   sync.Mutex
	dl   sync.Mutex // one download at a time
	rel  *update.Release
	info *UpdateInfo
	file string // downloaded and verified
}

func (a *App) updateInfo() *UpdateInfo {
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	if a.upd.info == nil {
		return nil
	}
	i := *a.upd.info
	return &i
}

func (a *App) setUpdate(f func(u *updater)) {
	a.upd.mu.Lock()
	f(&a.upd)
	a.upd.mu.Unlock()
	a.changed()
}

// updateLoop checks for a new stable release at startup and every 6 hours.
// With automatic updates on, it downloads it and installs it once the window
// is closed and no files are being sent or received.
func (a *App) updateLoop() {
	if !update.Supported(version) || !selfUpdateSupported() {
		return // dev builds don't update
	}
	time.Sleep(time.Minute)
	for {
		a.checkUpdate()
		for i := 0; i < 6*60; i++ {
			time.Sleep(time.Minute)
			a.autoInstall()
		}
	}
}

func (a *App) checkUpdate() (*UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rel, err := update.Latest(ctx)
	if err != nil {
		log.Printf("update check: %v", err)
		return nil, err
	}
	if !update.Newer(rel.Version, version) {
		return nil, nil
	}
	a.setUpdate(func(u *updater) {
		if u.info == nil || u.info.Version != rel.Version {
			log.Printf("update available: %s", rel.Version)
			u.rel, u.file = rel, ""
			u.info = &UpdateInfo{Version: rel.Version, Page: rel.Page, State: "available"}
		}
	})
	if !a.cfg.NoAutoUpdate {
		go a.downloadUpdate()
	}
	return a.updateInfo(), nil
}

func (a *App) downloadUpdate() error {
	a.upd.dl.Lock()
	defer a.upd.dl.Unlock()
	a.upd.mu.Lock()
	rel, done := a.upd.rel, a.upd.file != ""
	a.upd.mu.Unlock()
	if rel == nil {
		return errors.New("no update to download")
	}
	if done {
		return nil
	}
	a.setUpdate(func(u *updater) { u.info.State, u.info.Error = "downloading", "" })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	path, err := downloadUpdate(ctx, rel)
	a.setUpdate(func(u *updater) {
		if err != nil {
			u.info.State, u.info.Error = "failed", err.Error()
		} else {
			u.file, u.info.State = path, "ready"
		}
	})
	if err != nil {
		log.Printf("update download: %v", err)
	}
	return err
}

func (a *App) autoInstall() {
	a.upd.mu.Lock()
	ready := a.upd.file != ""
	a.upd.mu.Unlock()
	if !ready || a.cfg.NoAutoUpdate || windowVisible() || a.svc.busy() {
		return
	}
	a.installUpdate(true)
}

func (a *App) installUpdate(hidden bool) error {
	var file string
	a.setUpdate(func(u *updater) {
		file = u.file
		if file != "" {
			u.info.State = "installing"
		}
	})
	if file == "" {
		return errors.New("the update isn't downloaded yet")
	}
	log.Printf("installing %s", a.updateInfo().Version)
	if err := installUpdate(file, hidden); err != nil {
		a.setUpdate(func(u *updater) { u.file, u.info.State, u.info.Error = "", "failed", err.Error() })
		return fmt.Errorf("couldn't install the update: %w", err)
	}
	a.Quit()
	return nil
}

// CheckForUpdates checks now; the result also shows up in State.
func (a *App) CheckForUpdates() (string, error) {
	if !update.Supported(version) {
		return "This is a development build, which doesn't update itself.", nil
	}
	info, err := a.checkUpdate()
	if err != nil {
		return "", err
	}
	if info == nil {
		return "You have the latest version (" + version + ").", nil
	}
	return "", nil
}

// InstallUpdate downloads the update if needed and restarts into it.
func (a *App) InstallUpdate() error {
	if err := a.downloadUpdate(); err != nil {
		return err
	}
	return a.installUpdate(false)
}

// SetAutoUpdate turns automatic updates on or off.
func (a *App) SetAutoUpdate(on bool) error {
	a.cfg.NoAutoUpdate = !on
	if err := a.save(); err != nil {
		return err
	}
	if on && a.updateInfo() != nil {
		go a.downloadUpdate()
	}
	a.changed()
	return nil
}
