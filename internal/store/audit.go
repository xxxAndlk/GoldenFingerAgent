package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"
)

type AuditRepo struct{ q Querier }

func NewAuditRepo(q Querier) *AuditRepo { return &AuditRepo{q: q} }

// Append 记录一条审计条目（所有写路径都必须调用）。
// id 为 INTEGER AUTOINCREMENT，由 DB 自增生成。
func (r *AuditRepo) Append(ctx context.Context, ownerID *string, actor, action, target string, detail json.RawMessage) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO audit_log (owner_user_id, actor, action, target, detail_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		ownerID, actor, action, target, notNullJSON(detail), nowForDB())
	return err
}

// Exists 报告是否存在 (owner, action, target) 的审计条目。
// 用作幂等保护（例如每个用户每天一条摘要）。
func (r *AuditRepo) Exists(ctx context.Context, ownerID, action, target string) (bool, error) {
	var found bool
	err := r.q.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM audit_log WHERE owner_user_id = ? AND action = ? AND target = ?
		)`, ownerID, action, target).Scan(&found)
	return found, err
}

// Recent 返回某位 owner 的最新审计条目（供溯源 UI 使用）。
func (r *AuditRepo) Recent(ctx context.Context, ownerID string, limit int) ([]AuditEntry, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, owner_user_id, actor, action, target, detail_json, created_at
		FROM audit_log WHERE owner_user_id = ?
		ORDER BY created_at DESC LIMIT ?`, ownerID, limit)
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
	err := r.q.QueryRowContext(ctx, `
		SELECT id, subject_user_id, scope, granted_by_guardian_id, granted_at, revoked_at
		FROM consent
		WHERE subject_user_id = ? AND scope = ? AND revoked_at IS NULL
		ORDER BY granted_at DESC LIMIT 1`, subjectUserID, scope).
		Scan(&c.ID, &c.SubjectUserID, &c.Scope, &c.GrantedByGuardianID, &c.GrantedAt, &c.RevokedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

func (r *ConsentRepo) Grant(ctx context.Context, subjectUserID, scope string, guardianID *string) (*Consent, error) {
	c := &Consent{ID: uuid.NewString(), SubjectUserID: subjectUserID, Scope: scope, GrantedByGuardianID: guardianID}
	err := r.q.QueryRowContext(ctx, `
		INSERT INTO consent (id, subject_user_id, scope, granted_by_guardian_id, granted_at)
		VALUES (?, ?, ?, ?, ?)
		RETURNING granted_at`, c.ID, subjectUserID, scope, guardianID, nowForDB()).
		Scan(&c.GrantedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (r *ConsentRepo) Revoke(ctx context.Context, subjectUserID, scope string) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE consent SET revoked_at = ?
		WHERE subject_user_id = ? AND scope = ? AND revoked_at IS NULL`, nowForDB(), subjectUserID, scope)
	return err
}
