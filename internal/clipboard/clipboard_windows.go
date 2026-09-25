//go:build windows

package clipboard

import (
	"errors"
	"runtime"
	"syscall"
	"time"
	"unsafe"
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
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procLstrlenW                   = kernel32.NewProc("lstrlenW")
	procRtlMoveMemory              = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// Windows is the native Windows clipboard.
type Windows struct{}

// System returns the native clipboard for this platform.
func System() Clipboard { return Windows{} }

// open retries because other programs routinely hold the clipboard open for a
// few milliseconds.
func open() error {
	var err error
	for i := 0; i < 20; i++ {
		r, _, e := procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		err = e
		time.Sleep(15 * time.Millisecond)
	}
	return errors.New("clipboard: could not open: " + err.Error())
}

func (Windows) Read() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
		return "", nil
	}
	if err := open(); err != nil {
		return "", err
	}
	defer procCloseClipboard.Call()

	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", errors.New("clipboard: GlobalLock failed")
	}
	defer procGlobalUnlock.Call(h)
	n, _, _ := procLstrlenW.Call(p)
	if n == 0 {
		return "", nil
	}
	buf := make([]uint16, n)
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), p, n*2)
	return syscall.UTF16ToString(buf), nil
}

func (Windows) Write(text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	u, err := syscall.UTF16FromString(text)
	if err != nil {
		return err // text contains NUL
	}
	size := uintptr(len(u) * 2)
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return errors.New("clipboard: GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("clipboard: GlobalLock failed")
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	procGlobalUnlock.Call(h)

	if err := open(); err != nil {
		procGlobalFree.Call(h)
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	if r, _, e := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("clipboard: SetClipboardData failed: " + e.Error())
	}
	// The system owns h now.
	return nil
}

func (Windows) Seq() (uint64, error) {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return uint64(r), nil
}
