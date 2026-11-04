package wechatdb

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func testKey() KeyBlob {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*7 + 3)
	}
	return KeyBlob{Key: key}
}

// encryptTestPage 生成一页加密数据，布局与 decryptPage 期望的一致。
func encryptTestPage(t *testing.T, key KeyBlob, plain []byte, pgno int) []byte {
	t.Helper()
	page := make([]byte, PageSize)
	iv := make([]byte, 16)
	for i := range iv {
		iv[i] = byte(i + 1)
	}
	copy(page[PageSize-Reserve:PageSize-Reserve+16], iv)
	block, err := aes.NewCipher(key.Key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	var data, target []byte
	if pgno == 1 {
		copy(page[:16], plain[:16])
		data = plain[16 : PageSize-Reserve]
		target = page[16 : PageSize-Reserve]
	} else {
		data = plain[:PageSize-Reserve]
		target = page[:PageSize-Reserve]
	}
	encrypted := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, data)
	copy(target, encrypted)
	return page
}

func walFrameBytes(pageNo, dbSize int, page []byte, salt []byte) []byte {
	frame := make([]byte, WalFrame)
	binary.BigEndian.PutUint32(frame[:4], uint32(pageNo))
	binary.BigEndian.PutUint32(frame[4:8], uint32(dbSize))
	copy(frame[8:16], salt)
	copy(frame[24:], page)
	return frame
}

func walHeaderBytes(salt []byte) []byte {
	header := make([]byte, WalHeader)
	binary.BigEndian.PutUint32(header[:4], walMagicLE)
	binary.BigEndian.PutUint32(header[8:12], PageSize)
	copy(header[16:24], salt)
	return header
}

func plainPage(marker byte) []byte {
	plain := make([]byte, PageSize)
	for i := range plain {
		plain[i] = marker
	}
	plain[0] = 0x0D // 叶子页页头，mergeWAL 只接受合法页类型
	return plain
}

func TestSelectWalFramesIgnoresUncommittedTail(t *testing.T) {
	salt := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	data := walHeaderBytes(salt)
	data = append(data, walFrameBytes(5, 0, plainPage(1), salt)...)
	data = append(data, walFrameBytes(6, 9, plainPage(2), salt)...)
	data = append(data, walFrameBytes(7, 0, plainPage(3), salt)...)

	frames, ok := selectWalFrames(data)
	if !ok {
		t.Fatal("selectWalFrames failed")
	}
	// 提交点之后的帧属于未提交事务，不能落到解密副本上。
	if len(frames) != 2 {
		t.Fatalf("frames=%d", len(frames))
	}
	if frames[0].PageNo != 5 || frames[1].PageNo != 6 {
		t.Fatalf("frames=%v", frames)
	}
	if frames[1].DbSize != 9 {
		t.Fatalf("db size=%d", frames[1].DbSize)
	}
}

func TestSelectWalFramesStopsOnSaltMismatch(t *testing.T) {
	salt := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	data := walHeaderBytes(salt)
	data = append(data, walFrameBytes(5, 0, plainPage(1), []byte{9, 9, 9, 9, 9, 9, 9, 9})...)
	if _, ok := selectWalFrames(data); ok {
		t.Fatal("salt mismatch must invalidate the wal")
	}
}

func TestDecryptAndMergeWALAppliesCommittedPages(t *testing.T) {
	dir := t.TempDir()
	key := testKey()
	dbPath := filepath.Join(dir, "message_0.db")
	walPath := dbPath + "-wal"

	first := make([]byte, PageSize)
	copy(first[:16], "SQLite format 3\x00")
	for i := 16; i < PageSize; i++ {
		first[i] = byte(i)
	}
	binary.BigEndian.PutUint32(first[28:32], 2)

	old := plainPage(0x11)
	updated := plainPage(0x22)

	encrypted := append(encryptTestPage(t, key, first, 1), encryptTestPage(t, key, old, 2)...)
	if err := os.WriteFile(dbPath, encrypted, 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	salt := []byte{2, 4, 6, 8, 10, 12, 14, 16}
	wal := walHeaderBytes(salt)
	wal = append(wal, walFrameBytes(2, 2, encryptTestPage(t, key, updated, 2), salt)...)
	if err := os.WriteFile(walPath, wal, 0o600); err != nil {
		t.Fatalf("write wal: %v", err)
	}

	out := filepath.Join(dir, "out.db")
	if err := decryptFile(dbPath, out, key); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(raw) != 2*PageSize {
		t.Fatalf("size=%d", len(raw))
	}
	if string(raw[:16]) != "SQLite format 3\x00" {
		t.Fatalf("magic=%q", raw[:16])
	}
	if raw[PageSize+1] != 0x11 {
		t.Fatalf("page2 before merge=%x", raw[PageSize])
	}
	if err := mergeWAL(out, walPath, key); err != nil {
		t.Fatalf("merge: %v", err)
	}
	raw, err = os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if raw[PageSize+1] != 0x22 {
		t.Fatalf("page2 after merge=%x", raw[PageSize])
	}
}
