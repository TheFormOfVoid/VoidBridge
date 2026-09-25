package node

import (
	"sync"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/clipboard"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// HistoryItem is one past clip. It is kept in memory only.
type HistoryItem struct {
	ID         string
	Time       time.Time
	Origin     string
	OriginName string
	Local      bool
	Content    *clipboard.Content
}

// History remembers recent clips, bounded by count and total bytes.
type History struct {
	mu       sync.Mutex
	items    []HistoryItem // newest first
	MaxItems int
	MaxBytes int
	Enabled  bool
}

// NewHistory creates an enabled history.
func NewHistory() *History {
	return &History{MaxItems: 50, MaxBytes: 150 << 20, Enabled: true}
}

// Add records a clip.
func (h *History) Add(m *protocol.Message, c *clipboard.Content, local bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.Enabled {
		return
	}
	// Recopying from history makes the same content newest again.
	hash := c.Hash()
	for i, it := range h.items {
		if it.Content.Hash() == hash {
			h.items = append(h.items[:i], h.items[i+1:]...)
			break
		}
	}
	item := HistoryItem{ID: m.ID, Time: time.UnixMilli(m.Time), Origin: m.Origin, OriginName: m.OriginName, Local: local, Content: c}
	h.items = append([]HistoryItem{item}, h.items...)
	total := 0
	for i, it := range h.items {
		total += len(it.Content.Bytes())
		if i >= h.MaxItems || total > h.MaxBytes {
			h.items = h.items[:i]
			break
		}
	}
}

// Items returns a copy of the history, newest first.
func (h *History) Items() []HistoryItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]HistoryItem(nil), h.items...)
}

// Get finds an item by id.
func (h *History) Get(id string) *HistoryItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, it := range h.items {
		if it.ID == id {
			it := it
			return &it
		}
	}
	return nil
}

// Clear forgets everything.
func (h *History) Clear() {
	h.mu.Lock()
	h.items = nil
	h.mu.Unlock()
}

// SetEnabled turns recording on or off; turning it off clears it.
func (h *History) SetEnabled(on bool) {
	h.mu.Lock()
	h.Enabled = on
	if !on {
		h.items = nil
	}
	h.mu.Unlock()
}
