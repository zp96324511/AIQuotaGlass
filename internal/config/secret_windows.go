//go:build windows

package config

import (
	"syscall"
	"unsafe"
)

type blob struct {
	Size uint32
	Data *byte
}

var (
	crypt32       = syscall.NewLazyDLL("crypt32.dll")
	kernel32      = syscall.NewLazyDLL("kernel32.dll")
	procProtect   = crypt32.NewProc("CryptProtectData")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree = kernel32.NewProc("LocalFree")
)

// Encrypt protects plain using DPAPI bound to the current Windows user.
func Encrypt(plain []byte) (string, error) {
	if len(plain) == 0 {
		return "", nil
	}
	in := blob{Size: uint32(len(plain)), Data: &plain[0]}
	var out blob
	r, _, err := procProtect.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return "", err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.Data)))
	enc := make([]byte, out.Size)
	copy(enc, unsafe.Slice(out.Data, out.Size))
	return encodeBase64(enc), nil
}

// Decrypt reverses Encrypt using DPAPI.
func Decrypt(enc string) ([]byte, error) {
	if enc == "" {
		return nil, nil
	}
	raw, err := decodeBase64(enc)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	in := blob{Size: uint32(len(raw)), Data: &raw[0]}
	var out blob
	r, _, err := procUnprotect.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.Data)))
	plain := make([]byte, out.Size)
	copy(plain, unsafe.Slice(out.Data, out.Size))
	return plain, nil
}
