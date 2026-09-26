// Package intent: 常驻意图——事件触发的预期记忆
// （"当……时提醒我"）。受 OpenClaw 的常驻意图启发：确定性
// 关键词匹配（匹配路径不含模型）、冷却、触发预算、
// 过期与仅显式取消。
package intent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
)

// 保守默认值（OpenClaw 的默认值，按家用管家规模缩放）。
const (
	DefaultMaxFires   = 3
	DefaultCooldown   = 24 * time.Hour
	DefaultExpiry     = 90 * 24 * time.Hour
	maxGroups         = 8
	maxTermsPerGroup  = 8
	maxDescriptionLen = 200
)

// ErrInvalid 在创建输入格式错误时返回。
var ErrInvalid = errors.New("intent: invalid input")

// MatchTrigger 报告文本是否匹配 OR-of-AND 触发组：
// 至少一个组的每个词都必须出现在文本中（子串、
// 大小写不敏感）。纯确定性——不调用模型。
func MatchTrigger(groups [][]string, text string) bool {
	lower := strings.ToLower(text)
	for _, g := range groups {
		seen := 0
		ok := true
		for _, term := range g {
			term = strings.ToLower(strings.TrimSpace(term))
			if term == "" {
				continue
			}
			seen++
			if !strings.Contains(lower, term) {
				ok = false
				break
			}
		}
		if ok && seen > 0 {
			return true
		}
	}
	return false
}

// Service 负责常驻意图的生命周期规则。
type Service struct {
	repo  *store.IntentRepo
	audit *store.AuditRepo
	now   func() time.Time
}

func NewService(repo *store.IntentRepo, audit *store.AuditRepo, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repo: repo, audit: audit, now: now}
}

type CreateInput struct {
	OwnerUserID string
	Description string
	// TriggerGroups 是 OR-of-ANDs：[["张阿姨","来电话"],["张妈","电话"]]。
	TriggerGroups [][]string
	MaxFires      int           // 0 → DefaultMaxFires
	Cooldown      time.Duration // 0 → DefaultCooldown
	ExpiresIn     time.Duration // 0 → DefaultExpiry
}

// Create 校验并武装一个常驻意图。
func (s *Service) Create(ctx context.Context, in CreateInput) (*store.StandingIntent, error) {
	groups, err := normalizeGroups(in.TriggerGroups)
	if err != nil {
		return nil, err
	}
	desc := strings.TrimSpace(in.Description)
	if desc == "" || len(desc) > maxDescriptionLen {
		return nil, ErrInvalid
	}
	it := &store.StandingIntent{
		OwnerUserID:     in.OwnerUserID,
		Description:     desc,
		TriggerGroups:   groups,
		Status:          store.IntentArmed,
		MaxFires:        in.MaxFires,
		CooldownSeconds: int((in.Cooldown / time.Second)),
	}
	if it.MaxFires <= 0 {
		it.MaxFires = DefaultMaxFires
	}
	if in.Cooldown <= 0 {
		it.CooldownSeconds = int(DefaultCooldown / time.Second)
	}
	exp := s.now().Add(DefaultExpiry)
	if in.ExpiresIn > 0 {
		exp = s.now().Add(in.ExpiresIn)
	}
	it.ExpiresAt = &exp

	if err := s.repo.Insert(ctx, it); err != nil {
		return nil, err
	}
	s.auditAppend(ctx, in.OwnerUserID, "intent_create", it.ID, map[string]any{
		"description": desc, "groups": groups, "max_fires": it.MaxFires,
	})
	return it, nil
}

func (s *Service) List(ctx context.Context, ownerID string) ([]store.StandingIntent, error) {
	return s.repo.ListByOwner(ctx, ownerID)
}

// Cancel 仅显式生效：日常对话中的任何内容都不会取消意图。
func (s *Service) Cancel(ctx context.Context, ownerID, id string) error {
	it, err := s.repo.Cancel(ctx, ownerID, id)
	if err != nil {
		return err
	}
	s.auditAppend(ctx, ownerID, "intent_cancel", it.ID, nil)
	return nil
}

// Check 将 userText 与符合条件的意图匹配并触发命中项。
// 确定性：过期维护 + 关键词匹配 + CAS 触发。返回已触发的
// 意图，供调用方展示可见的提醒行。
func (s *Service) Check(ctx context.Context, ownerID, userText string) ([]store.StandingIntent, error) {
	now := s.now()
	// 过期维护顺带在这里做——无需额外的定时器子系统。
	_ = s.repo.MarkExpired(ctx, now)

	cands, err := s.repo.MatchCandidates(ctx, ownerID, now)
	if err != nil {
		return nil, err
	}
	var fired []store.StandingIntent
	for _, c := range cands {
		if !MatchTrigger(c.TriggerGroups, userText) {
			continue
		}
		hit, err := s.repo.Fire(ctx, c.ID, now)
		if err != nil {
			continue // 在 CAS 竞争中落败；另一轮会投递提醒
		}
		s.auditAppend(ctx, ownerID, "intent_fire", hit.ID, map[string]any{
			"description": hit.Description, "fire_count": hit.FireCount,
		})
		fired = append(fired, *hit)
	}
	return fired, nil
}

// normalizeGroups 去除空白、丢弃空词/空组，并约束形状。
func normalizeGroups(groups [][]string) ([][]string, error) {
	if len(groups) == 0 || len(groups) > maxGroups {
		return nil, ErrInvalid
	}
	out := make([][]string, 0, len(groups))
	for _, g := range groups {
		terms := make([]string, 0, len(g))
		for _, t := range g {
			t = strings.TrimSpace(t)
			if t != "" {
				terms = append(terms, t)
			}
		}
		if len(terms) == 0 || len(terms) > maxTermsPerGroup {
			return nil, ErrInvalid
		}
		out = append(out, terms)
	}
	return out, nil
}

func (s *Service) auditAppend(ctx context.Context, ownerID, action, target string, detail map[string]any) {
	if s.audit == nil {
		return
	}
	var raw json.RawMessage
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	owner := ownerID
	_ = s.audit.Append(ctx, &owner, "agent", action, target, raw)
}
