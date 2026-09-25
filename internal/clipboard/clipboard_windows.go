//go:build windows

package clipboard

import (
	"errors"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/TheFormOfVoid/VoidBridge/internal/protocol"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
	procLstrlenW                   = kernel32.NewProc("lstrlenW")
	procRtlMoveMemory              = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13
	cfDIB         = 8
	cfDIBV5       = 17
	gmemMoveable  = 0x0002
)

func registerFormat(name string) uintptr {
	p, _ := syscall.UTF16PtrFromString(name)
	r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(p)))
	return r
}

var (
	cfPNG = registerFormat("PNG")
	// Set by password managers and other apps for content that must not be
	// shared, logged or put in clipboard history.
	cfExclude   = registerFormat("ExcludeClipboardContentFromMonitorProcessing")
	cfNoHistory = registerFormat("CanIncludeInClipboardHistory")
	cfNoCloud   = registerFormat("CanUploadToCloudClipboard")
	cfOurs      = registerFormat("VoidBridgeSynced") // marks clips we wrote
)

// Windows is the native clipboard.
type Windows struct{}

// System returns the native clipboard.
func System() Clipboard { return Windows{} }

func open() error {
	var err error
	for i := 0; i < 30; i++ {
		r, _, e := procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		err = e
		time.Sleep(15 * time.Millisecond)
	}
	return errors.New("clipboard: busy: " + err.Error())
}

func available(f uintptr) bool {
	r, _, _ := procIsClipboardFormatAvailable.Call(f)
	return r != 0
}

// readHGlobal copies the bytes of a clipboard format (clipboard must be open).
func readHGlobal(format uintptr, max int) ([]byte, bool) {
	h, _, _ := procGetClipboardData.Call(format)
	if h == 0 {
		return nil, false
	}
	size, _, _ := procGlobalSize.Call(h)
	if size == 0 || int(size) > max {
		return nil, false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return nil, false
	}
	defer procGlobalUnlock.Call(h)
	buf := make([]byte, size)
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), p, size)
	return buf, true
}

// dwordFormat reads a clipboard format holding a DWORD (clipboard open).
func dwordFormat(f uintptr) (uint32, bool) {
	b, ok := readHGlobal(f, 64)
	if !ok || len(b) < 4 {
		return 0, false
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24, true
}

func (Windows) Read() (*Content, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hasText, hasPNG := available(cfUnicodeText), available(cfPNG)
	hasDIB := available(cfDIBV5) || available(cfDIB)
	if !hasText && !hasPNG && !hasDIB {
		return nil, nil
	}
	if err := open(); err != nil {
		return nil, err
	}
	defer procCloseClipboard.Call()

	sensitive := available(cfExclude)
	if v, ok := dwordFormat(cfNoHistory); ok && v == 0 {
		sensitive = true
	}
	if v, ok := dwordFormat(cfNoCloud); ok && v == 0 {
		sensitive = true
	}

	if hasText {
		h, _, _ := procGetClipboardData.Call(cfUnicodeText)
		if h != 0 {
			if p, _, _ := procGlobalLock.Call(h); p != 0 {
				n, _, _ := procLstrlenW.Call(p)
				var text string
				if n > 0 && n <= protocol.MaxText {
					buf := make([]uint16, n)
					procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), p, n*2)
					text = syscall.UTF16ToString(buf)
				}
				procGlobalUnlock.Call(h)
				if text != "" {
					return &Content{Type: protocol.ClipText, Text: text, Mime: "text/plain", Sensitive: sensitive}, nil
				}
			}
		}
	}
	if hasPNG {
		if b, ok := readHGlobal(cfPNG, protocol.MaxImage); ok {
			return &Content{Type: protocol.ClipImage, Data: b, Mime: "image/png", Sensitive: sensitive}, nil
		}
	}
	for _, f := range []uintptr{cfDIBV5, cfDIB} {
		if b, ok := readHGlobal(f, 200<<20); ok {
			img, err := DIBToImage(b)
			if err != nil {
				continue
			}
			p, err := EncodePNG(img)
			if err != nil || len(p) > protocol.MaxImage {
				return nil, nil
			}
			return &Content{Type: protocol.ClipImage, Data: p, Mime: "image/png", Sensitive: sensitive}, nil
		}
	}
	return nil, nil
}

func setHGlobal(format uintptr, data []byte) error {
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if h == 0 {
		return errors.New("clipboard: out of memory")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("clipboard: GlobalLock failed")
	}
	if len(data) > 0 {
		procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	}
	procGlobalUnlock.Call(h)
	if r, _, e := procSetClipboardData.Call(format, h); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("clipboard: SetClipboardData failed: " + e.Error())
	}
	return nil // the system owns h now
}

func (Windows) Write(c *Content) error {
	// Prepare everything before opening the clipboard, to hold it briefly.
	var formats []struct {
		f    uintptr
		data []byte
	}
	add := func(f uintptr, d []byte) {
		formats = append(formats, struct {
			f    uintptr
			data []byte
		}{f, d})
	}
	switch c.Type {
	case protocol.ClipText:
		u, err := syscall.UTF16FromString(c.Text)
		if err != nil {
			return err
		}
		b := unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*2)
		add(cfUnicodeText, append([]byte(nil), b...))
	case protocol.ClipImage:
		pngData, img, err := ToPNG(c.Data)
		if err != nil {
			return err
		}
		add(cfPNG, pngData)
		add(cfDIB, ImageToDIB(img))
	default:
		return errors.New("clipboard: unknown content type")
	}
	add(cfOurs, []byte{1, 0, 0, 0})

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := open(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	for _, f := range formats {
		if err := setHGlobal(f.f, f.data); err != nil {
			return err
		}
	}
	return nil
}

func (Windows) Seq() (uint64, error) {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return uint64(r), nil
}
