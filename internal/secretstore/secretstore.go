package secretstore

import (
	"bytes"
	"errors"
	"os"
	"sync"
)

// Bridge 由 Android Kotlin 使用 Keystore 实现。加解密在系统安全边界内完成，
// Go 只接触当前调用所需的明文，不把密钥或明文 Token 写入磁盘。
type Bridge interface {
	Encrypt(plain []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

var (
	bridgeMu sync.RWMutex
	bridge   Bridge
)

// SetBridge 在平台服务启动前注册系统加密桥；Android 必须注册后才能使用。
func SetBridge(value Bridge) {
	bridgeMu.Lock()
	bridge = value
	bridgeMu.Unlock()
}

func currentBridge() Bridge {
	bridgeMu.RLock()
	defer bridgeMu.RUnlock()
	return bridge
}

var bridgedPrefix = []byte("GFAK1")

// Save 优先使用系统加密桥，否则使用平台默认实现（Windows 为 DPAPI）。
func Save(path string, secret []byte) error {
	if len(secret) == 0 {
		return errors.New("secretstore: empty secret")
	}
	if b := currentBridge(); b != nil {
		ciphertext, err := b.Encrypt(secret)
		if err != nil {
			return err
		}
		payload := append(append([]byte(nil), bridgedPrefix...), ciphertext...)
		return os.WriteFile(path, payload, 0o600)
	}
	return platformSave(path, secret)
}

// Load 读取并解密秘密。带 GFAK1 前缀的是 Keystore 密文；无前缀数据按旧版
// 明文迁移，读取成功后立即重写为密文，避免升级后遗留明文副本。
func Load(path string) ([]byte, error) {
	if b := currentBridge(); b != nil {
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(payload, bridgedPrefix) {
			if len(payload) == 0 {
				return nil, errors.New("secretstore: empty encrypted file")
			}
			if err := Save(path, payload); err != nil {
				return nil, err
			}
			return payload, nil
		}
		ciphertext := payload[len(bridgedPrefix):]
		if len(ciphertext) == 0 {
			return nil, errors.New("secretstore: empty encrypted file")
		}
		return b.Decrypt(ciphertext)
	}
	return platformLoad(path)
}
