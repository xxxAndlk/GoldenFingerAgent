//go:build android

package secretstore

import "errors"

// Android 禁止回退为私有目录明文。Kotlin 必须先通过 gosvc.SetSecretStore
// 注册 Android Keystore 桥，否则保存/读取明确失败，不能静默降低安全性。
func platformSave(_ string, _ []byte) error {
	return errors.New("secretstore: Android Keystore bridge is not configured")
}

func platformLoad(_ string) ([]byte, error) {
	return nil, errors.New("secretstore: Android Keystore bridge is not configured")
}
