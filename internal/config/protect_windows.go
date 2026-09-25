package config

import (
	"errors"
	"syscall"
	"unsafe"
)

// DPAPI ties the key to the Windows user account, so copying settings.json
// to another account or machine doesn't reveal it.

var (
	crypt32           = syscall.NewLazyDLL("crypt32.dll")
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procProtectData   = crypt32.NewProc("CryptProtectData")
	procUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree     = kernel32.NewProc("LocalFree")
	procRtlMoveMemory = kernel32.NewProc("RtlMoveMemory")
)

type dataBlob struct {
	size uint32
	data *byte
}

const cryptProtectUIForbidden = 0x1

func blob(b []byte) *dataBlob {
	if len(b) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{size: uint32(len(b)), data: &b[0]}
}

func (d *dataBlob) bytes() []byte {
	out := make([]byte, d.size)
	if d.size > 0 {
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&out[0])), uintptr(unsafe.Pointer(d.data)), uintptr(d.size))
	}
	return out
}

func protect(b []byte) ([]byte, error) {
	var out dataBlob
	r, _, err := procProtectData.Call(uintptr(unsafe.Pointer(blob(b))), 0, 0, 0, 0, cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, errors.New("CryptProtectData: " + err.Error())
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))
	return out.bytes(), nil
}

func unprotect(b []byte) ([]byte, error) {
	var out dataBlob
	r, _, err := procUnprotectData.Call(uintptr(unsafe.Pointer(blob(b))), 0, 0, 0, 0, cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, errors.New("CryptUnprotectData: " + err.Error())
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))
	return out.bytes(), nil
}
