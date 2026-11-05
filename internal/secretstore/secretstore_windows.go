//go:build windows

// Package secretstore 用当前用户凭据加密小段敏感数据（如设备 Token）。
package secretstore

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type dataBlob struct {
	size uint32
	data *byte
}

var (
	crypt32       = windows.NewLazySystemDLL("crypt32.dll")
	kernel32      = windows.NewLazySystemDLL("kernel32.dll")
	procProtect   = crypt32.NewProc("CryptProtectData")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree = kernel32.NewProc("LocalFree")
)

const cryptProtectUIForbidden = 0x1

// platformSave 用 Windows DPAPI（当前用户范围）加密后写入文件。
func platformSave(path string, secret []byte) error {
	if len(secret) == 0 {
		return errors.New("secretstore: empty secret")
	}
	in := dataBlob{size: uint32(len(secret)), data: &secret[0]}
	var out dataBlob
	r, _, err := procProtect.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))
	blob := make([]byte, out.size)
	copy(blob, unsafe.Slice(out.data, out.size))
	return os.WriteFile(path, blob, 0o600)
}

// platformLoad 读取并用 DPAPI 解密文件；文件不存在返回 os.ErrNotExist。
func platformLoad(path string) ([]byte, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(blob) == 0 {
		return nil, errors.New("secretstore: empty encrypted file")
	}
	in := dataBlob{size: uint32(len(blob)), data: &blob[0]}
	var out dataBlob
	r, _, err := procUnprotect.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))
	plain := make([]byte, out.size)
	copy(plain, unsafe.Slice(out.data, out.size))
	return plain, nil
}
