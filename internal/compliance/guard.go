// Package compliance: guardian consent, child content filtering, fact-type
// policy and audit writes (doc §9 — 未成年人是底线).
package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
)

// ErrConsentRequired blocks an operation until guardian consent is recorded.
var ErrConsentRequired = errors.New("compliance: guardian consent required")

// ErrContentBlocked blocks unsafe output for child accounts.
var ErrContentBlocked = errors.New("compliance: content blocked")

// childBlockedKeywords is the content-filter stub (real filter is an external service).
var childBlockedKeywords = []string{"暴力", "血腥", "色情", "赌博", "毒品", "自杀", "自残", "抽烟", "喝酒"}

// Guard enforces the compliance gates. All write paths also record audit entries.
type Guard struct {
	consents *store.ConsentRepo
	audit    *store.AuditRepo
	users    *store.UserRepo
}

func NewGuard(consents *store.ConsentRepo, audit *store.AuditRepo, users *store.UserRepo) *Guard {
	return &Guard{consents: consents, audit: audit, users: users}
}

// RequireConsent enforces guardian consent for child accounts on sensitive scopes.
// Elder/general users pass through. Children need an active consent row.
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

// FilterContent blocks unsafe text for child users (keyword stub + hook point
// for a real external moderation service).
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

// FactTypeAllowed enforces 儿童仅事实型 + the universal evaluative ban.
func (g *Guard) FactTypeAllowed(user *store.User, factType, valueText string) error {
	if nlu.IsEvaluative(factType, valueText) {
		return ErrContentBlocked // evaluative labels: never auto-write
	}
	if user.UserType == store.UserChild && !nlu.ChildFactAllowed(factType) {
		return ErrContentBlocked
	}
	return nil
}

// Audit writes an audit entry on behalf of an actor.
func (g *Guard) Audit(ctx context.Context, ownerID *string, actor, action, target string, detail map[string]any) error {
	var raw json.RawMessage
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	return g.audit.Append(ctx, ownerID, actor, action, target, raw)
}
