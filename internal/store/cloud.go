package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// CloudDevice 是云端共享记忆/交付 API 的设备凭据。Token 只以哈希形式保存。
type CloudDevice struct {
	ID          string     `json:"id"`
	OwnerUserID string     `json:"owner_user_id"`
	DeviceName  string     `json:"device_name"`
	TokenHash   string     `json:"-"`
	Scopes      []string   `json:"scopes"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type CloudDeviceRepo struct{ q Querier }

func NewCloudDeviceRepo(q Querier) *CloudDeviceRepo { return &CloudDeviceRepo{q: q} }

func (r *CloudDeviceRepo) Create(ctx context.Context, d *CloudDevice) error {
	scopes, err := json.Marshal(d.Scopes)
	if err != nil {
		return err
	}
	now := nowForDB()
	d.CreatedAt = now
	_, err = r.q.ExecContext(ctx, `INSERT INTO cloud_device
		(id, owner_user_id, device_name, token_hash, scopes_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, d.OwnerUserID, d.DeviceName, d.TokenHash, string(scopes), now)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func scanCloudDevice(row interface{ Scan(...any) error }) (*CloudDevice, error) {
	var d CloudDevice
	var scopes string
	if err := row.Scan(&d.ID, &d.OwnerUserID, &d.DeviceName, &d.TokenHash, &scopes,
		&d.CreatedAt, &d.LastSeenAt, &d.RevokedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopes), &d.Scopes)
	return &d, nil
}

const cloudDeviceColumns = `id, owner_user_id, device_name, token_hash, scopes_json,
	created_at, last_seen_at, revoked_at`

func (r *CloudDeviceRepo) GetByTokenHash(ctx context.Context, hash string) (*CloudDevice, error) {
	d, err := scanCloudDevice(r.q.QueryRowContext(ctx,
		`SELECT `+cloudDeviceColumns+` FROM cloud_device WHERE token_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (r *CloudDeviceRepo) Get(ctx context.Context, ownerID, id string) (*CloudDevice, error) {
	d, err := scanCloudDevice(r.q.QueryRowContext(ctx,
		`SELECT `+cloudDeviceColumns+` FROM cloud_device WHERE owner_user_id = ? AND id = ?`,
		ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (r *CloudDeviceRepo) List(ctx context.Context, ownerID string) ([]CloudDevice, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+cloudDeviceColumns+` FROM cloud_device WHERE owner_user_id = ?
		 ORDER BY created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CloudDevice, 0)
	for rows.Next() {
		d, err := scanCloudDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (r *CloudDeviceRepo) Revoke(ctx context.Context, ownerID, id string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx,
		`UPDATE cloud_device SET revoked_at = ? WHERE owner_user_id = ? AND id = ? AND revoked_at IS NULL`,
		at, ownerID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *CloudDeviceRepo) Touch(ctx context.Context, id string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE cloud_device SET last_seen_at = ? WHERE id = ?`, at, id)
	return err
}

// CloudEpisodeMeta 维护云端命名空间 SourceID 与端侧原始 SourceID 的映射。
// 遗忘后保留墓碑（forgotten_at），阻止其他设备把同一事件重新上传复活。
type CloudEpisodeMeta struct {
	OwnerUserID      string     `json:"owner_user_id"`
	OriginalSourceID string     `json:"original_source_id"`
	StoredSourceID   string     `json:"stored_source_id"`
	DeviceID         string     `json:"device_id"`
	ContentHash      string     `json:"content_hash"`
	CreatedAt        time.Time  `json:"created_at"`
	ForgottenAt      *time.Time `json:"forgotten_at,omitempty"`
}

type CloudEpisodeMetaRepo struct{ q Querier }

func NewCloudEpisodeMetaRepo(q Querier) *CloudEpisodeMetaRepo { return &CloudEpisodeMetaRepo{q: q} }

const cloudEpisodeMetaColumns = `owner_user_id, original_source_id, stored_source_id,
	device_id, content_hash, created_at, forgotten_at`

func scanCloudEpisodeMeta(row interface{ Scan(...any) error }) (*CloudEpisodeMeta, error) {
	var m CloudEpisodeMeta
	if err := row.Scan(&m.OwnerUserID, &m.OriginalSourceID, &m.StoredSourceID,
		&m.DeviceID, &m.ContentHash, &m.CreatedAt, &m.ForgottenAt); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *CloudEpisodeMetaRepo) GetByOriginal(ctx context.Context, ownerID, sourceID string) (*CloudEpisodeMeta, error) {
	m, err := scanCloudEpisodeMeta(r.q.QueryRowContext(ctx,
		`SELECT `+cloudEpisodeMetaColumns+` FROM cloud_episode_meta
		 WHERE owner_user_id = ? AND original_source_id = ?`, ownerID, sourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// MapStoredToOriginal 将一批云端存储 SourceID 还原为端侧原始 SourceID。
// 找不到映射的条目保持原样（例如旧数据或手工导入）。
func (r *CloudEpisodeMetaRepo) MapStoredToOriginal(ctx context.Context, ownerID string, stored []string) (map[string]string, error) {
	out := make(map[string]string, len(stored))
	if len(stored) == 0 {
		return out, nil
	}
	for _, id := range stored {
		var original string
		err := r.q.QueryRowContext(ctx,
			`SELECT original_source_id FROM cloud_episode_meta
			 WHERE owner_user_id = ? AND stored_source_id = ?`, ownerID, id).Scan(&original)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = original
	}
	return out, nil
}

func (r *CloudEpisodeMetaRepo) Insert(ctx context.Context, m *CloudEpisodeMeta) error {
	now := m.CreatedAt
	if now.IsZero() {
		now = nowForDB()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO cloud_episode_meta
		(owner_user_id, original_source_id, stored_source_id, device_id, content_hash,
		 created_at, forgotten_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.OwnerUserID, m.OriginalSourceID, m.StoredSourceID, m.DeviceID, m.ContentHash, now, m.ForgottenAt)
	return err
}

// MarkForgotten 写入遗忘墓碑；已经存在时只更新时间，不动已有内容哈希。
func (r *CloudEpisodeMetaRepo) MarkForgotten(ctx context.Context, ownerID, original, stored, deviceID string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO cloud_episode_meta
		(owner_user_id, original_source_id, stored_source_id, device_id, content_hash,
		 created_at, forgotten_at)
		VALUES (?, ?, ?, ?, '', ?, ?)
		ON CONFLICT(owner_user_id, original_source_id)
		DO UPDATE SET forgotten_at = excluded.forgotten_at`,
		ownerID, original, stored, deviceID, at, at)
	return err
}

// ListByOriginalPrefix 列出某个来源前缀下的全部映射。
//
// 共享记忆的一个槽位可能有多个版本（内容每次变化都换来源 ID），
// 按前缀遗忘时要一次拿到所有版本。
func (r *CloudEpisodeMetaRepo) ListByOriginalPrefix(ctx context.Context, ownerID, prefix string) ([]CloudEpisodeMeta, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+cloudEpisodeMetaColumns+` FROM cloud_episode_meta
		WHERE owner_user_id = ? AND original_source_id LIKE ? ESCAPE '\'
		ORDER BY created_at`, ownerID, escapeLike(prefix)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudEpisodeMeta
	for rows.Next() {
		m, err := scanCloudEpisodeMeta(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// Delete 彻底删除一条映射（连同墓碑）。
//
// 用于按前缀遗忘共享记忆：槽位内容以后可能被重新写入，
// 留着墓碑会让同一份内容再也传不上去。
func (r *CloudEpisodeMetaRepo) Delete(ctx context.Context, ownerID, original string) error {
	_, err := r.q.ExecContext(ctx, `
		DELETE FROM cloud_episode_meta WHERE owner_user_id = ? AND original_source_id = ?`,
		ownerID, original)
	return err
}
