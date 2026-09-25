package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/config"
	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/peer"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
	"github.com/TheFormOfVoid/VoidBridge/internal/relay"
)

// Service runs sync according to the config and rebuilds it when the config
// changes.
type Service struct {
	cfg  *config.Config
	cb   clipboard.Clipboard
	hist *node.History
	// OnUpdate is called (from any goroutine) when anything visible changed.
	OnUpdate func()

	mu        sync.Mutex
	stop      chan struct{}
	node      *node.Node
	peers     *peer.Manager
	peerErr   string
	relay     relay.State
	signedOut bool
}

func NewService(cfg *config.Config) *Service {
	h := node.NewHistory()
	h.SetEnabled(cfg.History)
	return &Service{cfg: cfg, cb: clipboard.System(), hist: h}
}

func (s *Service) identity() protocol.Identity {
	name := s.cfg.DeviceName
	if name == "" {
		name, _ = os.Hostname()
	}
	return protocol.Identity{ID: s.cfg.DeviceID, Name: name, Kind: "windows"}
}

func (s *Service) update() {
	if s.OnUpdate != nil {
		s.OnUpdate()
	}
}

// Restart stops whatever is running and starts again from the config.
func (s *Service) Restart() {
	s.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peerErr, s.relay, s.signedOut = "", relay.State{}, false

	keys, ok := s.cfg.Keys()
	if !ok {
		s.update()
		return
	}
	stop := make(chan struct{})
	s.stop = stop
	n := node.New(s.identity(), keys, s.cb, s.hist)
	n.OnChange = s.update
	n.SetSettings(node.Settings{Paused: s.cfg.Paused, SkipSensitive: s.cfg.SkipSensitive})
	s.node = n
	go n.Run(stop)

	if s.cfg.Direct {
		m := peer.NewManager(n, keys, s.cfg.Port)
		m.SetManual(s.cfg.Manual)
		if s.cfg.Tailscale {
			m.Tailscale = peer.TailscalePeers
		}
		s.peers = m
		go func() {
			if err := m.Run(stop); err != nil {
				log.Printf("direct links: %v", err)
				s.mu.Lock()
				s.peerErr = fmt.Sprintf("Can't listen on port %d (%v). Is VoidBridge already running?", s.cfg.Port, err)
				s.mu.Unlock()
				s.update()
			}
		}()
	}

	if s.cfg.Mode == config.ModeAccount && s.cfg.Token != "" {
		c := &relay.Client{Base: s.cfg.Server, Token: s.cfg.Token, Node: n}
		c.OnState = func(st relay.State) {
			s.mu.Lock()
			s.relay = st
			s.mu.Unlock()
			s.update()
		}
		c.OnUnauthorized = func() {
			s.mu.Lock()
			s.signedOut = true
			s.mu.Unlock()
			s.update()
		}
		go c.Run(stop)
	}
	s.update()
}

// Stop halts sync.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	s.node, s.peers = nil, nil
	time.Sleep(50 * time.Millisecond) // let listeners release their ports
}

// ApplySettings pushes runtime settings without a restart.
func (s *Service) ApplySettings() {
	s.mu.Lock()
	n := s.node
	s.mu.Unlock()
	s.hist.SetEnabled(s.cfg.History)
	if n != nil {
		n.SetSettings(node.Settings{Paused: s.cfg.Paused, SkipSensitive: s.cfg.SkipSensitive})
	}
	s.update()
}

// SetManual updates typed-in device addresses without a restart.
func (s *Service) SetManual(addrs []string) {
	s.mu.Lock()
	m := s.peers
	s.mu.Unlock()
	if m != nil {
		m.SetManual(addrs)
	}
}

// Snapshot is the sync state for the UI.
type Snapshot struct {
	Node       node.Status
	PeerErr    string
	Relay      relay.State
	SignedOut  bool
	Running    bool
	LocalIPs   []string
	TSDetected bool
}

func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	n := s.node
	snap := Snapshot{PeerErr: s.peerErr, Relay: s.relay, SignedOut: s.signedOut, Running: n != nil}
	s.mu.Unlock()
	if n != nil {
		snap.Node = n.Status()
	}
	snap.LocalIPs = peer.LocalIPs()
	return snap
}

// Recopy puts a history item back on the clipboard.
func (s *Service) Recopy(id string) error {
	it := s.hist.Get(id)
	if it == nil {
		return errors.New("that item is no longer in history")
	}
	s.mu.Lock()
	n := s.node
	s.mu.Unlock()
	if n == nil {
		return s.cb.Write(it.Content)
	}
	return n.Recopy(it.Content)
}
