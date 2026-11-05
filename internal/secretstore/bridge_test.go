package secretstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

type testBridge struct{}

func (testBridge) Encrypt(plain []byte) ([]byte, error) {
	out := make([]byte, len(plain))
	for i, value := range plain {
		out[i] = value ^ 0x5a
	}
	return out, nil
}

func (testBridge) Decrypt(ciphertext []byte) ([]byte, error) {
	return testBridge{}.Encrypt(ciphertext)
}

func TestBridgeStoresOnlyCiphertext(t *testing.T) {
	SetBridge(testBridge{})
	t.Cleanup(func() { SetBridge(nil) })
	path := filepath.Join(t.TempDir(), "cloud-token.bin")
	secret := []byte("device-token-123")
	if err := Save(path, secret); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, secret) || !bytes.HasPrefix(raw, bridgedPrefix) {
		t.Fatalf("token file is not versioned ciphertext: %q", raw)
	}
	plain, err := Load(path)
	if err != nil || !bytes.Equal(plain, secret) {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
}

func TestBridgeMigratesLegacyPlaintextImmediately(t *testing.T) {
	SetBridge(testBridge{})
	t.Cleanup(func() { SetBridge(nil) })
	path := filepath.Join(t.TempDir(), "cloud-token.bin")
	secret := []byte("legacy-token")
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		t.Fatal(err)
	}
	plain, err := Load(path)
	if err != nil || !bytes.Equal(plain, secret) {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, secret) || !bytes.HasPrefix(raw, bridgedPrefix) {
		t.Fatalf("legacy plaintext was not migrated: %q", raw)
	}
}
