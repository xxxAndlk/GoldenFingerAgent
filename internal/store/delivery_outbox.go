package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DeliveryOutboxEntry 是端侧待上传到云端临时交付区的文件任务。
type DeliveryOutboxEntry struct {
	IdempotencyKey  string
	OwnerUserID     string
	ArtifactID      string
	ArtifactVersion int
	TargetDeviceID  string
	AllowExecutable bool
	Status          string
	Attempts        int
	NextRetryAt     *time.Time
	LastError       string
	DeliveryID      string
	CreatedAt       time.Time
	CompletedAt     *time.Time
}

const (
	DeliveryOutboxPending   = "pending"
	DeliveryOutboxUploading = "uploading"
	DeliveryOutboxDone      = "done"
	DeliveryOutboxFailed    = "failed"
)

type DeliveryOutboxRepo struct{ q Querier }

func NewDeliveryOutboxRepo(q Querier) *DeliveryOutboxRepo { return &DeliveryOutboxRepo{q: q} }

func (r *DeliveryOutboxRepo) Enqueue(ctx context.Context, e *DeliveryOutboxEntry) error {
	if e.Status == "" {
		e.Status = DeliveryOutboxPending
	}
	now := nowForDB()
	e.CreatedAt = now
	_, err := r.q.ExecContext(ctx, `INSERT INTO artifact_delivery_outbox
		(idempotency_key, owner_user_id, artifact_id, artifact_version, target_device_id,
		 allow_executable, status, attempts, next_retry_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, NULL, ?)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		e.IdempotencyKey, e.OwnerUserID, e.ArtifactID, e.ArtifactVersion, e.TargetDeviceID,
		boolInt(e.AllowExecutable), e.Status, now)
	return err
}

const deliveryOutboxColumns = `idempotency_key, owner_user_id, artifact_id, artifact_version,
	target_device_id, allow_executable, status, attempts, next_retry_at, last_error,
	delivery_id, created_at, completed_at`

func scanDeliveryOutbox(row interface{ Scan(...any) error }) (*DeliveryOutboxEntry, error) {
	var e DeliveryOutboxEntry
	var allowExecutable int
	if err := row.Scan(&e.IdempotencyKey, &e.OwnerUserID, &e.ArtifactID, &e.ArtifactVersion,
		&e.TargetDeviceID, &allowExecutable, &e.Status, &e.Attempts, &e.NextRetryAt,
		&e.LastError, &e.DeliveryID, &e.CreatedAt, &e.CompletedAt); err != nil {
		return nil, err
	}
	e.AllowExecutable = allowExecutable != 0
	return &e, nil
}

func (r *DeliveryOutboxRepo) Get(ctx context.Context, key string) (*DeliveryOutboxEntry, error) {
	e, err := scanDeliveryOutbox(r.q.QueryRowContext(ctx,
		`SELECT `+deliveryOutboxColumns+` FROM artifact_delivery_outbox WHERE idempotency_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// Due 返回待上传条目；上一次进程在 uploading 中断的条目会自动重试。
func (r *DeliveryOutboxRepo) Due(ctx context.Context, now time.Time, limit int) ([]DeliveryOutboxEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+deliveryOutboxColumns+` FROM artifact_delivery_outbox
		WHERE (status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?))
		   OR status = ?
		ORDER BY created_at LIMIT ?`, DeliveryOutboxPending, now, DeliveryOutboxUploading, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryOutboxEntry
	for rows.Next() {
		e, err := scanDeliveryOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (r *DeliveryOutboxRepo) MarkUploading(ctx context.Context, key string) error {
	_, err := r.q.ExecContext(ctx, `UPDATE artifact_delivery_outbox
		SET status = ?, attempts = attempts + 1 WHERE idempotency_key = ?`,
		DeliveryOutboxUploading, key)
	return err
}

func (r *DeliveryOutboxRepo) MarkDone(ctx context.Context, key, deliveryID string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE artifact_delivery_outbox
		SET status = ?, delivery_id = ?, completed_at = ?, last_error = '', next_retry_at = NULL
		WHERE idempotency_key = ?`, DeliveryOutboxDone, deliveryID, at, key)
	return err
}

func (r *DeliveryOutboxRepo) MarkRetry(ctx context.Context, key, lastError string, next time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE artifact_delivery_outbox
		SET status = ?, last_error = ?, next_retry_at = ? WHERE idempotency_key = ?`,
		DeliveryOutboxPending, lastError, next, key)
	return err
}

func (r *DeliveryOutboxRepo) MarkFailed(ctx context.Context, key, lastError string) error {
	_, err := r.q.ExecContext(ctx, `UPDATE artifact_delivery_outbox
		SET status = ?, last_error = ?, next_retry_at = NULL WHERE idempotency_key = ?`,
		DeliveryOutboxFailed, lastError, key)
	return err
}

func (r *DeliveryOutboxRepo) ListByOwner(ctx context.Context, ownerID string, limit int) ([]DeliveryOutboxEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+deliveryOutboxColumns+` FROM artifact_delivery_outbox
		WHERE owner_user_id = ? ORDER BY created_at DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryOutboxEntry
	for rows.Next() {
		e, err := scanDeliveryOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}
