package main

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/config"
	"github.com/TheFormOfVoid/VoidBridge/internal/files"
	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// Transfer is one file being sent or received, for the UI.
type Transfer struct {
	ID       string `json:"id"`
	Incoming bool   `json:"incoming"`
	Name     string `json:"name"`
	Device   string `json:"device"`
	Size     int64  `json:"size"`
	Done     int64  `json:"done"`
	State    string `json:"state"` // "active", "done", "failed"
	Error    string `json:"error,omitempty"`
	Path     string `json:"path,omitempty"`
	Time     int64  `json:"time"`
}

type transfers struct {
	mu       sync.Mutex
	list     []*Transfer // newest first
	onChange func()
}

func (t *transfers) add(tr *Transfer) *Transfer {
	t.mu.Lock()
	tr.Time = time.Now().UnixMilli()
	t.list = append([]*Transfer{tr}, t.list...)
	if len(t.list) > 30 {
		t.list = t.list[:30]
	}
	t.mu.Unlock()
	t.onChange()
	return tr
}

func (t *transfers) update(f func()) {
	t.mu.Lock()
	f()
	t.mu.Unlock()
	t.onChange()
}

func (t *transfers) find(id string) *Transfer {
	for _, tr := range t.list {
		if tr.ID == id {
			return tr
		}
	}
	return nil
}

func (t *transfers) snapshot() []Transfer {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Transfer, len(t.list))
	for i, tr := range t.list {
		out[i] = *tr
	}
	return out
}

// busy reports whether a file is being sent or received.
func (s *Service) busy() bool {
	s.xfers.mu.Lock()
	defer s.xfers.mu.Unlock()
	for _, t := range s.xfers.list {
		if t.State == "active" {
			return true
		}
	}
	return false
}

// receiver returns the node.FileReceiver for this service.
func (s *Service) receiver() node.FileReceiver {
	return &files.DirReceiver{
		Dir: s.cfg.ReceiveFolder,
		OnProgress: func(id string, meta protocol.FileMeta, from node.FileSender, done int64) {
			s.xfers.mu.Lock()
			tr := s.xfers.find("in-" + id)
			s.xfers.mu.Unlock()
			if tr == nil {
				s.xfers.add(&Transfer{ID: "in-" + id, Incoming: true, Name: meta.Name, Device: from.Name, Size: meta.Size, Done: done, State: "active"})
				return
			}
			s.xfers.update(func() { tr.Done = done })
		},
		OnReceived: func(e files.Event) {
			// Small files finish before any progress was reported; match by name.
			s.xfers.mu.Lock()
			var tr *Transfer
			for _, x := range s.xfers.list {
				if x.Incoming && x.State == "active" && x.Name == e.Meta.Name && x.Device == e.From.Name {
					tr = x
					break
				}
			}
			s.xfers.mu.Unlock()
			if tr == nil {
				tr = s.xfers.add(&Transfer{ID: "in-" + protocol.NewID(), Incoming: true, Name: e.Meta.Name, Device: e.From.Name, Size: e.Meta.Size, State: "active"})
			}
			s.xfers.update(func() {
				if e.Err != nil {
					tr.State, tr.Error = "failed", e.Err.Error()
					return
				}
				tr.State, tr.Done, tr.Path, tr.Name = "done", tr.Size, e.Path, filepath.Base(e.Path)
			})
			if e.Err == nil {
				log.Printf("received %s from %s", e.Path, e.From.Name)
				if !s.cfg.NoClipReceived {
					clipboard.WriteFiles([]string{e.Path})
				}
			}
		},
	}
}

// SendFiles sends local files to a device, one after another. If the device
// isn't connected yet (e.g. VoidBridge was just started from the right-click
// menu), it waits a little for it.
func (s *Service) SendFiles(deviceID string, paths []string) error {
	name := deviceID
	if d, ok := s.cfg.KnownDevices()[deviceID]; ok {
		name = d.Name
	}
	var n *node.Node
	for i := 0; i < 40; i++ { // up to 20 s
		s.mu.Lock()
		n = s.node
		s.mu.Unlock()
		if n != nil {
			for _, d := range n.Status().Devices {
				if d.ID == deviceID {
					goto ready
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if n == nil {
		return errors.New("VoidBridge isn't set up yet")
	}
	return fmt.Errorf("%s isn't connected right now", name)

ready:
	var firstErr error
	for _, p := range paths {
		tr := s.xfers.add(&Transfer{ID: "out-" + protocol.NewID(), Name: filepath.Base(p), Device: name, State: "active"})
		var last time.Time
		err := files.Send(n, nil, deviceID, p, func(sent, total int64) {
			if time.Since(last) < 100*time.Millisecond && sent != total {
				return
			}
			last = time.Now()
			s.xfers.update(func() { tr.Done, tr.Size = sent, total })
		})
		s.xfers.update(func() {
			if err != nil {
				tr.State, tr.Error = "failed", err.Error()
			} else {
				tr.State, tr.Done, tr.Path = "done", tr.Size, p
			}
		})
		if err != nil {
			log.Printf("sending %s to %s: %v", p, name, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("couldn't send %s to %s: %w", filepath.Base(p), name, err)
			}
		}
	}
	return firstErr
}

// rememberDevices records connected devices for the right-click menu and
// refreshes the menu when the list changes.
func (s *Service) rememberDevices(devs []node.Device) {
	now := time.Now().Unix()
	menu, dirty := false, false
	for _, d := range devs {
		m, dd := s.cfg.Remember(d.ID, d.Name, d.Kind, now)
		menu, dirty = menu || m, dirty || dd
	}
	if s.cfg.ForgetOlderThan(now - 60*24*3600) {
		menu, dirty = true, true
	}
	if dirty {
		s.cfg.Save()
	}
	if menu {
		s.syncMenu()
	}
}

// ForgetDevice removes a device from the right-click menu.
func (s *Service) ForgetDevice(id string) {
	s.cfg.Forget(id)
	s.cfg.Save()
	s.syncMenu()
}

// syncMenu writes the right-click menu for the current settings.
func (s *Service) syncMenu() {
	var devs []menuDevice
	for id, d := range s.cfg.KnownDevices() {
		devs = append(devs, menuDevice{ID: id, Name: d.Name, Kind: d.Kind})
	}
	remove := s.cfg.HideContextMenu || s.cfg.Mode == config.ModeNone
	sort.Slice(devs, func(i, j int) bool { return devs[i].Name < devs[j].Name })
	if err := setContextMenu(remove, devs); err != nil {
		log.Printf("right-click menu: %v", err)
	}
}
