package store

import (
	"context"
	"encoding/json"
)

type AuditRepo struct{ q Querier }

func NewAuditRepo(q Querier) *AuditRepo { return &AuditRepo{q: q} }

// Append 记录一条审计条目（所有写路径都必须调用）。
func (r *AuditRepo) Append(ctx context.Context, ownerID *string, actor, action, target string, detail json.RawMessage) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO audit_log (owner_user_id, actor, action, target, detail_jsonb)
		VALUES ($1, $2, $3, $4, $5)`,
		ownerID, actor, action, target, notNullJSON(detail))
	return err
}

// Exists 报告是否存在 (owner, action, target) 的审计条目。
// 用作幂等保护（例如每个用户每天一条摘要）。
func (r *AuditRepo) Exists(ctx context.Context, ownerID, action, target string) (bool, error) {
	var found bool
	err := r.q.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM audit_log WHERE owner_user_id = $1 AND action = $2 AND target = $3
		)`, ownerID, action, target).Scan(&found)
	return found, err
}

// Recent 返回某位 owner 的最新审计条目（供溯源 UI 使用）。
func (r *AuditRepo) Recent(ctx context.Context, ownerID string, limit int) ([]AuditEntry, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, owner_user_id, actor, action, target, detail_jsonb, created_at
		FROM audit_log WHERE owner_user_id = $1
		ORDER BY created_at DESC LIMIT $2`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var detail []byte
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Actor, &e.Action, &e.Target, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Detail = detail
		out = append(out, e)
	}
	return out, rows.Err()
}

type ConsentRepo struct{ q Querier }

func NewConsentRepo(q Querier) *ConsentRepo { return &ConsentRepo{q: q} }

// Active 返回 (subject, scope) 下未撤销的同意记录。
func (r *ConsentRepo) Active(ctx context.Context, subjectUserID, scope string) (*Consent, error) {
	c := &Consent{}
	err := r.q.QueryRow(ctx, `
		SELECT id, subject_user_id, scope, granted_by_guardian_id, granted_at, revoked_at
		FROM consent
		WHERE subject_user_id = $1 AND scope = $2 AND revoked_at IS NULL
		ORDER BY granted_at DESC LIMIT 1`, subjectUserID, scope).
		Scan(&c.ID, &c.SubjectUserID, &c.Scope, &c.GrantedByGuardianID, &c.GrantedAt, &c.RevokedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return c, nil
}

func (r *ConsentRepo) Grant(ctx context.Context, subjectUserID, scope string, guardianID *string) (*Consent, error) {
	c := &Consent{SubjectUserID: subjectUserID, Scope: scope, GrantedByGuardianID: guardianID}
	err := r.q.QueryRow(ctx, `
		INSERT INTO consent (subject_user_id, scope, granted_by_guardian_id)
		VALUES ($1, $2, $3)
		RETURNING id, granted_at`, subjectUserID, scope, guardianID).
		Scan(&c.ID, &c.GrantedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (r *ConsentRepo) Revoke(ctx context.Context, subjectUserID, scope string) error {
	_, err := r.q.Exec(ctx, `
		UPDATE consent SET revoked_at = now()
		WHERE subject_user_id = $1 AND scope = $2 AND revoked_at IS NULL`, subjectUserID, scope)
	return err
}
