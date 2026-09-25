package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Standing-intent lifecycle states (event-conditioned prospective memory).
const (
	IntentPending   = "pending"
	IntentArmed     = "armed"
	IntentFired     = "fired"
	IntentDone      = "done"
	IntentCancelled = "cancelled"
	IntentExpired   = "expired"
)

// StandingIntent is an event-conditioned reminder ("当……时提醒我"). It fires on
// a matching message, not on a clock. TriggerGroups is OR-of-ANDs: a message
// matches when every term of at least one group appears in it.
type StandingIntent struct {
	ID              string     `json:"id"`
	OwnerUserID     string     `json:"owner_user_id"`
	Description     string     `json:"description"`
	TriggerGroups   [][]string `json:"trigger_groups"`
	Status          string     `json:"status"`
	FireCount       int        `json:"fire_count"`
	MaxFires        int        `json:"max_fires"`
	CooldownSeconds int        `json:"cooldown_seconds"`
	LastFiredAt     *time.Time `json:"last_fired_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type IntentRepo struct{ q Querier }

func NewIntentRepo(q Querier) *IntentRepo { return &IntentRepo{q: q} }

const intentCols = `id, owner_user_id, description, trigger_groups, status,
	fire_count, max_fires, cooldown_seconds, last_fired_at, expires_at, created_at`

func scanIntent(row pgx.Row) (*StandingIntent, error) {
	it := &StandingIntent{}
	var groups []byte
	err := row.Scan(&it.ID, &it.OwnerUserID, &it.Description, &groups, &it.Status,
		&it.FireCount, &it.MaxFires, &it.CooldownSeconds, &it.LastFiredAt, &it.ExpiresAt, &it.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(groups) > 0 {
		_ = json.Unmarshal(groups, &it.TriggerGroups)
	}
	return it, nil
}

func scanIntents(rows pgx.Rows) ([]StandingIntent, error) {
	var out []StandingIntent
	for rows.Next() {
		it := StandingIntent{}
		var groups []byte
		if err := rows.Scan(&it.ID, &it.OwnerUserID, &it.Description, &groups, &it.Status,
			&it.FireCount, &it.MaxFires, &it.CooldownSeconds, &it.LastFiredAt, &it.ExpiresAt, &it.CreatedAt); err != nil {
			return nil, err
		}
		if len(groups) > 0 {
			_ = json.Unmarshal(groups, &it.TriggerGroups)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *IntentRepo) Insert(ctx context.Context, it *StandingIntent) error {
	groups, _ := json.Marshal(it.TriggerGroups)
	return r.q.QueryRow(ctx, `
		INSERT INTO standing_intent (owner_user_id, description, trigger_groups,
			status, max_fires, cooldown_seconds, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`,
		it.OwnerUserID, it.Description, groups, it.Status,
		it.MaxFires, it.CooldownSeconds, it.ExpiresAt,
	).Scan(&it.ID, &it.CreatedAt)
}

func (r *IntentRepo) ListByOwner(ctx context.Context, ownerID string) ([]StandingIntent, error) {
	rows, err := r.q.Query(ctx, `
		SELECT `+intentCols+` FROM standing_intent
		WHERE owner_user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIntents(rows)
}

func (r *IntentRepo) Get(ctx context.Context, ownerID, id string) (*StandingIntent, error) {
	return scanIntent(r.q.QueryRow(ctx, `
		SELECT `+intentCols+` FROM standing_intent
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL`, id, ownerID))
}

// Cancel is always explicit (an intent is never cancelled by inference).
func (r *IntentRepo) Cancel(ctx context.Context, ownerID, id string) (*StandingIntent, error) {
	return scanIntent(r.q.QueryRow(ctx, `
		UPDATE standing_intent SET status = '`+IntentCancelled+`', updated_at = now()
		WHERE id = $1 AND owner_user_id = $2 AND status IN ('pending','armed','fired')
		RETURNING `+intentCols, id, ownerID))
}

// MatchCandidates returns armed intents eligible to fire right now (cooldown
// elapsed; not expired). Fired intents whose cooldown has passed become
// eligible again without a separate timer.
func (r *IntentRepo) MatchCandidates(ctx context.Context, ownerID string, now time.Time) ([]StandingIntent, error) {
	rows, err := r.q.Query(ctx, `
		SELECT `+intentCols+` FROM standing_intent
		WHERE owner_user_id = $1 AND deleted_at IS NULL
		  AND (expires_at IS NULL OR expires_at > $2)
		  AND (status = 'armed'
		       OR (status = 'fired' AND last_fired_at IS NOT NULL
		           AND last_fired_at + cooldown_seconds * interval '1 second' <= $2))
		ORDER BY created_at ASC
		LIMIT 64`, ownerID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIntents(rows)
}

// Fire marks a hit atomically: fire budget exhausted → done, else fired.
// CAS on the same eligibility predicate as MatchCandidates so concurrent
// turns cannot double-fire the same intent.
func (r *IntentRepo) Fire(ctx context.Context, id string, now time.Time) (*StandingIntent, error) {
	return scanIntent(r.q.QueryRow(ctx, `
		UPDATE standing_intent SET
			fire_count = fire_count + 1,
			last_fired_at = $2,
			status = CASE WHEN fire_count + 1 >= max_fires THEN 'done' ELSE 'fired' END,
			updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		  AND (status = 'armed'
		       OR (status = 'fired' AND last_fired_at IS NOT NULL
		           AND last_fired_at + cooldown_seconds * interval '1 second' <= $2))
		RETURNING `+intentCols, id, now))
}

// MarkExpired is maintenance: expiry piggybacks on the chat/check path
// (OpenClaw does the same — no extra timer subsystem).
func (r *IntentRepo) MarkExpired(ctx context.Context, now time.Time) error {
	_, err := r.q.Exec(ctx, `
		UPDATE standing_intent SET status = '`+IntentExpired+`', updated_at = now()
		WHERE status IN ('pending','armed','fired') AND expires_at IS NOT NULL AND expires_at <= $1`,
		now)
	return err
}
