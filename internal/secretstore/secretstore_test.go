package secretstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.bin")
	if err := Save(path, []byte("device-token-123")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "device-token-123" {
		t.Fatal("secret was stored in plaintext")
	}
	plain, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "device-token-123" {
		t.Fatalf("loaded=%q", plain)
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.bin")); !os.IsNotExist(err) {
		t.Fatalf("err=%v, want not exist", err)
	}
}
