// Package peer connects devices of the same group directly, over the local
// network or Tailscale, without a server.
//
// Every device listens on TCP 47829 and finds others by:
//   - UDP broadcast beacons on the local network (port 47830),
//   - addresses the user typed in,
//   - Tailscale peers (desktop only, via the tailscale CLI),
//   - peer exchange: connected devices tell each other the addresses of every
//     device they know, so one working connection is enough to find the rest.
package peer

import (
	"encoding/json"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// Tunables (vars for tests).
var (
	PingInterval     = 10 * time.Second
	IdleTimeout      = 25 * time.Second
	HandshakeTimeout = 10 * time.Second
	BeaconInterval   = 3 * time.Second
	DialInterval     = 1 * time.Second
	PexInterval      = 60 * time.Second
	ForgetAfter      = 30 * time.Minute
)

// Manager owns all direct links for one node.
type Manager struct {
	Node *node.Node
	Keys protocol.Keys
	Me   protocol.Identity
	Port int // TCP port to listen on; 0 picks one (tests)
	// Discovery: set to false in tests to avoid broadcasting.
	Discovery bool
	// Tailscale returns peer IPs to try; nil disables.
	Tailscale func() []string
	Logf      func(string, ...any)

	mu      sync.Mutex
	manual  []string
	known   map[string]*known  // device id -> addresses
	targets map[string]*target // "host:port" -> dial state
	links   map[string]map[*link]struct{}
	ln      net.Listener
}

type known struct {
	name, kind string
	addrs      map[string]time.Time // addr -> last seen
}

type target struct {
	fails   int
	next    time.Time
	dialing bool
	manual  bool
	lastID  string
}

// NewManager creates a manager; call Run to start it.
func NewManager(n *node.Node, keys protocol.Keys, port int) *Manager {
	return &Manager{
		Node: n, Keys: keys, Me: n.Identity(), Port: port, Discovery: true, Logf: log.Printf,
		known: map[string]*known{}, targets: map[string]*target{}, links: map[string]map[*link]struct{}{},
	}
}

// SetManual sets the addresses the user typed in ("host" or "host:port").
func (m *Manager) SetManual(addrs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.manual = nil
	for _, a := range addrs {
		if a = normAddr(a); a != "" {
			m.manual = append(m.manual, a)
		}
	}
	for _, t := range m.targets {
		t.manual = false
	}
	for _, a := range m.manual {
		m.target(a).manual = true
		m.targets[a].next = time.Time{}
	}
}

func normAddr(a string) string {
	a = strings.TrimSpace(a)
	if a == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(a); err != nil {
		a = net.JoinHostPort(strings.Trim(a, "[]"), strconv.Itoa(protocol.PeerPort))
	}
	return a
}

// Addr returns the listening address (after Run has started).
func (m *Manager) Addr() net.Addr {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ln == nil {
		return nil
	}
	return m.ln.Addr()
}

// Run listens, discovers and dials until stop closes. It returns an error only
// if the TCP port cannot be opened.
func (m *Manager) Run(stop <-chan struct{}) error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(m.Port))
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.ln = ln
	m.mu.Unlock()
	go func() {
		<-stop
		ln.Close()
		m.mu.Lock()
		for _, set := range m.links {
			for l := range set {
				l.Close()
			}
		}
		m.mu.Unlock()
	}()
	go m.acceptLoop(ln, stop)
	if m.Discovery {
		go m.discovery(stop)
	}
	go m.dialLoop(stop)
	go m.pexLoop(stop)
	if m.Tailscale != nil {
		go m.tailscaleLoop(stop)
	}
	<-stop
	return nil
}

func (m *Manager) acceptLoop(ln net.Listener, stop <-chan struct{}) {
	for {
		raw, err := ln.Accept()
		if err != nil {
			select {
			case <-stop:
				return
			default:
				time.Sleep(200 * time.Millisecond)
				continue
			}
		}
		go func() {
			tune(raw)
			c, err := protocol.Handshake(raw, m.Keys.Link, m.Me, m.myAddrs(), false, HandshakeTimeout)
			if err != nil {
				raw.Close()
				return
			}
			m.addLink(c)
		}()
	}
}

func tune(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(10 * time.Second)
		tc.SetNoDelay(true)
	}
}

// ---- links ----

type link struct {
	m    *Manager
	c    *protocol.Conn
	info node.LinkInfo
	once sync.Once
	done chan struct{}
}

func (l *link) Send(msg *protocol.Message) error { return l.c.Send(msg) }
func (l *link) Info() node.LinkInfo              { return l.info }
func (l *link) Close() {
	l.once.Do(func() {
		close(l.done)
		l.c.Close()
	})
}

func via(addr net.Addr) string {
	if ta, ok := addr.(*net.TCPAddr); ok && isTailscale(ta.IP) {
		return "Tailscale"
	}
	return "Wi-Fi"
}

// allowLoopback lets tests on one machine exercise peer exchange.
var allowLoopback atomic.Bool

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isTailscale(ip net.IP) bool { return cgnat.Contains(ip) }

func (m *Manager) addLink(c *protocol.Conn) {
	l := &link{m: m, c: c, done: make(chan struct{})}
	l.info = node.LinkInfo{PeerID: c.Peer.ID, Name: c.Peer.Name, Kind: c.Peer.Kind, Via: via(c.RemoteAddr()), Addr: c.RemoteAddr().String()}

	m.mu.Lock()
	set := m.links[c.Peer.ID]
	if set == nil {
		set = map[*link]struct{}{}
		m.links[c.Peer.ID] = set
	}
	if c.Dialer && len(set) > 0 {
		// Both sides dialled at once; one link is plenty.
		m.mu.Unlock()
		c.Close()
		return
	}
	set[l] = struct{}{}
	k := m.knownLocked(c.Peer.ID)
	k.name, k.kind = c.Peer.Name, c.Peer.Kind
	for _, a := range c.PeerAddrs {
		k.addrs[a] = time.Now()
	}
	m.mu.Unlock()

	m.Logf("linked with %s (%s) via %s", c.Peer.Name, c.RemoteAddr(), l.info.Via)
	m.Node.AddLink(l)
	go m.sendPeers(l)
	go l.pinger()
	l.readLoop()

	m.mu.Lock()
	delete(m.links[c.Peer.ID], l)
	if len(m.links[c.Peer.ID]) == 0 {
		delete(m.links, c.Peer.ID)
	}
	m.mu.Unlock()
	m.Node.RemoveLink(l)
	m.Logf("unlinked %s", c.Peer.Name)
}

func (l *link) pinger() {
	t := time.NewTicker(PingInterval)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-t.C:
			if err := l.c.Send(&protocol.Message{Type: protocol.TypePing}); err != nil {
				l.Close()
				return
			}
		}
	}
}

func (l *link) readLoop() {
	defer l.Close()
	for {
		msg, err := l.c.Recv(IdleTimeout)
		if err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypePing:
			l.c.Send(&protocol.Message{Type: protocol.TypePong})
		case protocol.TypePeers:
			l.m.learn(msg.Peers)
		case protocol.TypeClip:
			l.m.Node.Handle(l, msg)
		}
	}
}

// ---- peer knowledge ----

func (m *Manager) knownLocked(id string) *known {
	k := m.known[id]
	if k == nil {
		k = &known{addrs: map[string]time.Time{}}
		m.known[id] = k
	}
	return k
}

func (m *Manager) target(addr string) *target {
	t := m.targets[addr]
	if t == nil {
		t = &target{}
		m.targets[addr] = t
	}
	return t
}

// learn merges peer-exchange information.
func (m *Manager) learn(peers []protocol.PeerInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, p := range peers {
		if p.ID == m.Me.ID || p.ID == "" {
			continue
		}
		k := m.knownLocked(p.ID)
		if p.Name != "" {
			k.name, k.kind = p.Name, p.Kind
		}
		for i, a := range p.Addrs {
			if i >= 8 {
				break
			}
			if host, _, err := net.SplitHostPort(a); err == nil {
				if ip := net.ParseIP(host); ip != nil && (allowLoopback.Load() || !ip.IsLoopback()) && !ip.IsUnspecified() {
					k.addrs[a] = now
				}
			}
		}
	}
}

// heard records a LAN beacon.
func (m *Manager) heard(id, name, kind, addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.knownLocked(id)
	k.name, k.kind = name, kind
	if _, seen := k.addrs[addr]; !seen {
		// A device we haven't heard at this address: try it right away.
		m.target(addr).next = time.Time{}
	}
	k.addrs[addr] = time.Now()
}

// peerList is what we tell other devices: ourselves plus everyone we know.
func (m *Manager) peerList() []protocol.PeerInfo {
	out := []protocol.PeerInfo{{ID: m.Me.ID, Name: m.Me.Name, Kind: m.Me.Kind, Addrs: m.myAddrs()}}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, k := range m.known {
		p := protocol.PeerInfo{ID: id, Name: k.name, Kind: k.kind}
		for a, t := range k.addrs {
			if time.Since(t) < ForgetAfter {
				p.Addrs = append(p.Addrs, a)
			}
		}
		if len(p.Addrs) > 0 {
			sort.Strings(p.Addrs)
			out = append(out, p)
		}
	}
	return out
}

func (m *Manager) sendPeers(l *link) {
	l.Send(&protocol.Message{Type: protocol.TypePeers, Peers: m.peerList()})
}

func (m *Manager) pexLoop(stop <-chan struct{}) {
	t := time.NewTicker(PexInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			m.mu.Lock()
			var all []*link
			for _, set := range m.links {
				for l := range set {
					all = append(all, l)
				}
			}
			m.mu.Unlock()
			for _, l := range all {
				go m.sendPeers(l)
			}
		}
	}
}

// ---- dialing ----

func (m *Manager) dialLoop(stop <-chan struct{}) {
	t := time.NewTicker(DialInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			for _, addr := range m.dialCandidates() {
				go m.dial(addr, stop)
			}
		}
	}
}

// dialCandidates picks addresses worth trying now: every address of every
// known device we have no link to, plus manual/Tailscale addresses whose
// device (if any) isn't linked.
func (m *Manager) dialCandidates() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var out []string
	consider := func(addr string) {
		t := m.target(addr)
		if t.dialing || now.Before(t.next) {
			return
		}
		if t.lastID != "" && len(m.links[t.lastID]) > 0 {
			return
		}
		t.dialing = true
		out = append(out, addr)
	}
	for id, k := range m.known {
		if len(m.links[id]) > 0 {
			continue
		}
		for a, seen := range k.addrs {
			if now.Sub(seen) > ForgetAfter {
				delete(k.addrs, a)
				continue
			}
			consider(a)
		}
	}
	for addr, t := range m.targets {
		if t.manual {
			consider(addr)
		}
	}
	return out
}

// AddTargets adds id-less addresses to try (e.g. from Tailscale).
func (m *Manager) addTargets(addrs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range addrs {
		t := m.target(a)
		t.manual = true
	}
}

func (m *Manager) dial(addr string, stop <-chan struct{}) {
	raw, err := net.DialTimeout("tcp", addr, 3*time.Second)
	var c *protocol.Conn
	if err == nil {
		tune(raw)
		c, err = protocol.Handshake(raw, m.Keys.Link, m.Me, m.myAddrs(), true, HandshakeTimeout)
		if err != nil {
			raw.Close()
		}
	}
	m.mu.Lock()
	t := m.target(addr)
	t.dialing = false
	if err != nil {
		t.fails++
		backoff := time.Second << min(t.fails, 6) // 2s .. 64s
		t.next = time.Now().Add(backoff)
		m.mu.Unlock()
		return
	}
	t.fails = 0
	t.lastID = c.Peer.ID
	t.next = time.Now().Add(2 * time.Second)
	m.mu.Unlock()
	select {
	case <-stop:
		c.Close()
		return
	default:
	}
	m.addLink(c)
}

// ---- local addresses ----

// myAddrs lists addresses other devices may reach us at.
func (m *Manager) myAddrs() []string {
	port := m.Port
	if a := m.Addr(); a != nil {
		port = a.(*net.TCPAddr).Port
	}
	var out []string
	if allowLoopback.Load() {
		out = append(out, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}
	for _, ip := range LocalIPs() {
		out = append(out, net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	return out
}

// LocalIPs returns this machine's private-LAN and Tailscale IPv4 addresses.
func LocalIPs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip.IsPrivate() || isTailscale(ip) {
			out = append(out, ip.String())
		}
	}
	return out
}

// ---- LAN discovery ----

type beacon struct {
	App  string `json:"app"`
	V    int    `json:"v"`
	Type string `json:"type,omitempty"` // "probe" for probes
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
	Port int    `json:"port,omitempty"`
	FP   string `json:"fp,omitempty"`
}

func (m *Manager) discovery(stop <-chan struct{}) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: protocol.DiscoveryPort})
	if err != nil {
		m.Logf("discovery: can't listen on udp %d (%v); broadcasting only", protocol.DiscoveryPort, err)
		if conn, err = net.ListenUDP("udp4", &net.UDPAddr{}); err != nil {
			return
		}
	}
	go func() {
		<-stop
		conn.Close()
	}()
	port := m.Addr().(*net.TCPAddr).Port
	fp := m.Keys.Fingerprint()
	me, _ := json.Marshal(beacon{App: "voidbridge", V: protocol.Version, ID: m.Me.ID, Name: m.Me.Name, Kind: m.Me.Kind, Port: port, FP: fp})
	probe, _ := json.Marshal(beacon{App: "voidbridge", V: protocol.Version, Type: "probe"})

	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				select {
				case <-stop:
					return
				default:
					time.Sleep(100 * time.Millisecond)
					continue
				}
			}
			var b beacon
			if json.Unmarshal(buf[:n], &b) != nil || b.App != "voidbridge" {
				continue
			}
			if b.Type == "probe" {
				conn.WriteToUDP(me, from)
				continue
			}
			if b.FP != fp || b.ID == m.Me.ID || b.ID == "" || b.Port <= 0 {
				continue
			}
			m.heard(b.ID, b.Name, b.Kind, net.JoinHostPort(from.IP.String(), strconv.Itoa(b.Port)))
		}
	}()

	send := func(p []byte) {
		for _, dst := range broadcastAddrs() {
			conn.WriteToUDP(p, &net.UDPAddr{IP: dst, Port: protocol.DiscoveryPort})
		}
	}
	send(probe)
	t := time.NewTicker(BeaconInterval)
	defer t.Stop()
	for {
		send(me)
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

func broadcastAddrs() []net.IP {
	out := []net.IP{net.IPv4bcast}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || len(ipn.Mask) != 4 {
				continue
			}
			bc := make(net.IP, 4)
			for i := range bc {
				bc[i] = ip4[i] | ^ipn.Mask[i]
			}
			out = append(out, bc)
		}
	}
	return out
}

// ---- Tailscale ----

func (m *Manager) tailscaleLoop(stop <-chan struct{}) {
	for {
		var addrs []string
		for _, ip := range m.Tailscale() {
			addrs = append(addrs, net.JoinHostPort(ip, strconv.Itoa(protocol.PeerPort)))
		}
		m.addTargets(addrs)
		select {
		case <-stop:
			return
		case <-time.After(30 * time.Second):
		}
	}
}
