package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goldenfinger/agent/internal/wechatdb"
)

const protocolVersion = 1
const scanLimit = 500
const seenLimit = 500

type stateFile struct {
	Seen    map[string][]string `json:"seen"`
	LastSeq map[string]int64    `json:"last_seq,omitempty"`
}

type chatConfig struct {
	ChatKey     string `json:"chat_key"`
	SourceID    string `json:"source_id"`
	DisplayName string `json:"display_name"`
}

type connector struct {
	mu        sync.Mutex
	out       *json.Encoder
	writeMu   sync.Mutex
	reader    *wechatdb.Reader
	readerErr error
	workDir   string
	statePath string
	state     stateFile
	enabled   bool
	chats     []chatConfig
	stopCh    chan struct{}
	pollDone  chan struct{}
	lastRead  string
	lastEvent string
	dbDir     string
	account   string
}

func main() {
	workDir := flag.String("workdir", "", "解密工作目录")
	statePath := flag.String("state", "", "检查点文件")
	dbDir := flag.String("db-dir", "", "微信数据根目录")
	account := flag.String("account", "", "账号目录名")
	flag.Parse()
	if *workDir == "" || *statePath == "" {
		fmt.Fprintln(os.Stderr, "缺少 --workdir 或 --state")
		os.Exit(2)
	}
	c := &connector{workDir: *workDir, statePath: *statePath, dbDir: *dbDir, account: *account,
		out: json.NewEncoder(os.Stdout)}
	c.loadState()
	c.write(map[string]any{"type": "ready", "protocol_version": protocolVersion})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			c.write(map[string]any{"type": "error", "code": "bad_request", "message": "无效的请求"})
			continue
		}
		kind, _ := request["type"].(string)
		id, _ := request["id"].(string)
		reply := c.handle(kind, request)
		if id != "" {
			reply["request_id"] = id
		}
		c.write(reply)
		if kind == "stop" {
			return
		}
	}
	c.stopPoll()
}

func (c *connector) handle(kind string, request map[string]any) map[string]any {
	switch kind {
	case "configure":
		return c.configure(request)
	case "list_chats":
		query, _ := request["query"].(string)
		limit := intNumber(request["limit"], 50)
		chats, err := c.listChats(query, limit)
		if err != nil {
			return safeError(err)
		}
		return map[string]any{"type": "chats", "chats": chats}
	case "test":
		chatKey, _ := request["chat_key"].(string)
		sourceID, _ := request["source_id"].(string)
		if chatKey == "" {
			parts := strings.Split(sourceID, ":")
			chatKey = parts[len(parts)-1]
		}
		preview, err := c.test(chatKey)
		if err != nil {
			return safeError(err)
		}
		return map[string]any{"type": "test", "ok": true, "chat_key": chatKey, "preview": preview}
	case "health":
		return c.health()
	case "stop":
		c.stopPoll()
		return map[string]any{"type": "stopped"}
	default:
		return map[string]any{"type": "error", "code": "unknown_request", "message": "未知请求"}
	}
}

func (c *connector) ensureReader() (*wechatdb.Reader, error) {
	if c.reader != nil {
		return c.reader, nil
	}
	if c.readerErr != nil {
		return nil, c.readerErr
	}
	reader, err := wechatdb.Open(c.dbDir, c.workDir, c.account)
	if err != nil {
		c.readerErr = err
		return nil, err
	}
	c.reader = reader
	return reader, nil
}

func (c *connector) configure(request map[string]any) map[string]any {
	c.stopPoll()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled, _ = request["enabled"].(bool)
	c.chats = nil
	if raw, ok := request["chats"].([]any); ok {
		for _, item := range raw {
			switch value := item.(type) {
			case string:
				c.chats = append(c.chats, chatConfig{ChatKey: value})
			case map[string]any:
				chat := chatConfig{}
				chat.ChatKey, _ = value["chat_key"].(string)
				chat.SourceID, _ = value["source_id"].(string)
				chat.DisplayName, _ = value["display_name"].(string)
				if chat.ChatKey != "" {
					c.chats = append(c.chats, chat)
				}
			}
		}
	}
	if c.enabled && len(c.chats) > 0 {
		if _, err := c.ensureReader(); err != nil {
			return safeError(err)
		}
		c.stopCh = make(chan struct{})
		c.pollDone = make(chan struct{})
		interval := time.Duration(intNumber(request["interval_ms"], 2000)) * time.Millisecond
		if interval < 500*time.Millisecond {
			interval = 500 * time.Millisecond
		}
		go c.pollLoop(interval, c.stopCh, c.pollDone)
	}
	return map[string]any{"type": "configured", "enabled": c.enabled, "chats": len(c.chats)}
}

func (c *connector) stopPoll() {
	c.mu.Lock()
	stop, done := c.stopCh, c.pollDone
	c.stopCh, c.pollDone = nil, nil
	c.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *connector) pollLoop(interval time.Duration, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case <-time.After(interval):
			c.pollOnce()
		}
	}
}

func (c *connector) pollOnce() {
	c.mu.Lock()
	defer c.mu.Unlock()
	reader, err := c.ensureReader()
	if err != nil {
		return
	}
	for _, chat := range c.chats {
		messages, err := reader.RecentMessages(chat.ChatKey, scanLimit)
		if err != nil {
			continue
		}
		keys := make([]string, 0, len(messages))
		maxSeq := int64(0)
		for _, message := range messages {
			keys = append(keys, message.MessageID)
			if message.SortSeq > maxSeq {
				maxSeq = message.SortSeq
			}
		}
		previous, existed := c.state.Seen[chat.ChatKey]
		previousSeq, hasSeq := c.state.LastSeq[chat.ChatKey]
		// 首次启用或从旧检查点迁移：只记录当前高水位，不回灌。
		if !existed || !hasSeq {
			c.state.Seen[chat.ChatKey] = tail(keys, seenLimit)
			if c.state.LastSeq == nil {
				c.state.LastSeq = map[string]int64{}
			}
			c.state.LastSeq[chat.ChatKey] = maxSeq
			c.saveState()
			continue
		}
		previousSet := make(map[string]bool, len(previous))
		for _, key := range previous {
			previousSet[key] = true
		}
		overlap := false
		var fresh []wechatdb.Message
		for _, message := range messages {
			if message.SortSeq < previousSeq {
				continue
			}
			if message.SortSeq == previousSeq && previousSet[message.MessageID] {
				overlap = true
				continue
			}
			if previousSet[message.MessageID] {
				overlap = true
				continue
			}
			fresh = append(fresh, message)
		}
		if len(fresh) > 0 {
			gap := len(previousSet) > 0 && !overlap && len(messages) >= scanLimit
			for _, message := range fresh {
				if gap {
					message.Completeness = "partial"
				}
				c.writeMessage(message)
			}
			c.lastEvent = time.Now().In(time.Local).Format(time.RFC3339)
		}
		c.state.Seen[chat.ChatKey] = tail(keys, seenLimit)
		if c.state.LastSeq == nil {
			c.state.LastSeq = map[string]int64{}
		}
		if maxSeq > previousSeq {
			c.state.LastSeq[chat.ChatKey] = maxSeq
		}
		c.saveState()
	}
	c.lastRead = time.Now().In(time.Local).Format(time.RFC3339)
}

func (c *connector) write(value any) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.out.Encode(value)
}

func (c *connector) writeMessage(message wechatdb.Message) {
	c.write(map[string]any{"type": "message", "message": message})
}

func (c *connector) listChats(query string, limit int) ([]wechatdb.Chat, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reader, err := c.ensureReader()
	if err != nil {
		return nil, err
	}
	return reader.ListChats(query, limit)
}

func (c *connector) test(chatKey string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reader, err := c.ensureReader()
	if err != nil {
		return nil, err
	}
	messages, err := reader.RecentMessages(chatKey, 1)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return map[string]any{"text": ""}, nil
	}
	message := messages[len(messages)-1]
	text := []rune(message.Text)
	if len(text) > 120 {
		text = text[:120]
	}
	return map[string]any{
		"sender": message.Sender, "direction": message.Direction,
		"message_type": message.MessageType, "text": string(text),
		"occurred_at": message.OccurredAt,
	}, nil
}

func (c *connector) health() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := "off"
	if c.readerErr != nil {
		state = "needs_login"
	} else if c.enabled && len(c.chats) > 0 {
		state = "observing"
	}
	accountKey := ""
	if c.reader != nil {
		accountKey = c.reader.AccountKey
	}
	return map[string]any{
		"type": "health", "state": state, "protocol_version": protocolVersion,
		"authorized_chats": len(c.chats), "last_db_read_at": c.lastRead,
		"last_event_at": c.lastEvent, "account_key": accountKey,
	}
}

func (c *connector) loadState() {
	raw, err := os.ReadFile(c.statePath)
	if err == nil {
		_ = json.Unmarshal(raw, &c.state)
	}
	if c.state.Seen == nil {
		c.state.Seen = map[string][]string{}
	}
	if c.state.LastSeq == nil {
		c.state.LastSeq = map[string]int64{}
	}
}

func (c *connector) saveState() {
	_ = os.MkdirAll(filepath.Dir(c.statePath), 0o700)
	raw, err := json.Marshal(c.state)
	if err != nil {
		return
	}
	temp := c.statePath + ".tmp"
	if err := os.WriteFile(temp, raw, 0o600); err == nil {
		_ = os.Rename(temp, c.statePath)
	}
}

func tail(values []string, limit int) []string {
	if len(values) <= limit {
		return append([]string(nil), values...)
	}
	return append([]string(nil), values[len(values)-limit:]...)
}

func intNumber(value any, fallback int) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	default:
		return fallback
	}
}

func safeError(err error) map[string]any {
	text := strings.ToLower(err.Error())
	code := "internal"
	message := "微信数据库读取失败"
	switch {
	case strings.Contains(text, "weixin") || strings.Contains(text, "登录"):
		code, message = "needs_login", "未检测到已登录的微信"
	case strings.Contains(text, "密钥") || strings.Contains(text, "key"):
		code, message = "key_unavailable", "无法读取数据库密钥"
	case strings.Contains(text, "permission") || strings.Contains(text, "拒绝"):
		code, message = "permission_denied", "没有读取微信数据库的权限"
	case strings.Contains(text, "锁定") || strings.Contains(text, "locked"):
		code, message = "db_locked", "微信数据库正忙"
	}
	return map[string]any{"type": "error", "code": code, "message": message}
}
