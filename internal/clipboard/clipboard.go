// Package clipboard abstracts the system clipboard.
package clipboard

import (
	"sync"

	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// Content is one clipboard value.
type Content struct {
	Type string // protocol.ClipText or protocol.ClipImage
	Text string
	Data []byte // image bytes
	Mime string // "text/plain", "image/png", ...
	// Sensitive is set when the source app marked the clip as a password or
	// otherwise not to be shared (clipboard history / cloud clipboard opt-out).
	Sensitive bool
}

// Bytes returns the payload that gets synced.
func (c *Content) Bytes() []byte {
	if c.Type == protocol.ClipText {
		return []byte(c.Text)
	}
	return c.Data
}

// Hash identifies the content.
func (c *Content) Hash() string { return protocol.Hash(c.Type, c.Bytes()) }

// Clipboard is a clipboard with a change counter.
type Clipboard interface {
	// Read returns the current content, or nil if it holds nothing we sync.
	Read() (*Content, error)
	// Write replaces the clipboard contents.
	Write(c *Content) error
	// Seq returns a number that changes whenever the clipboard changes.
	Seq() (uint64, error)
}

// Memory is an in-process clipboard for tests and non-Windows development.
type Memory struct {
	mu  sync.Mutex
	c   *Content
	seq uint64
}

func (m *Memory) Read() (*Content, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.c == nil {
		return nil, nil
	}
	cp := *m.c
	return &cp, nil
}

func (m *Memory) Write(c *Content) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	m.c = &cp
	m.seq++
	return nil
}

// WriteText is a test convenience.
func (m *Memory) WriteText(s string) {
	m.Write(&Content{Type: protocol.ClipText, Text: s, Mime: "text/plain"})
}

// Text is a test convenience.
func (m *Memory) Text() string {
	c, _ := m.Read()
	if c == nil {
		return ""
	}
	return c.Text
}

func (m *Memory) Seq() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seq, nil
}
