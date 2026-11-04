package wechatdb

import (
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var (
	eventNamespace = uuid.MustParse("9f1c4d1e-2b0a-4d7a-9f3e-6a7c1f0d2b11")
	accountSuffix  = regexp.MustCompile(`_\w{4}$`)
)

type Reader struct {
	DBDir      string
	Account    string
	AccountDir string
	WorkDir    string
	WXID       string
	AccountKey string
	Files      []DBFile
	Keys       map[string]KeyBlob
	Unkeyed    []string

	mu        sync.Mutex
	candidates []Candidate
	states     map[string]fileState
	names      map[string]string
	shards     []string
}

type Chat struct {
	SourceID      string `json:"source_id"`
	ChatKey       string `json:"chat_key"`
	DisplayName   string `json:"display_name"`
	ChatType      string `json:"chat_type"`
	AccountID     string `json:"account_id"`
	LastTime      int64  `json:"last_time"`
	LastMessageAt string `json:"last_message_at"`
	Unread        int    `json:"unread"`
	Summary       string `json:"summary,omitempty"`
}

type Message struct {
	SchemaVersion int    `json:"schema_version"`
	EventID       string `json:"event_id"`
	Source        string `json:"source"`
	SourceID      string `json:"source_id"`
	DisplayName   string `json:"display_name"`
	AccountID     string `json:"account_id"`
	ChatType      string `json:"chat_type"`
	MessageID     string `json:"message_id"`
	Sender        string `json:"sender"`
	SenderID      string `json:"sender_id"`
	ActorRole     string `json:"actor_role"`
	Direction     string `json:"direction"`
	MessageType   string `json:"message_type"`
	Text          string `json:"text"`
	Kind          string `json:"kind"`
	OccurredAt    string `json:"occurred_at"`
	ObservedAt    string `json:"observed_at"`
	ReceivedAt    string `json:"received_at"`
	ContentHash   string `json:"content_hash"`
	Completeness  string `json:"completeness"`
	SyncScope     string `json:"sync_scope"`
	Incomplete    bool   `json:"incomplete"`
	Shard         string `json:"shard"`
	LocalID       int64  `json:"local_id"`
	SortSeq       int64  `json:"sort_seq"`
	Cursor        string `json:"cursor"`
	RawRef        string `json:"raw_ref"`
}

func Open(dbDir, workDir, preferredAccount string) (*Reader, error) {
	if dbDir == "" {
		dbDir = DetectDBDir()
	}
	if dbDir == "" {
		return nil, fmt.Errorf("未找到微信数据库目录")
	}
	accounts := ListAccounts(dbDir)
	if len(accounts) == 0 {
		return nil, fmt.Errorf("未找到已登录账号目录")
	}
	candidates, err := ExtractKeyCandidates()
	if err != nil {
		return nil, err
	}
	type scored struct {
		account string
		files   []DBFile
		keys    map[string]KeyBlob
	}
	var best scored
	for _, account := range accounts {
		accountDir := filepath.Join(dbDir, account)
		files := CollectDBFiles(accountDir)
		keys := FindKeys(candidates, files)
		if len(keys) == 0 {
			continue
		}
		if preferredAccount != "" && account != preferredAccount {
			continue
		}
		if best.account == "" || len(keys) > len(best.keys) {
			best = scored{account: account, files: files, keys: keys}
		}
	}
	if best.account == "" {
		return nil, fmt.Errorf("无法解密任何账号数据库")
	}
	accountDir := filepath.Join(dbDir, best.account)
	workAccountDir := filepath.Join(workDir, best.account)
	states := map[string]fileState{}
	var unkeyed []string
	for _, file := range best.files {
		key, ok := best.keys[file.Rel]
		if !ok {
			unkeyed = append(unkeyed, file.Rel)
			states[file.Rel] = fileState{}
			continue
		}
		dst := filepath.Join(workAccountDir, filepath.FromSlash(file.Rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := decryptFile(file.Path, dst, key); err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", file.Rel, err)
		}
		walPath := file.Path + "-wal"
		if _, err := os.Stat(walPath); err == nil {
			_ = mergeWAL(dst, walPath, key)
		}
		if state, err := probeFile(file.Path); err == nil {
			states[file.Rel] = state
		}
	}
	wxid := accountSuffix.ReplaceAllString(best.account, "")
	key := accountKey(wxid)
	return &Reader{
		DBDir: dbDir, Account: best.account, AccountDir: accountDir,
		WorkDir: workAccountDir, WXID: wxid, AccountKey: key,
		Files: best.files, Keys: best.keys, Unkeyed: unkeyed,
		candidates: candidates, states: states,
	}, nil
}

// Refresh 把源库的最新内容同步到解密副本。微信写入先进 WAL，因此每次轮询前
// 都要刷新，否则读到的永远是连接器启动那一刻的快照。
func (r *Reader) Refresh() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshLocked()
	return nil
}

func accountKey(wxid string) string {
	sum := sha256.Sum256([]byte(wxid))
	return hex.EncodeToString(sum[:])[:16]
}

func (r *Reader) SourceID(chatKey string) string {
	return "wechat-db:" + r.AccountKey + ":" + chatKey
}

func (r *Reader) decryptedPath(name string) string {
	var found string
	_ = filepath.Walk(r.WorkDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.EqualFold(info.Name(), name) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func (r *Reader) contactNamesLocked() map[string]string {
	if r.names != nil {
		return r.names
	}
	names := map[string]string{}
	path := r.decryptedPath("contact.db")
	if path == "" {
		r.names = names
		return names
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return names
	}
	defer db.Close()
	rows, err := db.Query("SELECT username, nick_name, remark FROM contact")
	if err != nil {
		return names
	}
	defer rows.Close()
	for rows.Next() {
		var username, nick, remark sql.NullString
		if err := rows.Scan(&username, &nick, &remark); err != nil {
			continue
		}
		name := remark.String
		if name == "" {
			name = nick.String
		}
		if name == "" {
			name = username.String
		}
		names[username.String] = name
	}
	r.names = names
	return names
}

func (r *Reader) ListChats(query string, limit int) ([]Chat, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	path := r.decryptedPath("session.db")
	if path == "" {
		return nil, fmt.Errorf("session.db 未解密")
	}
	names := r.contactNamesLocked()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT username, unread_count, summary, last_timestamp,
		last_msg_sender, last_sender_display_name FROM SessionTable
		WHERE is_hidden=0 ORDER BY sort_timestamp DESC LIMIT ?`, 1000)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	needle := strings.ToLower(strings.TrimSpace(query))
	var out []Chat
	for rows.Next() {
		var username, summary, lastMsg, lastDisplay sql.NullString
		var unread sql.NullInt64
		var lastTime sql.NullInt64
		if err := rows.Scan(&username, &unread, &summary, &lastTime, &lastMsg, &lastDisplay); err != nil {
			continue
		}
		name := names[username.String]
		if name == "" {
			name = firstNonEmpty(lastDisplay.String, lastMsg.String, username.String)
		}
		if needle != "" && !strings.Contains(strings.ToLower(name), needle) && !strings.Contains(strings.ToLower(username.String), needle) {
			continue
		}
		chat := Chat{
			SourceID: r.SourceID(username.String), ChatKey: username.String,
			DisplayName: name, ChatType: chatType(username.String), AccountID: r.AccountKey,
			Unread: int(unread.Int64), Summary: summary.String,
			LastTime: lastTime.Int64,
		}
		if lastTime.Int64 > 0 {
			chat.LastMessageAt = epochISO(lastTime.Int64)
		}
		out = append(out, chat)
		if len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Reader) RecentMessages(chatKey string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	names := r.contactNamesLocked()
	display := names[chatKey]
	if display == "" {
		display = chatKey
	}
	target := "Msg_" + md5Hex(chatKey)
	paths := r.shardPaths()
	var all []Message
	for _, path := range paths {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			continue
		}
		var exists int
		_ = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", target).Scan(&exists)
		if exists == 0 {
			db.Close()
			continue
		}
		senderNames := map[int64]string{}
		if nameRows, err := db.Query("SELECT rowid, user_name FROM Name2Id"); err == nil {
			for nameRows.Next() {
				var rowid int64
				var username sql.NullString
				if err := nameRows.Scan(&rowid, &username); err == nil {
					senderNames[rowid] = username.String
				}
			}
			nameRows.Close()
		}
		rows, err := db.Query(fmt.Sprintf(`SELECT local_id, local_type, real_sender_id, create_time,
			message_content, sort_seq FROM "%s" ORDER BY sort_seq DESC, local_id DESC LIMIT ?`, target), limit)
		if err != nil {
			db.Close()
			continue
		}
		rel, _ := filepath.Rel(r.WorkDir, path)
		shard := strings.ReplaceAll(filepath.ToSlash(rel), "/", "__")
		for rows.Next() {
			var localID, localType, senderID, createTime, sortSeq sql.NullInt64
			var content []byte
			if err := rows.Scan(&localID, &localType, &senderID, &createTime, &content, &sortSeq); err != nil {
				continue
			}
			sender := senderNames[senderID.Int64]
			all = append(all, r.messageFromRow(chatKey, display, shard, localID.Int64, localType.Int64, sender, createTime.Int64, content, sortSeq.Int64))
		}
		rows.Close()
		db.Close()
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].SortSeq == all[j].SortSeq {
			return all[i].LocalID < all[j].LocalID
		}
		return all[i].SortSeq < all[j].SortSeq
	})
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

// shardPaths 列出解密副本里的消息分片，刷新后缓存失效。
func (r *Reader) shardPaths() []string {
	if r.shards != nil {
		return r.shards
	}
	var paths []string
	_ = filepath.Walk(r.WorkDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(name, "message_") && strings.HasSuffix(strings.ToLower(name), ".db") {
			paths = append(paths, path)
		}
		return nil
	})
	r.shards = paths
	return paths
}

func (r *Reader) messageFromRow(chatKey, display, shard string, localID, localType int64,
	senderID string, createTime int64, content []byte, sortSeq int64) Message {
	messageKey := fmt.Sprintf("%s:%s:%s:%d", r.AccountKey, chatKey, shard, localID)
	eventID := uuid.NewSHA1(eventNamespace, []byte(messageKey)).String()
	kind := messageType(localType)
	text := decodeContent(content, kind)
	senderDisplay := senderID
	if senderID == r.WXID {
		senderDisplay = "我"
	} else if senderID != "" {
		if name := r.contactNamesLocked()[senderID]; name != "" {
			senderDisplay = name
		}
	}
	if senderID == "" {
		senderDisplay = ""
	}
	direction := "unknown"
	actorRole := "unknown"
	if localType == 10000 {
		direction = "system"
		actorRole = "system"
		senderDisplay = "系统"
	} else if senderID == r.WXID {
		direction = "self"
		actorRole = "owner"
	} else if senderID != "" {
		direction = "other"
		actorRole = "contact"
	}
	observed := time.Now().In(time.Local).Format(time.RFC3339)
	occurred := ""
	if createTime > 0 {
		occurred = epochISO(createTime)
	}
	hash := sha256.Sum256([]byte(text))
	return Message{
		SchemaVersion: 2, EventID: eventID, Source: display, SourceID: r.SourceID(chatKey),
		DisplayName: display, AccountID: r.AccountKey, ChatType: chatType(chatKey),
		MessageID: messageKey, Sender: senderDisplay, SenderID: senderID,
		ActorRole: actorRole, Direction: direction, MessageType: kind, Text: text,
		Kind: "message", OccurredAt: occurred, ObservedAt: observed, ReceivedAt: observed,
		ContentHash: hex.EncodeToString(hash[:]), Completeness: "complete",
		SyncScope: "local_incremental", Shard: shard, LocalID: localID,
		SortSeq: sortSeq, Cursor: messageKey,
	}
}

func messageType(localType int64) string {
	switch localType {
	case 1:
		return "text"
	case 3:
		return "image"
	case 34:
		return "voice"
	case 43:
		return "video"
	case 49:
		return "file"
	case 10000:
		return "system"
	default:
		return "other"
	}
}

func decodeContent(content []byte, kind string) string {
	if kind == "text" {
		if utf8.Valid(content) {
			text := strings.TrimSpace(string(content))
			if index := strings.Index(text, "\x01"); index >= 0 {
				text = text[:index]
			}
			if text != "" {
				return text
			}
		}
	}
	switch kind {
	case "image":
		return "[图片]"
	case "voice":
		return "[语音]"
	case "video":
		return "[视频]"
	case "file":
		return "[文件/链接/卡片]"
	case "system":
		return "[系统消息]"
	default:
		return "[其他消息]"
	}
}

func chatType(username string) string {
	if strings.HasSuffix(username, "@chatroom") {
		return "group"
	}
	if strings.HasPrefix(username, "gh_") || strings.HasPrefix(username, "brand") {
		return "system"
	}
	return "private"
}

func epochISO(epoch int64) string {
	return time.Unix(epoch, 0).In(time.Local).Format(time.RFC3339)
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
