// Package node is the heart of VoidBridge: it watches the local clipboard and
// exchanges clips with every connected link (other devices directly, or a
// relay server).
//
// Sync is flooding with de-duplication: a new clip is sent to every link; a
// device that receives a clip newer than its current one applies it and
// forwards it to its other links; clip ids already seen are dropped. Any
// topology — a mesh of devices, a star around a server, or both — converges
// on the newest clip. When a link comes up both sides send their current clip,
// which is how devices catch up after being offline.
package node

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

var PollInterval = 250 * time.Millisecond

// Link is one connection a clip can travel over.
type Link interface {
	Send(m *protocol.Message) error
	Close()
	Info() LinkInfo
}

// LinkInfo describes a link for the UI.
type LinkInfo struct {
	PeerID string // device id, or "server"
	Name   string
	Kind   string // device kind ("windows", "android") or "server"
	Via    string // "Wi-Fi", "Tailscale", "Server"
	Addr   string
}

// Device is a device we can currently reach, for the UI.
type Device struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Kind string   `json:"kind"`
	Via  []string `json:"via"`
}

// Settings the user can change at runtime.
type Settings struct {
	Paused        bool
	SkipSensitive bool
}

// Node syncs one clipboard with a set of links.
type Node struct {
	me   protocol.Identity
	keys protocol.Keys
	cb   clipboard.Clipboard
	hist *History

	OnChange func() // links/devices/current clip changed
	Logf     func(string, ...any)

	mu        sync.Mutex
	settings  Settings
	links     map[Link]struct{}
	remote    map[Link][]protocol.PeerInfo // devices reachable through a relay link
	current   *protocol.Message
	seen      map[string]struct{}
	seenOrder []string
	lastHash  string
	lastSeq   uint64
	lastSync  time.Time
}

// New creates a node. hist may be nil.
func New(me protocol.Identity, keys protocol.Keys, cb clipboard.Clipboard, hist *History) *Node {
	return &Node{
		me: me, keys: keys, cb: cb, hist: hist,
		Logf:     log.Printf,
		links:    map[Link]struct{}{},
		remote:   map[Link][]protocol.PeerInfo{},
		seen:     map[string]struct{}{},
		settings: Settings{SkipSensitive: true},
	}
}

// Identity returns the local device identity.
func (n *Node) Identity() protocol.Identity { return n.me }

// SetSettings updates runtime settings.
func (n *Node) SetSettings(s Settings) {
	n.mu.Lock()
	n.settings = s
	n.mu.Unlock()
	n.changed()
}

// Run watches the clipboard until stop closes.
func (n *Node) Run(stop <-chan struct{}) {
	if seq, err := n.cb.Seq(); err == nil {
		n.lastSeq = seq
	}
	// Whatever is on the clipboard at startup is not a new copy.
	if c, err := n.cb.Read(); err == nil && c != nil {
		n.lastHash = c.Hash()
	}
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			n.poll()
		}
	}
}

// AddLink registers a new connection and sends it our current clip.
func (n *Node) AddLink(l Link) {
	n.mu.Lock()
	n.links[l] = struct{}{}
	cur := n.current
	n.mu.Unlock()
	if cur != nil {
		go n.send(l, cur)
	}
	n.changed()
}

// RemoveLink forgets a connection.
func (n *Node) RemoveLink(l Link) {
	n.mu.Lock()
	delete(n.links, l)
	delete(n.remote, l)
	n.mu.Unlock()
	n.changed()
}

// SetRemoteDevices records which devices a relay link can reach.
func (n *Node) SetRemoteDevices(l Link, devs []protocol.PeerInfo) {
	n.mu.Lock()
	if _, ok := n.links[l]; ok {
		n.remote[l] = devs
	}
	n.mu.Unlock()
	n.changed()
}

// Handle processes a clip message received on l.
func (n *Node) Handle(l Link, m *protocol.Message) {
	if m.Type != protocol.TypeClip {
		return
	}
	n.mu.Lock()
	if n.settings.Paused {
		n.mu.Unlock()
		return
	}
	if _, dup := n.seen[m.ID]; dup || !m.Newer(n.current) {
		n.markSeen(m.ID)
		n.mu.Unlock()
		return
	}
	n.markSeen(m.ID)
	n.mu.Unlock()

	plain, err := protocol.OpenClip(n.keys.Content, m)
	if err != nil {
		n.Logf("dropping clip %s from %s: %v", m.ID, l.Info().Name, err)
		return
	}
	c, ok := contentFrom(m, plain)
	if !ok {
		return
	}

	n.mu.Lock()
	if !m.Newer(n.current) { // something newer arrived while decrypting
		n.mu.Unlock()
		return
	}
	n.current = m
	h := c.Hash()
	apply := h != n.lastHash
	n.lastHash = h
	n.lastSync = time.Now()
	others := n.linksExcept(l)
	n.mu.Unlock()

	for _, o := range others {
		go n.send(o, m)
	}
	if apply {
		if err := n.cb.Write(c); err != nil {
			n.Logf("writing clipboard: %v", err)
		} else if seq, err := n.cb.Seq(); err == nil {
			n.mu.Lock()
			n.lastSeq = seq
			n.mu.Unlock()
		}
	}
	if n.hist != nil {
		n.hist.Add(m, c, false)
	}
	n.changed()
}

func (n *Node) poll() {
	seq, err := n.cb.Seq()
	if err != nil {
		return
	}
	n.mu.Lock()
	if seq == n.lastSeq {
		n.mu.Unlock()
		return
	}
	n.lastSeq = seq
	n.mu.Unlock()

	c, err := n.cb.Read()
	if err != nil || c == nil {
		return
	}
	n.LocalCopy(c)
}

// LocalCopy handles content the user copied on this device. Platforms that
// learn about copies some other way (Android) call it directly.
func (n *Node) LocalCopy(c *clipboard.Content) {
	if !valid(c) {
		return
	}
	h := c.Hash()
	n.mu.Lock()
	if h == n.lastHash {
		n.mu.Unlock()
		return // our own write echoing back, or the same thing copied again
	}
	n.lastHash = h
	if n.settings.Paused || (c.Sensitive && n.settings.SkipSensitive) {
		n.mu.Unlock()
		return
	}
	now := time.Now().UnixMilli()
	if n.current != nil && now <= n.current.Time {
		now = n.current.Time + 1 // never lose to a clip stamped by a fast clock
	}
	m := &protocol.Message{
		Type: protocol.TypeClip, ID: protocol.NewID(), Origin: n.me.ID, OriginName: n.me.Name,
		Time: now, ClipType: c.Type, Mime: c.Mime,
	}
	n.mu.Unlock()

	protocol.SealClip(n.keys.Content, m, c.Bytes())

	n.mu.Lock()
	n.current = m
	n.markSeen(m.ID)
	targets := n.linksExcept(nil)
	if len(targets) > 0 {
		n.lastSync = time.Now()
	}
	n.mu.Unlock()
	for _, l := range targets {
		go n.send(l, m)
	}
	if n.hist != nil {
		n.hist.Add(m, c, true)
	}
	n.changed()
}

// Recopy puts an old clip back on the clipboard (from history); the normal
// change detection then syncs it as a new copy.
func (n *Node) Recopy(c *clipboard.Content) error {
	if err := n.cb.Write(c); err != nil {
		return err
	}
	n.poll()
	return nil
}

func valid(c *clipboard.Content) bool {
	switch c.Type {
	case protocol.ClipText:
		return c.Text != "" && len(c.Text) <= protocol.MaxText
	case protocol.ClipImage:
		return len(c.Data) > 0 && len(c.Data) <= protocol.MaxImage
	}
	return false
}

func contentFrom(m *protocol.Message, plain []byte) (*clipboard.Content, bool) {
	c := &clipboard.Content{Type: m.ClipType, Mime: m.Mime}
	switch m.ClipType {
	case protocol.ClipText:
		c.Text = string(plain)
	case protocol.ClipImage:
		c.Data = plain
	default:
		return nil, false // a newer app's clip type we don't understand
	}
	return c, valid(c)
}

func (n *Node) send(l Link, m *protocol.Message) {
	if err := l.Send(m); err != nil {
		l.Close()
	}
}

func (n *Node) linksExcept(except Link) []Link {
	var out []Link
	for l := range n.links {
		if l != except {
			out = append(out, l)
		}
	}
	return out
}

func (n *Node) markSeen(id string) {
	if _, ok := n.seen[id]; ok {
		return
	}
	n.seen[id] = struct{}{}
	n.seenOrder = append(n.seenOrder, id)
	if len(n.seenOrder) > 4096 {
		delete(n.seen, n.seenOrder[0])
		n.seenOrder = n.seenOrder[1:]
	}
}

func (n *Node) changed() {
	if n.OnChange != nil {
		n.OnChange()
	}
}

// Status is a snapshot for the UI.
type Status struct {
	Links    []LinkInfo
	Devices  []Device // every device we can reach, directly or via a server
	LastSync time.Time
	Settings Settings
}

// Status returns the current state.
func (n *Node) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := Status{LastSync: n.lastSync, Settings: n.settings}
	devs := map[string]*Device{}
	add := func(id, name, kind, via string) {
		if id == n.me.ID || id == "" {
			return
		}
		d := devs[id]
		if d == nil {
			d = &Device{ID: id, Name: name, Kind: kind}
			devs[id] = d
		}
		for _, v := range d.Via {
			if v == via {
				return
			}
		}
		d.Via = append(d.Via, via)
	}
	for l := range n.links {
		info := l.Info()
		st.Links = append(st.Links, info)
		if info.Kind != "server" {
			add(info.PeerID, info.Name, info.Kind, info.Via)
		}
	}
	for _, list := range n.remote {
		for _, p := range list {
			add(p.ID, p.Name, p.Kind, "Server")
		}
	}
	for _, d := range devs {
		st.Devices = append(st.Devices, *d)
	}
	sort.Slice(st.Devices, func(i, j int) bool { return st.Devices[i].Name < st.Devices[j].Name })
	sort.Slice(st.Links, func(i, j int) bool { return st.Links[i].Name < st.Links[j].Name })
	return st
}
