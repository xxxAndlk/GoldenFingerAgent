package wechatdb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"os"
)

const (
	PageSize  = 4096
	Reserve   = 80
	WalHeader = 32
	WalFrame  = 4120
)

type KeyBlob struct {
	Key  []byte
	Salt []byte
}

func (k KeyBlob) encKey() []byte {
	if len(k.Key) >= 32 {
		return k.Key[:32]
	}
	return k.Key
}

func pbkdf2SHA512(password, salt []byte, iterations, keyLen int) []byte {
	mac := hmac.New(sha512.New, password)
	hashLen := mac.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	for block := 1; block <= blocks; block++ {
		mac.Reset()
		mac.Write(salt)
		var index [4]byte
		binary.BigEndian.PutUint32(index[:], uint32(block))
		mac.Write(index[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func verifyKey(encKey, page1, salt []byte) bool {
	if len(page1) < PageSize || len(encKey) < 32 {
		return false
	}
	if len(encKey) == 48 && salt == nil {
		encKey, salt = encKey[:32], encKey[32:]
	}
	if salt == nil {
		salt = page1[:16]
	}
	if len(salt) != 16 {
		return false
	}
	macSalt := make([]byte, len(salt))
	for i, b := range salt {
		macSalt[i] = b ^ 0x3A
	}
	macKey := pbkdf2SHA512(encKey[:32], macSalt, 2, 32)
	mac := hmac.New(sha512.New, macKey)
	mac.Write(page1[16 : PageSize-Reserve+16])
	var pageNo [4]byte
	binary.LittleEndian.PutUint32(pageNo[:], 1)
	mac.Write(pageNo[:])
	return hmac.Equal(mac.Sum(nil), page1[PageSize-64:PageSize])
}

func decryptPage(key KeyBlob, page []byte, pgno int) ([]byte, error) {
	if len(page) < PageSize {
		return nil, errors.New("short page")
	}
	iv := page[PageSize-Reserve : PageSize-Reserve+16]
	encKey := key.encKey()
	var plain []byte
	var err error
	if pgno == 1 {
		enc := page[16 : PageSize-Reserve]
		plain, err = aesCBCDecrypt(encKey, iv, enc)
		if err != nil {
			return nil, err
		}
		header := []byte("SQLite format 3\x00")
		if len(key.Key) == 48 {
			header = page[:16]
		}
		out := make([]byte, PageSize)
		copy(out, header)
		copy(out[16:], plain)
		return out, nil
	}
	enc := page[:PageSize-Reserve]
	plain, err = aesCBCDecrypt(encKey, iv, enc)
	if err != nil {
		return nil, err
	}
	out := make([]byte, PageSize)
	copy(out, plain)
	return out, nil
}

func aesCBCDecrypt(key, iv, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, errors.New("bad cipher length")
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return out, nil
}

func decryptFile(src, dst string, key KeyBlob) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	page := make([]byte, PageSize)
	for pgno := 1; ; pgno++ {
		n, readErr := in.Read(page)
		if n == 0 {
			break
		}
		if n < PageSize {
			for i := n; i < PageSize; i++ {
				page[i] = 0
			}
		}
		plain, err := decryptPage(key, page, pgno)
		if err != nil {
			return err
		}
		if _, err := out.Write(plain); err != nil {
			return err
		}
		if readErr != nil {
			break
		}
	}
	return out.Sync()
}

func mergeWAL(dst, walPath string, key KeyBlob) error {
	wal, err := os.Open(walPath)
	if err != nil {
		return nil
	}
	defer wal.Close()
	header := make([]byte, WalHeader)
	if _, err := wal.Read(header); err != nil {
		return nil
	}
	walSalt := append([]byte(nil), header[16:24]...)
	info, err := wal.Stat()
	if err != nil {
		return err
	}
	frames := (info.Size() - WalHeader) / WalFrame
	if frames <= 0 {
		return nil
	}
	out, err := os.OpenFile(dst, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer out.Close()
	maxPage := 0
	for i := int64(0); i < frames; i++ {
		if _, err := wal.Seek(WalHeader+i*WalFrame, 0); err != nil {
			break
		}
		frameHeader := make([]byte, 24)
		page := make([]byte, PageSize)
		if _, err := wal.Read(frameHeader); err != nil {
			break
		}
		if _, err := wal.Read(page); err != nil {
			break
		}
		if !hmac.Equal(frameHeader[8:16], walSalt) {
			continue
		}
		pgno := int(binary.BigEndian.Uint32(frameHeader[:4]))
		if pgno <= 0 {
			continue
		}
		plain, err := decryptPage(key, page, pgno)
		if err != nil {
			continue
		}
		if pgno == 1 {
			if len(key.Key) == 48 {
				if len(plain) < PageSize || plain[16] != 1 {
					continue
				}
			} else if string(plain[:16]) != "SQLite format 3\x00" {
				continue
			}
		} else if !validPageType(plain[0]) {
			continue
		}
		if _, err := out.Seek(int64(pgno-1)*PageSize, 0); err != nil {
			break
		}
		if _, err := out.Write(plain); err != nil {
			break
		}
		if pgno > maxPage {
			maxPage = pgno
		}
	}
	if maxPage > 0 {
		page1 := make([]byte, PageSize)
		if _, err := out.ReadAt(page1, 0); err == nil {
			pages := int(binary.BigEndian.Uint32(page1[28:32]))
			if maxPage > pages {
				binary.BigEndian.PutUint32(page1[28:32], uint32(maxPage))
				_, _ = out.WriteAt(page1, 0)
			}
		}
	}
	return out.Sync()
}

func validPageType(b byte) bool {
	switch b {
	case 0, 2, 5, 10, 13:
		return true
	default:
		return false
	}
}
