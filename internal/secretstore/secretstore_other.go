//go:build !windows && !android

// Package secretstore 用当前用户凭据加密小段敏感数据（如设备 Token）。
package secretstore

import "os"

// platformSave 在非 Android 的非 Windows 平台写入仅当前用户可读的文件。
// Android 不使用此后端，必须经 Kotlin Keystore 桥加密。
func platformSave(path string, secret []byte) error {
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// platformLoad 读取文件；文件不存在返回 os.ErrNotExist。
func platformLoad(path string) ([]byte, error) {
	return os.ReadFile(path)
}
