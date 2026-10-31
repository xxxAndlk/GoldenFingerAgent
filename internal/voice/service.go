// Package voice 是「持续听写」的口令判定层：口令词识别、指令窗口、
// 片段合并与上下文窗口全部在这里决定，Android 只负责识别音频、执行动作
// （播报/提问）。判定放在 Go 是为了让两端行为一致、并且可被单元测试。
package voice

import (
	"strings"
	"sync"
	"time"
)

// 动作类型。
const (
	// ActionSpeak：让手机播报一句话（例如唤醒应答“我在”）。
	ActionSpeak = "speak"
	// ActionChat：让手机把这句当提问发给管家，并播报回复。
	ActionChat = "chat"
	// ActionCapture：一段需要写入情景记忆的片段（含前后文）。
	ActionCapture = "capture"
)

// Action 是判定层返回给手机的单步动作。
type Action struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Before/After 只在 capture 时有值：同一句话的上下文。
	Before string `json:"context_before,omitempty"`
	After  string `json:"context_after,omitempty"`
	// At 是片段被说出的时间（captured_at）。
	At time.Time `json:"captured_at,omitempty"`
}

// Config 是判定参数。零值表示使用默认值。
type Config struct {
	// WakeWord 唤醒词，默认「小金小金」。
	WakeWord string
	// CommandWindow 是唤醒后等待指令的时长，默认 10s。
	CommandWindow time.Duration
	// MergeWait 是片段等待后文的时长，默认 4s。
	MergeWait time.Duration
	// ContextWindow 是上下文有效期，默认 30s。
	ContextWindow time.Duration
	// ContextSegments 是塞进 context_before 的最近片段数，默认 4。
	ContextSegments int
	// RecentKeep 是内存里保留的最近片段数，默认 6。
	RecentKeep int
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.WakeWord) == "" {
		c.WakeWord = "小金小金"
	}
	if c.CommandWindow <= 0 {
		c.CommandWindow = 10 * time.Second
	}
	if c.MergeWait <= 0 {
		c.MergeWait = 4 * time.Second
	}
	if c.ContextWindow <= 0 {
		c.ContextWindow = 30 * time.Second
	}
	if c.ContextSegments <= 0 {
		c.ContextSegments = 4
	}
	if c.RecentKeep <= 0 {
		c.RecentKeep = 6
	}
	if c.RecentKeep < c.ContextSegments {
		c.RecentKeep = c.ContextSegments
	}
	return c
}

// AckText 是唤醒但没听清指令时的应答。
const AckText = "我在"

type segment struct {
	text   string
	before string
	at     time.Time
}

// device 是单台手机的听写状态。手机是单设备场景，但仍按 id 隔离，
// 以免同一进程内多个来源互相污染（例如调试时的第二台设备）。
type device struct {
	pending      *segment
	recent       []segment
	awaitingTill time.Time
}

// Service 是无 IO 的口令判定状态机（可安全并发使用）。
type Service struct {
	cfg     Config
	now     func() time.Time
	mu      sync.Mutex
	devices map[string]*device
}

// NewService 创建判定服务；now 为 nil 时用 time.Now。
func NewService(cfg Config, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{cfg: cfg.withDefaults(), now: now, devices: map[string]*device{}}
}

// MergeWait 告诉手机「多久没有后文就应该来 flush」。
func (s *Service) MergeWait() time.Duration { return s.cfg.MergeWait }

func (s *Service) device(id string) *device {
	d := s.devices[id]
	if d == nil {
		d = &device{}
		s.devices[id] = d
	}
	return d
}

// OnText 处理一条最终转写文本，返回手机应执行的动作序列。
//
// 规则（与原 Kotlin 行为一致，便于灰度替换）：
//  1. 含唤醒词：先把上一段落库；唤醒词后为空 → 开 10s 指令窗并播报“我在”；
//     唤醒词后有内容 → 直接当指令。
//  2. 指令窗内：这句就是指令（并关闭窗口）。
//  3. 其它情况：只是随口说的话，落库。
func (s *Service) OnText(deviceID, text string) []Action {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.device(deviceID)

	compact := stripSpaces(text)
	if at := strings.Index(compact, s.cfg.WakeWord); at >= 0 {
		actions := s.flushLocked(d, "", now)
		command := trimLeadingPunct(strings.TrimSpace(compact[at+len(s.cfg.WakeWord):]))
		if command == "" {
			d.awaitingTill = now.Add(s.cfg.CommandWindow)
			return append(actions, Action{Type: ActionSpeak, Text: AckText})
		}
		d.awaitingTill = time.Time{}
		return s.commandLocked(d, command, now, actions)
	}

	if !d.awaitingTill.IsZero() && now.Before(d.awaitingTill) {
		d.awaitingTill = time.Time{}
		return s.commandLocked(d, text, now, nil)
	}
	// 过期的指令窗要清掉，避免下次唤醒前误吞一句普通对话。
	d.awaitingTill = time.Time{}
	return s.acceptLocked(d, text, now, nil)
}

// Flush 结束当前片段（等待后文超时或服务停止时调用），返回落库动作。
func (s *Service) Flush(deviceID string) []Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.device(deviceID)
	return s.flushLocked(d, "", s.now())
}

// Pending 报告是否还有未落库的片段（用于测试与可观测性）。
func (s *Service) Pending(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.device(deviceID).pending != nil
}

// commandLocked 处理“这句是指令”：落库 + 让手机提问。
func (s *Service) commandLocked(d *device, command string, now time.Time, actions []Action) []Action {
	actions = s.acceptLocked(d, command, now, actions)
	return append(actions, Action{Type: ActionChat, Text: command})
}

// acceptLocked 把一句话作为新片段暂存，并把上一段落库（这句作为后文）。
func (s *Service) acceptLocked(d *device, text string, now time.Time, actions []Action) []Action {
	actions = s.flushLocked(d, text, now)
	cut := now.Add(-s.cfg.ContextWindow)
	kept := d.recent[:0]
	for _, seg := range d.recent {
		if seg.at.After(cut) {
			kept = append(kept, seg)
		}
	}
	d.recent = kept
	d.pending = &segment{text: text, before: s.beforeLocked(d), at: now}
	return actions
}

// beforeLocked 拼出这条片段的上下文（最近 N 句，按时间顺序）。
func (s *Service) beforeLocked(d *device) string {
	if len(d.recent) == 0 {
		return ""
	}
	start := len(d.recent) - s.cfg.ContextSegments
	if start < 0 {
		start = 0
	}
	texts := make([]string, 0, s.cfg.ContextSegments)
	for _, seg := range d.recent[start:] {
		texts = append(texts, seg.text)
	}
	return strings.Join(texts, "。")
}

// flushLocked 把暂存片段变成 capture 动作，并把这句话记入上下文。
func (s *Service) flushLocked(d *device, after string, _ time.Time) []Action {
	seg := d.pending
	if seg == nil {
		return nil
	}
	d.pending = nil
	d.recent = append(d.recent, segment{text: seg.text, at: seg.at})
	if len(d.recent) > s.cfg.RecentKeep {
		d.recent = d.recent[len(d.recent)-s.cfg.RecentKeep:]
	}
	return []Action{{
		Type: ActionCapture, Text: seg.text, Before: seg.before, After: after, At: seg.at,
	}}
}

func stripSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\u3000' {
			return -1
		}
		return r
	}, s)
}

// trimLeadingPunct 去掉唤醒词后面常见的停顿标点，例如「小金小金，放首歌」。
func trimLeadingPunct(s string) string {
	return strings.TrimLeft(s, "，,。.：:、!！?？~～-—")
}
