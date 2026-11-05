package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CloudDelivery 是云端临时交付区中的一条文件传输记录。
type CloudDelivery struct {
	DeliveryID       string     `json:"delivery_id"`
	OwnerUserID      string     `json:"owner_user_id"`
	UploadedByDevice string     `json:"uploaded_by_device"`
	TargetDeviceID   string     `json:"target_device_id"`
	ArtifactID       string     `json:"artifact_id"`
	ArtifactVersion  int        `json:"artifact_version"`
	Name             string     `json:"name"`
	MimeType         string     `json:"mime_type"`
	SizeBytes        int64      `json:"size_bytes"`
	SHA256           string     `json:"sha256"`
	StoragePath      string     `json:"-"`
	IdempotencyKey   string     `json:"-"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	AckedAt          *time.Time `json:"acked_at,omitempty"`
	DeletedAt        *time.Time `json:"deleted_at,omitempty"`
}

const (
	DeliveryActive  = "active"
	DeliveryRevoked = "revoked"
	DeliveryExpired = "expired"
)

type CloudDeliveryRepo struct{ q Querier }

func NewCloudDeliveryRepo(q Querier) *CloudDeliveryRepo { return &CloudDeliveryRepo{q: q} }

const cloudDeliveryColumns = `delivery_id, owner_user_id, uploaded_by_device, target_device_id,
	artifact_id, artifact_version, name, mime_type, size_bytes, sha256, storage_path,
	idempotency_key, status, created_at, expires_at, acked_at, deleted_at`

func scanCloudDelivery(row interface{ Scan(...any) error }) (*CloudDelivery, error) {
	var d CloudDelivery
	if err := row.Scan(&d.DeliveryID, &d.OwnerUserID, &d.UploadedByDevice, &d.TargetDeviceID,
		&d.ArtifactID, &d.ArtifactVersion, &d.Name, &d.MimeType, &d.SizeBytes, &d.SHA256,
		&d.StoragePath, &d.IdempotencyKey, &d.Status, &d.CreatedAt, &d.ExpiresAt,
		&d.AckedAt, &d.DeletedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *CloudDeliveryRepo) Create(ctx context.Context, d *CloudDelivery) error {
	if d.Status == "" {
		d.Status = DeliveryActive
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO cloud_delivery
		(delivery_id, owner_user_id, uploaded_by_device, target_device_id, artifact_id,
		 artifact_version, name, mime_type, size_bytes, sha256, storage_path, idempotency_key,
		 status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.DeliveryID, d.OwnerUserID, d.UploadedByDevice, d.TargetDeviceID, d.ArtifactID,
		d.ArtifactVersion, d.Name, d.MimeType, d.SizeBytes, d.SHA256, d.StoragePath,
		d.IdempotencyKey, d.Status, d.CreatedAt, d.ExpiresAt)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (r *CloudDeliveryRepo) Get(ctx context.Context, ownerID, id string) (*CloudDelivery, error) {
	d, err := scanCloudDelivery(r.q.QueryRowContext(ctx,
		`SELECT `+cloudDeliveryColumns+` FROM cloud_delivery
		 WHERE owner_user_id = ? AND delivery_id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (r *CloudDeliveryRepo) GetByIdempotencyKey(ctx context.Context, ownerID, key string) (*CloudDelivery, error) {
	d, err := scanCloudDelivery(r.q.QueryRowContext(ctx,
		`SELECT `+cloudDeliveryColumns+` FROM cloud_delivery
		 WHERE owner_user_id = ? AND idempotency_key = ?`, ownerID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (r *CloudDeliveryRepo) Inbox(ctx context.Context, targetDeviceID string, limit int) ([]CloudDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+cloudDeliveryColumns+` FROM cloud_delivery
		WHERE target_device_id = ? AND status = ?
		ORDER BY created_at DESC LIMIT ?`, targetDeviceID, DeliveryActive, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CloudDelivery, 0)
	for rows.Next() {
		d, err := scanCloudDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (r *CloudDeliveryRepo) Ack(ctx context.Context, ownerID, id string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE cloud_delivery SET acked_at = ?
		WHERE owner_user_id = ? AND delivery_id = ? AND status = ?`,
		at, ownerID, id, DeliveryActive)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *CloudDeliveryRepo) Revoke(ctx context.Context, ownerID, id string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE cloud_delivery
		SET status = ?, deleted_at = ? WHERE owner_user_id = ? AND delivery_id = ?
		  AND status = ?`, DeliveryRevoked, at, ownerID, id, DeliveryActive)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Expired 返回已经过期但仍标记 active 的交付（清理任务使用）。
func (r *CloudDeliveryRepo) Expired(ctx context.Context, now time.Time, limit int) ([]CloudDelivery, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+cloudDeliveryColumns+` FROM cloud_delivery
		WHERE status = ? AND expires_at <= ? ORDER BY expires_at LIMIT ?`,
		DeliveryActive, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CloudDelivery, 0)
	for rows.Next() {
		d, err := scanCloudDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (r *CloudDeliveryRepo) MarkExpired(ctx context.Context, id string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE cloud_delivery
		SET status = ?, deleted_at = ? WHERE delivery_id = ? AND status = ?`,
		DeliveryExpired, at, id, DeliveryActive)
	return err
}
