package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	ArtifactDraft        = "draft"
	ArtifactPending      = "pending_acceptance"
	ArtifactAccepted     = "accepted"
	ArtifactRejected     = "rejected"
	ArtifactDelivered    = "delivered"
	ArtifactSuperseded   = "superseded"
	ArtifactKindText     = "text"
	ArtifactKindDoc      = "document"
	ArtifactKindCode     = "code"
	ArtifactKindAnalysis = "analysis"
	ArtifactKindEmail    = "email_draft"
	ArtifactKindFile     = "file"
	ArtifactKindExternal = "external_ref"

	ArtifactStorageText = "text"
	ArtifactStorageFile = "file"
)

type Artifact struct {
	ID             string    `json:"id"`
	OwnerUserID    string    `json:"owner_user_id"`
	TaskID         string    `json:"task_id"`
	Kind           string    `json:"kind"`
	Title          string    `json:"title"`
	StorageDir     string    `json:"storage_dir"`
	CurrentVersion int       `json:"current_version"`
	Status         string    `json:"status"`
	CreatedByJobID string    `json:"created_by_job_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ArtifactVersion struct {
	ID             string          `json:"id"`
	OwnerUserID    string          `json:"owner_user_id"`
	ArtifactID     string          `json:"artifact_id"`
	Version        int             `json:"version"`
	StorageKind    string          `json:"storage_kind"`
	RelativePath   string          `json:"relative_path,omitempty"`
	OriginalName   string          `json:"original_name,omitempty"`
	ContentText    string          `json:"content_text,omitempty"`
	ContentHash    string          `json:"content_hash"`
	MimeType       string          `json:"mime_type"`
	SizeBytes      int64           `json:"size_bytes"`
	SourceRefs     json.RawMessage `json:"source_refs,omitempty"`
	SourceKind     string          `json:"source_kind,omitempty"`
	SourceRef      string          `json:"source_ref,omitempty"`
	ChangeSummary  string          `json:"change_summary,omitempty"`
	CreatedByJobID string          `json:"created_by_job_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type ArtifactRepo struct{ q Querier }

func NewArtifactRepo(q Querier) *ArtifactRepo { return &ArtifactRepo{q: q} }

func (d *DB) Artifacts() *ArtifactRepo { return NewArtifactRepo(d) }

const artifactColumns = `id, owner_user_id, task_id, kind, title, storage_dir,
	current_version, status, created_by_job_id, created_at, updated_at`

func scanArtifact(row interface{ Scan(...any) error }) (*Artifact, error) {
	var a Artifact
	if err := row.Scan(&a.ID, &a.OwnerUserID, &a.TaskID, &a.Kind, &a.Title, &a.StorageDir,
		&a.CurrentVersion, &a.Status, &a.CreatedByJobID, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *ArtifactRepo) Create(ctx context.Context, a *Artifact) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.Kind == "" {
		a.Kind = ArtifactKindText
	}
	if a.Status == "" {
		a.Status = ArtifactDraft
	}
	now := nowForDB()
	a.CreatedAt, a.UpdatedAt = now, now
	_, err := r.q.ExecContext(ctx, `INSERT INTO artifact
		(id, owner_user_id, task_id, kind, title, storage_dir, current_version, status,
		 created_by_job_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		a.ID, a.OwnerUserID, a.TaskID, a.Kind, a.Title, a.StorageDir, a.Status,
		a.CreatedByJobID, now, now)
	return err
}

func (r *ArtifactRepo) Get(ctx context.Context, ownerID, id string) (*Artifact, error) {
	a, err := scanArtifact(r.q.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifact WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (r *ArtifactRepo) ListByTask(ctx context.Context, ownerID, taskID string) ([]Artifact, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+artifactColumns+` FROM artifact
		WHERE owner_user_id = ? AND task_id = ? ORDER BY updated_at DESC, id`, ownerID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Artifact, 0)
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// SetStatus 以 current_version 做 CAS；验收冲突返回 false。
func (r *ArtifactRepo) SetStatus(ctx context.Context, ownerID, id string, expectedVersion int, status string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE artifact SET status = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND current_version = ?`,
		status, at, ownerID, id, expectedVersion)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *ArtifactRepo) Advance(ctx context.Context, ownerID, id string, expectedVersion, nextVersion int, status string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE artifact
		SET current_version = ?, status = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND current_version = ?`,
		nextVersion, status, at, ownerID, id, expectedVersion)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type ArtifactVersionRepo struct{ q Querier }

func NewArtifactVersionRepo(q Querier) *ArtifactVersionRepo { return &ArtifactVersionRepo{q: q} }

func (d *DB) ArtifactVersions() *ArtifactVersionRepo { return NewArtifactVersionRepo(d) }

const artifactVersionColumns = `id, owner_user_id, artifact_id, version, storage_kind,
	relative_path, original_name, content_text, content_hash, mime_type, size_bytes,
	source_refs_json, source_kind, source_ref, change_summary, created_by_job_id, created_at`

func scanArtifactVersion(row interface{ Scan(...any) error }) (*ArtifactVersion, error) {
	var v ArtifactVersion
	var refs []byte
	if err := row.Scan(&v.ID, &v.OwnerUserID, &v.ArtifactID, &v.Version, &v.StorageKind,
		&v.RelativePath, &v.OriginalName, &v.ContentText, &v.ContentHash, &v.MimeType,
		&v.SizeBytes, &refs, &v.SourceKind, &v.SourceRef, &v.ChangeSummary,
		&v.CreatedByJobID, &v.CreatedAt); err != nil {
		return nil, err
	}
	if v.StorageKind == "" {
		v.StorageKind = ArtifactStorageText
	}
	v.SourceRefs = refs
	return &v, nil
}

func (r *ArtifactVersionRepo) Add(ctx context.Context, v *ArtifactVersion) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if len(v.SourceRefs) == 0 {
		v.SourceRefs = json.RawMessage(`[]`)
	}
	if v.StorageKind == "" {
		v.StorageKind = ArtifactStorageText
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = nowForDB()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO artifact_version
		(id, owner_user_id, artifact_id, version, storage_kind, relative_path, original_name,
		 content_text, content_hash, mime_type, size_bytes, source_refs_json, source_kind,
		 source_ref, change_summary, created_by_job_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.OwnerUserID, v.ArtifactID, v.Version, v.StorageKind, v.RelativePath,
		v.OriginalName, v.ContentText, v.ContentHash, v.MimeType, v.SizeBytes,
		string(v.SourceRefs), v.SourceKind, v.SourceRef, v.ChangeSummary,
		v.CreatedByJobID, v.CreatedAt)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (r *ArtifactVersionRepo) Get(ctx context.Context, ownerID, artifactID string, version int) (*ArtifactVersion, error) {
	v, err := scanArtifactVersion(r.q.QueryRowContext(ctx,
		`SELECT `+artifactVersionColumns+` FROM artifact_version
		 WHERE owner_user_id = ? AND artifact_id = ? AND version = ?`, ownerID, artifactID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

func (r *ArtifactVersionRepo) List(ctx context.Context, ownerID, artifactID string) ([]ArtifactVersion, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+artifactVersionColumns+` FROM artifact_version
		WHERE owner_user_id = ? AND artifact_id = ? ORDER BY version DESC`, ownerID, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ArtifactVersion, 0)
	for rows.Next() {
		v, err := scanArtifactVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}
