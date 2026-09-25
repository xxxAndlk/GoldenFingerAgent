// Package intent: standing intents — event-conditioned prospective memory
// ("当……时提醒我"). Inspired by OpenClaw's standing intents: deterministic
// keyword matching (no model in the matching path), cooldown, fire budget,
// expiry, and explicit-only cancellation.
package intent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
)

// Conservative defaults (OpenClaw's defaults, scaled for a home butler).
const (
	DefaultMaxFires   = 3
	DefaultCooldown   = 24 * time.Hour
	DefaultExpiry     = 90 * 24 * time.Hour
	maxGroups         = 8
	maxTermsPerGroup  = 8
	maxDescriptionLen = 200
)

// ErrInvalid is returned for malformed create input.
var ErrInvalid = errors.New("intent: invalid input")

// MatchTrigger reports whether text matches the OR-of-AND trigger groups:
// every term of at least one group must appear in the text (substring,
// case-insensitive). Pure and deterministic — no model call.
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

// Service owns the standing-intent lifecycle rules.
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
	// TriggerGroups is OR-of-ANDs: [["张阿姨","来电话"],["张妈","电话"]].
	TriggerGroups [][]string
	MaxFires      int           // 0 → DefaultMaxFires
	Cooldown      time.Duration // 0 → DefaultCooldown
	ExpiresIn     time.Duration // 0 → DefaultExpiry
}

// Create validates and arms a standing intent.
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

// Cancel is explicit-only: nothing in ordinary conversation cancels an intent.
func (s *Service) Cancel(ctx context.Context, ownerID, id string) error {
	it, err := s.repo.Cancel(ctx, ownerID, id)
	if err != nil {
		return err
	}
	s.auditAppend(ctx, ownerID, "intent_cancel", it.ID, nil)
	return nil
}

// Check matches userText against eligible intents and fires the hits.
// Deterministic: expiry maintenance + keyword match + CAS fire. Returns the
// fired intents so the caller can surface a visible reminder line.
func (s *Service) Check(ctx context.Context, ownerID, userText string) ([]store.StandingIntent, error) {
	now := s.now()
	// Expiry maintenance piggybacks here — no extra timer subsystem.
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
			continue // lost the CAS race; the other turn delivers the reminder
		}
		s.auditAppend(ctx, ownerID, "intent_fire", hit.ID, map[string]any{
			"description": hit.Description, "fire_count": hit.FireCount,
		})
		fired = append(fired, *hit)
	}
	return fired, nil
}

// normalizeGroups trims, drops empty terms/groups, and bounds the shape.
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
