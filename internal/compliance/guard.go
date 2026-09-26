// Package compliance：监护人同意、儿童内容过滤、事实类型
// 策略与审计写入（文档 §9 — 未成年人是底线）。
package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
)

// ErrConsentRequired 在监护人同意被记录前阻止该操作。
var ErrConsentRequired = errors.New("compliance: guardian consent required")

// ErrContentBlocked 阻止对儿童账号的不安全输出。
var ErrContentBlocked = errors.New("compliance: content blocked")

// childBlockedKeywords 是内容过滤的桩实现（真实过滤器是外部服务）。
var childBlockedKeywords = []string{"暴力", "血腥", "色情", "赌博", "毒品", "自杀", "自残", "抽烟", "喝酒"}

// Guard 强制合规门禁。所有写路径同时记录审计条目。
type Guard struct {
	consents *store.ConsentRepo
	audit    *store.AuditRepo
	users    *store.UserRepo
}

func NewGuard(consents *store.ConsentRepo, audit *store.AuditRepo, users *store.UserRepo) *Guard {
	return &Guard{consents: consents, audit: audit, users: users}
}

// RequireConsent 对儿童账号的敏感范围强制执行监护人同意。
// 老人/普通用户直接放行。儿童需要一条有效的同意记录。
func (g *Guard) RequireConsent(ctx context.Context, user *store.User, scope string) error {
	if user.UserType != store.UserChild {
		return nil
	}
	c, err := g.consents.Active(ctx, user.ID, scope)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			owner := user.ID
			_ = g.audit.Append(ctx, &owner, "system", "consent_missing", scope, nil)
			return ErrConsentRequired
		}
		return err
	}
	if c == nil {
		return ErrConsentRequired
	}
	return nil
}

// FilterContent 阻止对儿童用户的不安全文本（关键词桩 + 真实外部
// 内容审核服务的接入点）。
func (g *Guard) FilterContent(ctx context.Context, user *store.User, text string) error {
	if user.UserType != store.UserChild {
		return nil
	}
	for _, kw := range childBlockedKeywords {
		if strings.Contains(text, kw) {
			owner := user.ID
			_ = g.audit.Append(ctx, &owner, "system", "content_blocked", kw, nil)
			return ErrContentBlocked
		}
	}
	return nil
}

// FactTypeAllowed 强制执行儿童仅事实型 + 通用评价性内容禁令。
func (g *Guard) FactTypeAllowed(user *store.User, factType, valueText string) error {
	if nlu.IsEvaluative(factType, valueText) {
		return ErrContentBlocked // 评价性标签：绝不自动写入
	}
	if user.UserType == store.UserChild && !nlu.ChildFactAllowed(factType) {
		return ErrContentBlocked
	}
	return nil
}

// Audit 以某个行为者的名义写入审计条目。
func (g *Guard) Audit(ctx context.Context, ownerID *string, actor, action, target string, detail map[string]any) error {
	var raw json.RawMessage
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	return g.audit.Append(ctx, ownerID, actor, action, target, raw)
}
