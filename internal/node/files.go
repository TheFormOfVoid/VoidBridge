package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

// File transfers go to one chosen device, directly if there's a link to it,
// otherwise through the server. They're streamed in chunks, end-to-end
// encrypted, and acknowledged by the receiver once the file is complete.

var (
	// FileAckTimeout is how long a sender waits for the receiver to confirm.
	FileAckTimeout = 60 * time.Second
	// FileIdleTimeout aborts incoming transfers that stop arriving.
	FileIdleTimeout = 60 * time.Second
)

// ErrDeviceUnreachable means no link leads to the target device.
var ErrDeviceUnreachable = errors.New("that device isn't connected right now")

// FileSender identifies who a received file came from.
type FileSender struct {
	ID   string
	Name string
}

// FileWriter receives the bytes of one incoming file.
type FileWriter interface {
	Write(p []byte) error
	// Commit finishes the file; the returned string describes where it went.
	Commit() (string, error)
	Abort()
}

// FileReceiver decides where incoming files go.
type FileReceiver interface {
	Begin(meta protocol.FileMeta, from FileSender) (FileWriter, error)
	// Received is called after a file was stored (or failed, with err set).
	Received(meta protocol.FileMeta, from FileSender, where string, err error)
	// Progress reports bytes received so far.
	Progress(id string, meta protocol.FileMeta, from FileSender, done int64)
}

type incoming struct {
	meta   protocol.FileMeta
	from   FileSender
	link   Link
	w      FileWriter
	next   int64
	got    int64
	sum    hash.Hash
	active time.Time
}

// SetFileReceiver enables receiving files.
func (n *Node) SetFileReceiver(r FileReceiver) {
	n.mu.Lock()
	n.fileRecv = r
	n.mu.Unlock()
}

// linkTo finds the best link to a device: direct first, then a server that
// says the device is online.
func (n *Node) linkTo(id string) Link {
	n.mu.Lock()
	defer n.mu.Unlock()
	for l := range n.links {
		if l.Info().PeerID == id {
			return l
		}
	}
	for l, devs := range n.remote {
		for _, d := range devs {
			if d.ID == id {
				return l
			}
		}
	}
	return nil
}

// SendFile streams r (size bytes) to device `to` and waits until the
// receiver confirms it stored the file. progress may be nil.
func (n *Node) SendFile(ctx context.Context, to string, meta protocol.FileMeta, r io.Reader, progress func(sent int64)) error {
	l := n.linkTo(to)
	if l == nil {
		return ErrDeviceUnreachable
	}
	id := protocol.NewID()
	ack := make(chan string, 1)
	n.mu.Lock()
	n.fileAcks[id] = ack
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.fileAcks, id)
		n.mu.Unlock()
	}()

	msg := func(t string, seq int64, plain []byte) error {
		m := &protocol.Message{Type: t, ID: id, Origin: n.me.ID, OriginName: n.me.Name, To: to, Seq: seq}
		protocol.SealFile(n.keys.Content, m, plain)
		select {
		case e := <-ack: // the receiver gave up (or the server says it's offline)
			if e == "" {
				e = "the other device stopped the transfer"
			}
			return errors.New(e)
		default:
		}
		return l.Send(m)
	}

	mb, _ := json.Marshal(meta)
	if err := msg(protocol.TypeFileStart, 0, mb); err != nil {
		return err
	}
	sum := sha256.New()
	buf := make([]byte, protocol.FileChunkSize)
	var seq, sent int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		k, rerr := io.ReadFull(r, buf)
		if k > 0 {
			sum.Write(buf[:k])
			if err := msg(protocol.TypeFileChunk, seq, buf[:k]); err != nil {
				return err
			}
			seq++
			sent += int64(k)
			if progress != nil {
				progress(sent)
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if sent != meta.Size {
		return fmt.Errorf("file changed while sending (%d of %d bytes)", sent, meta.Size)
	}
	if err := msg(protocol.TypeFileEnd, seq, []byte(hex.EncodeToString(sum.Sum(nil)))); err != nil {
		return err
	}
	select {
	case e := <-ack:
		if e != "" {
			return errors.New(e)
		}
		return nil
	case <-time.After(FileAckTimeout):
		return errors.New("the other device didn't confirm the file (is VoidBridge up to date there?)")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *Node) sendAck(l Link, m *protocol.Message, errText string) {
	go l.Send(&protocol.Message{Type: protocol.TypeFileAck, ID: m.ID, Origin: n.me.ID, To: m.Origin, Error: errText})
}

// handleFile processes file messages addressed to this device.
func (n *Node) handleFile(l Link, m *protocol.Message) {
	if m.To != n.me.ID {
		return
	}
	if m.Type == protocol.TypeFileAck {
		n.mu.Lock()
		ch := n.fileAcks[m.ID]
		n.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m.Error:
			default:
			}
		}
		return
	}

	n.mu.Lock()
	recv := n.fileRecv
	in := n.incoming[m.ID]
	n.mu.Unlock()
	from := FileSender{ID: m.Origin, Name: m.OriginName}

	fail := func(in *incoming, reason string) {
		if in != nil {
			in.w.Abort()
			n.mu.Lock()
			delete(n.incoming, m.ID)
			n.mu.Unlock()
			recv.Received(in.meta, in.from, "", errors.New(reason))
		}
		n.sendAck(l, m, reason)
	}

	plain, err := protocol.OpenFile(n.keys.Content, m)
	if err != nil {
		return // not from our group, or tampered with: ignore
	}

	switch m.Type {
	case protocol.TypeFileStart:
		if recv == nil {
			n.sendAck(l, m, "that device can't receive files")
			return
		}
		var meta protocol.FileMeta
		if json.Unmarshal(plain, &meta) != nil || meta.Size < 0 {
			n.sendAck(l, m, "bad file header")
			return
		}
		w, err := recv.Begin(meta, from)
		if err != nil {
			n.sendAck(l, m, "the other device couldn't save the file: "+err.Error())
			return
		}
		n.mu.Lock()
		n.incoming[m.ID] = &incoming{meta: meta, from: from, link: l, w: w, sum: sha256.New(), active: time.Now()}
		n.mu.Unlock()

	case protocol.TypeFileChunk:
		if in == nil {
			return
		}
		if m.Seq != in.next {
			fail(in, "file data arrived out of order")
			return
		}
		in.next++
		in.got += int64(len(plain))
		n.mu.Lock()
		in.active = time.Now()
		n.mu.Unlock()
		if in.got > in.meta.Size {
			fail(in, "more data than announced")
			return
		}
		in.sum.Write(plain)
		if err := in.w.Write(plain); err != nil {
			fail(in, "the other device couldn't write the file: "+err.Error())
			return
		}
		recv.Progress(m.ID, in.meta, in.from, in.got)

	case protocol.TypeFileEnd:
		if in == nil {
			return
		}
		n.mu.Lock()
		delete(n.incoming, m.ID)
		n.mu.Unlock()
		if m.Seq != in.next || in.got != in.meta.Size || string(plain) != hex.EncodeToString(in.sum.Sum(nil)) {
			in.w.Abort()
			recv.Received(in.meta, in.from, "", errors.New("the file arrived incomplete"))
			n.sendAck(l, m, "the file arrived incomplete")
			return
		}
		where, err := in.w.Commit()
		recv.Received(in.meta, in.from, where, err)
		if err != nil {
			n.sendAck(l, m, "the other device couldn't save the file: "+err.Error())
			return
		}
		n.sendAck(l, m, "")
	}
}

// expireFiles aborts incoming transfers that went quiet (sender vanished).
func (n *Node) expireFiles() {
	n.mu.Lock()
	var dead []*incoming
	for id, in := range n.incoming {
		if time.Since(in.active) > FileIdleTimeout {
			dead = append(dead, in)
			delete(n.incoming, id)
		}
	}
	recv := n.fileRecv
	n.mu.Unlock()
	for _, in := range dead {
		in.w.Abort()
		if recv != nil {
			recv.Received(in.meta, in.from, "", errors.New("the transfer stopped (connection lost)"))
		}
	}
}
