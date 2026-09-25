// Package clipboard abstracts the system clipboard.
package clipboard

import "sync"

// Clipboard is a text clipboard with a change counter.
type Clipboard interface {
	// Read returns the current clipboard text ("" if it holds no text).
	Read() (string, error)
	// Write replaces the clipboard contents with text.
	Write(text string) error
	// Seq returns a number that changes whenever the clipboard changes.
	Seq() (uint64, error)
}

// Memory is an in-process clipboard, used in tests and on platforms without a
// native implementation.
type Memory struct {
	mu   sync.Mutex
	text string
	seq  uint64
}

func (m *Memory) Read() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.text, nil
}

func (m *Memory) Write(text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.text = text
	m.seq++
	return nil
}

func (m *Memory) Seq() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seq, nil
}
