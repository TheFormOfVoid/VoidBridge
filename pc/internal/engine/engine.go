// Package engine keeps the PC clipboard and connected phones in sync.
package engine

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/pc/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/pc/internal/protocol"
)

// Tunables. Vars rather than consts so tests can shrink them.
var (
	PollInterval     = 200 * time.Millisecond
	PingInterval     = 10 * time.Second
	ReadTimeout      = 25 * time.Second
	HandshakeTimeout = 10 * time.Second
)

// Clip is one clipboard value.
type Clip struct {
	ID   string
	Time int64 // unix ms, in our clock
	Text string
}

// Peer describes a connected phone for status display.
type Peer struct {
	ID, Name, Addr string
	Since          time.Time
}

// Status is reported to the UI whenever something changes.
type Status struct {
	Listening bool
	Peers     []Peer
	LastSync  time.Time
	LastError string
}

// Engine is the PC side of VoidBridge: it listens for phones and mirrors the
// clipboard in both directions.
type Engine struct {
	cb       clipboard.Clipboard
	key      []byte
	me       protocol.Identity
	OnStatus func(Status)
	Logf     func(format string, args ...any)

	mu        sync.Mutex
	sessions  map[string]*session // by peer id
	current   *Clip               // newest clipboard value we know about
	acked     map[string]string   // peer id -> clip id it has confirmed
	lastHash  string              // hash of the text we last saw on/put on the clipboard
	lastSeq   uint64
	listening bool
	lastSync  time.Time
	lastErr   string
}

type session struct {
	conn  *protocol.Conn
	since time.Time
	done  chan struct{}
}

// New creates an engine. pairingKey comes from protocol.DeriveKey.
func New(cb clipboard.Clipboard, pairingKey []byte, me protocol.Identity) *Engine {
	return &Engine{
		cb:       cb,
		key:      pairingKey,
		me:       me,
		sessions: map[string]*session{},
		acked:    map[string]string{},
		Logf:     log.Printf,
	}
}

// Run serves on ln and watches the clipboard until stop is closed.
func (e *Engine) Run(ln net.Listener, stop <-chan struct{}) {
	if seq, err := e.cb.Seq(); err == nil {
		e.lastSeq = seq
	}
	if text, err := e.cb.Read(); err == nil {
		e.lastHash = hash(text)
	}
	e.mu.Lock()
	e.listening = true
	e.mu.Unlock()
	e.notify()

	go func() {
		<-stop
		ln.Close()
		e.mu.Lock()
		for _, s := range e.sessions {
			s.conn.Close()
		}
		e.mu.Unlock()
	}()
	go e.watchClipboard(stop)

	for {
		raw, err := ln.Accept()
		if err != nil {
			select {
			case <-stop:
				return
			default:
			}
			e.Logf("accept: %v", err)
			time.Sleep(time.Second)
			continue
		}
		go e.serve(raw)
	}
}

func (e *Engine) serve(raw net.Conn) {
	if tc, ok := raw.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(10 * time.Second)
		tc.SetNoDelay(true)
	}
	conn, err := protocol.Handshake(raw, e.key, e.me, true, HandshakeTimeout)
	if err != nil {
		raw.Close()
		e.Logf("handshake with %s failed: %v", raw.RemoteAddr(), err)
		if errors.Is(err, protocol.ErrAuth) {
			e.setError("A device tried to connect with the wrong pairing code")
		}
		return
	}
	s := &session{conn: conn, since: time.Now(), done: make(chan struct{})}

	e.mu.Lock()
	// A phone that reconnects replaces its previous (probably dead) session.
	if old := e.sessions[conn.PeerID]; old != nil {
		old.conn.Close()
	}
	e.sessions[conn.PeerID] = s
	e.lastErr = ""
	var resend *Clip
	if e.current != nil && e.acked[conn.PeerID] != e.current.ID {
		resend = e.current
	}
	e.mu.Unlock()
	e.Logf("%s (%s) connected", conn.PeerName, conn.RemoteAddr())
	e.notify()

	if resend != nil {
		e.sendClip(s, resend)
	}

	go e.pinger(s)
	err = e.readLoop(s)
	close(s.done)
	conn.Close()

	e.mu.Lock()
	if e.sessions[conn.PeerID] == s {
		delete(e.sessions, conn.PeerID)
	}
	e.mu.Unlock()
	e.Logf("%s disconnected: %v", conn.PeerName, err)
	e.notify()
}

func (e *Engine) pinger(s *session) {
	t := time.NewTicker(PingInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if err := s.conn.Send(&protocol.Message{Type: protocol.TypePing}); err != nil {
				s.conn.Close()
				return
			}
		}
	}
}

func (e *Engine) readLoop(s *session) error {
	for {
		s.conn.SetReadDeadline(time.Now().Add(ReadTimeout))
		m, err := s.conn.Recv()
		if err != nil {
			return err
		}
		switch m.Type {
		case protocol.TypePing:
			if err := s.conn.Send(&protocol.Message{Type: protocol.TypePong}); err != nil {
				return err
			}
		case protocol.TypePong:
		case protocol.TypeAck:
			e.mu.Lock()
			e.acked[s.conn.PeerID] = m.ID
			e.mu.Unlock()
		case protocol.TypeClip:
			e.receiveClip(s, m)
		}
	}
}

// receiveClip applies a clip from a phone, acks it, and relays it to any other
// connected phones.
func (e *Engine) receiveClip(from *session, m *protocol.Message) {
	// Always ack, even a clip we decide is stale, so the sender stops resending.
	from.conn.Send(&protocol.Message{Type: protocol.TypeAck, ID: m.ID})

	clip := &Clip{ID: m.ID, Time: m.Time - from.conn.ClockOffset, Text: m.Text}
	e.mu.Lock()
	e.acked[from.conn.PeerID] = clip.ID
	if e.current != nil && (e.current.ID == clip.ID || e.current.Time > clip.Time) {
		// Duplicate, or we have something newer (it will reach them via resend).
		e.mu.Unlock()
		return
	}
	e.current = clip
	h := hash(clip.Text)
	changed := h != e.lastHash
	e.lastHash = h
	e.lastSync = time.Now()
	others := e.othersLocked(from.conn.PeerID)
	e.mu.Unlock()

	if changed {
		if err := e.cb.Write(clip.Text); err != nil {
			e.Logf("writing clipboard: %v", err)
			e.setError("Could not write to the clipboard: " + err.Error())
		} else if seq, err := e.cb.Seq(); err == nil {
			e.mu.Lock()
			e.lastSeq = seq
			e.mu.Unlock()
		}
	}
	for _, s := range others {
		e.sendClip(s, clip)
	}
	e.notify()
}

func (e *Engine) othersLocked(except string) []*session {
	var out []*session
	for id, s := range e.sessions {
		if id != except {
			out = append(out, s)
		}
	}
	return out
}

func (e *Engine) sendClip(s *session, c *Clip) {
	err := s.conn.Send(&protocol.Message{Type: protocol.TypeClip, ID: c.ID, Time: c.Time, Text: c.Text})
	if err != nil {
		s.conn.Close()
	}
}

func (e *Engine) watchClipboard(stop <-chan struct{}) {
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			e.pollClipboard()
		}
	}
}

func (e *Engine) pollClipboard() {
	seq, err := e.cb.Seq()
	if err != nil {
		return
	}
	e.mu.Lock()
	if seq == e.lastSeq {
		e.mu.Unlock()
		return
	}
	e.lastSeq = seq
	e.mu.Unlock()

	text, err := e.cb.Read()
	if err != nil || text == "" || len(text) > protocol.MaxText {
		return // non-text content, clipboard busy, or too large
	}
	h := hash(text)
	e.mu.Lock()
	if h == e.lastHash {
		e.mu.Unlock()
		return // our own write echoing back, or same text copied again
	}
	e.lastHash = h
	clip := &Clip{ID: newID(), Time: time.Now().UnixMilli(), Text: text}
	e.current = clip
	targets := e.othersLocked("")
	e.mu.Unlock()

	for _, s := range targets {
		e.sendClip(s, clip)
	}
	if len(targets) > 0 {
		e.mu.Lock()
		e.lastSync = time.Now()
		e.mu.Unlock()
		e.notify()
	}
}

func (e *Engine) setError(msg string) {
	e.mu.Lock()
	e.lastErr = msg
	e.mu.Unlock()
	e.notify()
}

// Status returns a snapshot of the engine state.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := Status{Listening: e.listening, LastSync: e.lastSync, LastError: e.lastErr}
	for id, s := range e.sessions {
		st.Peers = append(st.Peers, Peer{ID: id, Name: s.conn.PeerName, Addr: s.conn.RemoteAddr().String(), Since: s.since})
	}
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].Since.Before(st.Peers[j].Since) })
	return st
}

func (e *Engine) notify() {
	if e.OnStatus != nil {
		e.OnStatus(e.Status())
	}
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
