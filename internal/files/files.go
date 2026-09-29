// Package files saves files received from other devices into a folder.
package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/TheFormOfVoid/VoidBridge/internal/node"
	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// SafeName turns a name chosen by another device into a plain file name that
// can't escape the target folder or collide with Windows device names.
func SafeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = name[strings.LastIndex(name, "/")+1:]
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32 || r == 127 || strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimRightFunc(strings.TrimSpace(b.String()), func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
	s = strings.TrimLeft(s, ".")
	if s == "" {
		s = "file"
	}
	base := strings.ToUpper(strings.TrimSuffix(s, filepath.Ext(s)))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		s = "_" + s
	}
	if len(s) > 200 {
		ext := filepath.Ext(s)
		if len(ext) > 20 {
			ext = ""
		}
		s = s[:200-len(ext)] + ext
	}
	return s
}

// uniquePath returns dir/name, or dir/name (2).ext etc. if that exists.
func uniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
	}
}

// Event describes a finished or failed incoming file, for the UI.
type Event struct {
	Meta protocol.FileMeta
	From node.FileSender
	Path string // where it was saved (empty on error)
	Err  error
}

// DirReceiver stores incoming files in a folder.
type DirReceiver struct {
	// Dir returns the folder to save into (read on every file, so a changed
	// setting applies immediately).
	Dir        func() string
	OnReceived func(Event)
	OnProgress func(id string, meta protocol.FileMeta, from node.FileSender, done int64)
}

type writer struct {
	mu       sync.Mutex
	f        *os.File
	dir      string
	name     string
	done     bool
	tempPath string
}

// Begin opens a temporary file; it's renamed into place on Commit.
func (d *DirReceiver) Begin(meta protocol.FileMeta, from node.FileSender) (node.FileWriter, error) {
	dir := d.Dir()
	if dir == "" {
		return nil, errors.New("no folder for received files is set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".voidbridge-*.part")
	if err != nil {
		return nil, err
	}
	return &writer{f: f, dir: dir, name: SafeName(meta.Name), tempPath: f.Name()}, nil
}

func (w *writer) Write(p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return errors.New("transfer was cancelled")
	}
	_, err := w.f.Write(p)
	return err
}

func (w *writer) Commit() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return "", errors.New("transfer was cancelled")
	}
	w.done = true
	if err := w.f.Close(); err != nil {
		os.Remove(w.tempPath)
		return "", err
	}
	final := uniquePath(w.dir, w.name)
	if err := os.Rename(w.tempPath, final); err != nil {
		os.Remove(w.tempPath)
		return "", err
	}
	return final, nil
}

func (w *writer) Abort() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return
	}
	w.done = true
	w.f.Close()
	os.Remove(w.tempPath)
}

func (d *DirReceiver) Received(meta protocol.FileMeta, from node.FileSender, where string, err error) {
	if d.OnReceived != nil {
		d.OnReceived(Event{Meta: meta, From: from, Path: where, Err: err})
	}
}

func (d *DirReceiver) Progress(id string, meta protocol.FileMeta, from node.FileSender, done int64) {
	if d.OnProgress != nil {
		d.OnProgress(id, meta, from, done)
	}
}

// Send opens a local file and sends it to a device through n.
func Send(n *node.Node, ctxDone <-chan struct{}, to, path string, progress func(sent, total int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return errors.New("folders can't be sent yet; zip it first")
	}
	meta := protocol.FileMeta{Name: filepath.Base(path), Size: st.Size(), Mime: mimeFor(path)}
	ctx, cancel := contextFrom(ctxDone)
	defer cancel()
	return n.SendFile(ctx, to, meta, f, func(sent int64) {
		if progress != nil {
			progress(sent, st.Size())
		}
	})
}
