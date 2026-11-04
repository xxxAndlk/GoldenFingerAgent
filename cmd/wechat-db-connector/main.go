package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
const resendAfter = 30 * time.Second

type stateFile struct {
	Seen      map[string][]string `json:"seen"`
	LastSeq   map[string]int64    `json:"last_seq,omitempty"`
	Pending   map[string][]string `json:"pending,omitempty"`
	Baselined map[string]bool     `json:"baselined,omitempty"`
}

type chatConfig struct {
	ChatKey     string `json:"chat_key"`
	SourceID    string `json:"source_id"`
	DisplayName string `json:"display_name"`
}

// pendingMessage 是已经交给桌面端、但还没有收到入库确认的消息。
type pendingMessage struct {
	chatKey string
	message wechatdb.Message
	sentAt  time.Time
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
	pending   map[string]pendingMessage
	maxSeq    map[string]int64
}

func main() {
	if err := run(os.Stdin, os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// run 是连接器的协议循环。输入输出被参数化，方便测试直接驱动。
func run(in io.Reader, out io.Writer, args []string) error {
	flags := flag.NewFlagSet("wechat-db-connector", flag.ContinueOnError)
	workDir := flags.String("workdir", "", "解密工作目录")
	statePath := flags.String("state", "", "检查点文件")
	dbDir := flags.String("db-dir", "", "微信数据根目录")
	account := flags.String("account", "", "账号目录名")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *workDir == "" || *statePath == "" {
		return errors.New("缺少 --workdir 或 --state")
	}
	c := &connector{workDir: *workDir, statePath: *statePath, dbDir: *dbDir, account: *account,
		pending: map[string]pendingMessage{}, maxSeq: map[string]int64{},
		out: json.NewEncoder(out)}
	c.loadState()
	c.write(map[string]any{"type": "ready", "protocol_version": protocolVersion})
	scanner := bufio.NewScanner(in)
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
			return nil
		}
	}
	c.stopPoll()
	return nil
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
	case "ack":
		return c.acknowledge(request)
	case "stop":
		c.stopPoll()
		return map[string]any{"type": "stopped"}
	default:
		return map[string]any{"type": "error", "code": "unknown_request", "message": "未知请求"}
	}
}

// acknowledge 记录桌面端已把消息持久化到待发队列。只有收到确认后，
// 该消息才计入已处理，检查点才允许向前推进。
func (c *connector) acknowledge(request map[string]any) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	messageID, _ := request["message_id"].(string)
	if messageID == "" {
		return map[string]any{"type": "error", "code": "bad_ack", "message": "确认缺少 message_id"}
	}
	item, known := c.pending[messageID]
	if !known {
		return map[string]any{"type": "acked", "message_id": messageID, "known": false}
	}
	persisted, _ := request["ok"].(bool)
	discard, _ := request["discard"].(bool)
	if !persisted && !discard {
		// 桌面端落盘失败必须保留 pending，等待重发；绝不能推进检查点。
		return map[string]any{"type": "acked", "message_id": messageID, "known": true, "persisted": false}
	}
	delete(c.pending, messageID)
	if persisted {
		c.state.Seen[item.chatKey] = append(c.state.Seen[item.chatKey], messageID)
	}
	// discard 只用于越权等明确不应入库的消息，允许推进但不能写入 Seen。
	c.persistState(item.chatKey)
	return map[string]any{"type": "acked", "message_id": messageID, "known": true, "persisted": persisted, "discarded": discard}
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
	// 撤销授权的会话不再投递，未确认的消息直接丢弃，否则检查点会被它永久挡住。
	keep := make(map[string]bool, len(c.chats))
	for _, chat := range c.chats {
		keep[chat.ChatKey] = true
	}
	for id, item := range c.pending {
		if !keep[item.chatKey] {
			delete(c.pending, id)
		}
	}
	for _, chat := range c.chats {
		// 还没建立过高水位的会话留给首轮基线处理，这里不要提前写入。
		if _, ok := c.state.LastSeq[chat.ChatKey]; !ok {
			continue
		}
		c.persistState(chat.ChatKey)
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
	// 微信的新写入先进 WAL，不刷新就只能看到连接器启动那一刻的快照。
	_ = reader.Refresh()
	for _, chat := range c.chats {
		c.pollChat(reader, chat)
	}
	c.lastRead = time.Now().In(time.Local).Format(time.RFC3339)
}

func (c *connector) pollChat(reader *wechatdb.Reader, chat chatConfig) {
	if c.state.Baselined == nil {
		c.state.Baselined = map[string]bool{}
	}
	messages, err := reader.RecentMessages(chat.ChatKey, scanLimit)
	if err != nil {
		return
	}
	maxSeq := int64(0)
	for _, message := range messages {
		if message.SortSeq > maxSeq {
			maxSeq = message.SortSeq
		}
	}
	c.maxSeq[chat.ChatKey] = maxSeq
	previous, existed := c.state.Seen[chat.ChatKey]
	previousSeq, hasSeq := c.state.LastSeq[chat.ChatKey]
	if !c.isBaselined(chat.ChatKey, previous, existed, previousSeq, hasSeq) {
		// 首次启用或从旧检查点迁移：只记录当前高水位，不回灌历史。
		c.state.Seen[chat.ChatKey] = tail(messageIDs(messages), seenLimit)
		c.state.Baselined[chat.ChatKey] = true
		c.persistState(chat.ChatKey)
		return
	}
	c.state.Baselined[chat.ChatKey] = true
	fresh := admitMessages(previous, c.pending, previousSeq, messages)
	if len(fresh) > 0 {
		gap := len(previous) > 0 && !hasOverlap(previous, messages) && len(messages) >= scanLimit
		for _, message := range fresh {
			if gap {
				message.Completeness = "partial"
			}
			c.emit(chat.ChatKey, message)
		}
		c.lastEvent = time.Now().In(time.Local).Format(time.RFC3339)
	}
	c.retryStale(chat.ChatKey)
	c.persistState(chat.ChatKey)
}

// isBaselined 判断会话是否已经建立过高水位。旧检查点没有这个标记，
// 只能靠“确实记过消息”来推断；否则会把 last_seq=0 的空会话当成已基线，
// 进而在启用瞬间回灌整段历史。
func (c *connector) isBaselined(chatKey string, previous []string, existed bool, previousSeq int64, hasSeq bool) bool {
	if c.state.Baselined[chatKey] {
		return true
	}
	return existed && hasSeq && len(previous) > 0 && previousSeq > 0
}

// admitMessages 决定这一轮要投递哪些消息：已确认的不再投，未确认的继续等待确认。
func admitMessages(previous []string, pending map[string]pendingMessage, watermark int64, messages []wechatdb.Message) []wechatdb.Message {
	acked := make(map[string]bool, len(previous))
	for _, key := range previous {
		acked[key] = true
	}
	var fresh []wechatdb.Message
	for _, message := range messages {
		if message.SortSeq < watermark {
			continue
		}
		if acked[message.MessageID] {
			continue
		}
		if _, waiting := pending[message.MessageID]; waiting {
			continue
		}
		fresh = append(fresh, message)
	}
	return fresh
}

// nextWatermark 算出新的高水位。只要还有消息没有确认，高水位就不能越过它，
// 否则进程重启后这条消息会被永久跳过。
func nextWatermark(maxSeq int64, pending map[string]pendingMessage, chatKey string) int64 {
	value := maxSeq
	for _, item := range pending {
		if item.chatKey != chatKey {
			continue
		}
		if item.message.SortSeq-1 < value {
			value = item.message.SortSeq - 1
		}
	}
	if value < 0 {
		value = 0
	}
	return value
}

// hasOverlap 判断扫描窗口里是否还有已知消息。没有重叠说明中间存在缺口。
func hasOverlap(previous []string, messages []wechatdb.Message) bool {
	if len(previous) == 0 {
		return false
	}
	known := make(map[string]bool, len(previous))
	for _, key := range previous {
		known[key] = true
	}
	for _, message := range messages {
		if known[message.MessageID] {
			return true
		}
	}
	return false
}

// emit 把消息交给桌面端，并登记为未确认。确认到达前检查点不会越过它。
func (c *connector) emit(chatKey string, message wechatdb.Message) {
	c.pending[message.MessageID] = pendingMessage{
		chatKey: chatKey, message: message, sentAt: time.Now(),
	}
	c.writeMessage(message)
}

// retryStale 重发长时间没有收到确认的消息，避免确认丢失导致消息永久卡住。
func (c *connector) retryStale(chatKey string) {
	now := time.Now()
	for id, item := range c.pending {
		if item.chatKey != chatKey {
			continue
		}
		if now.Sub(item.sentAt) < resendAfter {
			continue
		}
		item.sentAt = now
		c.pending[id] = item
		c.writeMessage(item.message)
	}
}

// persistState 保存检查点。高水位不会越过尚未确认的消息，
// 否则进程重启后这些消息会被永久跳过。
func (c *connector) persistState(chatKey string) {
	if c.state.LastSeq == nil {
		c.state.LastSeq = map[string]int64{}
	}
	if c.state.Pending == nil {
		c.state.Pending = map[string][]string{}
	}
	if c.state.Baselined == nil {
		c.state.Baselined = map[string]bool{}
	}
	watermark := nextWatermark(c.maxSeq[chatKey], c.pending, chatKey)
	var pendingIDs []string
	for id, item := range c.pending {
		if item.chatKey != chatKey {
			continue
		}
		pendingIDs = append(pendingIDs, id)
	}
	c.state.LastSeq[chatKey] = watermark
	c.state.Seen[chatKey] = tail(c.state.Seen[chatKey], seenLimit)
	c.state.Pending[chatKey] = pendingIDs
	c.saveState()
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
		"pending_unacked": len(c.pending),
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
	if c.state.Pending == nil {
		c.state.Pending = map[string][]string{}
	}
	if c.state.Baselined == nil {
		c.state.Baselined = map[string]bool{}
	}
}

func (c *connector) saveState() {
	if c.statePath == "" {
		return
	}
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

func messageIDs(messages []wechatdb.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.MessageID)
	}
	return ids
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
