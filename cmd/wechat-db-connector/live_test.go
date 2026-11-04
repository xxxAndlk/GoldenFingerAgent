package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha512"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"goldenfinger/agent/internal/wechatdb"
)

const (
	livePageSize  = 4096
	liveReserve   = 80
	liveWalHeader = 32
	liveWalFrame  = 4120
)

// liveEncryptDB 把明文 SQLite 文件逐页加密，写成 SQLCipher 库文件。
// 布局与 wechatdb 解密端一致：首页前 16 字节是明文盐，尾部 80 字节放 IV 和 HMAC。
func liveEncryptDB(t *testing.T, key []byte, plain []byte, outPath string) [][]byte {
	t.Helper()
	pages := liveSplitPages(plain)
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i*5 + 7)
	}
	encrypted := make([][]byte, len(pages))
	for index, page := range pages {
		enc := liveEncryptPage(t, key, page, index+1)
		if index == 0 {
			copy(enc[:16], salt)
			copy(enc[livePageSize-64:], livePageHMAC(key, salt, enc))
		}
		encrypted[index] = enc
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	blob := []byte{}
	for _, page := range encrypted {
		blob = append(blob, page...)
	}
	if err := os.WriteFile(outPath, blob, 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	return encrypted
}

func liveSplitPages(plain []byte) [][]byte {
	var pages [][]byte
	for offset := 0; offset < len(plain); offset += livePageSize {
		end := offset + livePageSize
		if end > len(plain) {
			end = len(plain)
		}
		page := make([]byte, livePageSize)
		copy(page, plain[offset:end])
		pages = append(pages, page)
	}
	return pages
}

func liveEncryptPage(t *testing.T, key, plain []byte, pgno int) []byte {
	t.Helper()
	page := make([]byte, livePageSize)
	iv := make([]byte, 16)
	for i := range iv {
		iv[i] = byte(i + 1)
	}
	copy(page[livePageSize-liveReserve:livePageSize-liveReserve+16], iv)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	var data, target []byte
	if pgno == 1 {
		copy(page[:16], plain[:16])
		data = plain[16 : livePageSize-liveReserve]
		target = page[16 : livePageSize-liveReserve]
	} else {
		data = plain[:livePageSize-liveReserve]
		target = page[:livePageSize-liveReserve]
	}
	encrypted := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, data)
	copy(target, encrypted)
	return page
}

func livePageHMAC(key, salt, encryptedPage []byte) []byte {
	macSalt := make([]byte, len(salt))
	for i, b := range salt {
		macSalt[i] = b ^ 0x3A
	}
	macKey := livePBKDF2(key, macSalt, 2, 32)
	mac := hmac.New(sha512.New, macKey)
	// 与 verifyKey 一致：HMAC 覆盖盐之后到 HMAC 字段之前的全部内容（含 IV）。
	mac.Write(encryptedPage[16 : livePageSize-64])
	var pageNo [4]byte
	binary.LittleEndian.PutUint32(pageNo[:], uint32(1))
	mac.Write(pageNo[:])
	return mac.Sum(nil)
}

func livePBKDF2(password, salt []byte, iterations, keyLen int) []byte {
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

// liveWriteWAL 把变化的页写成 WAL 帧，模拟微信写入后尚未 checkpoint 的状态。
func liveWriteWAL(t *testing.T, encrypted [][]byte, changed map[int]bool, walPath string) {
	t.Helper()
	salt := []byte{9, 8, 7, 6, 5, 4, 3, 2}
	header := make([]byte, liveWalHeader)
	binary.BigEndian.PutUint32(header[:4], 0x377f0682)
	binary.BigEndian.PutUint32(header[8:12], livePageSize)
	copy(header[16:24], salt)
	blob := append([]byte(nil), header...)
	for index := range encrypted {
		if !changed[index+1] {
			continue
		}
		frame := make([]byte, liveWalFrame)
		binary.BigEndian.PutUint32(frame[:4], uint32(index+1))
		binary.BigEndian.PutUint32(frame[4:8], uint32(len(encrypted)))
		copy(frame[8:16], salt)
		copy(frame[24:], encrypted[index])
		blob = append(blob, frame...)
	}
	if err := os.WriteFile(walPath, blob, 0o600); err != nil {
		t.Fatalf("write wal: %v", err)
	}
}

// liveCreatePlainDB 造一个与微信消息库同构的明文 SQLite 库。
// 微信的库每页保留 80 字节给 IV 和 HMAC，所以这里同样按保留字节建库，
// 否则解密后每页尾部会被清零，schema 会被破坏。
func liveCreatePlainDB(t *testing.T, path, chatKey string) {
	t.Helper()
	_ = os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec("PRAGMA page_size=4096"); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		t.Fatalf("vacuum: %v", err)
	}
	db.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw[20] = liveReserve
	binary.BigEndian.PutUint16(raw[105:107], livePageSize-liveReserve)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("patch header: %v", err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	table := fmt.Sprintf("Msg_%s", liveMD5(chatKey))
	if _, err := db.Exec(`CREATE TABLE Name2Id (rowid INTEGER PRIMARY KEY, user_name TEXT)`); err != nil {
		t.Fatalf("create Name2Id: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE "%s" (
		local_id INTEGER PRIMARY KEY, local_type INTEGER, real_sender_id INTEGER,
		create_time INTEGER, message_content BLOB, sort_seq INTEGER)`, table)); err != nil {
		t.Fatalf("create message table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO Name2Id (rowid, user_name) VALUES (1, 'wxid_contact')`); err != nil {
		t.Fatalf("insert Name2Id: %v", err)
	}
}

func liveInsertMessage(t *testing.T, path, chatKey string, localID, sortSeq int64, text string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	table := fmt.Sprintf("Msg_%s", liveMD5(chatKey))
	if _, err := db.Exec(fmt.Sprintf(
		`INSERT INTO "%s" (local_id, local_type, real_sender_id, create_time, message_content, sort_seq)
		 VALUES (?, 1, 1, ?, ?, ?)`, table),
		localID, int64(1793351290)+localID, []byte(text), sortSeq); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

func liveMD5(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// TestConnectorSeesNewMessageWithoutRestart 用真实微信密钥构造合成库，
// 验证连接器在运行期间刷新数据库后能拿到新消息，不依赖重启。
func TestConnectorSeesNewMessageWithoutRestart(t *testing.T) {
	candidates, err := wechatdb.ExtractKeyCandidates()
	if err != nil {
		t.Skipf("跳过真机验证：%v", err)
	}
	if len(candidates) == 0 {
		t.Skip("跳过真机验证：没有可用密钥")
	}
	root := t.TempDir()
	dbStorage := filepath.Join(root, "wxid_live_1234", "db_storage")
	workDir := filepath.Join(root, "work")
	plainPath := filepath.Join(root, "plain.db")
	chatKey := "wxid_live_chat"

	liveCreatePlainDB(t, plainPath, chatKey)
	liveInsertMessage(t, plainPath, chatKey, 1, 100, "第一条")
	plain, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatalf("read plain: %v", err)
	}
	sourcePath := filepath.Join(dbStorage, "message", "message_0.db")
	liveEncryptDB(t, candidates[0].Key, plain, sourcePath)

	connector := &connector{
		workDir: workDir, dbDir: root, statePath: filepath.Join(root, "state.json"),
		state:   stateFile{Seen: map[string][]string{}, LastSeq: map[string]int64{}, Pending: map[string][]string{}},
		pending: map[string]pendingMessage{}, maxSeq: map[string]int64{},
		out: json.NewEncoder(io.Discard),
	}
	reader, err := connector.ensureReader()
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	chat := chatConfig{ChatKey: chatKey, SourceID: reader.SourceID(chatKey)}
	connector.chats = []chatConfig{chat}

	// 第一轮：建立基线，不回灌历史。
	connector.pollOnce()
	if len(connector.pending) != 0 {
		t.Fatalf("基线不应投递消息：%v", connector.pending)
	}
	first, err := reader.RecentMessages(chatKey, 50)
	if err != nil || len(first) != 1 {
		t.Fatalf("首轮读取=%d err=%v", len(first), err)
	}

	// 微信侧写入新消息：写进明文库后，以 WAL 帧形式落到加密库旁边。
	oldPages := liveSplitPages(plain)
	liveInsertMessage(t, plainPath, chatKey, 2, 101, "新消息")
	updated, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatalf("read plain: %v", err)
	}
	updatedPages := liveSplitPages(updated)
	changed := map[int]bool{}
	for index := range updatedPages {
		if index >= len(oldPages) || !liveSamePage(oldPages[index], updatedPages[index]) {
			changed[index+1] = true
		}
	}
	if len(changed) == 0 {
		t.Skip("合成库没有产生页面变化")
	}
	reEncrypted := make([][]byte, len(updatedPages))
	for index := range updatedPages {
		reEncrypted[index] = liveEncryptPage(t, candidates[0].Key, updatedPages[index], index+1)
	}
	liveWriteWAL(t, reEncrypted, changed, sourcePath+"-wal")

	// 第二轮：连接器不重启，只刷新数据库就应该看到新消息。
	connector.pollOnce()
	if len(connector.pending) != 1 {
		t.Fatalf("应投递一条新消息：%v", connector.pending)
	}
	wantID := fmt.Sprintf("%s:%s:message__message_0.db:2", reader.AccountKey, chatKey)
	for id := range connector.pending {
		if id != wantID {
			t.Fatalf("投递了错误的消息：%s", id)
		}
	}
	if connector.state.LastSeq[chatKey] != 100 {
		t.Fatalf("未确认前高水位不能越过新消息：%d", connector.state.LastSeq[chatKey])
	}
}

func liveSamePage(a []byte, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
