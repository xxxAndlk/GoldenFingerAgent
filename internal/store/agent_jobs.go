package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	AgentJobQueued      = "queued"
	AgentJobRunning     = "running"
	AgentJobCompleted   = "completed"
	AgentJobFailed      = "failed"
	AgentJobTimeout     = "timeout"
	AgentJobCancelled   = "cancelled"
	AgentJobInterrupted = "interrupted"
)

type AgentJob struct {
	ID             string     `json:"id"`
	OwnerUserID    string     `json:"owner_user_id"`
	AgentID        string     `json:"agent_id"`
	Task           string     `json:"task"`
	WorkDir        string     `json:"work_dir"`
	Status         string     `json:"status"`
	IdempotencyKey string     `json:"idempotency_key"`
	ExternalTaskID string     `json:"external_task_id,omitempty"`
	Output         string     `json:"output,omitempty"`
	Error          string     `json:"error,omitempty"`
	Attempt        int        `json:"attempt"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	HeartbeatAt    *time.Time `json:"heartbeat_at,omitempty"`
}

type AgentJobEvent struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	Type      string    `json:"type"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentNotification struct {
	ID          int64      `json:"id"`
	OwnerUserID string     `json:"owner_user_id"`
	JobID       string     `json:"job_id"`
	Body        string     `json:"body"`
	CreatedAt   time.Time  `json:"created_at"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
}

type AgentJobRepo struct{ q Querier }

func NewAgentJobRepo(q Querier) *AgentJobRepo { return &AgentJobRepo{q: q} }

// AgentJobs 返回当前数据库连接上的 Agent 任务仓储。
func (d *DB) AgentJobs() *AgentJobRepo { return NewAgentJobRepo(d) }

// AgentNotifications 返回当前数据库连接上的可靠通知仓储。
func (d *DB) AgentNotifications() *AgentNotificationRepo { return NewAgentNotificationRepo(d) }

func (r *AgentJobRepo) Create(ctx context.Context, job *AgentJob) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_job
		(id, owner_user_id, agent_id, task, work_dir, status, idempotency_key, attempt, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`, job.ID, job.OwnerUserID, job.AgentID, job.Task,
		job.WorkDir, job.Status, job.IdempotencyKey, job.CreatedAt, job.UpdatedAt)
	return err
}

func (r *AgentJobRepo) AddEvent(ctx context.Context, jobID, eventType, body string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_job_event (job_id, type, body, created_at) VALUES (?, ?, ?, ?)`,
		jobID, eventType, body, at)
	return err
}

func scanAgentJob(row interface{ Scan(...any) error }) (*AgentJob, error) {
	var job AgentJob
	var started, finished, heartbeat sql.NullTime
	err := row.Scan(&job.ID, &job.OwnerUserID, &job.AgentID, &job.Task, &job.WorkDir, &job.Status,
		&job.IdempotencyKey, &job.ExternalTaskID, &job.Output, &job.Error, &job.Attempt,
		&job.CreatedAt, &job.UpdatedAt, &started, &finished, &heartbeat)
	if err != nil {
		return nil, err
	}
	job.StartedAt, job.FinishedAt, job.HeartbeatAt = timePointer(started), timePointer(finished), timePointer(heartbeat)
	return &job, nil
}

func timePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

const agentJobColumns = `id, owner_user_id, agent_id, task, work_dir, status, idempotency_key,
	external_task_id, output, error, attempt, created_at, updated_at, started_at, finished_at, heartbeat_at`

func (r *AgentJobRepo) Get(ctx context.Context, ownerID, id string) (*AgentJob, error) {
	job, err := scanAgentJob(r.q.QueryRowContext(ctx, `SELECT `+agentJobColumns+` FROM agent_job WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return job, err
}

func (r *AgentJobRepo) GetByIdempotencyKey(ctx context.Context, ownerID, key string) (*AgentJob, error) {
	job, err := scanAgentJob(r.q.QueryRowContext(ctx, `SELECT `+agentJobColumns+` FROM agent_job WHERE owner_user_id = ? AND idempotency_key = ?`, ownerID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return job, err
}

func (r *AgentJobRepo) FirstQueued(ctx context.Context) (*AgentJob, error) {
	job, err := scanAgentJob(r.q.QueryRowContext(ctx, `SELECT `+agentJobColumns+` FROM agent_job WHERE status = 'queued' ORDER BY created_at, id LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

func (r *AgentJobRepo) List(ctx context.Context, ownerID string, limit int) ([]AgentJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+agentJobColumns+` FROM agent_job WHERE owner_user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]AgentJob, 0)
	for rows.Next() {
		job, err := scanAgentJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

func (r *AgentJobRepo) Events(ctx context.Context, ownerID, jobID string) ([]AgentJobEvent, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT e.id, e.job_id, e.type, e.body, e.created_at FROM agent_job_event e
		JOIN agent_job j ON j.id = e.job_id WHERE j.owner_user_id = ? AND e.job_id = ? ORDER BY e.id`, ownerID, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]AgentJobEvent, 0)
	for rows.Next() {
		var event AgentJobEvent
		if err := rows.Scan(&event.ID, &event.JobID, &event.Type, &event.Body, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *AgentJobRepo) MarkRunning(ctx context.Context, id string, at time.Time) (bool, error) {
	result, err := r.q.ExecContext(ctx, `UPDATE agent_job SET status = 'running', attempt = attempt + 1,
		started_at = ?, updated_at = ?, heartbeat_at = ? WHERE id = ? AND status = 'queued'`, at, at, at, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (r *AgentJobRepo) Heartbeat(ctx context.Context, id string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE agent_job SET heartbeat_at = ?, updated_at = ? WHERE id = ? AND status = 'running'`, at, at, id)
	return err
}

func (r *AgentJobRepo) Finish(ctx context.Context, id, status, output, taskError, externalTaskID string, at time.Time) (bool, error) {
	result, err := r.q.ExecContext(ctx, `UPDATE agent_job SET status = ?, output = ?, error = ?, external_task_id = ?,
		finished_at = ?, updated_at = ? WHERE id = ? AND status = 'running'`, status, output, taskError, externalTaskID, at, at, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (r *AgentJobRepo) Cancel(ctx context.Context, ownerID, id string, at time.Time) (bool, error) {
	result, err := r.q.ExecContext(ctx, `UPDATE agent_job SET status = 'cancelled', finished_at = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND status IN ('queued', 'running')`, at, at, ownerID, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (r *AgentJobRepo) InterruptRunning(ctx context.Context, at time.Time) ([]AgentJob, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+agentJobColumns+` FROM agent_job WHERE status = 'running'`)
	if err != nil {
		return nil, err
	}
	jobs := make([]AgentJob, 0)
	for rows.Next() {
		job, scanErr := scanAgentJob(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		jobs = append(jobs, *job)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, job := range jobs {
		if _, err := r.q.ExecContext(ctx, `UPDATE agent_job SET status = 'interrupted', finished_at = ?, updated_at = ? WHERE id = ? AND status = 'running'`, at, at, job.ID); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

type AgentNotificationRepo struct{ q Querier }

func NewAgentNotificationRepo(q Querier) *AgentNotificationRepo { return &AgentNotificationRepo{q: q} }

func (r *AgentNotificationRepo) Add(ctx context.Context, ownerID, jobID, body string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_notification (owner_user_id, job_id, body, created_at) VALUES (?, ?, ?, ?)`, ownerID, jobID, body, at)
	return err
}

func (r *AgentNotificationRepo) Unread(ctx context.Context, ownerID string, afterID int64, limit int) ([]AgentNotification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, owner_user_id, job_id, body, created_at, read_at
		FROM agent_notification WHERE owner_user_id = ? AND read_at IS NULL AND id > ? ORDER BY id LIMIT ?`, ownerID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AgentNotification, 0)
	for rows.Next() {
		var item AgentNotification
		var readAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.OwnerUserID, &item.JobID, &item.Body, &item.CreatedAt, &readAt); err != nil {
			return nil, err
		}
		item.ReadAt = timePointer(readAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *AgentNotificationRepo) Ack(ctx context.Context, ownerID string, ids []int64, at time.Time) error {
	for _, id := range ids {
		if _, err := r.q.ExecContext(ctx, `UPDATE agent_notification SET read_at = ? WHERE owner_user_id = ? AND id = ? AND read_at IS NULL`, at, ownerID, id); err != nil {
			return err
		}
	}
	return nil
}
